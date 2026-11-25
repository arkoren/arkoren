package layout_test

import (
	"slices"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/layout"
)

// allIAMFeatures selects every optional feature of add ark iam.
var allIAMFeatures = layout.IAMFeatures

// stepTexts joins the text and command of every step, one per line.
func stepTexts(steps []console.Step) string {
	texts := make([]string, len(steps))

	for index, step := range steps {
		texts[index] = step.Text + " " + step.Command
	}

	return strings.Join(texts, "\n")
}

// created returns the project-relative slash paths a plan creates.
func created(plan *layout.Plan) []string {
	return files.Paths(plan.Create)
}

// edited returns the lines a plan inserts into path, or nil when it does not
// edit path.
func edited(plan *layout.Plan, path string) []string {
	for _, edit := range plan.Edit {
		if edit.Path == path {
			return edit.Inserted
		}
	}

	return nil
}

// TestAddIAMWritesProvidersAndWiresCompose creates the module's providers, the
// shared ones it needs and the Compose insertions, with the jobs in the server
// by default and the migrations applied before the routes are served.
func TestAddIAMWritesProvidersAndWiresCompose(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddIAM(layout.IAMOptions{With: allIAMFeatures}, templates)
	apply(t, project, plan, err)

	for _, want := range []string{
		"internal/bootstrap/iam.go", "internal/bootstrap/password_reset.go", "internal/bootstrap/email_verification.go",
		"internal/bootstrap/token_scopes.go", "internal/bootstrap/roles.go", "internal/bootstrap/account_view.go",
		"internal/bootstrap/database.go", "internal/bootstrap/cache.go", "internal/bootstrap/hasher.go",
		"internal/bootstrap/link_signer.go", "internal/bootstrap/mailer.go", "internal/bootstrap/queue.go",
		"internal/bootstrap/dispatcher.go", "internal/bootstrap/consumer.go", "internal/bootstrap/work.go",
		"internal/bootstrap/two_factor_key.go", "internal/bootstrap/migrator.go", "internal/migrations/migrations.go",
	} {
		if !slices.Contains(created(plan), want) {
			t.Fatalf("plan does not create %s: %v", want, created(plan))
		}
	}

	for _, unwanted := range []string{"internal/bootstrap/google.go", "internal/bootstrap/github.go", "internal/bootstrap/oauth_key.go"} {
		if slices.Contains(created(plan), unwanted) {
			t.Fatalf("plan creates %s without a sign-in client: %v", unwanted, created(plan))
		}
	}

	compose := read(t, root, "internal/bootstrap/server/compose.go")
	want := "compose.Use(bootstrap.Migrate)\n\tcompose.Use(bootstrap.IAMRouter)\n\tcompose.Use(bootstrap.IAMJobs)\n\tcompose.Use(bootstrap.Work)\n\n\tif compose.App().Debug() {"

	if !strings.Contains(compose, want) {
		t.Fatalf("compose.go:\n%s", compose)
	}

	if identity := read(t, root, "internal/bootstrap/iam.go"); !strings.Contains(identity, `web.Group("/auth", identity.Register)`) {
		t.Fatalf("iam.go does not mount the routes under /auth:\n%s", identity)
	}

	if found := load(t, root).Check(); len(found) != 0 {
		t.Fatalf("generated project violates the layout rules: %+v", found)
	}
}

// TestAddIAMWritesMigratorAndMigrate applies the migrations in a Startup
// callback, with the application's own migrations and the module's, and creates
// the empty All the Migrator calls.
func TestAddIAMWritesMigratorAndMigrate(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddIAM(layout.IAMOptions{}, templates)
	apply(t, project, plan, err)

	migrator := read(t, root, "internal/bootstrap/migrator.go")

	for _, want := range []string{
		"migrator.Add(migrations.All()...)", "migrator.Add(compose.Use(IAM).Migrations()...)", "compose.Startup(migrator.Up)",
		`"example.com/shop/internal/migrations"`, "var Migrate = application.Provide(",
	} {
		if !strings.Contains(migrator, want) {
			t.Fatalf("migrator.go misses %q:\n%s", want, migrator)
		}
	}

	if all := read(t, root, "internal/migrations/migrations.go"); !strings.Contains(all, "func All() []migrate.Definition") {
		t.Fatalf("migrations.go:\n%s", all)
	}
}

// TestAddIAMKeepsExistingMigrations leaves the application's All alone and
// calls it from the new Migrator.
func TestAddIAMKeepsExistingMigrations(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddMigration("create_orders", templates)
	apply(t, project, plan, err)

	plan, err = load(t, root).AddIAM(layout.IAMOptions{}, templates)
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(created(plan), "internal/migrations/migrations.go") || !slices.Contains(created(plan), "internal/bootstrap/migrator.go") {
		t.Fatalf("plan creates %v", created(plan))
	}
}

