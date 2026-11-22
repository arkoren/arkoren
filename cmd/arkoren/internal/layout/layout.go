// Package layout reads and extends a project that follows the structure
// described by the arkoren-structure skill: one cmd/<process>/main.go per
// process, run with the App and Compose of its process root
// internal/bootstrap/<process>; internal/bootstrap holding only providers;
// one plain-Go internal/<feature> directory per feature, wired by
// internal/bootstrap/<feature>.go; internal/migrations and internal/platform.
//
// The package analyzes source with go/parser only. It never type-checks,
// builds, or reaches the network, so it works on a project whose dependencies
// are not downloaded and reports on code that does not compile yet. The three
// consumers are `arkoren check` ([Project.Check]), `arkoren map`
// ([Project.Map]) and `arkoren add` (the Add* methods and [Project.Apply]).
//
// Load a project with [Load]; a directory that lacks the layout is refused with
// a usage error that points at the skill.
package layout

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// applicationPackage is the import path of the package whose Composer,
// Provide and Provider the layout rules recognize.
const applicationPackage = "arkoren.dev/core/application"

// Surfaces lists the child packages that sit above a feature root, in the
// order flags and providers are presented. Each imports the root and is
// imported only by internal/bootstrap. The `<feature>test` fixtures package is
// also above the root but is created by hand, not by `arkoren add`.
var Surfaces = []string{"web", "jobs", "flow", "tools", "events", "schedules"}

// bannedNames are package names the structure skill rejects because they name
// a role instead of a purpose.
var bannedNames = []string{
	"util", "common", "shared", "helpers", "models", "controllers", "services", "handlers", "repositories",
}

// kind classifies a package directory by its place in the layout.
type kind int

const (
	// The zero kind is any directory the layout assigns no role.
	_ kind = iota
	// kindCmd is a package below cmd/.
	kindCmd
	// kindBootstrap is the internal/bootstrap package itself.
	kindBootstrap
	// kindProcess is a process root, internal/bootstrap/<process> or anything
	// below it: it holds the process's App provider and Compose.
	kindProcess
	// kindMigrations is internal/migrations.
	kindMigrations
	// kindPlatform is internal/platform and anything below it.
	kindPlatform
	// kindFeature is the root package internal/<feature>.
	kindFeature
	// kindAbove is a child of a feature that imports the root: web, jobs,
	// flow, tools, events, schedules or <feature>test.
	kindAbove
	// kindBelow is any other child of a feature: it is imported by the root
	// and imports nothing from the feature.
	kindBelow
)

// class is the layout role of one package directory.
type class struct {
	kind    kind
	feature string
}

// classify maps a slash-separated directory relative to the project root to
// its layout role. Directories outside cmd/ and internal/ are kindOther.
func classify(dir string) class {
	parts := strings.Split(dir, "/")

	if parts[0] == "cmd" {
		return class{kind: kindCmd}
	}

	if parts[0] != "internal" || len(parts) < 2 {
		return class{}
	}

	switch parts[1] {
	case "bootstrap":
		if len(parts) > 2 {
			return class{kind: kindProcess}
		}

		return class{kind: kindBootstrap}
	case "migrations":
		return class{kind: kindMigrations}
	case "platform":
		return class{kind: kindPlatform}
	}

	feature := parts[1]

	if len(parts) == 2 {
		return class{kind: kindFeature, feature: feature}
	}

	if parts[2] == feature+"test" || slices.Contains(Surfaces, parts[2]) {
		return class{kind: kindAbove, feature: feature}
	}

	return class{kind: kindBelow, feature: feature}
}

// source is one parsed Go file of the project.
type source struct {
	// rel is the slash-separated file path relative to the project root.
	rel string
	// dir is the slash-separated directory of rel.
	dir string
	// file is the parsed syntax tree, without comments or object resolution.
	file *ast.File
	// test is true for _test.go files.
	test bool
}

// Project is a loaded project: its module path and the parsed Go files below
// cmd/ and internal/. It is read-only after [Load]; the Add* methods return
// plans instead of changing it.
type Project struct {
	// Root is the absolute project directory.
	Root string
	// Module is the module path declared in go.mod.
	Module string

	fset    *token.FileSet
	sources []*source
}

// errNotConforming is the usage error for a directory without the layout. It
// names the missing piece and points at the skill that defines the layout.
func errNotConforming(reason string) error {
	return console.UsageError(
		"not an Arkoren project layout: %s; the layout needs go.mod, cmd/<process>/main.go and internal/bootstrap "+
			"(see the arkoren-structure skill; arkoren new creates it)", reason)
}

