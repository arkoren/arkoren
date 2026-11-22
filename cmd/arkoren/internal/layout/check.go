package layout

import (
	"cmp"
	"go/ast"
	"slices"
	"strconv"
	"strings"
)

// Rule names reported in [Violation.Rule]. They are stable identifiers for
// scripts and agents that filter the check output.
const (
	// RuleBootstrapImport marks internal/bootstrap importing a feature.
	RuleBootstrapImport = "bootstrap-imports-feature"
	// RuleSurfaceImport marks a surface child imported by something other than cmd.
	RuleSurfaceImport = "surface-imported-outside-cmd"
	// RuleBelowImport marks a child below a feature importing that feature.
	RuleBelowImport = "below-child-imports-feature"
	// RuleDriverImport marks feature or platform code importing a driver or ark/iam.
	RuleDriverImport = "feature-imports-driver"
	// RuleBannedName marks a package path segment that names a role.
	RuleBannedName = "banned-package-name"
	// RuleComposerUse marks Composer.Use called outside a provider or registration function body.
	RuleComposerUse = "composer-use-outside-composition"
	// RuleBootstrapNew marks a bootstrap.New* call outside internal/bootstrap.
	RuleBootstrapNew = "bootstrap-new-outside-bootstrap"
)

// Violation is one broken layout rule at a source position.
type Violation struct {
	// Rule is one of the Rule constants.
	Rule string `json:"rule"`
	// File is the slash-separated path relative to the project root.
	File string `json:"file"`
	// Line is the 1-based line of the offending import, call or package.
	Line int `json:"line"`
	// Message explains the violation and names the fix.
	Message string `json:"message"`
}

// Check reports every violation of the structure skill's mechanical rules,
// sorted by file and line. An empty result means the project conforms.
//
// Rules:
//   - the internal/bootstrap package never imports a feature (root, child or
//     below); its process roots, internal/bootstrap/<process>, may;
//   - a surface child (web, jobs, flow, tools, events, schedules,
//     <feature>test) is imported only by process roots and cmd packages;
//     _test.go files may import any child, since fixtures exist for tests;
//   - a child below a feature never imports that feature;
//   - feature and platform packages never import arkoren.dev/driver/... or
//     arkoren.dev/ark/iam: only bootstrap wires vendors;
//   - no package path below cmd/ or internal/ contains a banned role name
//     such as util, common, models or handlers;
//   - Composer.Use is called only directly in the body of a function that
//     declares a *application.Composer parameter (a provider's build function,
//     a registration function, a New*(compose, ...) constructor), never inside
//     a method, nor inside a closure the function creates, because a closure
//     runs after composition (handlers, Startup and Run callbacks);
//   - bootstrap.New* functions are called only from internal/bootstrap.
//
// The Use rule is a syntactic heuristic: it recognizes a Use call whose
// receiver is a parameter declared as *application.Composer. A Composer
// stored in a struct field or passed on to another function is not followed.
//
// Example:
//
//	for _, violation := range project.Check() {
//		fmt.Printf("%s:%d: %s\n", violation.File, violation.Line, violation.Message)
//	}
func (project *Project) Check() []Violation {
	var violations []Violation

	add := func(rule, file string, line int, message string) {
		violations = append(violations, Violation{Rule: rule, File: file, Line: line, Message: message})
	}

	for _, src := range project.sources {
		project.checkImports(src, add)
		project.checkCalls(src, add)
	}

	project.checkNames(add)

	slices.SortStableFunc(violations, func(left, right Violation) int {
		return cmp.Or(strings.Compare(left.File, right.File), cmp.Compare(left.Line, right.Line))
	})

	return violations
}

// checkImports applies the import-direction rules to one file.
func (project *Project) checkImports(src *source, add func(rule, file string, line int, message string)) {
	from := classify(src.dir)

	for _, spec := range src.file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)

		if err != nil {
			continue
		}

		line := project.line(spec.Pos())
		vendor := strings.HasPrefix(importPath, "arkoren.dev/driver/") ||
			importPath == "arkoren.dev/ark/iam" || strings.HasPrefix(importPath, "arkoren.dev/ark/iam/")

		if vendor && slices.Contains([]kind{kindFeature, kindAbove, kindBelow, kindPlatform}, from.kind) {
			add(RuleDriverImport, src.rel, line, "import "+importPath+": only internal/bootstrap wires drivers and ark/iam; "+
				"depend on the core contract and obtain the value through a bootstrap provider")
		}

		rel, local := strings.CutPrefix(importPath, project.Module+"/")

		if !local {
			continue
		}

		to := classify(rel)
		toFeature := slices.Contains([]kind{kindFeature, kindAbove, kindBelow}, to.kind)

		if from.kind == kindBootstrap && toFeature {
			add(RuleBootstrapImport, src.rel, line, "internal/bootstrap imports feature package "+rel+
				": bootstrap never imports a feature; register features from a process root, internal/bootstrap/<process>")
		}

		if to.kind == kindAbove && from.kind != kindProcess && from.kind != kindCmd && !src.test {
			add(RuleSurfaceImport, src.rel, line, "import "+rel+": a surface child is imported only by process roots; "+
				"register it from the process's Compose in internal/bootstrap/<process>")
		}

		if from.kind == kindBelow && to.feature == from.feature && (to.kind == kindFeature || to.kind == kindAbove) {
			add(RuleBelowImport, src.rel, line, "import "+rel+": a child below the root imports nothing from its feature; "+
				"move the shared type below the root or the caller above it")
		}
	}
}

