package layout

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
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

// addUses returns the content of a process root's file with one
// `compose.Use(bootstrap.Provider)` statement per provider inserted into
// Compose, the bootstrap import if the file lacks it, and the inserted
// statement texts. A provider the Compose already uses is skipped; when every
// provider is already used the content is returned unchanged with no statements.
//
// The statements go before the tail of Compose: the first top-level `if`
// statement (such as `if compose.App().Debug()` around bootstrap.MCP) or
// `compose.Use(bootstrap.Server)` or `compose.Use(bootstrap.Work)`, whichever
// comes first. Without a tail they
// are appended after the last statement; in an empty Compose they become its
// only statements. An existing bootstrap import keeps its local name. A new one
// goes into the import block as its own group after the last non-module import,
// or after the last import of this module.
//
// It returns a usage error naming rel when Compose is missing or does not take
// a named *application.Composer, when imports are not parenthesized, or when
// another package is already imported as bootstrap; nothing is partially edited.
func addUses(rel string, content []byte, module string, providers []string) ([]byte, []string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, content, parser.ParseComments|parser.SkipObjectResolution)

	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", rel, err)
	}

	compose := composeDecl(file)
	param := ""

	if compose != nil {
		param = composerParamName(compose.Type, importName(file, applicationPackage))
	}

	if param == "" {
		return nil, nil, console.UsageError("%s has no func Compose(compose *application.Composer); "+
			"arkoren add inserts compose.Use calls only into that function", rel)
	}

	bootstrapPath := module + "/internal/bootstrap"
	imported := importName(file, bootstrapPath)
	bootstrap := cmp.Or(imported, "bootstrap")
	offset := func(pos token.Pos) int { return fset.Position(pos).Offset }

	var calls []string

	for _, provider := range providers {
		call := param + ".Use(" + bootstrap + "." + provider + ")"

		if imported == "" || !composeUses(compose, call) {
			calls = append(calls, call)
		}
	}

	if len(calls) == 0 {
		return content, nil, nil
	}

	var edits []textEdit

	if imported == "" {
		edit, err := importEdit(rel, file, content, module, offset)

		if err != nil {
			return nil, nil, err
		}

		edits = append(edits, edit)
	}

	serve := []string{param + ".Use(" + bootstrap + ".Server)", param + ".Use(" + bootstrap + ".Work)"}
	edits = append(edits, composeEdit(content, compose, serve, calls, offset))

	updated, err := applyEdits(content, edits)

	return updated, calls, err
}

// importEdit builds the insertion of the module's internal/bootstrap import into
// file, as its own group after the last non-module import or directly after the
// last import of module. It returns a usage error naming rel when another
// package is already imported as bootstrap or the file has no parenthesized
// import block.
func importEdit(rel string, file *ast.File, content []byte, module string, offset func(token.Pos) int) (textEdit, error) {
	if slices.ContainsFunc(file.Imports, func(spec *ast.ImportSpec) bool { return importSpecName(spec) == "bootstrap" }) {
		return textEdit{}, console.UsageError("%s already imports another package as bootstrap", rel)
	}

	anchor := importAnchor(file, module)

	if anchor == nil {
		return textEdit{}, console.UsageError("%s has no parenthesized import block to extend", rel)
	}

	separator := "\n\t"

	if value, _ := strconv.Unquote(anchor.Path.Value); !strings.HasPrefix(value, module+"/") {
		separator = "\n\n\t"
	}

	return textEdit{endOfLine(content, offset(anchor.End())), separator + strconv.Quote(module+"/internal/bootstrap")}, nil
}

// composeUses reports whether the body of compose has a top-level statement
// whose source text is call, such as compose.Use(bootstrap.Work).
func composeUses(compose *ast.FuncDecl, call string) bool {
	return slices.ContainsFunc(compose.Body.List, func(statement ast.Stmt) bool {
		expression, ok := statement.(*ast.ExprStmt)

		return ok && types.ExprString(expression.X) == call
	})
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
// [addUses]. serve lists the source texts of the statements that serve the
// process and so end the list of features.
func composeEdit(content []byte, compose *ast.FuncDecl, serve []string, calls []string, offset func(token.Pos) int) textEdit {
	statements := compose.Body.List
	anchor := slices.IndexFunc(statements, func(statement ast.Stmt) bool {
		if expression, ok := statement.(*ast.ExprStmt); ok {
			return slices.Contains(serve, types.ExprString(expression.X))
		}

		_, conditional := statement.(*ast.IfStmt)

		return conditional
	})

	if anchor < 0 {
		anchor = len(statements)
	}

	text := "\n\t" + strings.Join(calls, "\n\t")

	if anchor == 0 {
		// A blank line keeps the new statements apart from the tail they precede.
		if len(statements) > 0 {
			text += "\n"
		}

		return textEdit{offset(compose.Body.Lbrace) + 1, text}
	}

	return textEdit{endOfLine(content, offset(statements[anchor-1].End())), text}
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
