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

// stepTexts joins the text of every step, one per line.
func stepTexts(steps []console.Step) string {
	texts := make([]string, len(steps))

	for index, step := range steps {
		texts[index] = step.Text
	}

	return strings.Join(texts, "\n")
}

// created returns the project-relative slash paths a plan creates.
func created(plan *layout.Plan) []string {
	return files.Paths(plan.Create)
}

// TestAddIAMWritesProvidersAndWiresCompose creates the module's providers, the
// shared ones it needs and the Compose insertions, with the jobs in the server
// by default.
func TestAddIAMWritesProvidersAndWiresCompose(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddIAM(layout.IAMOptions{With: allIAMFeatures}, templates)
	apply(t, project, plan, err)

	for _, want := range []string{
		"internal/bootstrap/iam.go", "internal/bootstrap/password_reset.go", "internal/bootstrap/email_verification.go",
		"internal/bootstrap/token_scopes.go", "internal/bootstrap/roles.go", "internal/bootstrap/account_view.go",
		"internal/bootstrap/google.go", "internal/bootstrap/github.go", "internal/bootstrap/github_sign_up.go",
		"internal/bootstrap/database.go", "internal/bootstrap/cache.go", "internal/bootstrap/hasher.go",
		"internal/bootstrap/link_signer.go", "internal/bootstrap/mailer.go", "internal/bootstrap/queue.go",
		"internal/bootstrap/dispatcher.go", "internal/bootstrap/consumer.go", "internal/bootstrap/work.go",
		"internal/bootstrap/two_factor_key.go", "internal/bootstrap/oauth_key.go",
	} {
		if !slices.Contains(created(plan), want) {
			t.Fatalf("plan does not create %s: %v", want, created(plan))
		}
	}

	compose := read(t, root, "internal/bootstrap/server/compose.go")

	if !strings.Contains(compose, "compose.Use(bootstrap.IAMRouter)\n\tcompose.Use(bootstrap.IAMJobs)\n\tcompose.Use(bootstrap.Work)\n\n\tif compose.App().Debug() {") {
		t.Fatalf("compose.go:\n%s", compose)
	}

	if found := load(t, root).Check(); len(found) != 0 {
		t.Fatalf("generated project violates the layout rules: %+v", found)
	}
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
		"arkoren.dev/ark/iam@v0.5.0", "arkoren.dev/ark/oauth@v0.7.0", "arkoren.dev/driver/aes@v0.1.0", "arkoren.dev/driver/bcrypt@v0.1.0",
		"arkoren.dev/driver/sqlite@v0.1.0", "arkoren.dev/foundation/key@v0.1.1", "arkoren.dev/foundation/totp@v0.2.0",
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

	if slices.Contains(plan.Requires, "arkoren.dev/ark/iam@v0.5.0") || !slices.Contains(plan.Requires, "arkoren.dev/driver/sqlite@v0.1.0") {
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

	requireUsage(t, err, "--with accepts reset, verification, two-factor, tokens, permissions, account, google, github")
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

// TestAddIAMReportsWhatItCannotDo lists the migrator, environment and key steps.
func TestAddIAMReportsWhatItCannotDo(t *testing.T) {
	t.Parallel()

	plan, err := load(t, scaffoldProject(t)).AddIAM(layout.IAMOptions{With: allIAMFeatures}, templates)
	if err != nil {
		t.Fatal(err)
	}

	notes := stepTexts(plan.Steps)

	for _, want := range []string{
		"Migrations()", "DATABASE_URL", "PASSWORD_RESET_URL", "EMAIL_VERIFY_URL", "LINK_KEY_FILE", "TWO_FACTOR_KEY_FILE", "OAUTH_KEY_FILE",
		"GOOGLE_CLIENT_ID", "GITHUB_CLIENT_SECRET", "worker",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes miss %q:\n%s", want, notes)
		}
	}
}

// TestAddOAuthWritesTheClientProvider creates the provider and the key it needs,
// and tells how to hand the client to the identity module.
func TestAddOAuthWritesTheClientProvider(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddOAuth("google", templates)
	apply(t, project, plan, err)

	if !slices.Equal(created(plan), []string{"internal/bootstrap/google.go", "internal/bootstrap/oauth_key.go"}) || len(plan.Edit) != 0 {
		t.Fatalf("plan = %+v", plan)
	}

	if google := read(t, root, "internal/bootstrap/google.go"); !strings.Contains(google, "compose.Startup(client.Start)") ||
		!strings.Contains(google, "var Google = application.Provide(") {
		t.Fatalf("google.go:\n%s", google)
	}

	if !slices.Equal(plan.Requires, []string{"arkoren.dev/ark/oauth@v0.7.0", "arkoren.dev/driver/aes@v0.1.0", "arkoren.dev/foundation/key@v0.1.1"}) {
		t.Fatalf("requires = %v", plan.Requires)
	}

	if !strings.Contains(stepTexts(plan.Steps), "identity.Provider(compose.Use(Google))") {
		t.Fatalf("steps = %v", plan.Steps)
	}
}

// TestAddOAuthRefusesUnknownAndExistingProviders names the supported providers.
func TestAddOAuthRefusesUnknownAndExistingProviders(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)

	_, err := project.AddOAuth("myspace", templates)
	requireUsage(t, err, "supports google and github")

	plan, err := project.AddOAuth("github", templates)
	apply(t, project, plan, err)

	_, err = load(t, root).AddOAuth("github", templates)
	requireUsage(t, err, "internal/bootstrap already declares GitHub")
}
