package remote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// maxIndex is the largest array index a flag may set. Indexes must be dense,
// so a larger one is almost certainly a typo, and refusing it keeps a mistake
// from allocating a huge array.
const maxIndex = 10000

// opArgs is the command line of one operation after the command words,
// separated into the base input, the positional arguments, and the path
// flags in the order they were given.
type opArgs struct {
	// input is the value of -input: inline JSON, @file, or - for stdin.
	input string

	// hasInput distinguishes an explicit empty -input from an omitted flag.
	hasInput bool

	// body retains already-materialized input bytes. Nil means input still names
	// a source; non-nil bytes are data even when malformed JSON starts with @ or
	// equals -. Keeping source text separate prevents recursive source reads.
	body []byte

	// positionals are the bare arguments in order.
	positionals []string

	// flags are the path flags in command-line order.
	flags []flagArg
}

// flagArg is one path flag.
type flagArg struct {
	// name is the dotted path as typed, without dashes.
	name string

	// value is the flag's text: its argument, or "true" for a bare boolean.
	value string
}

// origin ties a JSON Pointer of the request body to the flag or argument that
// set it, so a server error about the pointer can name the flag.
type origin struct {
	// pointer is the escaped JSON Pointer of the value supplied by this source.
	pointer string

	// label is how the user wrote it: "-address.city" or "<invoice>".
	label string
}

// requestInput is the body to send and where its parts came from.
type requestInput struct {
	// root is the schema used for conversion. Retaining the same immutable tree
	// lets wait-budget updates inspect declarations without parsing it again.
	root *Schema

	// body is the JSON object, ready to POST.
	body []byte

	// origins are the pointers set by flags and arguments, in the order they
	// were applied; a later entry for a pointer wins.
	origins []origin
}

// parseOpArgs splits the arguments after the command words. Flags are
// -name value, -name=value, and the same with two dashes; a boolean flag
// takes a value only as -name=value or when the next argument is exactly true
// or false; "--" ends the flags; a bare "-" and a negative number are
// arguments. A flag that names no property of root is a usage error listing
// close names. Flags do not bundle.
func parseOpArgs(root *Schema, operation string, args []string) (opArgs, error) {
	var parsed opArgs
	flagsEnded := false

	for index := 0; index < len(args); index++ {
		arg := args[index]
		_, numberErr := strconv.ParseFloat(arg, 64)

		if flagsEnded || len(arg) < 2 || arg[0] != '-' || numberErr == nil {
			parsed.positionals = append(parsed.positionals, arg)

			continue
		}

		if arg == "--" {
			flagsEnded = true

			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		takesValue := true

		if name != "input" {
			leaf, err := resolveFlag(root, operation, name)

			if err != nil {
				return parsed, err
			}

			takesValue = leaf.kind() != "boolean"
		}

		if !hasValue {
			switch {
			case !takesValue && index+1 < len(args) && (args[index+1] == "true" || args[index+1] == "false"):
				index++
				value = args[index]
			case !takesValue:
				value = "true"
			case index+1 < len(args):
				index++
				value = args[index]
			default:
				return parsed, console.UsageError("flag -%s needs a value", name)
			}
		}

		if name == "input" {
			parsed.input, parsed.hasInput = value, true

			continue
		}

		parsed.flags = append(parsed.flags, flagArg{
			name:  name,
			value: value,
		})
	}

	return parsed, nil
}

// resolveFlag finds the schema a flag path sets. An unknown path is a usage
// error with the closest known flags.
func resolveFlag(root *Schema, operation, name string) (*Schema, error) {
	_, leaf, err := root.flagPath(name)

	if err != nil {
		return nil, unknownFlag(root, operation, name, err)
	}

	return leaf, nil
}

// flagPath resolves exact property names first, then kebab-case aliases for
// snake_case properties. Map keys retain their literal spelling.
func (node *Schema) flagPath(name string) ([]string, *Schema, error) {
	path := strings.Split(name, ".")
	leaf := node

	for index, segment := range path {
		if leaf.Properties[segment] == nil {
			alias := strings.ReplaceAll(segment, "-", "_")

			if leaf.Properties[alias] != nil {
				segment, path[index] = alias, alias
			}
		}

		next, err := leaf.step(segment)

		if err != nil {
			return nil, nil, err
		}

		leaf = next
	}

	return path, leaf, nil
}

// unknownFlag builds the usage error for a flag that is not an input
// property, suggesting the nearest paths or, when nothing is near, the
// top-level properties.
func unknownFlag(root *Schema, operation, name string, cause error) error {
	var paths []string

	for _, info := range root.flags("", 0) {
		if !strings.Contains(info.path, "<") {
			paths = append(paths, info.path)
		}
	}

	hint := "run 'arkoren " + operation + " --help' to list them"

	if near := closest(name, paths, 3); len(near) > 0 {
		hint = "did you mean -" + strings.Join(near, ", -") + "?"
	}

	return console.UsageError("unknown flag -%s for %s (%v); %s", name, operation, cause, hint)
}

// buildInput combines the base -input, the positional arguments, and the path
// flags into one JSON object. Positionals fill the property names listed in
// positional in order, then flags apply in command-line order, so a later
// source overrides an earlier one at the same path. Values are converted by
// the schema type at their path and nothing else is validated: the server is
// the only validator. dir is the base of a relative @file, and stdin is read
// for "-input -".
func buildInput(root *Schema, positional []string, args opArgs, dir string, stdin io.Reader) (*requestInput, error) {
	var tree any = map[string]any{}

	if args.hasInput {
		raw, err := args.inputBytes(dir, stdin)

		if err != nil {
			return nil, err
		}

		tree, err = parseTree(raw)

		if err != nil {
			return nil, console.UsageError("-input is not valid JSON: %v", err)
		}

		if _, ok := tree.(map[string]any); !ok {
			return nil, console.UsageError("-input must be a JSON object")
		}
	}

	if len(args.positionals) > len(positional) {
		return nil, console.UsageError("too many arguments: takes %d positional arguments, got %d; use --help for argument order", len(positional), len(args.positionals))
	}

	result := &requestInput{}

	apply := func(path []string, text, label string) error {
		added := -1
		var err error

		tree, err = assign(tree, root, path, func(leaf *Schema, existing any) (any, error) {
			value, index, err := convertFlag(leaf, existing, text)
			added = index

			return value, err
		})

		if err != nil {
			return console.UsageError("%s: %v", label, err)
		}

		pointer := pointerOf(path)

		if added >= 0 {
			pointer += "/" + strconv.Itoa(added)
		}

		result.origins = append(result.origins, origin{
			pointer: pointer,
			label:   label,
		})

		return nil
	}

	for index, text := range args.positionals {
		if err := apply([]string{positional[index]}, text, "<"+positional[index]+">"); err != nil {
			return nil, err
		}
	}

	for _, flag := range args.flags {
		path, _, err := root.flagPath(flag.name)

		if err != nil {
			return nil, console.UsageError("-%s: %v", flag.name, err)
		}

		if err := apply(path, flag.value, "-"+flag.name); err != nil {
			return nil, err
		}
	}

	if err := checkDense(tree, nil); err != nil {
		return nil, err
	}

	body, err := json.Marshal(tree, json.Deterministic(true))

	if err != nil {
		return nil, fmt.Errorf("encode input: %w", err)
	}

	result.body = body

	return result, nil
}

// inputBytes respects the materialization boundary: retained bytes are final
// data, not another path/stdin selector. Unmaterialized callers retain the
// original readInputSource behavior, with errors returned before JSON parsing.
func (args opArgs) inputBytes(dir string, stdin io.Reader) ([]byte, error) {
	if args.body != nil {
		return args.body, nil
	}

	return readInputSource(args.input, dir, stdin)
}

// readInputSource returns the text of an -input value.
func readInputSource(source, dir string, stdin io.Reader) ([]byte, error) {
	switch {
	case source == "-":
		data, err := io.ReadAll(stdin)

		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}

		return data, nil
	case strings.HasPrefix(source, "@"):
		path := source[1:]

		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}

		data, err := os.ReadFile(path)

		if err != nil {
			return nil, console.UsageError("-input: %v", err)
		}

		return data, nil
	}

	return []byte(source), nil
}

