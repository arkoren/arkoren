package console

import (
	"testing"
)

// TestRenderIndentsAndKeepsKeyOrder checks whitespace-only pretty printing.
// Key order, large integers, and literal HTML characters must remain unchanged.
func TestRenderIndentsAndKeepsKeyOrder(t *testing.T) {
	t.Parallel()

	got := string(Render([]byte(sample), Style{
		Indent: true,
	}))

	want := `{
  "zeta": 1,
  "alpha": [
    1,
    2,
    {
      "c": null
    }
  ],
  "empty": {},
  "none": [],
  "big": 12345678901234567890,
  "s": "a<b",
  "ok": true
}`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderCompactIsOneLine checks reversal of pretty-print whitespace.
// Compact rendering must recover the original order-preserving JSON text.
func TestRenderCompactIsOneLine(t *testing.T) {
	t.Parallel()

	pretty := Render([]byte(sample), Style{
		Indent: true,
	})

	if got := string(Render(pretty, Style{})); got != sample {
		t.Fatalf("got %s, want %s", got, sample)
	}
}

// TestRenderColorsEachKind checks exact ANSI boundaries for JSON token kinds.
// Structural punctuation remains uncolored between the token escape sequences.
func TestRenderColorsEachKind(t *testing.T) {
	t.Parallel()

	got := string(Render([]byte(`{"k":["s",1,true,null]}`), Style{
		Color: true,
	}))

	want := "{" + ColorKey + `"k"` + ColorReset + ":[" + ColorString + `"s"` + ColorReset + "," + ColorNumber + "1" + ColorReset + "," +
		colorBool + "true" + ColorReset + "," + colorNull + "null" + ColorReset + "]}"

	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRenderReturnsInvalidJSONUnchanged checks the rendering fallback.
// Incomplete JSON, HTML, and trailing garbage must be returned verbatim.
func TestRenderReturnsInvalidJSONUnchanged(t *testing.T) {
	t.Parallel()

	for _, input := range []string{`{"a":`, `<html>`, `{"a":1} x`} {
		if got := string(Render([]byte(input), Style{
			Indent: true,
		})); got != input {
			t.Fatalf("render(%q) = %q", input, got)
		}
	}
}

// sample has keys in a deliberately non-alphabetical order, nesting, an empty
// object and array, a large integer, and an escaped string.
const sample = `{"zeta":1,"alpha":[1,2,{"c":null}],"empty":{},"none":[],"big":12345678901234567890,"s":"a<b","ok":true}`
