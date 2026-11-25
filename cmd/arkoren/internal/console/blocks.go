package console

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// shortDigits is how many leading digits of a revision or fingerprint the text
// presentation prints; JSON keeps all of them.
const shortDigits = 12

// digestPattern matches a whole string that is a 64-digit lowercase
// hexadecimal digest, such as a SHA-256 fingerprint or a skill revision.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// document writes a whole translated document. A top-level object with a
// "summary" string gets a bold headline marked "✓" in green, "✗" in red when
// "ok" is false, or "→" in yellow when "dry_run" is true, and the members
// summary, ok and dry_run are not repeated below it. An error document, whose
// "error" object holds a "message", starts with a red "✗" headline and then
// shows the error's other members followed by the document's remaining ones,
// so its "next_steps" print like a success's. Every other document is written
// generically by [textWriter.value].
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

	details := member(root, "error")
	message := member(details, "message")

	if details.kind != '{' || message.kind != '"' {
		writer.value(root, 0)

		return
	}

	rest := root.without("error")
	context := details.without("message")

	writer.headline("✗", colorFalse, message.text)
	writer.body(node{
		kind:  '{',
		keys:  slices.Concat(context.keys, rest.keys),
		items: slices.Concat(context.items, rest.items),
	})
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

// steps reports whether an array is a non-empty list of step objects: a string
// text and an optional string command, nothing else.
func (value node) steps() bool {
	if value.kind != '[' || len(value.items) == 0 {
		return false
	}

	return !slices.ContainsFunc(value.items, func(item node) bool {
		if item.kind != '{' || member(item, "text").kind != '"' {
			return true
		}

		for index, key := range item.keys {
			if key != "text" && (key != "command" || item.items[index].kind != '"') {
				return true
			}
		}

		return false
	})
}

// stepCommandIndent is the fixed left margin of a step's command, which sits
// on its own line below the numbered text so long commands never wrap into it.
const stepCommandIndent = "        "

// steps writes next steps as a numbered list. A step with a command shows it on
// the next line, indented by stepCommandIndent, in a distinct color; a step
// without one is just its text.
func (writer *textWriter) steps(value node, depth int) {
	width := len(strconv.Itoa(len(value.items)))

	for index, item := range value.items {
		writer.line(depth, fmt.Sprintf("%*d. %s", width, index+1, safe(member(item, "text").text)))

		if command := member(item, "command"); command.kind == '"' {
			writer.line(0, stepCommandIndent+writer.paint(ColorKey, flat(safe(command.text))))
		}
	}
}

// warnings reports whether an array is a non-empty list of warning objects: a
// string message and an optional array of string details, nothing else.
func (value node) warnings() bool {
	if value.kind != '[' || len(value.items) == 0 {
		return false
	}

	return !slices.ContainsFunc(value.items, func(item node) bool {
		if item.kind != '{' || member(item, "message").kind != '"' {
			return true
		}

		for index, key := range item.keys {
			details := item.items[index]

			if key != "message" && (key != "details" || details.kind != '[' ||
				slices.ContainsFunc(details.items, func(detail node) bool { return detail.kind != '"' })) {
				return true
			}
		}

		return false
	})
}

// warnings writes each warning as a yellow "!" line followed by its details as
// bullets indented under the message.
func (writer *textWriter) warnings(value node) {
	for _, item := range value.items {
		writer.line(0, writer.paint(colorBold+ColorNumber, "! "+safe(member(item, "message").text)))

		for _, detail := range member(item, "details").items {
			writer.line(1, "- "+safe(detail.text))
		}
	}
}

// abbreviated returns a copy of the value in which every string a reader does
// not need exactly is shortened: a 64-digit hexadecimal digest keeps its first
// [shortDigits] digits, and a path under one of directories (see
// [workingDirectories]), or the directory itself, prints relative to it ("."
// for the directory itself). The "command" members of steps are left exact because
// they are meant to be pasted. key is the member name the value sits under.
// The document's JSON is untouched; this runs only for the text presentation.
func (value node) abbreviated(directories []string, key string) node {
	switch value.kind {
	case '"':
		if key != "command" {
			value.text = abbreviate(value.text, directories)
		}
	case '{', '[':
		items := make([]node, len(value.items))

		for index, item := range value.items {
			childKey := key

			if value.kind == '{' {
				childKey = value.keys[index]
			}

			items[index] = item.abbreviated(directories, childKey)
		}

		value.items = items
	}

	return value
}

// workingDirectories lists dir and, when it differs, its symlink-resolved
// form, since commands print either spelling of one directory (macOS /var is
// /private/var). An empty dir yields none.
func workingDirectories(dir string) []string {
	if dir == "" {
		return nil
	}

	directories := []string{dir}

	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		directories = append(directories, resolved)
	}

	return directories
}

// abbreviate shortens one string as described at [node.abbreviated]. A path is
// recognized wherever a directory followed by a separator starts a word, so it
// also shortens paths inside messages; a bare directory is replaced only when
// it is the whole text. The longest directory goes first, so the resolved
// spelling of a directory is not cut short by its alias, and a directory that
// merely ends another path ("/private" + "/tmp/x" versus "/tmp/x") never matches.
func abbreviate(text string, directories []string) string {
	if digestPattern.MatchString(text) {
		return text[:shortDigits]
	}

	for _, dir := range slices.SortedFunc(slices.Values(directories), func(left, right string) int {
		return len(right) - len(left)
	}) {
		if text == dir {
			return "."
		}

		text = trimPrefixAtWordStart(text, dir+string(os.PathSeparator))
	}

	return text
}

// trimPrefixAtWordStart removes every occurrence of prefix that starts the text
// or follows a character that cannot continue a path, such as a space, a quote
// or a colon. An occurrence in the middle of a longer path stays.
func trimPrefixAtWordStart(text, prefix string) string {
	var out strings.Builder

	for {
		index := strings.Index(text, prefix)

		if index < 0 {
			break
		}

		out.WriteString(text[:index])

		if index > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:index])

			if unicode.IsLetter(previous) || unicode.IsDigit(previous) || strings.ContainsRune("/._-~", previous) {
				out.WriteString(prefix)
			}
		}

		text = text[index+len(prefix):]
	}

	out.WriteString(text)

	return out.String()
}
