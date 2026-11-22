package layout

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/scaffold"
)

// namePattern accepts lowercase words of letters and digits joined by single
// hyphens or underscores, starting with a letter.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`)

// featurePattern accepts a feature name: lowercase letters and digits only,
// starting with a letter, because it becomes a Go package name.
var featurePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// reservedFeatureNames cannot name a feature: internal directories with their
// own role, and package names the generated surface files import.
var reservedFeatureNames = []string{
	"bootstrap", "migrations", "platform", "application", "router", "http", "context", "time",
	"queue", "workflow", "mcp", "schedule", "event", "web", "jobs", "flow", "tools", "events", "schedules",
	"request", "response", "problem", "errormap", "query",
}

// surfaceExtras are the files a surface child gets beside <surface>.go, as
// template suffix and file name: web declares its problems in problems.go.
var surfaceExtras = map[string][][2]string{
	"web": {{"web_problems", "problems"}},
}

// Plan is the set of changes one `arkoren add` operation would make: new files
// and single-point insertions into existing files. Nothing is written until
// [Project.Apply].
type Plan struct {
	// Create lists new files, relative to the project root. Apply refuses to
	// overwrite an existing path.
	Create []files.File
	// Edit lists existing files changed by inserting lines at one anchor.
	Edit []Edit
}

// Edit is one insertion into an existing file.
type Edit struct {
	// Path is the slash-separated file path relative to the project root.
	Path string
	// Inserted describes each inserted line, such as a registration call.
	Inserted []string
	// original is the content the edit was computed from; Apply refuses to
	// write if the file changed since.
	original []byte
	// content is the complete edited file, gofmt-formatted.
	content []byte
}

// FeatureOptions selects what [Project.AddFeature] creates.
type FeatureOptions struct {
	// Name is the feature and package name: lowercase letters and digits.
	Name string
	// Surfaces are the child packages to create, a subset of web, jobs,
	// flow, tools, events and schedules.
	Surfaces []string
	// Process names the cmd/<process> whose Compose registers the surfaces.
	// Empty selects "server". It is unused when Surfaces is empty.
	Process string
}

// surfaceFunctions names the registration function each surface exports.
var surfaceFunctions = map[string]string{
	"web": "Routes", "jobs": "Handlers", "flow": "Workflows", "tools": "Tools", "events": "Listeners", "schedules": "Entries",
}

// AddFeature plans internal/<name>: the root files (<name>.go, service.go,
// store.go, feature.go with DefaultStore and DefaultService) and one child
// package per requested surface exporting a registration function (web also
// gets problems.go mapping the root's ErrNotFound to a problem), plus the
// calls and imports that register each surface in the Compose of the chosen
// process. Child packages are imported with the alias <name><surface>.
//
// Surface stubs compile as generated. Where a working registration needs a
// driver the application has not chosen (queue consumer, workflow engine,
// MCP endpoint, scheduler), the stub defines the job, workflow, tool or entry
// and leaves a TODO in an empty registration function that says what to add.
//
// It returns a usage error when the name is invalid or reserved, internal/<name>
// exists, a surface is unknown, or the process's Compose cannot be edited. It
// never changes the project; call [Project.Apply] on the plan.
//
// Example:
//
//	plan, err := project.AddFeature(layout.FeatureOptions{Name: "orders", Surfaces: []string{"web"}}, templates)
//	if err != nil {
//		return err
//	}
//	err = project.Apply(ctx, plan)
func (project *Project) AddFeature(options FeatureOptions, templates fs.FS) (*Plan, error) {
	name := options.Name

	if !featurePattern.MatchString(name) || token.IsKeyword(name) {
		return nil, console.UsageError("feature name %q must be lowercase letters and digits, start with a letter and not be a Go keyword", name)
	}

	if slices.Contains(reservedFeatureNames, name) || slices.Contains(bannedNames, name) {
		return nil, console.UsageError("feature name %q is reserved or names a role, not a purpose", name)
	}

	if err := project.refuseExisting("internal/" + name); err != nil {
		return nil, err
	}

	data := map[string]string{"Module": project.Module, "Feature": name}
	plan := &Plan{}
	process := cmp.Or(options.Process, "server")

	for _, file := range [][2]string{{"root", name}, {"service", "service"}, {"store", "store"}, {"feature", "feature"}} {
		err := project.render(plan, templates, "add/feature/"+file[0]+".go", "internal/"+name+"/"+file[1]+".go", data)

		if err != nil {
			return nil, err
		}
	}

	var registrations []registration

	for _, surface := range Surfaces {
		if !slices.Contains(options.Surfaces, surface) {
			continue
		}

		err := project.render(plan, templates, "add/feature/"+surface+".go", "internal/"+name+"/"+surface+"/"+surface+".go", data)

		if err != nil {
			return nil, err
		}

		for _, extra := range surfaceExtras[surface] {
			err := project.render(plan, templates, "add/feature/"+extra[0]+".go", "internal/"+name+"/"+surface+"/"+extra[1]+".go", data)

			if err != nil {
				return nil, err
			}
		}

		registrations = append(registrations, registration{
			importPath: project.Module + "/internal/" + name + "/" + surface,
			alias:      name + surface,
			function:   surfaceFunctions[surface],
		})
	}

	if len(registrations) == 0 {
		return plan, nil
	}

	target := project.composeFile(process)

	if target == "" {
		return nil, console.UsageError("process %q not found: no Compose in internal/bootstrap/%s (create it with arkoren add process %s)",
			process, process, process)
	}

	return plan, project.planEdit(plan, target, func(content []byte) ([]byte, []string, error) {
		return addRegistrations(target, content, project.Module, registrations)
	})
}

