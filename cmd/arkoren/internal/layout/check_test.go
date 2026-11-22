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

// TestCheckBootstrapImportsFeature flags bootstrap reaching into a feature.
func TestCheckBootstrapImportsFeature(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orders.go":    "package orders\n",
		"internal/bootstrap/orders.go": "package bootstrap\n\nimport _ \"example.com/shop/internal/orders\"\n",
	})

	requireViolation(t, found, layout.RuleBootstrapImport, "internal/bootstrap/orders.go", 3)
}

// TestCheckProcessRootMayImportFeatures allows internal/bootstrap/<process> to
// import feature roots and surfaces, while the bootstrap package stays clean.
func TestCheckProcessRootMayImportFeatures(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/orders/orders.go":  "package orders\n",
		"internal/orders/web/web.go": "package web\n",
		"internal/bootstrap/worker/compose.go": "package worker\n\nimport (\n\t_ \"example.com/shop/internal/orders\"\n" +
			"\t_ \"example.com/shop/internal/orders/web\"\n)\n",
	})

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckSurfaceImportedOutsideCmd flags a surface child imported by another feature.
func TestCheckSurfaceImportedOutsideCmd(t *testing.T) {
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

// composerFile builds a file in the orders feature with the given declarations.
func composerFile(declarations string) map[string]string {
	return map[string]string{
		"internal/orders/orders.go": "package orders\n\nimport (\n\t\"net/http\"\n\n\t\"arkoren.dev/core/application\"\n)\n\nvar _ http.Handler\n\n" +
			declarations,
	}
}

// TestCheckUseInsideClosure flags Use called from a callback the provider registers.
func TestCheckUseInsideClosure(t *testing.T) {
	t.Parallel()

	found := violations(t, composerFile(`func Routes(compose *application.Composer) {
	compose.Startup(func(ctx context.Context) error {
		compose.Use(Default)
		return nil
	})
}
`))

	requireViolation(t, found, layout.RuleComposerUse, "internal/orders/orders.go", 13)
}

// TestCheckUseInsideMethod flags Use called from a method on a service.
func TestCheckUseInsideMethod(t *testing.T) {
	t.Parallel()

	found := violations(t, composerFile(`type Service struct{}

func (service *Service) Load(compose *application.Composer) {
	compose.Use(Default)
}
`))

	requireViolation(t, found, layout.RuleComposerUse, "internal/orders/orders.go", 14)
}

// TestCheckUseInProviderAndRegistration allows Use at the top of provider
// builds, Action literals and registration functions.
func TestCheckUseInProviderAndRegistration(t *testing.T) {
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

func Routes(compose *application.Composer) {
	compose.Use(Default)
}
`))

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckUseIgnoresOtherReceivers ignores router.Use and similar names.
func TestCheckUseIgnoresOtherReceivers(t *testing.T) {
	t.Parallel()

	found := violations(t, composerFile(`func Routes(compose *application.Composer, routes interface{ Use(int) }) {
	routes.Use(1)
}
`))

	if len(found) != 0 {
		t.Fatalf("violations = %+v", found)
	}
}

// TestCheckBootstrapNewOutsideBootstrap flags lifecycle constructors called by features.
func TestCheckBootstrapNewOutsideBootstrap(t *testing.T) {
	t.Parallel()

	found := violations(t, map[string]string{
		"internal/bootstrap/database.go": "package bootstrap\n\nfunc NewDatabase() {}\n",
		"internal/orders/orders.go":      "package orders\n\nimport \"example.com/shop/internal/bootstrap\"\n\nfunc init() {\n\tbootstrap.NewDatabase()\n}\n",
	})

	requireViolation(t, found, layout.RuleBootstrapNew, "internal/orders/orders.go", 6)

	if !strings.Contains(found[0].Message, "bootstrap.NewDatabase") {
		t.Fatalf("message = %q", found[0].Message)
	}
}