// errConflict is returned by [assign] when a path runs through a value of
// the wrong shape that an earlier source put there.
var errConflict = errors.New("conflicts with a value set earlier, which is not an object or array there")

// assign walks tree along path, guided by sch, creating the objects and
// arrays on the way, and replaces the value at the end with what set returns
// for the schema and the existing value (nil when there is none). It returns
// the updated tree; an array index past the end pads with nil holes, which
// [checkDense] rejects if they are still there once every flag applied.
func assign(node any, sch *Schema, path []string, set func(*Schema, any) (any, error)) (any, error) {
	if len(path) == 0 {
		return set(sch, node)
	}

	child, err := sch.step(path[0])

	if err != nil {
		return nil, err
	}

	if sch.kind() == "array" {
		list, ok := node.([]any)

		if node != nil && !ok {
			return nil, errConflict
		}

		// sch.step already validated this segment as a nonnegative integer.
		index, _ := strconv.Atoi(path[0])

		if index > maxIndex {
			return nil, fmt.Errorf("array index %d is too large (at most %d)", index, maxIndex)
		}

		for len(list) <= index {
			list = append(list, nil)
		}

		list[index], err = assign(list[index], child, path[1:], set)

		return list, err
	}

	members, ok := node.(map[string]any)

	if node != nil && !ok {
		return nil, errConflict
	}

	if members == nil {
		members = map[string]any{}
	}

	members[path[0]], err = assign(members[path[0]], child, path[1:], set)

	return members, err
}

