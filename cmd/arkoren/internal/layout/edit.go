package layout

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// textEdit inserts text at a byte offset of the original content.
type textEdit struct {
	offset int
	text   string
}

// applyEdits returns content with every edit inserted, then gofmt-formatted.
// Edits are applied from the highest offset down so earlier offsets stay
// valid. The formatted result is also the syntax check: if an insertion broke
// the file, the error says so instead of writing it.
func applyEdits(content []byte, edits []textEdit) ([]byte, error) {
	slices.SortStableFunc(edits, func(left, right textEdit) int { return right.offset - left.offset })

	for _, edit := range edits {
		content = slices.Concat(content[:edit.offset], []byte(edit.text), content[edit.offset:])
	}

	formatted, err := format.Source(content)

	if err != nil {
		return nil, fmt.Errorf("format edited source: %w", err)
	}

	return formatted, nil
}

// endOfLine returns the offset of the newline that ends the line holding
// offset, or the end of content when the last line has no newline.
func endOfLine(content []byte, offset int) int {
	if index := bytes.IndexByte(content[offset:], '\n'); index >= 0 {
		return offset + index
	}

	return len(content)
}

// registration is one call to insert into Compose: the function of the
// package at importPath, imported under alias.
type registration struct {
	importPath string
	alias      string
	function   string
}

// addRegistrations returns the content of a cmd/<process>/main.go with one
// `alias.Function(compose)` call per registration inserted into Compose, plus
// the imports they need, and the inserted call texts.
//
// The calls go after the last top-level statement that precedes the first
// bootstrap tail: a statement calling a function of internal/bootstrap, or an
// if statement whose body does (such as `if compose.App().Debug()` around
// bootstrap.Diagnostics). That keeps routes registered before diagnostics list
// them and before bootstrap.Listen serves the router. Without such a statement
// the calls are appended after the last statement; in an empty Compose they
// become its only statements. An import already present keeps its local name.
// A new import goes after the last import of this module, or the last import,
// of a parenthesized import block.
//
// It returns a usage error naming rel when Compose is missing or does not take
// a named *application.Composer, when imports are not parenthesized, or when
// an alias collides with another import; nothing is partially edited.
func addRegistrations(rel string, content []byte, module string, registrations []registration) ([]byte, []string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, content, parser.ParseComments|parser.SkipObjectResolution)

	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", rel, err)
	}

	app := importName(file, applicationPackage)
	compose := composeDecl(file)

	if compose == nil || composerParamName(compose.Type, app) == "" {
		return nil, nil, console.UsageError("%s has no func Compose(compose *application.Composer); "+
			"arkoren add inserts registration calls only into that function", rel)
	}

	param := composerParamName(compose.Type, app)
	offset := func(pos token.Pos) int { return fset.Position(pos).Offset }

	var edits []textEdit
	var imports, calls []string

	for _, reg := range registrations {
		name := importName(file, reg.importPath)

		if name == "" {
			name = reg.alias

			if slices.ContainsFunc(file.Imports, func(spec *ast.ImportSpec) bool { return importSpecName(spec) == name }) {
				return nil, nil, console.UsageError("%s already imports another package as %s", rel, name)
			}

			imports = append(imports, name+" "+strconv.Quote(reg.importPath))
		}

		calls = append(calls, name+"."+reg.function+"("+param+")")
	}

	if len(imports) > 0 {
		anchor := importAnchor(file, module)

		if anchor == nil {
			return nil, nil, console.UsageError("%s has no parenthesized import block to extend", rel)
		}

		edits = append(edits, textEdit{endOfLine(content, offset(anchor.End())), "\n\t" + strings.Join(imports, "\n\t")})
	}

	edits = append(edits, composeEdit(content, compose, importName(file, module+"/internal/bootstrap"), calls, offset))

	updated, err := applyEdits(content, edits)

	return updated, calls, err
}

// composeDecl returns the top-level function Compose, or nil.
func composeDecl(file *ast.File) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Compose" && fn.Recv == nil && fn.Body != nil {
			return fn
		}
	}

	return nil
}

