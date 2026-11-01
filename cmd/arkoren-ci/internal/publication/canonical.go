// Package publication validates durable signed batches before any release writes.
package publication

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// integer rejects floats and exponent notation from the signed-v1 protocol.
var integer = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// Canonical reproduces Python's sorted, compact, ensure_ascii JSON bytes.
// This is a durable signature protocol, not the Go artifact JSON encoding.
func Canonical(value any) (string, error) {
	data, err := json.Marshal(value, json.WithMarshalers(json.JoinMarshalers(
		json.MarshalFunc(func(float64) ([]byte, error) { return nil, fmt.Errorf("canonical-v1 does not support floats") }),
		json.MarshalFunc(func(float32) ([]byte, error) { return nil, fmt.Errorf("canonical-v1 does not support floats") }),
	)))
	if err != nil {
		return "", err
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(data))
	result, err := canonicalValue(decoder)
	if err != nil {
		return "", err
	}
	if _, err := decoder.ReadToken(); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("trailing canonical JSON data")
	}

	return result, nil
}

// canonicalValue reads structure without converting integer tokens to floats.
func canonicalValue(decoder *jsontext.Decoder) (string, error) {
	token, err := decoder.ReadToken()
	if err != nil {
		return "", err
	}
	switch token.Kind() {
	case '{':
		members := map[string]string{}
		keys := []string{}
		for decoder.PeekKind() != '}' {
			key, err := decoder.ReadToken()
			if err != nil || key.Kind() != '"' {
				return "", fmt.Errorf("invalid canonical object key")
			}
			name := key.String()
			if _, ok := members[name]; ok {
				return "", fmt.Errorf("duplicate canonical key")
			}
			value, err := canonicalValue(decoder)
			if err != nil {
				return "", err
			}
			members[name] = value
			keys = append(keys, name)
		}
		if _, err := decoder.ReadToken(); err != nil {
			return "", err
		}
		slices.Sort(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, ascii(key)+":"+members[key])
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	case '[':
		parts := []string{}
		for decoder.PeekKind() != ']' {
			value, err := canonicalValue(decoder)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		if _, err := decoder.ReadToken(); err != nil {
			return "", err
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case '"':
		return ascii(token.String()), nil
	case 'n', 't', 'f':
		return token.String(), nil
	case '0':
		if !integer.MatchString(token.String()) {
			return "", fmt.Errorf("canonical-v1 supports integers, not floats")
		}
		return token.String(), nil
	default:
		return "", fmt.Errorf("unsupported canonical JSON token")
	}
}

// ascii applies Python's JSON escape spelling, including UTF-16 surrogate pairs.
func ascii(value string) string {
	var result strings.Builder
	result.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"', '\\':
			result.WriteByte('\\')
			result.WriteRune(char)
		case '\b':
			result.WriteString(`\b`)
		case '\f':
			result.WriteString(`\f`)
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if char >= 0x20 && char <= 0x7e {
				result.WriteRune(char)
				continue
			}
			units := []rune{char}
			if char > 0xffff {
				high, low := utf16.EncodeRune(char)
				units = []rune{high, low}
			}
			for _, unit := range units {
				result.WriteString(`\u`)
				hex := strconv.FormatInt(int64(unit), 16)
				result.WriteString(strings.Repeat("0", 4-len(hex)))
				result.WriteString(hex)
			}
		}
	}
	result.WriteByte('"')

	return result.String()
}
