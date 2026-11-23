package console

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ANSI escape sequences used only by the text presentation.
const (
	colorBold  = "\x1b[1m"
	colorTrue  = "\x1b[32m"
	colorFalse = "\x1b[31m"
)

// node is one decoded JSON value. Object members keep their document order and
// scalars keep their exact text, so the translation never reorders or rounds.
type node struct {
	// kind is the JSON kind: '{', '[', '"', '0', 't', 'f' or 'n'.
	kind byte

	// text is a scalar's display text: unquoted strings, raw numbers.
	text string

	// keys holds an object's member names, parallel to items.
	keys []string

	// items holds an object's member values or an array's elements.
	items []node
}

// Text translates a JSON document into aligned, human-readable text: objects
// become labeled lines and sections, arrays of flat objects become tables, and
// arrays of scalars become comma lists. The result has no trailing newline.
// Invalid JSON is returned unchanged, like [Render].
func Text(raw []byte, color bool) []byte {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	root, err := decodeNode(decoder)

	if err != nil || decoder.PeekKind() != 0 {
		return raw
	}

	writer := &textWriter{color: color}
	writer.value(root, 0)

	return bytes.TrimRight(writer.out.Bytes(), "\n")
}

// Format writes a JSON document in the given style: translated text when the
// style asks for it, laid-out JSON otherwise.
func Format(raw []byte, look Style) []byte {
	if look.Text {
		return Text(raw, look.Color)
	}

	return Render(raw, look)
}

// decodeNode reads one complete value from decoder.
func decodeNode(decoder *jsontext.Decoder) (node, error) {
	kind := decoder.PeekKind()

	if kind != '{' && kind != '[' {
		value, err := decoder.ReadValue()

		if err != nil {
			return node{}, err
		}

		return scalarNode(byte(kind), value)
	}

	if _, err := decoder.ReadToken(); err != nil {
		return node{}, err
	}

	parent := node{kind: byte(kind)}

	for next := decoder.PeekKind(); next != '}' && next != ']'; next = decoder.PeekKind() {
		if err := decodeMember(decoder, &parent); err != nil {
			return node{}, err
		}
	}

	_, err := decoder.ReadToken()

	return parent, err
}

// decodeMember appends one object member or array element to parent.
func decodeMember(decoder *jsontext.Decoder, parent *node) error {
	if parent.kind == '{' {
		name, err := decoder.ReadValue()

		if err != nil {
			return err
		}

		unquoted, err := jsontext.AppendUnquote(nil, name)

		if err != nil {
			return err
		}

		parent.keys = append(parent.keys, string(unquoted))
	}

	child, err := decodeNode(decoder)

	if err != nil {
		return err
	}

	parent.items = append(parent.items, child)

	return nil
}

// scalarNode keeps a scalar's exact text, unquoting strings.
func scalarNode(kind byte, value jsontext.Value) (node, error) {
	if kind != '"' {
		return node{kind: kind, text: string(value)}, nil
	}

	unquoted, err := jsontext.AppendUnquote(nil, value)

	return node{kind: kind, text: string(unquoted)}, err
}

// inline reports whether a value fits on one line: a scalar, an empty
// container, or an array of scalars.
func (value node) inline() bool {
	switch value.kind {
	case '{':
		return len(value.items) == 0
	case '[':
		for _, item := range value.items {
			if item.kind == '{' || item.kind == '[' {
				return false
			}
		}

		return true
	}

	return true
}

// tabular reports whether an array is a non-empty list of objects whose
// members all fit on one line, so it can print as a table.
func (value node) tabular() bool {
	if value.kind != '[' || len(value.items) == 0 {
		return false
	}

	for _, item := range value.items {
		if item.kind != '{' {
			return false
		}

		for _, member := range item.items {
			if !member.inline() {
				return false
			}
		}
	}

	return true
}

// textWriter accumulates translated lines.
type textWriter struct {
	// out receives the text.
	out bytes.Buffer

	// color wraps labels and scalars in ANSI colors.
	color bool
}