// composeFile returns the file declaring the Compose of process: a non-test
// file of the process root internal/bootstrap/<process>, or, in projects that
// still declare it there, cmd/<process>/main.go. It returns "" when neither
// declares Compose.
func (project *Project) composeFile(process string) string {
	legacy := ""

	for _, src := range project.sources {
		if src.test || composeDecl(src.file) == nil {
			continue
		}

		if src.dir == "internal/bootstrap/"+process {
			return src.rel
		}

		if src.rel == "cmd/"+process+"/main.go" {
			legacy = src.rel
		}
	}

	return legacy
}

// AddProcess plans a new process: internal/bootstrap/<name>/compose.go with
// an empty exported Compose, and cmd/<name>/main.go, which builds the
// application from bootstrap.Application and runs that Compose. The name is
// lowercase words of letters and digits joined by hyphens; the package name
// drops the hyphens. Neither directory may exist.
//
// Example:
//
//	plan, err := project.AddProcess("worker", templates)
func (project *Project) AddProcess(name string, templates fs.FS) (*Plan, error) {
	if !namePattern.MatchString(name) || strings.ContainsRune(name, '_') {
		return nil, console.UsageError("process name %q must be lowercase letters and digits, optionally joined by hyphens", name)
	}

	for _, dir := range []string{"cmd/" + name, "internal/bootstrap/" + name} {
		if err := project.refuseExisting(dir); err != nil {
			return nil, err
		}
	}

	pkg := strings.ReplaceAll(name, "-", "")

	if token.IsKeyword(pkg) {
		return nil, console.UsageError("process name %q is a Go keyword", name)
	}

	plan := &Plan{}
	data := map[string]string{"Module": project.Module, "Process": name, "Package": pkg}

	if err := project.render(plan, templates, "add/process/compose.go", "internal/bootstrap/"+name+"/compose.go", data); err != nil {
		return nil, err
	}

	return plan, project.render(plan, templates, "add/process/main.go", "cmd/"+name+"/main.go", data)
}

// AddProvider plans internal/bootstrap/<name>.go: an exported provider named
// for its resource (name "mail-queue" declares MailQueue) initialized with
// application.Provide. The stub builds an empty placeholder value and a TODO
// explains how to replace it. The name may use hyphens or underscores between
// words. The file and the exported identifier must not already exist in
// internal/bootstrap.
//
// Example:
//
//	plan, err := project.AddProvider("database", templates)
func (project *Project) AddProvider(name string, templates fs.FS) (*Plan, error) {
	if !namePattern.MatchString(name) {
		return nil, console.UsageError("provider name %q must be lowercase words of letters and digits joined by hyphens or underscores", name)
	}

	identifier := camel(name)

	if project.declares("internal/bootstrap", identifier) {
		return nil, console.UsageError("internal/bootstrap already declares %s", identifier)
	}

	plan := &Plan{}
	data := map[string]string{"Name": identifier, "Words": strings.Join(words(name), " ")}
	target := "internal/bootstrap/" + strings.ReplaceAll(name, "-", "_") + ".go"

	return plan, project.render(plan, templates, "add/provider.go", target, data)
}

