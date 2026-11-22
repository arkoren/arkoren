package layout

import (
	"go/ast"
	"go/types"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Map is the compact inventory printed by `arkoren map`: what each process
// registers, which shared providers and registration functions bootstrap
// offers, which surfaces and providers each feature has, and the migrations.
// It lists names only, so an agent can orient itself before searching.
type Map struct {
	// Module is the module path from go.mod.
	Module string `json:"module"`
	// Processes lists cmd/<process> packages in directory order.
	Processes []Process `json:"processes"`
	// Bootstrap lists the shared providers and registration functions.
	Bootstrap Bootstrap `json:"bootstrap"`
	// Features lists internal/<feature> packages in directory order.
	Features []Feature `json:"features,omitzero"`
	// Migrations describes internal/migrations; the zero value means absent.
	Migrations Migrations `json:"migrations,omitzero"`
	// Platform lists internal/platform/<purpose> packages.
	Platform []string `json:"platform,omitzero"`
}

// Process is one cmd/<process> package.
type Process struct {
	// Name is the directory name below cmd/.
	Name string `json:"name"`
	// Compose lists the calls of the exported Compose function in source
	// order. A call inside an if statement carries the condition, as in
	// "bootstrap.Diagnostics(compose) [if compose.App().Debug()]".
	Compose []string `json:"compose,omitzero"`
}

// Bootstrap is the content of internal/bootstrap.
type Bootstrap struct {
	// Providers are the package-level application.Provide variables.
	Providers []string `json:"providers,omitzero"`
	// Register are the exported functions taking only a *application.Composer.
	Register []string `json:"register,omitzero"`
}

// Feature is one internal/<feature> directory.
type Feature struct {
	// Name is the directory name below internal/.
	Name string `json:"name"`
	// Providers are the application.Provide variables of the root package
	// (conventionally Default<Type>).
	Providers []string `json:"providers,omitzero"`
	// Register are registration functions declared in the root package
	// itself, for a surface that has not been promoted to a child yet.
	Register []string `json:"register,omitzero"`
	// Above are the children that import the root, with their registration
	// functions.
	Above []Surface `json:"above,omitzero"`
	// Below are the children imported by the root.
	Below []string `json:"below,omitzero"`
}

// Surface is a child package above a feature root.
type Surface struct {
	// Name is the child directory name, such as web.
	Name string `json:"name"`
	// Register are its registration functions.
	Register []string `json:"register,omitzero"`
}

// Migrations describes internal/migrations.
type Migrations struct {
	// All is true when an All function is declared.
	All bool `json:"all,omitzero"`
	// IDs are the literal ids passed to NewMigration, in source order.
	IDs []string `json:"ids,omitzero"`
}

// Map builds the project inventory from source alone. Test files are ignored.
// Names follow directory order, so the result is deterministic.
//
// Example:
//
//	inventory := project.Map()
//	fmt.Print(inventory.Text())
func (project *Project) Map() Map {
	inventory := Map{Module: project.Module}
	features := map[string]*Feature{}

	for _, src := range project.sources {
		if src.test {
			continue
		}

		app := importName(src.file, applicationPackage)
		role := classify(src.dir)

		switch role.kind {
		case kindCmd:
			mapProcess(&inventory, src)
		case kindBootstrap:
			if src.dir == "internal/bootstrap" {
				inventory.Bootstrap.Providers = append(inventory.Bootstrap.Providers, providerVars(src.file, app)...)
				inventory.Bootstrap.Register = append(inventory.Bootstrap.Register, registrations(src.file, app)...)
			}
		case kindMigrations:
			mapMigrations(&inventory.Migrations, src)
		case kindPlatform:
			if parts := strings.Split(src.dir, "/"); len(parts) > 2 && !slices.Contains(inventory.Platform, parts[2]) {
				inventory.Platform = append(inventory.Platform, parts[2])
			}
		case kindFeature, kindAbove, kindBelow:
			if features[role.feature] == nil {
				features[role.feature] = &Feature{Name: role.feature}
				inventory.Features = append(inventory.Features, Feature{Name: role.feature})
			}

			mapFeature(features[role.feature], src, role.kind, app)
		}
	}

	for index := range inventory.Features {
		inventory.Features[index] = *features[inventory.Features[index].Name]
	}

	return inventory
}

// mapFeature records what one file contributes to its feature: providers and
// registration functions of the root, or the child package it belongs to.
func mapFeature(feature *Feature, src *source, role kind, app string) {
	parts := strings.Split(src.dir, "/")

	switch role {
	case kindFeature:
		feature.Providers = append(feature.Providers, providerVars(src.file, app)...)
		feature.Register = append(feature.Register, registrations(src.file, app)...)
	case kindBelow:
		if !slices.Contains(feature.Below, parts[2]) {
			feature.Below = append(feature.Below, parts[2])
		}
	case kindAbove:
		index := slices.IndexFunc(feature.Above, func(surface Surface) bool { return surface.Name == parts[2] })

		if index < 0 {
			feature.Above = append(feature.Above, Surface{Name: parts[2]})
			index = len(feature.Above) - 1
		}

		if len(parts) == 3 {
			feature.Above[index].Register = append(feature.Above[index].Register, registrations(src.file, app)...)
		}
	}
}

// registrations returns the exported registration functions of file.
func registrations(file *ast.File, app string) []string {
	var names []string

	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && isRegistration(fn, app) {
			names = append(names, fn.Name.Name)
		}
	}

	return names
}