// existingMigrator is the Migrator provider of a project that ran
// `arkoren add migration` and wrote its own migrate process.
const existingMigrator = `package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/database/migrate"

	"example.com/shop/internal/migrations"
)

// Migrator holds the application's migrations.
var Migrator = application.Provide(func(compose *application.Composer) *migrate.Migrator {
	migrator := migrate.New(compose.Use(Database))
	compose.Require(migrator.Add(migrations.All()...))

	return migrator
})
`

// TestAddIAMInsertsIntoExistingMigrator adds the module's migrations to the
// Migrator the project already has, and writes neither a second Migrator nor
// a Migrate for the server.
func TestAddIAMInsertsIntoExistingMigrator(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/migrator.go", existingMigrator)
	project := load(t, root)
	plan, err := project.AddIAM(layout.IAMOptions{}, templates)
	apply(t, project, plan, err)

	line := "compose.Require(migrator.Add(compose.Use(IAM).Migrations()...))"

	if inserted := edited(plan, "internal/bootstrap/migrator.go"); !slices.Equal(inserted, []string{line}) {
		t.Fatalf("inserted = %v, want %q", inserted, line)
	}

	migrator := read(t, root, "internal/bootstrap/migrator.go")

	if !strings.Contains(migrator, "migrator.Add(migrations.All()...))\n\t"+line+"\n\n\treturn migrator") {
		t.Fatalf("migrator.go:\n%s", migrator)
	}

	if compose := read(t, root, "internal/bootstrap/server/compose.go"); strings.Contains(compose, "Migrate") || !strings.Contains(compose, "bootstrap.IAMRouter") {
		t.Fatalf("compose.go:\n%s", compose)
	}

	if strings.Contains(stepTexts(plan.Steps), "Migrations()") {
		t.Fatalf("steps ask for what the command did: %v", plan.Steps)
	}
}

// TestAddIAMDoesNotInsertMigrationsTwice recognizes the line it would insert.
func TestAddIAMDoesNotInsertMigrationsTwice(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/migrator.go", strings.Replace(existingMigrator, "\n\treturn migrator",
		"\tcompose.Require(migrator.Add(compose.Use(IAM).Migrations()...))\n\n\treturn migrator", 1))

	plan, err := load(t, root).AddIAM(layout.IAMOptions{}, templates)
	if err != nil {
		t.Fatal(err)
	}

	if inserted := edited(plan, "internal/bootstrap/migrator.go"); inserted != nil {
		t.Fatalf("inserted %v into a Migrator that has the line", inserted)
	}
}

