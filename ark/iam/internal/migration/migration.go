// Package migration holds the migration definitions of every table of
// arkoren.dev/ark/iam, one function per package and group.
//
// The definitions depend only on the instance name: a table is "<instance>_<table>"
// and a group is "arkoren.dev:ark/access:<package>", plus ":<instance>". Keeping
// them here lets the facade ship the migrations of every feature without
// constructing the features it does not enable, and lets each package's
// Migrations method delegate to the same definition, so the two can never
// disagree. Nothing here runs DDL: the application's migrator executes them.
//
// Groups are append-only. Never rename a group or a migration id and never edit
// a shipped step: add a new migration at the end of the group instead.
package migration

import (
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/iam/internal/storage"
)

// TokenNameLength is the width of the "name" column of the token table, in
// bytes, and so the longest token name the apitoken package accepts.
const TokenNameLength = 255

// Account returns the group of the built-in users table for instance: the
// "users" table, created strictly, with the down step dropping it.
func Account(instance string) []migrate.Definition {
	users := storage.Table(instance, "users")

	return storage.Group("account", instance).Migrations(
		migrate.NewMigration("users-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(users, func(table *schema.Table) {
				table.String("id", 255).Bytewise().Primary()
				table.String("email", 255).Bytewise().Unique()
				table.BigInteger("version").Default(0)
				table.BigInteger("verified_at").Default(0)
				table.BigInteger("disabled_at").Default(0)
				table.BigInteger("created_at")
			})))
			migration.Down(migrate.Schema(schema.Drop(users)))
		}),
	)
}

// Password returns the group of the password package for instance: the
// "password_hashes" table.
func Password(instance string) []migrate.Definition {
	hashes := storage.Table(instance, "password_hashes")

	return storage.Group("password", instance).Migrations(
		migrate.NewMigration("hashes-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(hashes, func(table *schema.Table) {
				table.String("subject", 255).Bytewise().Primary()
				table.Text("hash")
				table.BigInteger("changed_at")
			})))
			migration.Down(migrate.Schema(schema.Drop(hashes)))
		}),
	)
}

// TwoFactor returns the group of the twofactor package for instance: the
// "totp_secrets" and "recovery_codes" tables, one migration each.
func TwoFactor(instance string) []migrate.Definition {
	secrets := storage.Table(instance, "totp_secrets")
	codes := storage.Table(instance, "recovery_codes")

	return storage.Group("twofactor", instance).Migrations(
		migrate.NewMigration("secrets-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(secrets, func(table *schema.Table) {
				table.String("subject", 255).Bytewise().Primary()
				table.Text("secret")
				table.BigInteger("last_step").Default(0)
				table.BigInteger("confirmed_at").Default(0)
			})))
			migration.Down(migrate.Schema(schema.Drop(secrets)))
		}),
		migrate.NewMigration("recovery-codes-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(codes, func(table *schema.Table) {
				table.String("subject", 255).Bytewise()
				table.SmallInteger("slot")
				table.String("code_hash", 64).Bytewise()
				table.BigInteger("used_at").Default(0)
				table.Primary("subject", "slot")
			})))
			migration.Down(migrate.Schema(schema.Drop(codes)))
		}),
	)
}

// Tokens returns the group of the apitoken package for instance: the
// "api_tokens" table.
func Tokens(instance string) []migrate.Definition {
	tokens := storage.Table(instance, "api_tokens")

	return storage.Group("apitoken", instance).Migrations(
		migrate.NewMigration("api-tokens-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(tokens, func(table *schema.Table) {
				table.String("id", 32).Bytewise().Primary()
				table.String("subject", 255).Bytewise()
				table.String("kind", 16)
				table.String("name", TokenNameLength)
				table.String("secret_hash", 64).Bytewise()
				table.String("user_version", 64).Bytewise()
				table.Text("scopes")
				table.BigInteger("last_used_at").Default(0)
				table.BigInteger("expires_at").Default(0)
				table.BigInteger("created_at")
				table.Index("subject", "created_at")
			})))
			migration.Down(migrate.Schema(schema.Drop(tokens)))
		}),
	)
}

// Links returns the group of the federation package for instance: the
// "federation_links" table and the later step that adds its claims column.
func Links(instance string) []migrate.Definition {
	links := storage.Table(instance, "federation_links")

	return storage.Group("federation", instance).Migrations(
		migrate.NewMigration("links-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(links, func(table *schema.Table) {
				table.String("id", 32).Bytewise().Primary()
				table.String("subject", 255).Bytewise()
				table.String("issuer", 255).Bytewise()
				table.String("external", 255).Bytewise()
				table.BigInteger("created_at")
				table.BigInteger("last_used_at").Default(0)
				table.Unique("issuer", "external")
				table.Unique("subject", "issuer")
			})))
			migration.Down(migrate.Schema(schema.Drop(links)))
		}),
		migrate.NewMigration("links-add-claims", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Alter(links, func(table *schema.Table) {
				table.JSON("claims").Nullable()
			})))
			migration.Down(migrate.Schema(schema.Alter(links, func(table *schema.Table) {
				table.DropColumn("claims")
			})))
		}),
	)
}

// Permissions returns the group of the permission package for instance: the
// "permission_grants" table.
func Permissions(instance string) []migrate.Definition {
	grants := storage.Table(instance, "permission_grants")

	return storage.Group("permission", instance).Migrations(
		migrate.NewMigration("permission-grants-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(grants, func(table *schema.Table) {
				table.String("subject", 255).Bytewise()
				table.String("kind", 16).Bytewise()
				table.String("name", 128).Bytewise()
				table.BigInteger("created_at")
				table.Primary("subject", "kind", "name")
				table.Index("kind", "name")
			})))
			migration.Down(migrate.Schema(schema.Drop(grants)))
		}),
	)
}
