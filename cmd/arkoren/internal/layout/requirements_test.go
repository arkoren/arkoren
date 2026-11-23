package layout_test

import (
	"testing"

	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/layout"
)

// TestNeedsTidy requires tidy only for an import whose module go.mod lacks:
// standard-library, project and already-required imports (including indirect
// ones and single-line requires) do not need it.
func TestNeedsTidy(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "go.mod", "module example.com/shop\n\ngo 1.27\n\nrequire arkoren.dev/ark/mcp v0.1.0\n\n"+
		"require (\n\tarkoren.dev/core v0.17.0\n\tarkoren.dev/foundation/problem v0.1.0 // indirect\n)\n")

	project := load(t, root)

	for _, test := range []struct {
		imports string
		want    bool
	}{
		{"\"net/http\"\n\t\"example.com/shop/internal/bootstrap\"", false},
		{"\"arkoren.dev/core/http/router\"\n\t\"arkoren.dev/ark/mcp\"", false},
		{"\"arkoren.dev/foundation/problem\"", false},
		{"\"arkoren.dev/foundation/query\"", true},
		{"\"arkoren.dev/core\"\n\t\"arkoren.dev/coreext/x\"", true},
	} {
		plan := &layout.Plan{Create: []files.File{
			{Path: "README.md", Content: []byte("import \"arkoren.dev/foundation/query\"\n")},
			{Path: "internal/orders/orders.go", Content: []byte("package orders\n\nimport (\n\t" + test.imports + "\n)\n")},
		}}

		got, err := project.NeedsTidy(plan)

		if err != nil || got != test.want {
			t.Fatalf("NeedsTidy(%s) = %v, %v; want %v", test.imports, got, err, test.want)
		}
	}
}