// TestAddIAMLeavesAStepWhenMigratorCannotTakeTheLine falls back to the manual
// step for a Migrator without a list of migrations to extend.
func TestAddIAMLeavesAStepWhenMigratorCannotTakeTheLine(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/migrator.go", `package bootstrap

import "arkoren.dev/core/application"

var Migrator = application.Provide(func(compose *application.Composer) int {
	return 1
})
`)

	plan, err := load(t, root).AddIAM(layout.IAMOptions{}, templates)
	if err != nil {
		t.Fatal(err)
	}

	if edited(plan, "internal/bootstrap/migrator.go") != nil || !strings.Contains(stepTexts(plan.Steps), "compose.Use(IAM).Migrations()") {
		t.Fatalf("plan = %+v", plan)
	}
}

// TestAddIAMRefusesMigrateWithoutMigrator would otherwise write a second Migrate.
func TestAddIAMRefusesMigrateWithoutMigrator(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/existing_migrate.go", "package bootstrap\n\nvar Migrate = 1\n")

	_, err := load(t, root).AddIAM(layout.IAMOptions{}, templates)

	requireUsage(t, err, "internal/bootstrap already declares Migrate")
}

// TestAddIAMPinsTheModulesItImports lists the modules the new files need with
// the versions the CLI pins, in order, and none the project already requires.
func TestAddIAMPinsTheModulesItImports(t *testing.T) {
	t.Parallel()

	plan, err := load(t, scaffoldProject(t)).AddIAM(layout.IAMOptions{With: allIAMFeatures}, templates)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"arkoren.dev/ark/iam@v0.8.0", "arkoren.dev/driver/aes@v0.2.0", "arkoren.dev/driver/argon2@v0.1.0",
		"arkoren.dev/driver/sqlite@v0.1.0", "arkoren.dev/foundation/key@v0.2.0", "arkoren.dev/foundation/totp@v0.2.0",
	}

	if !slices.Equal(plan.Requires, want) {
		t.Fatalf("requires = %v, want %v", plan.Requires, want)
	}

	root := scaffoldProject(t)
	writeFile(t, root, "go.mod", read(t, root, "go.mod")+"\nrequire arkoren.dev/ark/iam v0.4.0\n")

	plan, err = load(t, root).AddIAM(layout.IAMOptions{With: []string{"tokens"}}, templates)
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(plan.Requires, "arkoren.dev/ark/iam@v0.8.0") || !slices.Contains(plan.Requires, "arkoren.dev/driver/sqlite@v0.1.0") {
		t.Fatalf("requires = %v, want the missing modules only", plan.Requires)
	}
}

// TestAddIAMWithoutEmailWritesNoJobs leaves out the queue, the mailer, the link
// signer and IAMJobs when neither reset nor verification is selected.
func TestAddIAMWithoutEmailWritesNoJobs(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddIAM(layout.IAMOptions{With: []string{"tokens"}}, templates)
	apply(t, project, plan, err)

	for _, unwanted := range []string{"mailer.go", "queue.go", "dispatcher.go", "consumer.go", "work.go", "link_signer.go", "password_reset.go"} {
		if slices.Contains(created(plan), "internal/bootstrap/"+unwanted) {
			t.Fatalf("plan creates %s: %v", unwanted, created(plan))
		}
	}

	identity := read(t, root, "internal/bootstrap/iam.go")

	if strings.Contains(identity, "IAMJobs") || strings.Contains(identity, "identity.Queue") || !strings.Contains(identity, "identity.Password(compose.Use(Hasher))\n") {
		t.Fatalf("iam.go:\n%s", identity)
	}

	if compose := read(t, root, "internal/bootstrap/server/compose.go"); strings.Contains(compose, "IAMJobs") || !strings.Contains(compose, "compose.Use(bootstrap.IAMRouter)") {
		t.Fatalf("compose.go:\n%s", compose)
	}
}

// TestAddIAMKeepsExistingProviders finds shared providers by name, never writes
// them again and still wires the module to them.
func TestAddIAMKeepsExistingProviders(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)

	for _, name := range []string{"Database", "Cache", "Queue", "Dispatcher", "Consumer", "Work"} {
		writeFile(t, root, "internal/bootstrap/existing_"+strings.ToLower(name)+".go", "package bootstrap\n\nvar "+name+" = 1\n")
	}

	plan, err := load(t, root).AddIAM(layout.IAMOptions{With: []string{"reset"}}, templates)
	if err != nil {
		t.Fatal(err)
	}

	for _, kept := range []string{"database.go", "cache.go", "queue.go", "dispatcher.go", "consumer.go", "work.go"} {
		if slices.Contains(created(plan), "internal/bootstrap/"+kept) {
			t.Fatalf("plan rewrites the existing provider in %s", kept)
		}
	}

	for _, wanted := range []string{"hasher.go", "link_signer.go", "mailer.go", "iam.go", "password_reset.go"} {
		if !slices.Contains(created(plan), "internal/bootstrap/"+wanted) {
			t.Fatalf("plan does not create %s: %v", wanted, created(plan))
		}
	}

	if slices.Contains(edited(plan, ".env"), "DATABASE_URL=file:app.db") {
		t.Fatalf(".env edit sets DATABASE_URL for a Database provider the project wrote: %v", plan.Edit)
	}
}

// TestAddIAMSkipsComposeUsesAlreadyThere keeps a Compose that already runs Work
// from listing it twice, and inserts the jobs before it.
func TestAddIAMSkipsComposeUsesAlreadyThere(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddProcess("worker", templates)
	apply(t, project, plan, err)
	writeFile(t, root, "internal/bootstrap/worker/compose.go", `package worker

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
)

