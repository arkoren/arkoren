package console

import (
	"bytes"
	"encoding/json/jsontext"
	"strings"
)

// ANSI escape sequences of the color palette. The five colors are the basic
// ones every terminal theme keeps readable on both dark and light
// backgrounds: cyan keys, green strings, yellow numbers, magenta booleans,
// and gray null.
const (
	ColorKey    = "\x1b[36m"
	ColorString = "\x1b[32m"
	ColorNumber = "\x1b[33m"
	colorBool   = "\x1b[35m"
	colorNull   = "\x1b[90m"
	ColorReset  = "\x1b[0m"
)

// Style says how [Render] lays out JSON.
type Style struct {
	// Indent spreads the value over lines indented by two spaces; false
	// writes it on one line without insignificant whitespace.
	Indent bool

	// Color wraps every key and scalar in ANSI colors.
	Color bool
}

// frame is the position inside one open object or array during [Render].
type frame struct {
	// object is true for an object, false for an array.
	object bool

	// count is the number of members or elements completed so far.
	count int

	// expectName is true while an object waits for its next member name.
	expectName bool
}

// Render re-lays out a JSON document in the given style, preserving the
// order of object members exactly as the server sent them and the exact text
// of every scalar. It does not go through a Go map or number type, so key
// order and large numbers survive. Invalid JSON is returned unchanged, which
// shows the user what the server really sent.
func Render(raw []byte, look Style) []byte {
	if !jsontext.Value(raw).IsValid() {
		return raw
	}

	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	var out bytes.Buffer
	var stack []frame

	paint := func(color string, text []byte) {
		if look.Color {
			out.WriteString(color)
			out.Write(text)
			out.WriteString(ColorReset)

			return
		}

		out.Write(text)
	}

	newline := func(depth int) {
		if look.Indent {
			out.WriteByte('\n')
			out.WriteString(strings.Repeat("  ", depth))
		}
	}
	// completed records that the value at the top of the stack is finished.
	completed := func() {
		if len(stack) == 0 {
			return
		}

		top := &stack[len(stack)-1]
		top.count++
		top.expectName = top.object
	}

	for {
		kind := decoder.PeekKind()

		if kind == 0 {
			break
		}

		if kind == '}' || kind == ']' {
			if _, err := decoder.ReadToken(); err != nil {
				return raw
			}

			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if top.count > 0 {
				newline(len(stack))
			}

			out.WriteByte(byte(kind))
			completed()

			continue
		}

		if len(stack) > 0 {
			top := &stack[len(stack)-1]

			if top.object && top.expectName {
				name, err := decoder.ReadValue()

				if err != nil {
					return raw
				}

				if top.count > 0 {
					out.WriteByte(',')
				}

				newline(len(stack))
				paint(ColorKey, name)
				out.WriteByte(':')

				if look.Indent {
					out.WriteByte(' ')
				}

				top.expectName = false

				continue
			}

			if !top.object {
				if top.count > 0 {
					out.WriteByte(',')
				}

				newline(len(stack))
			}
		}

		if kind == '{' || kind == '[' {
			if _, err := decoder.ReadToken(); err != nil {
				return raw
			}

			out.WriteByte(byte(kind))
			stack = append(stack, frame{
				object:     kind == '{',
				expectName: kind == '{',
			})

			continue
		}

		value, err := decoder.ReadValue()

		if err != nil {
			return raw
		}

		switch kind {
		case '"':
			paint(ColorString, value)
		case '0':
			paint(ColorNumber, value)
		case 't', 'f':
			paint(colorBool, value)
		default:
			paint(colorNull, value)
		}

		completed()
	}

	if len(stack) != 0 {
		return raw
	}

	return out.Bytes()
}
