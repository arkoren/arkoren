package layout

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// NeedsTidy reports whether the Go files plan creates import a package from a
// module that go.mod does not require yet, so the project builds only after
// `go mod tidy`. Standard-library packages (no dot in the first path element)
// and the project's own packages never need a requirement. A requirement
// marked `// indirect` counts: it already pins the module and its checksums.
//
// Example:
//
//	if needed, err := project.NeedsTidy(plan); err == nil && needed {
//		// run go mod tidy in project.Root
//	}
func (project *Project) NeedsTidy(plan *Plan) (bool, error) {
	content, err := os.ReadFile(filepath.Join(project.Root, "go.mod"))

	if err != nil {
		return false, fmt.Errorf("read go.mod: %w", err)
	}

	required := requiredModules(content)

	for _, created := range plan.Create {
		if !strings.HasSuffix(created.Path, ".go") {
			continue
		}

		file, err := parser.ParseFile(token.NewFileSet(), created.Path, created.Content, parser.ImportsOnly)

		if err != nil {
			return false, fmt.Errorf("parse %s: %w", filepath.ToSlash(created.Path), err)
		}

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)

			if err != nil || !project.external(path) {
				continue
			}

			if !slices.ContainsFunc(required, func(module string) bool {
				return path == module || strings.HasPrefix(path, module+"/")
			}) {
				return true, nil
			}
		}
	}

	return false, nil
}

// external reports whether path belongs to neither the standard library nor
// the project's module.
func (project *Project) external(path string) bool {
	first, _, _ := strings.Cut(path, "/")

	if !strings.Contains(first, ".") {
		return false
	}

	return path != project.Module && !strings.HasPrefix(path, project.Module+"/")
}

// requiredModules returns the module paths of the require directives in a
// go.mod, both single-line and in blocks.
func requiredModules(content []byte) []string {
	var modules []string

	block := false

	for line := range strings.Lines(string(content)) {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)

		switch {
		case len(fields) == 0:
		case block && fields[0] == ")":
			block = false
		case block:
			modules = append(modules, unquote(fields[0]))
		case fields[0] == "require" && len(fields) > 1 && fields[1] == "(":
			block = true
		case fields[0] == "require" && len(fields) > 1:
			modules = append(modules, unquote(fields[1]))
		}
	}

	return modules
}

// unquote returns a go.mod module path without the quotes it may carry.
func unquote(path string) string {
	if unquoted, err := strconv.Unquote(path); err == nil {
		return unquoted
	}

	return path
}