func Compose(compose *application.Composer) {
	compose.Use(bootstrap.Work)
}
`)

	project = load(t, root)
	plan, err = project.AddIAM(layout.IAMOptions{With: []string{"verification"}, Process: "worker"}, templates)
	apply(t, project, plan, err)

	if compose := read(t, root, "internal/bootstrap/worker/compose.go"); strings.Count(compose, "bootstrap.Work") != 1 ||
		!strings.Contains(compose, "compose.Use(bootstrap.IAMJobs)\n\n\tcompose.Use(bootstrap.Work)\n") {
		t.Fatalf("worker compose.go:\n%s", compose)
	}

	if server := read(t, root, "internal/bootstrap/server/compose.go"); strings.Contains(server, "IAMJobs") || !strings.Contains(server, "bootstrap.IAMRouter") {
		t.Fatalf("server compose.go:\n%s", server)
	}
}

// TestAddIAMRefusesExistingProviders refuses to overwrite a provider the module
// declares, found by name.
func TestAddIAMRefusesExistingProviders(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/existing.go", "package bootstrap\n\nvar IAMRouter = 1\n")

	_, err := load(t, root).AddIAM(layout.IAMOptions{}, templates)

	requireUsage(t, err, "internal/bootstrap already declares IAMRouter")
}

// TestAddIAMRefusesExistingFile refuses a target file that exists, even when it
// declares none of the providers.
func TestAddIAMRefusesExistingFile(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/database.go", "package bootstrap\n")

	_, err := load(t, root).AddIAM(layout.IAMOptions{}, templates)

	requireConflict(t, err, "internal/bootstrap/database.go already exists")
}

// TestAddIAMRefusesUnknownFeature lists the accepted features.
func TestAddIAMRefusesUnknownFeature(t *testing.T) {
	t.Parallel()

	_, err := load(t, scaffoldProject(t)).AddIAM(layout.IAMOptions{With: []string{"passkeys"}}, templates)

	requireUsage(t, err, "--with accepts reset, verification, two-factor, tokens, permissions, account")
}

// TestAddIAMRefusesSignInProviders points the removed google and github values
// at the command that adds them now.
func TestAddIAMRefusesSignInProviders(t *testing.T) {
	t.Parallel()

	project := load(t, scaffoldProject(t))

	for _, provider := range []string{"google", "github"} {
		_, err := project.AddIAM(layout.IAMOptions{With: []string{provider}}, templates)

		requireUsage(t, err, "arkoren add ark oauth "+provider)
	}
}

// TestAddIAMRefusesMissingProcess needs a Compose for the jobs.
func TestAddIAMRefusesMissingProcess(t *testing.T) {
	t.Parallel()

	_, err := load(t, scaffoldProject(t)).AddIAM(layout.IAMOptions{With: []string{"reset"}, Process: "worker"}, templates)

	requireUsage(t, err, "arkoren add process")
}

// TestAddIAMIgnoresProcessWithoutJobs does not need the process when no email is sent.
func TestAddIAMIgnoresProcessWithoutJobs(t *testing.T) {
	t.Parallel()

	if _, err := load(t, scaffoldProject(t)).AddIAM(layout.IAMOptions{With: []string{"tokens"}, Process: "worker"}, templates); err != nil {
		t.Fatalf("AddIAM = %v, want no error", err)
	}
}

// TestAddIAMReportsOnlyWhatTheReaderMustDo lists the key files and ends with the
// command that starts the server; the environment and migrations need no step.
func TestAddIAMReportsOnlyWhatTheReaderMustDo(t *testing.T) {
	t.Parallel()

	plan, err := load(t, scaffoldProject(t)).AddIAM(layout.IAMOptions{With: allIAMFeatures}, templates)
	if err != nil {
		t.Fatal(err)
	}

	notes := stepTexts(plan.Steps)

	for _, want := range []string{
		"LINK_KEY=file:.secrets/link-key", "arkoren key generate shared --out .secrets/link-key", "TWO_FACTOR_KEY=file:.secrets/two-factor-key",
		"arkoren key generate shared --out .secrets/two-factor-key", "worker", "make run",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes miss %q:\n%s", want, notes)
		}
	}

	for _, unwanted := range []string{"DATABASE_URL", "PASSWORD_RESET_URL", "EMAIL_VERIFY_URL", "Migrations()"} {
		if strings.Contains(notes, unwanted) {
			t.Fatalf("notes ask for %q, which the command did:\n%s", unwanted, notes)
		}
	}

	if last := plan.Steps[len(plan.Steps)-1]; last.Command != "make run" {
		t.Fatalf("last step = %+v, want make run", last)
	}
}

// TestAddIAMWiresDeclaredSignInClients passes a client that internal/bootstrap
// already declares, with its sign-up fragment, to identity.Provider.
func TestAddIAMWiresDeclaredSignInClients(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddOAuth([]string{"google", "github"}, templates)
	apply(t, project, plan, err)

	project = load(t, root)
	plan, err = project.AddIAM(layout.IAMOptions{}, templates)
	apply(t, project, plan, err)

	identity := read(t, root, "internal/bootstrap/iam.go")

	for _, want := range []string{"identity.Provider(compose.Use(Google))", "identity.Provider(compose.Use(GitHub), compose.Use(GitHubSignUp))"} {
		if !strings.Contains(identity, want) {
			t.Fatalf("iam.go misses %q:\n%s", want, identity)
		}
	}

	if slices.Contains(created(plan), "internal/bootstrap/oauth_key.go") {
		t.Fatalf("plan rewrites the OAuthKey the clients declared: %v", created(plan))
	}

	env := read(t, root, ".env")

	if !strings.Contains(env, "GOOGLE_REDIRECT_URL=http://127.0.0.1:8080/auth/google/callback\n") {
		t.Fatalf(".env:\n%s", env)
	}

	if found := load(t, root).Check(); len(found) != 0 {
		t.Fatalf("generated project violates the layout rules: %+v", found)
	}
}
