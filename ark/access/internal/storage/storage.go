// Package storage holds the table conventions shared by the packages of
// arkoren.dev/ark/access: instance validation, table and migration-group
// names, identifiers, secret hashing and the readiness check.
//
// It is internal so the public packages never import each other: each one
// imports this package instead of a sibling. Nothing here knows about users,
// passwords or tokens, and nothing here runs DDL; the packages describe their
// tables with arkoren.dev/foundation/query/schema and the application's
// migrator executes them.
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
)

// MaxInstance is the longest instance name in bytes. A table name is
// "<instance>_<table>" and the generated index names add up to 20 more bytes;
// all of them must stay within the 63-byte identifier limit of
// arkoren.dev/foundation/query/schema.
const MaxInstance = 20

// ErrNotMigrated reports that a package's tables do not exist. Its message
// names Migrations, because the application owns running them.
var ErrNotMigrated = errors.New("access: tables are missing; add and run Migrations() with the migrator")

// instancePattern accepts the instance names that make valid unquoted SQL
// identifiers when prefixed to a table name.
var instancePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidInstance reports an error unless instance is empty (the default
// instance) or 1 to [MaxInstance] bytes of lowercase letters, digits and
// underscores that start with a letter.
func ValidInstance(instance string) error {
	if instance == "" {
		return nil
	}
	if len(instance) > MaxInstance || !instancePattern.MatchString(instance) {
		return fmt.Errorf("access: invalid instance %q: use 1 to %d bytes of [a-z0-9_] starting with a letter", instance, MaxInstance)
	}

	return nil
}

// Table returns the name of a package's table for instance: the plain name for
// the default instance and "<instance>_<name>" otherwise.
func Table(instance, name string) string {
	if instance == "" {
		return name
	}

	return instance + "_" + name
}

// Group returns the migration group of a package: "arkoren.dev:ark/access:<pkg>",
// plus ":<instance>" for a named instance.
func Group(pkg, instance string) migrate.MigrationGroup {
	name := "arkoren.dev:ark/access:" + pkg
	if instance != "" {
		name += ":" + instance
	}

	return migrate.NewGroup(name)
}

// Ping reads one row of each table and so verifies, read-only, that the tables
// exist. A failure wraps [ErrNotMigrated] and the database error.
func Ping(ctx context.Context, db *database.Database, tables ...string) error {
	for _, table := range tables {
		probe := query.Table(table, func(statement *query.Builder) {
			statement.SelectRaw("1 AS present").Limit(1)
		})
		if _, err := db.Select[struct {
			Present int `db:"present"`
		}](ctx, probe); err != nil {
			return fmt.Errorf("%w: table %s: %w", ErrNotMigrated, table, err)
		}
	}

	return nil
}

// NewID returns a random 128-bit identifier as 32 lowercase hexadecimal
// characters. Identifiers are generated in Go so inserts need no RETURNING.
func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("access: generate id: %w", err)
	}

	return hex.EncodeToString(raw[:]), nil
}

// Hash returns the SHA-256 of secret as 64 lowercase hexadecimal characters,
// the form in which tokens and codes are stored. It suits only secrets with
// enough entropy that guessing them offline is infeasible (at least 80 random
// bits); passwords go through the application's hasher.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))

	return hex.EncodeToString(sum[:])
}

// Unix returns t as Unix seconds. Every time column of the module is a
// bigint of Unix seconds, so values round-trip identically on SQLite,
// PostgreSQL and MySQL regardless of their time-zone and parsing rules.
func Unix(t time.Time) int64 {
	return t.Unix()
}

// Time converts a stored Unix-second value back to a UTC time. The zero value
// (0) maps to the zero time, which the JSON encoders of this module omit.
func Time(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}

	return time.Unix(seconds, 0).UTC()
}
