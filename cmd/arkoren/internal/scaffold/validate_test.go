package scaffold

import (
	"testing"
)

// TestNewValidModules accepts local module names, normal remote paths, and Go's
// two version-suffix conventions without adding a module-validation dependency.
func TestNewValidModules(t *testing.T) {
	t.Parallel()

	for _, module := range []string{"hello", "my_app", "example.com/team/App", "example.com/app/v2", "gopkg.in/yaml.v3"} {
		if err := ValidateNewModule(module); err != nil {
			t.Fatalf("module %q: %v", module, err)
		}
	}
}