// convertFlag turns the text of a flag into the JSON value for its schema.
// An array of scalars given as plain text (not a JSON array) appends one
// element to the existing array and reports the new element's index; every
// other value replaces what is there and reports -1.
func convertFlag(sch *Schema, existing any, text string) (value any, appended int, err error) {
	if sch.kind() == "array" && !strings.HasPrefix(strings.TrimSpace(text), "[") {
		items := sch.Items

		if items == nil {
			items = &Schema{}
		}

		if kind := items.kind(); kind == "object" || kind == "array" {
			return nil, -1, errors.New("expects a JSON array")
		}

		list, ok := existing.([]any)

		if existing != nil && !ok {
			return nil, -1, errConflict
		}

		element, err := convertValue(items, text)

		return append(list, element), len(list), err
	}

	value, err = convertValue(sch, text)

	return value, -1, err
}

// convertValue converts text by the schema type: strings as they are,
// integers in decimal without a machine-size limit, JSON numbers without a
// floating-point conversion, booleans as true or false, objects and arrays as
// JSON, and an untyped value as JSON when it parses and as a string otherwise.
func convertValue(sch *Schema, text string) (any, error) {
	switch kind := sch.kind(); kind {
	case "string":
		quoted, err := json.Marshal(text)

		return jsontext.Value(quoted), err
	case "integer", "number":
		return convertNumber(kind, text)
	case "boolean":
		if text != "true" && text != "false" {
			return nil, fmt.Errorf("%q is not true or false", text)
		}

		return jsontext.Value(text), nil
	case "object", "array":
		return convertContainer(kind, text)
	}

	if tree, err := parseTree([]byte(text)); err == nil {
		return tree, nil
	}

	quoted, err := json.Marshal(text)

	return jsontext.Value(quoted), err
}

// convertNumber preserves exact numeric values rather than converting through
// float64 or a machine-sized integer. Decimal integer spelling is normalized;
// number input must already use JSON syntax, with its precision retained.
func convertNumber(kind, text string) (jsontext.Value, error) {
	if kind == "integer" {
		number, ok := new(big.Int).SetString(strings.TrimSpace(text), 10)

		if !ok {
			return nil, fmt.Errorf("%q is not an integer", text)
		}

		return jsontext.Value(number.String()), nil
	}

	number := jsontext.Value(strings.TrimSpace(text))

	if number.Kind() != '0' || !number.IsValid() {
		return nil, fmt.Errorf("%q is not a number", text)
	}

	return number, nil
}

// convertContainer decodes a flag's JSON object or array while retaining exact
// scalar encodings for later path overrides. The requested outer shape must
// match; schema constraints on its contents remain server-side validation.
func convertContainer(kind, text string) (any, error) {
	tree, err := parseTree([]byte(text))

	if err != nil {
		return nil, fmt.Errorf("expects a JSON %s: %w", kind, err)
	}

	_, object := tree.(map[string]any)

	if object != (kind == "object") {
		return nil, fmt.Errorf("expects a JSON %s", kind)
	}

	return tree, nil
}

// parseTree decodes JSON into a tree of map[string]any for objects, []any
// for arrays, and the exact [jsontext.Value] text for scalars, so numbers
// keep their precision.
func parseTree(raw []byte) (any, error) {
	if !jsontext.Value(raw).IsValid() {
		return nil, errors.New("invalid JSON")
	}

	return readTree(jsontext.NewDecoder(strings.NewReader(string(raw))))
}

// readTree reads one value from a decoder positioned at a valid document.
func readTree(decoder *jsontext.Decoder) (any, error) {
	// parseTree validated the in-memory document, so delimiter reads cannot fail.
	switch decoder.PeekKind() {
	case '{':
		_, _ = decoder.ReadToken() // consumes the opening brace
		members := map[string]any{}

		for decoder.PeekKind() != '}' {
			name, err := decoder.ReadToken()

			if err != nil {
				return nil, err
			}

			members[name.String()], err = readTree(decoder)

			if err != nil {
				return nil, err
			}
		}

		_, _ = decoder.ReadToken() // consumes the closing brace

		return members, nil
	case '[':
		_, _ = decoder.ReadToken() // consumes the opening bracket
		list := []any{}

		for decoder.PeekKind() != ']' {
			element, err := readTree(decoder)

			if err != nil {
				return nil, err
			}

			list = append(list, element)
		}

		_, _ = decoder.ReadToken() // consumes the closing bracket

		return list, nil
	}

	value, err := decoder.ReadValue()

	return value.Clone(), err
}

// checkDense reports an array that still has a nil hole, which means a flag
// set an index without setting every lower one.
func checkDense(node any, path []string) error {
	switch typed := node.(type) {
	case map[string]any:
		for _, name := range slices.Sorted(maps.Keys(typed)) {
			if err := checkDense(typed[name], append(slices.Clone(path), name)); err != nil {
				return err
			}
		}
	case []any:
		for index, element := range typed {
			if element == nil {
				return console.UsageError("array %q is missing index %d: indexes must be dense from 0", strings.Join(path, "."), index)
			}

			if err := checkDense(element, append(slices.Clone(path), strconv.Itoa(index))); err != nil {
				return err
			}
		}
	}

	return nil
}

// pointerOf renders path as a JSON Pointer (RFC 6901).
func pointerOf(path []string) string {
	var pointer strings.Builder

	for _, segment := range path {
		pointer.WriteByte('/')
		pointer.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(segment))
	}

	return pointer.String()
}
