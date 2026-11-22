package layout_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/layout"
	"arkoren.dev/cmd/arkoren/internal/scaffold"
)

// templates is the cmd/arkoren directory, which holds templates/.
var templates = os.DirFS("../..")

// scaffoldProject writes a freshly generated project (module example.com/shop,
// no agent files, no dependency resolution) into a temporary directory.
func scaffoldProject(t *testing.T) string {
	t.Helper()

	// Resolve aliases such as macOS /var: file creation refuses symlinked parents.
	temporary, err := filepath.EvalSymlinks(t.TempDir())

	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(temporary, "shop")
	options := scaffold.Options{Folder: root, Module: "example.com/shop", NoAgentFiles: true, SkipTidy: true, SkillsDir: ".agents/skills"}
	inventory, err := scaffold.Render(t.Context(), options, templates, nil)

	if err != nil {
		t.Fatal(err)
	}

	if err := files.Write(t.Context(), root, inventory); err != nil {
		t.Fatal(err)
	}

	return root
}

// load loads the project in root or fails the test.
func load(t *testing.T, root string) *layout.Project {
	t.Helper()

	project, err := layout.Load(t.Context(), root)

	if err != nil {
		t.Fatal(err)
	}

	return project
}

// apply applies plan, failing on error.
func apply(t *testing.T, project *layout.Project, plan *layout.Plan, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}

	if err := project.Apply(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

// read returns the content of rel under root.
func read(t *testing.T, root, rel string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))

	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}

// requireUsage fails unless err is a usage error containing text.
func requireUsage(t *testing.T, err error, text string) {
	t.Helper()

	exit, ok := errors.AsType[*console.ExitError](err)

	if !ok || exit.Code != console.ExitUsage || !strings.Contains(err.Error(), text) {
		t.Fatalf("error = %v, want usage error containing %q", err, text)
	}
}

// TestLoadRefusesDirectoryWithoutLayout explains the layout and names the skill.
func TestLoadRefusesDirectoryWithoutLayout(t *testing.T) {
	t.Parallel()

	_, err := layout.Load(t.Context(), t.TempDir())

	requireUsage(t, err, "arkoren-structure")
}

// TestLoadRefusesMissingBootstrap needs internal/bootstrap, not only cmd.
func TestLoadRefusesMissingBootstrap(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/x\n")
	writeFile(t, root, "cmd/server/main.go", "package main\n")

	_, err := layout.Load(t.Context(), root)

	requireUsage(t, err, "internal/bootstrap")
}

// TestLoadReportsSyntaxError names the file instead of skipping it.
func TestLoadReportsSyntaxError(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/orders/orders.go", "package orders\nfunc (\n")

	if _, err := layout.Load(t.Context(), root); err == nil || !strings.Contains(err.Error(), "orders.go") {
		t.Fatalf("error = %v", err)
	}
}

// TestMapScaffold lists the generated process, providers and welcome feature.
func TestMapScaffold(t *testing.T) {
	t.Parallel()

	text := load(t, scaffoldProject(t)).Map().Text()

	for _, want := range []string{
		"module example.com/shop\n",
		"process server: welcomeweb.Routes(compose); bootstrap.Diagnostics(compose) [if compose.App().Debug()]; bootstrap.Listen(compose)\n",
		"bootstrap providers: Router\n",
		"bootstrap register: Listen; Diagnostics\n",
		"feature welcome above: web(Routes)\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("map text missing %q:\n%s", want, text)
		}
	}
}

// TestMapShowsAddedFeatureAndMigrations covers providers, surfaces and ids.
func TestMapShowsAddedFeatureAndMigrations(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web", "jobs"}}, templates)
	apply(t, project, plan, err)

	project = load(t, root)
	plan, err = project.AddMigration("create_orders", templates)
	apply(t, project, plan, err)

	text := load(t, root).Map().Text()

	for _, want := range []string{
		"feature orders providers: DefaultStore; DefaultService\n",
		"feature orders above: jobs(Handlers); web(Routes)\n",
		"migrations: All\nmigration ids: create-orders\n",
		"ordersweb.Routes(compose); ordersjobs.Handlers(compose); bootstrap.Diagnostics",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("map text missing %q:\n%s", want, text)
		}
	}
}