// Load reads the project in dir. A project conforms when dir holds go.mod,
// at least one cmd/<process>/main.go and an internal/bootstrap directory;
// otherwise Load returns a usage error that explains this and names the
// arkoren-structure skill. Only dir itself is inspected: parent directories
// are never searched.
//
// Go files below cmd/ and internal/ are parsed, skipping directories whose
// names start with "." or "_" and testdata. A syntax error in any of them is
// returned as an error naming the file and line, because the analyses would
// otherwise silently skip that file.
//
// Example:
//
//	project, err := layout.Load(ctx, ".")
//	if err != nil {
//		return err
//	}
//	violations := project.Check()
func Load(ctx context.Context, dir string) (*Project, error) {
	// Resolve platform aliases such as macOS /var once, because file creation
	// below the root refuses symlinked ancestors.
	root, err := filepath.EvalSymlinks(dir)

	if err == nil {
		root, err = filepath.Abs(root)
	}

	if err != nil {
		return nil, fmt.Errorf("resolve project directory: %w", err)
	}

	module, err := readModule(root)

	if err != nil {
		return nil, err
	}

	processes, err := filepath.Glob(filepath.Join(root, "cmd", "*", "main.go"))

	if err != nil || len(processes) == 0 {
		return nil, errNotConforming("no cmd/<process>/main.go")
	}

	if info, err := os.Stat(filepath.Join(root, "internal", "bootstrap")); err != nil || !info.IsDir() {
		return nil, errNotConforming("no internal/bootstrap directory")
	}

	project := &Project{Root: root, Module: module, fset: token.NewFileSet()}

	for _, top := range []string{"cmd", "internal"} {
		if err := project.parseTree(ctx, top); err != nil {
			return nil, err
		}
	}

	return project, nil
}

// readModule returns the module path of the go.mod in root.
func readModule(root string) (string, error) {
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))

	if errors.Is(err, fs.ErrNotExist) {
		return "", errNotConforming("no go.mod")
	}

	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}

	for line := range strings.Lines(string(content)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module")

		if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
			continue
		}

		rest, _, _ = strings.Cut(rest, "//")
		rest = strings.TrimSpace(rest)

		if module, err := strconv.Unquote(rest); err == nil {
			return module, nil
		}

		return rest, nil
	}

	return "", errNotConforming("go.mod declares no module")
}

// parseTree parses every Go file below the top-level directory top.
func (project *Project) parseTree(ctx context.Context, top string) error {
	base := filepath.Join(project.Root, top)

	return filepath.WalkDir(base, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		name := entry.Name()

		if entry.IsDir() {
			if file != base && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata") {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(name, ".go") {
			return nil
		}

		parsed, err := parser.ParseFile(project.fset, file, nil, parser.SkipObjectResolution)

		if err != nil {
			return fmt.Errorf("parse %s: %w", file, err)
		}

		rel, err := filepath.Rel(project.Root, file)

		if err != nil {
			return err
		}

		rel = filepath.ToSlash(rel)
		project.sources = append(project.sources, &source{
			rel: rel, dir: path.Dir(rel), file: parsed, test: strings.HasSuffix(name, "_test.go"),
		})

		return nil
	})
}

// line returns the 1-based line of pos.
func (project *Project) line(pos token.Pos) int {
	return project.fset.Position(pos).Line
}

// packageSources returns the non-test files of the package in dir.
func (project *Project) packageSources(dir string) []*source {
	var found []*source

	for _, src := range project.sources {
		if src.dir == dir && !src.test {
			found = append(found, src)
		}
	}

	return found
}

// importName returns the local name file gives the package at importPath, or
// "" when it is not imported (or imported for side effects or with a dot).
func importName(file *ast.File, importPath string) string {
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)

		if err != nil || value != importPath {
			continue
		}

		if spec.Name == nil {
			return path.Base(importPath)
		}

		if spec.Name.Name == "_" || spec.Name.Name == "." {
			return ""
		}

		return spec.Name.Name
	}

	return ""
}

// isComposerParam reports whether field is declared as *application.Composer,
// where app is the file's local name for the application package.
func isComposerParam(field *ast.Field, app string) bool {
	star, ok := field.Type.(*ast.StarExpr)

	if !ok {
		return false
	}

	selector, ok := star.X.(*ast.SelectorExpr)

	if !ok {
		return false
	}

	pkg, ok := selector.X.(*ast.Ident)

	return ok && pkg.Name == app && selector.Sel.Name == "Composer"
}

// composerParamName returns the name of the first *application.Composer
// parameter of typ, or "" when it has none or the parameter is unnamed.
func composerParamName(typ *ast.FuncType, app string) string {
	for _, field := range typ.Params.List {
		if isComposerParam(field, app) && len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}

	return ""
}

// providerVars returns the names of package-level variables initialized with
// application.Provide, in source order.
func providerVars(file *ast.File, app string) []string {
	var names []string

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)

		if !ok || gen.Tok != token.VAR {
			continue
		}

		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)

			if len(value.Names) == 1 && len(value.Values) == 1 && isProvideCall(value.Values[0], app) {
				names = append(names, value.Names[0].Name)
			}
		}
	}

	return names
}

// isProvideCall reports whether expr is a call of application.Provide,
// with or without explicit type arguments.
func isProvideCall(expr ast.Expr, app string) bool {
	call, ok := expr.(*ast.CallExpr)

	if !ok || app == "" {
		return false
	}

	fun := call.Fun

	if index, ok := fun.(*ast.IndexExpr); ok {
		fun = index.X
	}

	selector, ok := fun.(*ast.SelectorExpr)

	if !ok {
		return false
	}

	pkg, ok := selector.X.(*ast.Ident)

	return ok && pkg.Name == app && selector.Sel.Name == "Provide"
}