// AddMigration plans internal/migrations/<sequence>_<name>.go and registers the
// migration in All. The sequence is four digits, one more than the highest
// numbered file already in the directory, so files sort in running order. The
// migration id is the name with hyphens; the exported variable is its
// CamelCase form. The stub extracts its schema-builder operations into exported
// variables beside the migration: a name starting with "create" creates (and
// on Down drops) the table named by the remaining words, any other name alters
// that table with an empty change that fails until a TODO is resolved.
//
// If internal/migrations declares All, the new variable is appended to the
// slice literal it returns. Otherwise migrations.go is created with All; an
// existing migrations.go without All is refused. A migration with the same id
// or identifier is refused.
//
// Example:
//
//	plan, err := project.AddMigration("create_orders", templates)
func (project *Project) AddMigration(name string, templates fs.FS) (*Plan, error) {
	if !namePattern.MatchString(name) {
		return nil, console.UsageError("migration name %q must be lowercase words of letters and digits joined by hyphens or underscores", name)
	}

	const dir = "internal/migrations"

	identifier := camel(name)
	id := strings.ReplaceAll(name, "_", "-")

	if project.declares(dir, identifier) {
		return nil, console.UsageError("%s already declares %s", dir, identifier)
	}

	var existing Migrations

	for _, src := range project.packageSources(dir) {
		mapMigrations(&existing, src)
	}

	if slices.Contains(existing.IDs, id) {
		return nil, console.UsageError("%s already has a migration with id %q", dir, id)
	}

	sequence, err := project.nextSequence(dir)

	if err != nil {
		return nil, err
	}

	plan := &Plan{}
	data := map[string]string{"Name": identifier, "ID": id, "Table": strings.Join(words(name), "_")}

	if parts := words(name); len(parts) > 1 {
		data["Table"] = strings.Join(parts[1:], "_")

		if parts[0] == "create" {
			data["Create"] = "true"
		}
	}
	target := dir + "/" + fmt.Sprintf("%04d_%s.go", sequence, strings.ReplaceAll(name, "-", "_"))

	if err := project.render(plan, templates, "add/migration/migration.go", target, data); err != nil {
		return nil, err
	}

	for _, src := range project.packageSources(dir) {
		if declaresAll(src.file) {
			return plan, project.planEdit(plan, src.rel, func(content []byte) ([]byte, []string, error) {
				updated, err := addListElement(src.rel, content, identifier)

				return updated, []string{identifier}, err
			})
		}
	}

	if err := project.refuseExisting(dir + "/migrations.go"); err != nil {
		return nil, console.UsageError("%s/migrations.go exists without an All function; add `func All() []migrate.Definition` first", dir)
	}

	if err := project.render(plan, templates, "add/migration/migrations.go", dir+"/migrations.go", data); err != nil {
		return nil, err
	}

	created := &plan.Create[len(plan.Create)-1]
	created.Content, err = addListElement(dir+"/migrations.go", created.Content, identifier)

	return plan, err
}

// declaresAll reports whether file declares the top-level function All.
func declaresAll(file *ast.File) bool {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "All" && fn.Recv == nil {
			return true
		}
	}

	return false
}

// refuseExisting returns a usage error when rel, a slash path relative to the
// project root, exists (including as a dangling symlink).
func (project *Project) refuseExisting(rel string) error {
	_, err := os.Lstat(filepath.Join(project.Root, filepath.FromSlash(rel)))

	if err == nil {
		return console.UsageError("%s already exists; arkoren add never overwrites", rel)
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", rel, err)
	}

	return nil
}

// render appends the file rendered from the embedded template to the plan
// after checking its target does not exist.
func (project *Project) render(plan *Plan, templates fs.FS, template, target string, data map[string]string) error {
	if err := project.refuseExisting(target); err != nil {
		return err
	}

	content, err := scaffold.RenderFile(template, data, templates)

	if err != nil {
		return fmt.Errorf("render %s: %w", target, err)
	}

	plan.Create = append(plan.Create, files.File{Path: filepath.FromSlash(target), Content: content})

	return nil
}