// TestAddFeatureRegistersSurfacesBeforeBootstrapTail inserts after existing
// routes and before diagnostics, with aliased imports in the module group.
func TestAddFeatureRegistersSurfacesBeforeBootstrapTail(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web", "events"}}, templates)
	apply(t, project, plan, err)

	compose := read(t, root, "internal/bootstrap/server/compose.go")

	for _, want := range []string{
		"\tordersevents \"example.com/shop/internal/orders/events\"\n",
		"\tordersweb \"example.com/shop/internal/orders/web\"\n",
		"\twelcomeweb.Routes(compose)\n\tordersweb.Routes(compose)\n\tordersevents.Listeners(compose)\n\n\tif compose.App().Debug() {",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("compose.go missing %q:\n%s", want, compose)
		}
	}

	if len(plan.Create) != 7 || len(plan.Edit) != 1 || plan.Edit[0].Path != "internal/bootstrap/server/compose.go" {
		t.Fatalf("plan = %+v", plan)
	}
}

// TestAddFeatureWithoutSurfacesEditsNothing creates only the root files.
func TestAddFeatureWithoutSurfacesEditsNothing(t *testing.T) {
	t.Parallel()

	project := load(t, scaffoldProject(t))
	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders"}, templates)

	if err != nil || len(plan.Create) != 4 || len(plan.Edit) != 0 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
}

// TestAddFeatureRefusesExistingDirectory never overwrites.
func TestAddFeatureRefusesExistingDirectory(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders"}, templates)
	apply(t, project, plan, err)

	_, err = load(t, root).AddFeature(layout.FeatureOptions{Name: "orders"}, templates)

	requireUsage(t, err, "already exists")
}

// TestAddFeatureRejectsInvalidNames covers syntax, keywords, reserved and banned names.
func TestAddFeatureRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	project := load(t, scaffoldProject(t))

	for _, name := range []string{"Orders", "my-orders", "1st", "func", "bootstrap", "util", "router", ""} {
		if _, err := project.AddFeature(layout.FeatureOptions{Name: name}, templates); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
}

// TestAddFeatureRefusesUnknownProcess names the missing file.
func TestAddFeatureRefusesUnknownProcess(t *testing.T) {
	t.Parallel()

	project := load(t, scaffoldProject(t))
	_, err := project.AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web"}, Process: "worker"}, templates)

	requireUsage(t, err, "internal/bootstrap/worker")
}

// TestAddFeatureRefusesMissingCompose explains what edit it needs.
func TestAddFeatureRefusesMissingCompose(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/server/compose.go", "package server\n")

	_, err := load(t, root).AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web"}}, templates)

	requireUsage(t, err, "no Compose in internal/bootstrap/server")
}

