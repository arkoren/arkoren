package layout

import (
	"errors"
	"io/fs"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// errNoInsertionPoint reports that an existing declaration does not have the
// shape an insertion needs, so the caller leaves a manual step instead.
var errNoInsertionPoint = errors.New("no insertion point")

// planMigrator makes the migrations of the identity module run, and reports
// whether the plan wrote a Migrate provider that the server's Compose must use.
//
// Without a Migrator provider it writes migrator.go (the Migrator over
// Database, migrations.All and the module's Migrations, and the Migrate
// provider that applies them in a Startup callback) and, when internal/migrations
// declares no All, migrations.go with an empty one. With a Migrator it inserts
// the module's migrations into it and writes nothing else: the application
// already decides which process applies them. When the Migrator cannot take the
// insertion, it adds a step that names the line to write.
//
// It returns a usage error when Migrate exists without a Migrator, or when
// internal/migrations/migrations.go exists without an All.
func (project *Project) planMigrator(plan *Plan, templates fs.FS) (bool, error) {
	existing := project.declaringSource("internal/bootstrap", "Migrator")

	if existing != nil {
		err := project.planEdit(plan, existing.rel, func(content []byte) ([]byte, []string, error) {
			return addIAMMigrations(existing.rel, content)
		})

		if errors.Is(err, errNoInsertionPoint) {
			plan.Steps = append(plan.Steps, console.Step{Text: "Add compose.Require(migrator.Add(compose.Use(IAM).Migrations()...)) to the Migrator provider; " +
				"the process that applies migrations needs the same environment as the server (database, keys, URLs)"})

			return false, nil
		}

		return false, err
	}

	if project.declares("internal/bootstrap", "Migrate") {
		return false, console.UsageError("internal/bootstrap already declares Migrate")
	}

	if err := project.renderMigrations(plan, templates); err != nil {
		return false, err
	}

	data := map[string]string{"Module": project.Module}

	return true, project.render(plan, templates, "add/ark/provider/migrator.go", "internal/bootstrap/migrator.go", data)
}

// renderMigrations appends migrations.go, with an empty All, to plan unless a
// file of internal/migrations already declares All, which the Migrator
// provider calls. It returns a usage error when migrations.go exists without an
// All.
func (project *Project) renderMigrations(plan *Plan, templates fs.FS) error {
	const dir = "internal/migrations"

	for _, src := range project.packageSources(dir) {
		if declaresAll(src.file) {
			return nil
		}
	}

	if err := project.refuseExisting(dir + "/migrations.go"); err != nil {
		return console.Fail(console.TypeUsage,
			[]console.Step{{Text: "Add func All() []migrate.Definition to " + dir + "/migrations.go"}},
			"%s/migrations.go exists without an All function", dir)
	}

	return project.render(plan, templates, "add/migration/migrations.go", dir+"/migrations.go", nil)
}