// planEdit reads rel, lets edit compute the new content and the inserted
// lines, and appends the result to the plan.
func (project *Project) planEdit(plan *Plan, rel string, edit func([]byte) ([]byte, []string, error)) error {
	original, err := os.ReadFile(filepath.Join(project.Root, filepath.FromSlash(rel)))

	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}

	content, inserted, err := edit(original)

	if err != nil {
		return err
	}

	plan.Edit = append(plan.Edit, Edit{Path: rel, Inserted: inserted, original: original, content: content})

	return nil
}

// declares reports whether a non-test file of the package in dir declares a
// top-level function, variable, constant or type called name.
func (project *Project) declares(dir, name string) bool {
	for _, src := range project.packageSources(dir) {
		for _, decl := range src.file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil && decl.Name.Name == name {
					return true
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.ValueSpec:
						if slices.ContainsFunc(spec.Names, func(ident *ast.Ident) bool { return ident.Name == name }) {
							return true
						}
					case *ast.TypeSpec:
						if spec.Name.Name == name {
							return true
						}
					}
				}
			}
		}
	}

	return false
}

// nextSequence returns one more than the highest leading number of the
// "<digits>_*.go" files in dir, or 1 when there are none or dir is missing.
func (project *Project) nextSequence(dir string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(project.Root, filepath.FromSlash(dir)))

	if errors.Is(err, fs.ErrNotExist) {
		return 1, nil
	}

	if err != nil {
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}

	highest := 0

	for _, entry := range entries {
		digits, _, found := strings.Cut(entry.Name(), "_")

		if number, err := strconv.Atoi(digits); found && err == nil {
			highest = max(highest, number)
		}
	}

	return highest + 1, nil
}

// words splits a hyphen or underscore separated name into its words.
func words(name string) []string {
	return strings.FieldsFunc(name, func(character rune) bool { return character == '-' || character == '_' })
}

// camel joins the words of name in CamelCase: "mail-queue" is "MailQueue".
func camel(name string) string {
	var identifier strings.Builder

	for _, word := range words(name) {
		identifier.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}

	return identifier.String()
}

// Apply writes a plan: new files first, created exclusively so an existing path
// is never replaced, then the edits of existing files. Creating before editing
// means a collision stops the operation before any user code is touched.
//
// Each edit is rechecked against the content it was planned from and refused
// if the file changed since, so Apply never overwrites concurrent work. An
// edit replaces the file through a temporary file in the same directory and
// keeps its permissions; symlinks and non-regular files are refused. A failure
// partway can leave earlier new files in place; the error names the failing path.
//
// Example:
//
//	if err := project.Apply(ctx, plan); err != nil {
//		return err
//	}
func (project *Project) Apply(ctx context.Context, plan *Plan) error {
	if err := files.Write(ctx, project.Root, plan.Create); err != nil {
		return err
	}

	for _, edit := range plan.Edit {
		if err := project.writeEdit(ctx, edit); err != nil {
			return fmt.Errorf("edit %s: %w", edit.Path, err)
		}
	}

	return nil
}

// writeEdit replaces one file with its edited content, atomically within its
// directory.
func (project *Project) writeEdit(ctx context.Context, edit Edit) error {
	full := filepath.Join(project.Root, filepath.FromSlash(edit.Path))
	root, err := files.OpenDirectory(ctx, filepath.Dir(full), false)

	if err != nil {
		return err
	}

	// This closes only the directory handle; file Close errors are returned below.
	defer root.Close()

	name := path.Base(edit.Path)
	info, err := root.Lstat(name)

	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return console.UsageError("%s is a symlink or not a regular file", edit.Path)
	}

	current, err := root.ReadFile(name)

	if err != nil {
		return err
	}

	if !bytes.Equal(current, edit.original) {
		return console.UsageError("%s changed while arkoren add was planning; rerun the command", edit.Path)
	}

	temporary := name + ".arkoren-edit"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())

	if err != nil {
		return err
	}

	_, err = file.Write(edit.content)

	if err = errors.Join(err, file.Close()); err != nil {
		return errors.Join(err, root.Remove(temporary))
	}

	return root.Rename(temporary, name)
}