// TestAddFeatureAppendsWithoutBootstrapTail appends to an unusual Compose.
func TestAddFeatureAppendsWithoutBootstrapTail(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/server/compose.go", `package server

import (
	"arkoren.dev/core/application"
)

// Compose registers nothing yet.
func Compose(compose *application.Composer) {}
`)

	project := load(t, root)
	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web"}}, templates)
	apply(t, project, plan, err)

	compose := read(t, root, "internal/bootstrap/server/compose.go")

	if !strings.Contains(compose, "func Compose(compose *application.Composer) {\n\tordersweb.Routes(compose)\n}") ||
		!strings.Contains(compose, "\tordersweb \"example.com/shop/internal/orders/web\"\n") {
		t.Fatalf("compose.go:\n%s", compose)
	}
}

// TestAddFeatureEditsComposeInMain supports projects that still declare
// Compose in cmd/<process>/main.go and have no process root.
func TestAddFeatureEditsComposeInMain(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)

	if err := os.RemoveAll(filepath.Join(root, "internal/bootstrap/server")); err != nil {
		t.Fatal(err)
	}

	writeFile(t, root, "cmd/server/main.go", `package main

import (
	"arkoren.dev/core/application"
)

func main() {}

// Compose registers nothing yet.
func Compose(compose *application.Composer) {}
`)

	project := load(t, root)
	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web"}}, templates)
	apply(t, project, plan, err)

	if main := read(t, root, "cmd/server/main.go"); !strings.Contains(main, "func Compose(compose *application.Composer) {\n\tordersweb.Routes(compose)\n}") {
		t.Fatalf("main.go:\n%s", main)
	}
}

// TestAddProcessCreatesEmptyCompose generates a process root and a main
// package that runs it; hyphens are dropped from the package name.
func TestAddProcessCreatesEmptyCompose(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddProcess("worker", templates)
	apply(t, project, plan, err)

	compose := read(t, root, "internal/bootstrap/worker/compose.go")
	main := read(t, root, "cmd/worker/main.go")

	if !strings.Contains(compose, "package worker\n") || !strings.Contains(compose, "func Compose(compose *application.Composer) {}") {
		t.Fatalf("compose.go:\n%s", compose)
	}

	if !strings.Contains(main, "\t\"example.com/shop/internal/bootstrap/worker\"\n") || !strings.Contains(main, "app.Run(ctx, worker.Compose)") ||
		strings.Contains(main, "func Compose") {
		t.Fatalf("main.go:\n%s", main)
	}

	plan, err = load(t, root).AddProcess("admin-tasks", templates)
	apply(t, project, plan, err)

	if main := read(t, root, "cmd/admin-tasks/main.go"); !strings.Contains(main, "\tadmintasks \"example.com/shop/internal/bootstrap/admin-tasks\"\n") {
		t.Fatalf("hyphenated main.go:\n%s", main)
	}

	if _, err := load(t, root).AddProcess("worker", templates); err == nil {
		t.Fatal("existing process accepted")
	}
}

// TestAddProviderDeclaresResourceNamedProvider derives the identifier and
// refuses a duplicate declaration.
func TestAddProviderDeclaresResourceNamedProvider(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddProvider("mail-queue", templates)
	apply(t, project, plan, err)

	if content := read(t, root, "internal/bootstrap/mail_queue.go"); !strings.Contains(content, "var MailQueue = application.Provide(") {
		t.Fatalf("provider:\n%s", content)
	}

	project = load(t, root)

	if _, err := project.AddProvider("mail_queue", templates); err == nil {
		t.Fatal("duplicate provider accepted")
	}

	_, err = project.AddProvider("router", templates)

	requireUsage(t, err, "already declares Router")
}

// TestAddMigrationCreatesAllThenAppends numbers files and registers in order.
func TestAddMigrationCreatesAllThenAppends(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)

	for _, name := range []string{"create_orders", "add-order-status"} {
		project := load(t, root)
		plan, err := project.AddMigration(name, templates)
		apply(t, project, plan, err)
	}

	all := read(t, root, "internal/migrations/migrations.go")

	if !strings.Contains(all, "return []migrate.Definition{\n\t\tCreateOrders,\n\t\tAddOrderStatus,\n\t}") {
		t.Fatalf("migrations.go:\n%s", all)
	}

	if content := read(t, root, "internal/migrations/0002_add_order_status.go"); !strings.Contains(content, `migrate.NewMigration("add-order-status"`) {
		t.Fatalf("second migration:\n%s", content)
	}

	_, err := load(t, root).AddMigration("create-orders", templates)

	requireUsage(t, err, "already declares CreateOrders")
}

// TestAddMigrationAppendsToExistingAll handles a hand-written one-line list
// with and without a trailing comma.
func TestAddMigrationAppendsToExistingAll(t *testing.T) {
	t.Parallel()

	for _, literal := range []string{"{Existing}", "{\n\t\tExisting,\n\t}", "{Existing,}"} {
		root := scaffoldProject(t)
		writeFile(t, root, "internal/migrations/all.go", "package migrations\n\nimport \"arkoren.dev/core/service/database/migrate\"\n\n"+
			"var Existing = migrate.NewMigration(\"existing\")\n\nfunc All() []migrate.Definition {\n\treturn []migrate.Definition"+literal+"\n}\n")

		project := load(t, root)
		plan, err := project.AddMigration("create_orders", templates)
		apply(t, project, plan, err)

		if all := read(t, root, "internal/migrations/all.go"); !strings.Contains(all, "\t\tExisting,\n\t\tCreateOrders,\n\t}") {
			t.Fatalf("literal %q gave:\n%s", literal, all)
		}

		if _, err := os.Stat(filepath.Join(root, "internal/migrations/migrations.go")); err == nil {
			t.Fatal("created a second All")
		}
	}
}

// TestApplyRefusesChangedEdit protects files modified between plan and write.
func TestApplyRefusesChangedEdit(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web"}}, templates)

	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, root, "internal/bootstrap/server/compose.go", read(t, root, "internal/bootstrap/server/compose.go")+"\n// changed\n")

	requireUsage(t, project.Apply(t.Context(), plan), "changed while")
}
