package release

import (
	"regexp"
	"strings"
)

// autolink matches a CommonMark URI or email autolink, which is not raw HTML.
var autolink = regexp.MustCompile(
	`^<(?:[A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*|[A-Za-z0-9.!#$%&'*+/=?^_{|}~-]+@[A-Za-z0-9.-]+)>`,
)

// htmlStart matches a `<` that Markdown could parse as raw HTML.
var htmlStart = regexp.MustCompile(`^<[A-Za-z/!?]`)

// EscapeHTML neutralizes text that Markdown would parse as raw HTML, such as an
// unquoted `/<name>` placeholder copied from a commit body. Fenced blocks, indented
// code lines, inline code spans, existing escapes and autolinks are preserved.
// The consumer rejects raw HTML in release notes, so one stray placeholder would
// otherwise block every later import.
func EscapeHTML(text string) string {
	lines := strings.Split(text, "\n")
	fence := ""

	for index, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		marker := fenceMarker(trimmed)

		if fence != "" {
			if marker != "" && strings.HasPrefix(marker, fence) && strings.TrimSpace(trimmed) == marker {
				fence = ""
			}

			continue
		}
		if marker != "" && len(line)-len(trimmed) < 4 {
			fence = marker

			continue
		}
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		lines[index] = escapeLine(line)
	}

	return strings.Join(lines, "\n")
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

// escapeLine escapes HTML starts outside inline code spans.
func escapeLine(line string) string {
	var out strings.Builder

	for index := 0; index < len(line); {
		switch char := line[index]; {
		case char == '\\' && index+1 < len(line):
			out.WriteString(line[index : index+2])
			index += 2
		case char == '`':
			run := len(line[index:]) - len(strings.TrimLeft(line[index:], "`"))
			ticks := line[index : index+run]
			end := strings.Index(line[index+run:], ticks)

			if end < 0 {
				out.WriteString(ticks)
				index += run

				continue
			}
			span := index + run + end + run
			out.WriteString(line[index:span])
			index = span
		case char == '<' && !autolink.MatchString(line[index:]) && htmlStart.MatchString(line[index:]):
			out.WriteString(`\<`)
			index++
		default:
			out.WriteByte(char)
			index++
		}
	}

	return out.String()
}
