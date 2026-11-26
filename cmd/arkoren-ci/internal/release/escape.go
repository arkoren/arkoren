package release

import (
	"regexp"
	"strings"
)

// autolink matches a CommonMark autolink that the website importer accepts: an
// HTTP(S) or mailto URI without backslashes, or an email address. Autolinks
// with any other scheme are escaped like raw HTML, because the importer rejects
// their URL and the escaped text renders as the literal original.
var autolink = regexp.MustCompile(
	`^<(?:(?i:https?|mailto):[^\s<>\\]*|[A-Za-z0-9.!#$%&'*+/=?^_{|}~-]+@[A-Za-z0-9.-]+)>`,
)

// htmlStart matches a `<` that Markdown could parse as raw HTML.
var htmlStart = regexp.MustCompile(`^<[A-Za-z/!?]`)

// boundary matches a line that can start a block (blank line, list item,
// heading, block quote, setext underline, thematic break or table row). An
// inline code span never continues from an earlier line onto such a line.
var boundary = regexp.MustCompile(`^\s*$|^\s*([-+*>#=_|]|\d{1,9}[.)])`)

// EscapeHTML neutralizes text that Markdown would parse as raw HTML, such as an
// unquoted `/<name>` placeholder copied from a commit body, by writing `\<`.
// The website importer rejects raw HTML in release notes, so one stray
// placeholder would otherwise block every later import.
//
// It preserves fenced code blocks, inline code spans (including spans that wrap
// onto a following paragraph line), backslash escapes and the autolinks the
// importer accepts. It errs toward escaping: indented lines are escaped too,
// because inside a list item they are paragraph text rather than code, and a
// fence ends at a line indented less than its opening fence, as when its list
// item ends. Escaping inside real code shows a visible backslash; never
// escaping real raw HTML would block the import, so the former is preferred.
//
// Plan schema 4 applies it to every module's notes (see [Plan.SchemaVersion]).
// Once a schema-4 batch is signed its notes are frozen in the tag annotation,
// and recovery recomputes them with this function: after that, a change to its
// output requires a new plan schema.
//
// Example:
//
//	EscapeHTML("mount \"/auth/<name>\"; use `<name>`") // mount "/auth/\<name>"; use `<name>`
func EscapeHTML(text string) string {
	result := []string{}
	paragraph := []string{}
	flush := func() {
		if len(paragraph) > 0 {
			result = append(result, escapeText(strings.Join(paragraph, "\n")))
			paragraph = paragraph[:0]
		}
	}
	fence := ""
	indent := 0

	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		depth := len(line) - len(trimmed)

		if fence != "" && (trimmed == "" || depth >= indent) {
			if closes(trimmed, fence) {
				fence = ""
			}
			result = append(result, line)

			continue
		}
		fence = ""
		if marker := opener(trimmed, depth); marker != "" {
			flush()
			fence = marker
			indent = depth
			result = append(result, line)

			continue
		}
		if boundary.MatchString(line) {
			flush()
		}
		paragraph = append(paragraph, line)
		// A table row is its own block: a span cannot continue onto the next row.
		if strings.Contains(line, "|") {
			flush()
		}
	}
	flush()

	return strings.Join(result, "\n")
}

// opener returns the run of a code fence opened by trimmed at indentation
// depth, or empty. A backtick fence whose info string contains a backtick is
// inline text in CommonMark, not a fence.
func opener(trimmed string, depth int) string {
	marker := fenceMarker(trimmed)
	if depth >= 4 || (strings.HasPrefix(marker, "`") && strings.Contains(trimmed[len(marker):], "`")) {
		return ""
	}

	return marker
}

// closes reports whether trimmed closes a fence opened with fence: the same
// character, at least as long, followed only by spaces.
func closes(trimmed, fence string) bool {
	marker := fenceMarker(trimmed)

	return marker != "" && strings.HasPrefix(marker, fence) && strings.TrimSpace(trimmed) == marker
}

// fenceMarker returns the opening run of a code fence, or empty.
func fenceMarker(line string) string {
	for _, char := range []string{"`", "~"} {
		count := len(line) - len(strings.TrimLeft(line, char))
		if count >= 3 {
			return line[:count]
		}
	}

	return ""
}

// escapeText escapes HTML starts in one paragraph outside inline code spans.
// A span opened by a run of backticks ends at the next run of exactly the same
// length, which may lie on a later line of the paragraph; an unmatched run is
// literal text.
func escapeText(text string) string {
	var out strings.Builder

	for index := 0; index < len(text); {
		switch char := text[index]; {
		case char == '\\' && index+1 < len(text):
			out.WriteString(text[index : index+2])
			index += 2
		case char == '`':
			run := len(text[index:]) - len(strings.TrimLeft(text[index:], "`"))
			end := closing(text[index+run:], run)
			if end < 0 {
				end = 0
			}
			span := index + run + end
			out.WriteString(text[index:span])
			index = span
		case char == '<' && !autolink.MatchString(text[index:]) && htmlStart.MatchString(text[index:]):
			out.WriteString(`\<`)
			index++
		default:
			out.WriteByte(char)
			index++
		}
	}

	return out.String()
}

// closing returns the offset just past the first run of exactly run backticks
// in text, or -1 when the span never closes.
func closing(text string, run int) int {
	for index := 0; index < len(text); {
		if text[index] != '`' {
			index++

			continue
		}
		length := len(text[index:]) - len(strings.TrimLeft(text[index:], "`"))
		if length == run {
			return index + length
		}
		index += length
	}

	return -1
}
