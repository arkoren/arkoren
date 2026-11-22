package layout_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/layout"
)

// writeFile writes content to rel under root, creating directories.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// violations scaffolds a clean project, adds the given files, and returns what
// Check reports.
func violations(t *testing.T, extra map[string]string) []layout.Violation {
	t.Helper()

	root := scaffoldProject(t)

	for rel, content := range extra {
		writeFile(t, root, rel, content)
	}

	return load(t, root).Check()
}

// requireViolation fails unless exactly one violation of rule exists at file:line.
func requireViolation(t *testing.T, found []layout.Violation, rule, file string, line int) {
	t.Helper()

	var matches []layout.Violation

	for _, violation := range found {
		if violation.Rule == rule {
			matches = append(matches, violation)
		}
	}

	if len(matches) != 1 || matches[0].File != file || matches[0].Line != line {
		t.Fatalf("violations = %+v, want one %s at %s:%d", found, rule, file, line)
	}
}

// TestCheckScaffoldIsClean proves a freshly generated project passes.
func TestCheckScaffoldIsClean(t *testing.T) {
	t.Parallel()

	if found := load(t, scaffoldProject(t)).Check(); len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckBootstrapMayImportFeatures allows the bootstrap package and the
// process roots to import feature roots and surfaces: bootstrap wires features.
func TestCheckBootstrapMayImportFeatures(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orders.go":  "package orders\n",
		"internal/orders/web/web.go": "package web\n",
		"internal/bootstrap/orders.go": "package bootstrap\n\nimport (\n\t_ \"example.com/shop/internal/orders\"\n" +
			"\t_ \"example.com/shop/internal/orders/web\"\n)\n",
		"internal/bootstrap/worker/compose.go": "package worker\n\nimport (\n\t_ \"example.com/shop/internal/orders\"\n" +
			"\t_ \"example.com/shop/internal/orders/web\"\n)\n",
	})

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckFeatureImportsApplication flags feature roots, children and
// platform code importing the application package or internal/bootstrap.
func TestCheckFeatureImportsApplication(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orders.go":       "package orders\n\nimport _ \"arkoren.dev/core/application\"\n",
		"internal/orders/web/web.go":      "package web\n\nimport _ \"example.com/shop/internal/bootstrap\"\n",
		"internal/platform/ui/ui.go":      "package ui\n\nimport _ \"example.com/shop/internal/bootstrap/server\"\n",
		"internal/orders/carrier/send.go": "package carrier\n\nimport _ \"arkoren.dev/core/application\"\n",
	})

	if len(found) != 4 {
		t.Fatalf("violations = %+v", found)
	}

	for _, violation := range found {
		if violation.Rule != layout.RuleFeatureApplication || violation.Line != 3 {
			t.Fatalf("violation = %+v", violation)
		}
	}
}

// TestCheckFeatureTestImportsApplication applies the rule to test files too.
func TestCheckFeatureTestImportsApplication(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orders_test.go": "package orders_test\n\nimport _ \"arkoren.dev/core/application\"\n",
	})

	requireViolation(t, found, layout.RuleFeatureApplication, "internal/orders/orders_test.go", 3)
}

// TestCheckBootstrapActionFunc flags an exported bootstrap function that takes
// a Composer and returns nothing, while providers and value-returning
// constructors pass, as do process roots.
func TestCheckBootstrapActionFunc(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/bootstrap/listen.go": "package bootstrap\n\nimport \"arkoren.dev/core/application\"\n\n" +
			"func Listen(compose *application.Composer) {}\n\n" +
			"func NewThing(compose *application.Composer) int { return 1 }\n\n" +
			"func helper(compose *application.Composer) {}\n\n" +
			"var Thing = application.Provide(func(compose *application.Composer) int { return 1 })\n",
		"internal/bootstrap/worker/compose.go": "package worker\n\nimport \"arkoren.dev/core/application\"\n\n" +
			"func Compose(compose *application.Composer) {}\n",
	})

	requireViolation(t, found, layout.RuleBootstrapAction, "internal/bootstrap/listen.go", 5)
}

// TestCheckSurfaceImportedByBootstrap allows bootstrap providers to import surfaces.
func TestCheckSurfaceImportedByBootstrap(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/web/web.go":   "package web\n",
		"internal/bootstrap/orders.go": "package bootstrap\n\nimport _ \"example.com/shop/internal/orders/web\"\n",
	})

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckSurfaceImportedOutsideBootstrap flags a surface child imported by another feature.
func TestCheckSurfaceImportedOutsideBootstrap(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/web/web.go":  "package web\n",
		"internal/billing/billing.go": "package billing\n\nimport _ \"example.com/shop/internal/orders/web\"\n",
	})

	requireViolation(t, found, layout.RuleSurfaceImport, "internal/billing/billing.go", 3)
}

