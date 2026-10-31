package postgressearch

import (
	"fmt"

	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
)

// Migrations returns the migrations that create the driver's two tables:
// search_documents (with a GIN index on its tsvector) and search_indexes,
// each prefixed "<instance>_" when [Builder.Instance] is set. They belong to the
// group "arkoren.dev:driver/postgres:postgressearch", plus ":<instance>".
// The application adds them to its migrator, ahead of any migration of its
// own that needs them; the driver never creates tables itself. Each
// migration creates one table strictly, so a leftover table fails loudly, and
// its down step drops the table with its documents.
// Both migration directions quote generated table and index names without
// changing their identities, including when an instance starts with a digit.
// Definitions only describe DDL; constructing or reading them never runs it.
//
// Example:
//
//	if err := migrator.Add(driver.Migrations()...); err != nil {
//		return err
//	}
func (driver *Driver) Migrations() []migrate.Definition {
	group := migrate.NewGroup("arkoren.dev:driver/postgres:postgressearch")

	if driver.instance != "" {
		group = migrate.NewGroup("arkoren.dev:driver/postgres:postgressearch" + ":" + driver.instance)
	}

	return group.Migrations(
		migrate.NewMigration("documents-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Exec(
				query.Raw(fmt.Sprintf(`CREATE TABLE %s (
	index_name text NOT NULL,
	id text NOT NULL,
	source jsonb NOT NULL,
	tsv tsvector NOT NULL,
	PRIMARY KEY (index_name, id)
)`, query.Postgres.Quote(driver.documents))),
				query.Raw(fmt.Sprintf("CREATE INDEX %s ON %s USING GIN (tsv)",
					query.Postgres.Quote(driver.documents+"_tsv"), query.Postgres.Quote(driver.documents))),
			))
			// Generated instance names can start with digits, outside the schema
			// DSL's unquoted-name grammar. Quote them at this PostgreSQL boundary.
			migration.Down(migrate.Exec(query.Raw("DROP TABLE " + query.Postgres.Quote(driver.documents))))
			migration.Only(query.Postgres)
		}),
		migrate.NewMigration("indexes-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Exec(query.Raw(fmt.Sprintf(`CREATE TABLE %s (
	name text PRIMARY KEY,
	definition jsonb NOT NULL
)`, query.Postgres.Quote(driver.indexes)))))
			migration.Down(migrate.Exec(query.Raw("DROP TABLE " + query.Postgres.Quote(driver.indexes))))
			migration.Only(query.Postgres)
		}),
	)
}