// paint returns text wrapped in color when coloring is on.
func (writer *textWriter) paint(color, text string) string {
	if !writer.color || color == "" || text == "" {
		return text
	}

	return color + text + ColorReset
}

// line writes one line at the given indentation depth.
func (writer *textWriter) line(depth int, text string) {
	writer.out.WriteString(strings.Repeat("  ", depth))
	writer.out.WriteString(text)
	writer.out.WriteByte('\n')
}

// gap separates a section from what precedes it with one blank line.
func (writer *textWriter) gap() {
	if writer.out.Len() > 0 && !bytes.HasSuffix(writer.out.Bytes(), []byte("\n\n")) {
		writer.out.WriteByte('\n')
	}
}

// value writes any value at the given depth.
func (writer *textWriter) value(value node, depth int) {
	switch {
	case value.kind == '{' && len(value.items) > 0:
		writer.object(value, depth)
	case value.tabular():
		writer.table(value, depth)
	case value.inline():
		plain, color := cell(value)
		writer.line(depth, writer.paint(color, plain))
	default:
		writer.list(value, depth)
	}
}

// object writes runs of one-line members as aligned label/value lines and
// every other member as a labeled section indented below its heading.
func (writer *textWriter) object(value node, depth int) {
	start := 0
	width := 0

	for index, member := range value.items {
		if member.inline() {
			width = max(width, utf8.RuneCountInString(label(value.keys[index])))
		}
	}

	for index, member := range value.items {
		if member.inline() {
			continue
		}

		writer.pairs(value.keys[start:index], value.items[start:index], depth, width)
		writer.gap()
		writer.line(depth, writer.paint(colorBold+ColorKey, label(value.keys[index])))
		writer.value(member, depth+1)
		writer.gap()
		start = index + 1
	}

	writer.pairs(value.keys[start:], value.items[start:], depth, width)
}

// pairs writes one-line members with their values aligned after labels of
// the given width. Continuation lines of multiline strings align under the
// value column.
func (writer *textWriter) pairs(keys []string, items []node, depth, width int) {
	for index, key := range keys {
		name := label(key)
		padding := strings.Repeat(" ", width-utf8.RuneCountInString(name)+2)
		plain, color := cell(items[index])
		continuation := "\n" + strings.Repeat("  ", depth) + strings.Repeat(" ", width+2)
		text := strings.ReplaceAll(plain, "\n", continuation)
		writer.line(depth, writer.paint(ColorKey, name)+padding+writer.paint(color, text))
	}
}

// table writes an array of flat objects with one column per member name, in
// first-seen order. Objects missing a member leave that cell empty.
func (writer *textWriter) table(value node, depth int) {
	var columns []string

	for _, item := range value.items {
		for _, key := range item.keys {
			if !slices.Contains(columns, key) {
				columns = append(columns, key)
			}
		}
	}

	rows := make([][]node, len(value.items))
	widths := make([]int, len(columns))

	for column, key := range columns {
		widths[column] = utf8.RuneCountInString(header(key))
	}

	for row, item := range value.items {
		rows[row] = make([]node, len(columns))

		for column, key := range columns {
			rows[row][column] = member(item, key)
			plain, _ := cell(rows[row][column])
			widths[column] = max(widths[column], utf8.RuneCountInString(flat(plain)))
		}
	}

	writer.row(depth, widths, columns, nil)

	for _, cells := range rows {
		writer.row(depth, widths, nil, cells)
	}
}

// row writes one table line: the header when names is set, else cells.
func (writer *textWriter) row(depth int, widths []int, names []string, cells []node) {
	parts := make([]string, len(widths))

	for column, width := range widths {
		var plain, color string

		if names != nil {
			plain, color = header(names[column]), colorBold
		} else {
			plain, color = cell(cells[column])
			plain = flat(plain)
		}

		padding := ""

		if column < len(widths)-1 {
			padding = strings.Repeat(" ", width-utf8.RuneCountInString(plain))
		}

		parts[column] = writer.paint(color, plain) + padding
	}

	writer.line(depth, strings.TrimRight(strings.Join(parts, "  "), " "))
}

