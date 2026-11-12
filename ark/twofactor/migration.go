package twofactor

import (
	"fmt"

	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
)

// columnTypes are the native column types of one dialect for the ark's tables.
type columnTypes struct {
	// subject is the user_id type: varchar(255) compared bytewise. PostgreSQL
	// uses the C collation, MySQL a binary ASCII collation (its default one
	// treats "abc" and "ABC" as the same key), and SQLite compares TEXT bytewise
	// by default.
	subject string

	// binary holds the encrypted secret and the recovery code hashes.
	binary string

	// integer is a 64-bit integer for the last used time step.
	integer string

	// small is a small integer for the recovery code slot.
	small string

	// timestamp is a nullable instant.
	timestamp string
}

// columnTypesFor returns the column types of dialect, which [New] has already
// restricted to PostgreSQL, MySQL and SQLite.
func columnTypesFor(dialect query.Dialect) columnTypes {
	switch dialect {
	case query.Postgres:
		return columnTypes{`varchar(255) COLLATE "C"`, "bytea", "bigint", "smallint", "timestamptz"}
	case query.MySQL:
		return columnTypes{"varchar(255) CHARACTER SET ascii COLLATE ascii_bin", "blob", "bigint", "smallint", "datetime(6)"}
	default:
		return columnTypes{"TEXT", "BLOB", "INTEGER", "INTEGER", "DATETIME"}
	}
}

// Migrations returns the migrations that create the ark's two tables,
// twofactor_secrets and twofactor_recovery_codes, each prefixed "<instance>_"
// when [Builder.Instance] is set. They belong to the group
// "arkoren.dev:ark/twofactor:twofactor", plus ":<instance>". The application
// adds them to its migrator before using the ark; the ark never runs DDL. Each
// migration creates one table strictly, so a leftover table fails loudly, and
// its down step drops the table with its data.
//
// twofactor_secrets holds user_id (the primary key), secret (the encrypted TOTP
// key), last_step (the last accepted time step, used to reject replays) and
// confirmed_at (null until the first code is verified). twofactor_recovery_codes
// holds user_id, slot (the code's position, which together with user_id is the
// primary key), code_hash and used_at. Both user_id columns hold the subject
// compared bytewise; an application may add its own foreign key from them to a
// column on its users table, but the ark does not require one, and it must
// never cascade the secrets away from a user it merely disables. Table names
// are not configurable. Definitions only describe DDL for the database's
// dialect; constructing or reading them never runs it.
//
// Example:
//
//	if err := migrator.Add(factors.Migrations()...); err != nil {
//		return err
//	}
func (twofactor *TwoFactor) Migrations() []migrate.Definition {
	group := "arkoren.dev:ark/twofactor:twofactor"

	if twofactor.config.instance != "" {
		group += ":" + twofactor.config.instance
	}

	dialect := twofactor.db.Dialect()
	types := columnTypesFor(dialect)

	return migrate.NewGroup(group).Migrations(
		migrate.NewMigration("secrets-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Exec(query.Raw(fmt.Sprintf(`CREATE TABLE %s (
	user_id %s NOT NULL,
	secret %s NOT NULL,
	last_step %s NOT NULL DEFAULT 0,
	confirmed_at %s NULL,
	PRIMARY KEY (user_id)
)`, dialect.Quote(twofactor.secrets), types.subject, types.binary, types.integer, types.timestamp))))
			migration.Down(migrate.Exec(query.Raw("DROP TABLE " + dialect.Quote(twofactor.secrets))))
		}),
		migrate.NewMigration("recovery-codes-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Exec(query.Raw(fmt.Sprintf(`CREATE TABLE %s (
	user_id %s NOT NULL,
	slot %s NOT NULL,
	code_hash %s NOT NULL,
	used_at %s NULL,
	PRIMARY KEY (user_id, slot)
)`, dialect.Quote(twofactor.recovery), types.subject, types.small, types.binary, types.timestamp))))
			migration.Down(migrate.Exec(query.Raw("DROP TABLE " + dialect.Quote(twofactor.recovery))))
		}),
	)
}