// TestCheckSurfaceImportedByTestFile allows fixtures packages in tests.
func TestCheckSurfaceImportedByTestFile(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orderstest/fixtures.go": "package orderstest\n",
		"internal/orders/orders_test.go":         "package orders_test\n\nimport _ \"example.com/shop/internal/orders/orderstest\"\n",
	})

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckBelowChildImportsFeature flags a client importing the feature root.
func TestCheckBelowChildImportsFeature(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orders.go":          "package orders\n",
		"internal/orders/carrier/carrier.go": "package carrier\n\nimport _ \"example.com/shop/internal/orders\"\n",
	})

	requireViolation(t, found, layout.RuleBelowImport, "internal/orders/carrier/carrier.go", 3)
}

// TestCheckFeatureImportsDriver flags a feature importing a vendor driver and ark/iam.
func TestCheckFeatureImportsDriver(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orders.go": "package orders\n\nimport (\n\t_ \"arkoren.dev/driver/redis/rediscache\"\n\t_ \"arkoren.dev/ark/iam\"\n)\n",
	})

	if len(found) != 2 || found[0].Rule != layout.RuleDriverImport || found[0].Line != 4 || found[1].Line != 5 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckBootstrapMayImportDrivers keeps the allowed direction unflagged.
func TestCheckBootstrapMayImportDrivers(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/bootstrap/cache.go": "package bootstrap\n\nimport _ \"arkoren.dev/driver/redis/rediscache\"\n",
	})

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckBannedPackageName flags role names anywhere in a package path.
func TestCheckBannedPackageName(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{"internal/platform/util/strings.go": "package util\n"})

	requireViolation(t, found, layout.RuleBannedName, "internal/platform/util/strings.go", 1)
}

// composerFile builds a file in internal/bootstrap with the given declarations.
func composerFile(declarations string) map[string]string {
	return map[string]string{
		"internal/bootstrap/orders.go": "package bootstrap\n\nimport (\n\t\"net/http\"\n\n\t\"arkoren.dev/core/application\"\n)\n\nvar _ http.Handler\n\n" +
			declarations,
	}
}

// TestCheckUseInsideClosure flags Use called from a callback the provider registers.
func TestCheckUseInsideClosure(t *testing.T) {
	t.Parallel()

	found := violations(t, composerFile(`func NewRoutes(compose *application.Composer) int {
	compose.Startup(func(ctx context.Context) error {
		compose.Use(Default)
		return nil
	})

	return 1
}
`))

	requireViolation(t, found, layout.RuleComposerUse, "internal/bootstrap/orders.go", 13)
}

// TestCheckUseInsideMethod flags Use called from a method on a service.
func TestCheckUseInsideMethod(t *testing.T) {
	t.Parallel()

	found := violations(t, composerFile(`type Service struct{}

func (service *Service) Load(compose *application.Composer) {
	compose.Use(Default)
}
`))

	requireViolation(t, found, layout.RuleComposerUse, "internal/bootstrap/orders.go", 14)
}

// TestCheckUseInProviderAndConstructor allows Use at the top of provider
// builds and New* constructors.
func TestCheckUseInProviderAndConstructor(t *testing.T) {
	t.Parallel()

	found := violations(t, composerFile(`var Default = application.Provide(func(compose *application.Composer) int {
	compose.Use(Other)

	return 1
})

var Other = application.Provide(NewOther)

func NewOther(compose *application.Composer) int {
	compose.Use(Default)

	return 2
}

func NewRoutes(compose *application.Composer) int {
	return compose.Use(Default)
}
`))

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckUseIgnoresOtherReceivers ignores router.Use and similar names.
func TestCheckUseIgnoresOtherReceivers(t *testing.T) {
	t.Parallel()

	found := violations(t, composerFile(`func NewRoutes(compose *application.Composer, routes interface{ Use(int) }) int {
	routes.Use(1)

	return 1
}
`))

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckBootstrapNewOutsideBootstrap flags lifecycle constructors called outside the bootstrap package.
func TestCheckBootstrapNewOutsideBootstrap(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/bootstrap/database.go":       "package bootstrap\n\nfunc NewDatabase() {}\n",
		"internal/bootstrap/worker/compose.go": "package worker\n\nimport \"example.com/shop/internal/bootstrap\"\n\nfunc init() {\n\tbootstrap.NewDatabase()\n}\n",
	})

	requireViolation(t, found, layout.RuleBootstrapNew, "internal/bootstrap/worker/compose.go", 6)

	if !strings.Contains(found[0].Message, "bootstrap.NewDatabase") {
		t.Fatalf("message = %q", found[0].Message)
	}
}
