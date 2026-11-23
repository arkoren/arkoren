package console

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ANSI escape sequences used only by the text presentation.
const (
	colorBold  = "\x1b[1m"
	colorTrue  = "\x1b[32m"
	colorFalse = "\x1b[31m"
	colorDim   = "\x1b[2m"
)

// Limits of a one-line list: longer lists, and lists whose items contain
// spaces, are easier to read as bullets than as one comma-joined line.
const (
	maxInlineItems = 4
	maxInlineWidth = 60
)

// Limits of a table: a cell longer than maxCellWidth runes is a long text field,
// and rows wider than maxTableWidth runes would wrap on a wide terminal.
// Either makes a list of records print as blocks instead of a table. The last
// column is exempt, since a long final description wraps without breaking the
// columns before it.
const (
	maxCellWidth  = 80
	maxTableWidth = 140
)

// fractionalSeconds matches an RFC 3339 timestamp with a fraction of a second,
// such as Go's nanosecond time encoding. The fraction is noise to a reader.
var fractionalSeconds = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)\.\d+(Z|[+-]\d\d:\d\d)$`)

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
// short arrays of scalars become comma lists while longer ones become bullets.
// Null, empty strings, empty arrays and empty objects are omitted; a document
// with nothing left prints nothing. A top-level object with a "summary" string,
// or the single member "error" or "warning" holding a "message", starts with a
// marked headline; see [textWriter.document]. An object with an "items" array
// is a list: see [listing]. Members named "<name>_ms" or "<name>_bytes" are
// omitted when the object also has a string member "<name>", which says the
// same thing for people ("duration_ms" beside "duration"), and RFC 3339
// timestamps lose their fractional seconds. The result has no trailing
// newline. Invalid JSON is returned unchanged, like [Render].
func Text(raw []byte, color bool) []byte {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	root, err := decodeNode(decoder)

	if err != nil || decoder.PeekKind() != 0 {
		return raw
	}

	root, note := listing(root)

	writer := &textWriter{color: color}
	writer.document(root.pruned())

	if note != "" {
		writer.gap()
		writer.line(0, writer.paint(colorDim, safe(note)))
	}

	return bytes.TrimRight(writer.out.Bytes(), "\n")
}

// listing separates the bookkeeping of a list wrapper from its records. An
// object with an "items" array, such as {"items": [...], "truncated": false},
// prints its items as a bare table or list without an "Items" heading, and
// hides the bookkeeping while it is at rest: truncated, has_more and
// instance_changed false, dropped zero, and the continuation cursor. When
// truncated or has_more is true, the returned note, printed dim under the
// items, says more exist and names the cursor if the wrapper carries one.
// Other objects are returned unchanged with no note.
func listing(root node) (node, string) {
	if root.kind != '{' || member(root, "items").kind != '[' {
		return root, ""
	}

	hidden := []string{"truncated", "has_more", "cursor"}

	if member(root, "dropped").text == "0" {
		hidden = append(hidden, "dropped")
	}

	if member(root, "instance_changed").kind == 'f' {
		hidden = append(hidden, "instance_changed")
	}

	cursor := member(root, "cursor").text

	switch {
	case member(root, "has_more").kind == 't' && cursor != "":
		return root.without(hidden...), "… more results (continue with cursor " + cursor + ")"
	case member(root, "has_more").kind == 't' || member(root, "truncated").kind == 't':
		return root.without(hidden...), "… more results (narrow the query or use --verbose)"
	}

	return root.without(hidden...), ""
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

	return node{kind: kind, text: fractionalSeconds.ReplaceAllString(string(unquoted), "$1$2")}, err
}

// empty reports a value that text output omits: null, an empty string, or a
// container with no members. False and zero are data and stay visible.
func (value node) empty() bool {
	switch value.kind {
	case 'n':
		return true
	case '"':
		return value.text == ""
	case '{', '[':
		return len(value.items) == 0
	}

	return false
}

// pruned returns a copy of the value without empty members and elements,
// removing containers that become empty once their children are removed.
func (value node) pruned() node {
	if value.kind != '{' && value.kind != '[' {
		return value
	}

	kept := node{kind: value.kind}

	for index, item := range value.items {
		item = item.pruned()

		if item.empty() {
			continue
		}

		if value.kind == '{' {
			kept.keys = append(kept.keys, value.keys[index])
		}

		kept.items = append(kept.items, item)
	}

	if kept.kind == '{' {
		return kept.withoutTwins()
	}

	return kept
}

// withoutTwins drops the machine-readable twin of a human-readable member: a
// "<name>_ms" or "<name>_bytes" member beside a string member "<name>".
func (value node) withoutTwins() node {
	var twins []string

	for _, key := range value.keys {
		for _, suffix := range []string{"_ms", "_bytes"} {
			if name, ok := strings.CutSuffix(key, suffix); ok && member(value, name).kind == '"' {
				twins = append(twins, key)
			}
		}
	}

	if twins == nil {
		return value
	}

	return value.without(twins...)
}

// without returns an object without the named members.
func (value node) without(names ...string) node {
	kept := node{kind: value.kind}

	for index, key := range value.keys {
		if slices.Contains(names, key) {
			continue
		}

		kept.keys = append(kept.keys, key)
		kept.items = append(kept.items, value.items[index])
	}

	return kept
}

// inline reports whether a value fits on one line: a scalar, an empty
// container, or a short list of scalars.
func (value node) inline() bool {
	switch value.kind {
	case '{':
		return len(value.items) == 0
	case '[':
		return value.short()
	}

	return true
}

// short reports whether an array can be comma-joined on one line: at most
// maxInlineItems scalars, none containing a space, whose joined text fits in
// maxInlineWidth runes.
func (value node) short() bool {
	if len(value.items) > maxInlineItems {
		return false
	}

	width := 0

	for _, item := range value.items {
		if item.kind == '{' || item.kind == '[' || strings.Contains(item.text, " ") {
			return false
		}

		width += utf8.RuneCountInString(item.text) + 2
	}

	return width-2 <= maxInlineWidth
}

// changes reports whether an array is a list of file changes: objects with
// string action and path members and an optional string detail, nothing else.
func (value node) changes() bool {
	if value.kind != '[' || len(value.items) == 0 {
		return false
	}

	for _, item := range value.items {
		if item.kind != '{' || member(item, "action").kind != '"' || member(item, "path").kind != '"' {
			return false
		}

		for index, key := range item.keys {
			if key != "action" && key != "path" && (key != "detail" || item.items[index].kind != '"') {
				return false
			}
		}
	}

	return true
}

// steps reports whether an array is a non-empty list of strings.
func (value node) steps() bool {
	if value.kind != '[' || len(value.items) == 0 {
		return false
	}

	return !slices.ContainsFunc(value.items, func(item node) bool {
		return item.kind != '"'
	})
}

// columns returns the member names of an array of objects in first-seen order.
func (value node) columns() []string {
	var columns []string

	for _, item := range value.items {
		for _, key := range item.keys {
			if !slices.Contains(columns, key) {
				columns = append(columns, key)
			}
		}
	}

	return columns
}

// tabular reports whether an array is a non-empty list of objects whose
// members all fit on one line, so it can print as a table. A cell longer than
// maxCellWidth, or columns wider than maxTableWidth, make it not tabular:
// records with long text fields read better as blocks. The last column is
// exempt from both limits.
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

	columns := value.columns()
	width := 2 * (len(columns) - 1)

	for _, key := range columns[:len(columns)-1] {
		widest := utf8.RuneCountInString(header(key))

		for _, item := range value.items {
			plain, _ := cell(member(item, key))
			cells := utf8.RuneCountInString(flat(plain))

			if cells > maxCellWidth {
				return false
			}

			widest = max(widest, cells)
		}

		width += widest
	}

	return width <= maxTableWidth
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

// document writes a whole translated document. A top-level object with a
// "summary" string gets a bold headline marked "✓" in green, "✗" in red when
// "ok" is false, or "→" in yellow when "dry_run" is true, and the members
// summary, ok and dry_run are not repeated below it. A document that is only
// {"error": {...}} or {"warning": {...}} with a "message" starts with a red "✗"
// or yellow "!" headline, then the remaining members, and a dim "hint" last.
// Every other document is written generically by [textWriter.value].
func (writer *textWriter) document(root node) {
	if root.kind != '{' {
		writer.value(root, 0)

		return
	}

	if summary := member(root, "summary"); summary.kind == '"' {
		mark, color := "✓", colorTrue

		switch {
		case member(root, "ok").kind == 'f':
			mark, color = "✗", colorFalse
		case member(root, "dry_run").kind == 't':
			mark, color = "→", ColorNumber
		}

		writer.headline(mark, color, summary.text)
		writer.body(root.without("summary", "ok", "dry_run"))

		return
	}

	if len(root.items) != 1 || (root.keys[0] != "error" && root.keys[0] != "warning") {
		writer.value(root, 0)

		return
	}

	details := root.items[0]
	message := member(details, "message")

	if details.kind != '{' || message.kind != '"' {
		writer.value(root, 0)

		return
	}

	if root.keys[0] == "error" {
		writer.headline("✗", colorFalse, message.text)
	} else {
		writer.headline("!", ColorNumber, message.text)
	}

	writer.body(details.without("message", "hint"))

	if hint := member(details, "hint"); hint.kind == '"' {
		writer.gap()
		writer.line(0, writer.paint(colorDim, safe(hint.text)))
	}
}

// headline writes the bold, colored first line of a report.
func (writer *textWriter) headline(mark, color, message string) {
	writer.line(0, writer.paint(colorBold+color, mark+" "+safe(message)))
}

// body writes the members that follow a headline after one blank line.
func (writer *textWriter) body(rest node) {
	if len(rest.items) == 0 {
		return
	}

	writer.gap()
	writer.object(rest, 0)
}

// changes writes file changes as lines of aligned action, path and detail
// columns. The action is colored by kind and the detail is dim.
func (writer *textWriter) changes(value node, depth int) {
	actionWidth, pathWidth := 0, 0

	for _, item := range value.items {
		actionWidth = max(actionWidth, utf8.RuneCountInString(safe(member(item, "action").text)))

		if member(item, "detail").kind == '"' {
			pathWidth = max(pathWidth, utf8.RuneCountInString(safe(member(item, "path").text)))
		}
	}

	for _, item := range value.items {
		action := safe(member(item, "action").text)
		path := safe(member(item, "path").text)
		text := writer.paint(actionColor(action), action) + strings.Repeat(" ", actionWidth-utf8.RuneCountInString(action)+2) + path

		if detail := member(item, "detail"); detail.kind == '"' {
			text += strings.Repeat(" ", pathWidth-utf8.RuneCountInString(path)+2) + writer.paint(colorDim, flat(safe(detail.text)))
		}

		writer.line(depth, text)
	}
}

// actionColor picks the color of a change action.
func actionColor(action string) string {
	switch action {
	case "create":
		return colorTrue
	case "edit":
		return ColorNumber
	case "remove":
		return colorFalse
	case "write":
		return ColorKey
	}

	return colorDim
}

// section writes the value under a member heading. Directly below a top-level
// object, "changes" and "next_steps" use their dedicated layouts when the
// value has the matching shape; everything else is generic.
func (writer *textWriter) section(key string, value node, depth int) {
	switch {
	case depth == 1 && key == "changes" && value.changes():
		writer.changes(value, depth)
	case depth == 1 && key == "next_steps" && value.steps():
		width := len(strconv.Itoa(len(value.items)))

		for index, item := range value.items {
			writer.line(depth, fmt.Sprintf("%*d. %s", width, index+1, safe(item.text)))
		}
	default:
		writer.value(value, depth)
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

		// A top-level "items" array is the list itself, so it needs no heading.
		bare := depth == 0 && value.keys[index] == "items" && member.kind == '['
		child := depth + 1

		if bare {
			child = depth
		} else {
			writer.line(depth, writer.paint(colorBold+ColorKey, label(value.keys[index])))
		}

		writer.section(value.keys[index], member, child)
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
	columns := value.columns()
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
	for index, item := range value.items {
		if item.inline() {
			plain, color := cell(item)
			writer.line(depth, "- "+writer.paint(color, plain))

			continue
		}

		if index > 0 {
			writer.gap()
		}

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

// cell returns a one-line value's display text and color. Absent table cells
// display as nothing.
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
		texts := make([]string, len(value.items))

		for index, item := range value.items {
			texts[index], _ = cell(item)
		}

		return strings.Join(texts, ", "), ""
	}

	return "", ""
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

// acronyms are the words of a member name that labels write in capitals
// ("trace_id" becomes "Trace ID", "url_source" becomes "URL source").
var acronyms = []string{"api", "dns", "http", "https", "id", "ip", "json", "mcp", "sha256", "tls", "ttl", "uri", "url", "uuid"}

// label turns an identifier-like member name into sentence case
// ("last_seen" becomes "Last seen"), writing [acronyms] in capitals; other
// names, such as paths or JSON pointers, are data and print unchanged apart
// from control escapes.
func label(name string) string {
	parts := words(name)

	if parts == nil {
		return safe(name)
	}

	for index, part := range parts {
		if slices.Contains(acronyms, part) {
			parts[index] = strings.ToUpper(part)
		}
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