// mapProcess records the Compose calls of a cmd package file.
func mapProcess(inventory *Map, src *source) {
	name := path.Base(src.dir)
	index := slices.IndexFunc(inventory.Processes, func(process Process) bool { return process.Name == name })

	if index < 0 {
		inventory.Processes = append(inventory.Processes, Process{Name: name})
		index = len(inventory.Processes) - 1
	}

	for _, decl := range src.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Compose" && fn.Recv == nil && fn.Body != nil {
			inventory.Processes[index].Compose = composeCalls(fn.Body.List, "")
		}
	}
}

// composeCalls flattens the call statements of a Compose body. Calls nested in
// an if statement keep their condition so the listing shows what is optional.
func composeCalls(statements []ast.Stmt, condition string) []string {
	var calls []string

	suffix := ""

	if condition != "" {
		suffix = " [" + condition + "]"
	}

	for _, statement := range statements {
		switch statement := statement.(type) {
		case *ast.ExprStmt:
			if call, ok := statement.X.(*ast.CallExpr); ok {
				calls = append(calls, types.ExprString(call)+suffix)
			}
		case *ast.IfStmt:
			test := "if " + types.ExprString(statement.Cond)
			calls = append(calls, composeCalls(statement.Body.List, test)...)

			if block, ok := statement.Else.(*ast.BlockStmt); ok {
				calls = append(calls, composeCalls(block.List, "else of "+test)...)
			}
		}
	}

	return calls
}

// mapMigrations records All and the NewMigration ids found in a migrations file.
func mapMigrations(migrations *Migrations, src *source) {
	for _, decl := range src.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "All" && fn.Recv == nil {
			migrations.All = true
		}
	}

	ast.Inspect(src.file, func(node ast.Node) bool {
		if id, ok := migrationID(node); ok {
			migrations.IDs = append(migrations.IDs, id)
		}

		return true
	})
}

// migrationID returns the literal first argument of a NewMigration call.
func migrationID(node ast.Node) (string, bool) {
	call, ok := node.(*ast.CallExpr)

	if !ok || len(call.Args) == 0 {
		return "", false
	}

	selector, ok := call.Fun.(*ast.SelectorExpr)

	if !ok || selector.Sel.Name != "NewMigration" {
		return "", false
	}

	literal, ok := call.Args[0].(*ast.BasicLit)

	if !ok {
		return "", false
	}

	id, err := strconv.Unquote(literal.Value)

	return id, err == nil
}

// Text renders the inventory as a compact listing, one line per fact, for
// reading in a terminal or an agent's context. The JSON form carries the same
// content.
func (inventory Map) Text() string {
	var out strings.Builder

	line := func(label string, values ...string) {
		if len(values) > 0 {
			out.WriteString(label + " " + strings.Join(values, "; ") + "\n")
		}
	}

	out.WriteString("module " + inventory.Module + "\n")

	for _, process := range inventory.Processes {
		line("process "+process.Name+":", process.Compose...)
	}

	line("bootstrap providers:", inventory.Bootstrap.Providers...)
	line("bootstrap register:", inventory.Bootstrap.Register...)

	for _, feature := range inventory.Features {
		var above []string

		for _, surface := range feature.Above {
			above = append(above, surface.Name+"("+strings.Join(surface.Register, ", ")+")")
		}

		line("feature "+feature.Name+" providers:", feature.Providers...)
		line("feature "+feature.Name+" register:", feature.Register...)
		line("feature "+feature.Name+" above:", above...)
		line("feature "+feature.Name+" below:", feature.Below...)
	}

	if inventory.Migrations.All {
		out.WriteString("migrations: All\n")
		line("migration ids:", inventory.Migrations.IDs...)
	}

	line("platform:", inventory.Platform...)

	return out.String()
}
