// Package lexicon parses query syntax and supplies language-neutral words.
package lexicon

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Term is an internal parsed syntax item, not the public driver record.
type Term struct {
	// Value is the original word or phrase without syntax delimiters.
	Value string
	// Phrase requires consecutive words in one field.
	Phrase bool
	// Exclude rejects a matching document.
	Exclude bool
	// Prefix permits completion of the final plain word.
	Prefix bool
}

// Parse turns query text into terms. It never fails: a quote left open
// runs to the end of the text, a minus sign that is not followed by a word or
// phrase is part of an ordinary word, and any term without a letter or digit
// is dropped because no document could match it. Empty text yields no terms,
// which matches every document.
func Parse(text string) []Term {
	var terms []Term

	for rest := strings.TrimSpace(text); rest != ""; rest = strings.TrimLeftFunc(rest, unicode.IsSpace) {
		var term Term

		if after, ok := strings.CutPrefix(rest, "-"); ok {
			if next, _ := utf8.DecodeRuneInString(after); after != "" && !unicode.IsSpace(next) {
				term.Exclude = true
				rest = after
			}
		}

		if after, ok := strings.CutPrefix(rest, `"`); ok {
			term.Phrase = true
			term.Value, rest, _ = strings.Cut(after, `"`)
		} else {
			end := strings.IndexFunc(rest, unicode.IsSpace)

			if end < 0 {
				end = len(rest)
			}

			term.Value, rest = rest[:end], rest[end:]
		}

		if strings.ContainsFunc(term.Value, func(char rune) bool {
			return unicode.IsLetter(char) || unicode.IsDigit(char)
		}) {
			terms = append(terms, term)
		}
	}

	for i := len(terms) - 1; i >= 0; i-- {
		if !terms[i].Phrase && !terms[i].Exclude {
			terms[i].Prefix = true

			break
		}
	}

	return terms
}

// Words splits text into lowercase words: runs of letters and digits.
func Words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	})
}