// list writes an array that cannot be a table: one-line elements as bullets
// and nested elements as blocks separated by blank lines.
func (writer *textWriter) list(value node, depth int) {
	for _, item := range value.items {
		if item.inline() {
			plain, color := cell(item)
			writer.line(depth, "- "+writer.paint(color, plain))

			continue
		}

		writer.gap()
		writer.value(item, depth)
		writer.gap()
	}
}

// member returns the named member of an object, or an absent marker.
func member(object node, key string) node {
	for index, name := range object.keys {
		if name == key {
			return object.items[index]
		}
	}

	return node{kind: 'x'}
}

// cell returns a one-line value's display text and color. Null and empty
// containers display as "-"; absent table cells display as nothing.
func cell(value node) (string, string) {
	switch value.kind {
	case '"':
		return safe(value.text), ""
	case '0':
		return value.text, ColorNumber
	case 't':
		return value.text, colorTrue
	case 'f':
		return value.text, colorFalse
	case 'x':
		return "", ""
	case '[':
		if len(value.items) > 0 {
			texts := make([]string, len(value.items))

			for index, item := range value.items {
				texts[index], _ = cell(item)
			}

			return strings.Join(texts, ", "), ""
		}
	}

	return "-", colorNull
}

// safe escapes control characters other than newline and tab, so remote
// strings cannot inject terminal escape sequences into the translation.
func safe(text string) string {
	if !strings.ContainsFunc(text, unsafeRune) {
		return text
	}

	var out strings.Builder

	for _, char := range text {
		if unsafeRune(char) {
			fmt.Fprintf(&out, "\\u%04x", char)

			continue
		}

		out.WriteRune(char)
	}

	return out.String()
}

// unsafeRune reports a control character that a terminal may interpret.
func unsafeRune(char rune) bool {
	return unicode.IsControl(char) && char != '\n' && char != '\t'
}

// flat keeps a table cell on one line.
func flat(text string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(text)
}

// label turns an identifier-like member name into sentence case
// ("last_seen" becomes "Last seen"); other names, such as paths or JSON
// pointers, are data and print unchanged apart from control escapes.
func label(name string) string {
	parts := words(name)

	if parts == nil {
		return safe(name)
	}

	first, size := utf8.DecodeRuneInString(parts[0])
	parts[0] = string(unicode.ToUpper(first)) + parts[0][size:]

	return strings.Join(parts, " ")
}

// header turns a member name into an uppercase column title ("last_seen"
// becomes "LAST SEEN"); names that are not identifier-like print unchanged.
func header(name string) string {
	parts := words(name)

	if parts == nil {
		return safe(name)
	}

	return strings.ToUpper(strings.Join(parts, " "))
}

// words splits an identifier at underscores, hyphens and lower-to-upper case
// changes ("lastSeen", "last_seen" and "last-seen" give "last", "seen"). It
// returns nil for names that are not ASCII identifiers.
func words(name string) []string {
	var parts []string
	var current strings.Builder
	previousLower := false

	for index, char := range name {
		if !identifierRune(char, index) {
			return nil
		}

		boundary := char == '_' || char == '-'

		if boundary || (previousLower && 'A' <= char && char <= 'Z') {
			parts = appendWord(parts, &current)
		}

		previousLower = 'a' <= char && char <= 'z' || '0' <= char && char <= '9'

		if !boundary {
			current.WriteRune(char)
		}
	}

	return appendWord(parts, &current)
}

// identifierRune accepts ASCII letters, and digits, underscores and hyphens
// after the first character.
func identifierRune(char rune, index int) bool {
	letter := 'a' <= char && char <= 'z' || 'A' <= char && char <= 'Z'

	return letter || index > 0 && ('0' <= char && char <= '9' || char == '_' || char == '-')
}

// appendWord moves a completed word into parts.
func appendWord(parts []string, current *strings.Builder) []string {
	if current.Len() == 0 {
		return parts
	}

	parts = append(parts, current.String())
	current.Reset()

	return parts
}