// composeEdit builds the insertion of calls into compose, as described on
// [addRegistrations].
func composeEdit(content []byte, compose *ast.FuncDecl, bootstrap string, calls []string, offset func(token.Pos) int) textEdit {
	statements := compose.Body.List
	anchor := slices.IndexFunc(statements, func(statement ast.Stmt) bool { return callsBootstrap(statement, bootstrap) })

	if anchor < 0 {
		anchor = len(statements)
	}

	text := "\n\t" + strings.Join(calls, "\n\t")

	if anchor == 0 {
		return textEdit{offset(compose.Body.Lbrace) + 1, text}
	}

	return textEdit{endOfLine(content, offset(statements[anchor-1].End())), text}
}

// callsBootstrap reports whether statement is a call of a function of the
// bootstrap package, or an if statement containing one.
func callsBootstrap(statement ast.Stmt, bootstrap string) bool {
	if bootstrap == "" {
		return false
	}

	found := false

	ast.Inspect(statement, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}

		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == bootstrap {
					found = true
				}
			}
		}

		return !found
	})

	switch statement.(type) {
	case *ast.ExprStmt, *ast.IfStmt:
		return found
	}

	return false
}

// importSpecName returns the name an import spec binds in the file.
func importSpecName(spec *ast.ImportSpec) string {
	if spec.Name != nil {
		return spec.Name.Name
	}

	value, err := strconv.Unquote(spec.Path.Value)

	if err != nil {
		return ""
	}

	return path.Base(value)
}

// importAnchor returns the import spec after which a new import goes: the last
// import of module, else the last import of the first parenthesized block.
// It returns nil when the file has no parenthesized import block.
func importAnchor(file *ast.File, module string) *ast.ImportSpec {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)

		if !ok || gen.Tok != token.IMPORT || !gen.Lparen.IsValid() {
			continue
		}

		anchor := gen.Specs[len(gen.Specs)-1].(*ast.ImportSpec)

		for _, spec := range gen.Specs {
			if value, _ := strconv.Unquote(spec.(*ast.ImportSpec).Path.Value); strings.HasPrefix(value, module+"/") {
				anchor = spec.(*ast.ImportSpec)
			}
		}

		return anchor
	}

	return nil
}

// addListElement returns content with element appended to the slice literal
// returned by the function All, formatted. The literal may be empty, written
// on one line, or multi-line with or without a trailing comma. It returns a
// usage error naming rel when All does not return a single composite literal.
func addListElement(rel string, content []byte, element string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, content, parser.SkipObjectResolution)

	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}

	literal := returnedLiteral(file, "All")

	if literal == nil {
		return nil, console.UsageError("%s: func All must return a slice literal for arkoren add to append to", rel)
	}

	tokens := fset.File(literal.Pos())
	point := tokens.Offset(literal.Lbrace) + 1
	prefix := ""

	var edits []textEdit

	if count := len(literal.Elts); count > 0 {
		// A list written on one line moves its first element to its own line.
		if tokens.Line(literal.Lbrace) == tokens.Line(literal.Elts[0].Pos()) {
			edits = append(edits, textEdit{point, "\n\t"})
		}

		point = tokens.Offset(literal.Elts[count-1].End())
		rest := bytes.TrimLeft(content[point:], " \t")

		if bytes.HasPrefix(rest, []byte(",")) {
			point += len(content[point:]) - len(rest) + 1
		} else {
			prefix = ","
		}
	}

	text := prefix + "\n\t" + element + ","

	if tokens.Line(tokens.Pos(point)) == tokens.Line(literal.Rbrace) {
		text += "\n"
	}

	return applyEdits(content, append(edits, textEdit{point, text}))
}

// returnedLiteral returns the composite literal of the first single-result
// return statement in the top-level function name, or nil.
func returnedLiteral(file *ast.File, name string) *ast.CompositeLit {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)

		if !ok || fn.Name.Name != name || fn.Recv != nil || fn.Body == nil {
			continue
		}

		for _, statement := range fn.Body.List {
			if result, ok := statement.(*ast.ReturnStmt); ok && len(result.Results) == 1 {
				literal, _ := result.Results[0].(*ast.CompositeLit)

				return literal
			}
		}
	}

	return nil
}