// checkNames reports banned role names in the directories that hold Go files
// below cmd/ and internal/. Each directory is reported once, at the package
// clause of its first file.
func (project *Project) checkNames(add func(rule, file string, line int, message string)) {
	seen := map[string]bool{}

	for _, src := range project.sources {
		if seen[src.dir] {
			continue
		}

		seen[src.dir] = true

		for segment := range strings.SplitSeq(src.dir, "/") {
			if slices.Contains(bannedNames, segment) {
				add(RuleBannedName, src.rel, project.line(src.file.Name.Pos()), "package path "+src.dir+" uses the role name "+
					segment+"; name the package for its purpose")
			}
		}
	}
}

// checkCalls applies the call-site rules (Composer.Use placement and
// bootstrap.New* ownership) to one file.
func (project *Project) checkCalls(src *source, add func(rule, file string, line int, message string)) {
	app := importName(src.file, applicationPackage)
	bootstrap := importName(src.file, project.Module+"/internal/bootstrap")

	if bootstrap != "" && classify(src.dir).kind != kindBootstrap {
		ast.Inspect(src.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)

			if !ok {
				return true
			}

			selector, ok := call.Fun.(*ast.SelectorExpr)

			if !ok {
				return true
			}

			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == bootstrap && strings.HasPrefix(selector.Sel.Name, "New") {
				add(RuleBootstrapNew, src.rel, project.line(call.Pos()), "bootstrap."+selector.Sel.Name+
					" owns lifecycle and is called only from internal/bootstrap; use a bootstrap provider with compose.Use")
			}

			return true
		})
	}

	if app == "" {
		return
	}

	for _, decl := range src.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			project.checkUse(src, fn.Type, fn.Body, fn.Recv != nil, nil, app, add)
		}
	}

	// Package-level initializers hold provider build functions and other
	// literals; walk them as functions without a receiver.
	ast.Inspect(src.file, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncDecl); ok {
			return false
		}

		if lit, ok := node.(*ast.FuncLit); ok {
			project.checkUse(src, lit.Type, lit.Body, false, nil, app, add)

			return false
		}

		return true
	})
}

// useScope describes one function on the lexical stack of checkUse.
type useScope struct {
	// composer is the name of the function's *application.Composer
	// parameter, or "" when it has none.
	composer string
	// method is true for a function declared with a receiver.
	method bool
}

// checkUse walks one function body with the enclosing function scopes. A call
// X.Use(...) counts when X names the Composer parameter of some enclosing
// function. It is allowed only when that function is the innermost one (not a
// nested closure) and is not a method.
func (project *Project) checkUse(
	src *source, typ *ast.FuncType, body *ast.BlockStmt, method bool, outer []useScope, app string,
	add func(rule, file string, line int, message string),
) {
	scopes := append(slices.Clone(outer), useScope{composer: composerParamName(typ, app), method: method})
	current := len(scopes) - 1

	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncLit:
			project.checkUse(src, node.Type, node.Body, false, scopes, app, add)

			return false
		case *ast.CallExpr:
			selector, ok := node.Fun.(*ast.SelectorExpr)

			if !ok || selector.Sel.Name != "Use" {
				return true
			}

			receiver, ok := selector.X.(*ast.Ident)

			if !ok {
				return true
			}

			// Search from the innermost scope: the closest scope naming the receiver wins.
			owner := -1

			for index := current; index >= 0; index-- {
				if scopes[index].composer == receiver.Name {
					owner = index

					break
				}
			}

			switch {
			case owner < 0:
			case owner != current:
				add(RuleComposerUse, src.rel, project.line(node.Pos()), receiver.Name+".Use inside a closure: the closure runs after "+
					"composition; call Use at the top of the function that owns the Composer and capture the result")
			case scopes[current].method:
				add(RuleComposerUse, src.rel, project.line(node.Pos()), receiver.Name+".Use inside a method: services and stores "+
					"receive their dependencies from the provider; call Use in the provider's build function")
			}
		}

		return true
	})
}
