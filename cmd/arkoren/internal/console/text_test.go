package console_test

import (
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestTextAlignsLabelsAndKeepsOrder translates a flat object into aligned
// sentence-case labels in document order, with exact scalar text.
func TestTextAlignsLabelsAndKeepsOrder(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"zeta":1,"last_seen":"now","big":12345678901234567890,"ok":true,"none":null,"tags":["a","b"],"empty":[]}`), false))

	want := `Zeta       1
Last seen  now
Big        12345678901234567890
Ok         true
None       -
Tags       a, b
Empty      -`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsFlatObjectListsAsTables uses uppercase headers in first-seen
// member order and leaves missing cells empty.
func TestTextPrintsFlatObjectListsAsTables(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"jobs":[{"name":"send-invoice","lastSeen":"10:02"},{"name":"reconcile","retries":3}]}`), false))

	want := `Jobs
  NAME          LAST SEEN  RETRIES
  send-invoice  10:02
  reconcile                3`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextNestsObjectsAsSections indents nested objects below a heading and
// aligns all one-line labels of an object in one column across sections.
func TestTextNestsObjectsAsSections(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"message":"failed","fields":{"/user_name":["required"]},"hint":"retry"}`), false))

	want := `Message  failed

Fields
  /user_name  required

Hint     retry`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextLeavesDataKeysUnchanged humanizes only identifier-like names.
// Paths and pointers are data, so they keep their exact spelling.
func TestTextLeavesDataKeysUnchanged(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"/a_b":1,"app.info":2,"user_id":3}`), false))

	want := `/a_b      1
app.info  2
User id   3`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextIndentsMultilineValues aligns continuation lines under the value.
func TestTextIndentsMultilineValues(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"notes":"one\ntwo","id":1}`), false))

	want := "Notes  one\n       two\nId     1"

	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestTextEscapesControlCharacters keeps remote strings from emitting
// terminal escape sequences, in values, labels and table cells alike.
func TestTextEscapesControlCharacters(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"a\u001b":"x\u001b[31m","rows":[{"name":"\u0007"}]}`), false))

	if strings.ContainsAny(got, "\x1b\x07") || !strings.Contains(got, `x\u001b[31m`) || !strings.Contains(got, `\u0007`) {
		t.Fatalf("got %q", got)
	}
}

// TestTextColorsLabelsAndScalars colors labels, numbers, booleans and null.
func TestTextColorsLabelsAndScalars(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"n":1,"yes":true,"no":false,"none":null,"s":"plain"}`), true))

	for _, fragment := range []string{
		console.ColorKey + "N" + console.ColorReset,
		console.ColorNumber + "1" + console.ColorReset,
		"\x1b[32mtrue" + console.ColorReset,
		"\x1b[31mfalse" + console.ColorReset,
		"\x1b[90m-" + console.ColorReset,
		"  plain",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("output lacks %q: %q", fragment, got)
		}
	}
}

// TestTextListsNestedArrays bullets one-line elements and prints nested
// elements as blocks.
func TestTextListsNestedArrays(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`["a",{"b":{"c":1}},2]`), false))

	want := `- a

B
  C  1

- 2`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsScalarDocuments prints a top-level string without quotes.
func TestTextPrintsScalarDocuments(t *testing.T) {
	t.Parallel()

	if got := string(console.Text([]byte(`"hello"`), false)); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

// TestTextReturnsInvalidJSONUnchanged shows the user what was really sent.
func TestTextReturnsInvalidJSONUnchanged(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`{"a":`, `{} {}`, `nope`} {
		if got := string(console.Text([]byte(raw), false)); got != raw {
			t.Fatalf("Text(%q) = %q", raw, got)
		}
	}
}

// TestFormatSelectsTextOrJSON translates only when the style asks for text.
func TestFormatSelectsTextOrJSON(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"a":1}`)

	if got := string(console.Format(raw, console.Style{Text: true})); got != "A  1" {
		t.Fatalf("text = %q", got)
	}

	if got := string(console.Format(raw, console.Style{})); got != `{"a":1}` {
		t.Fatalf("json = %q", got)
	}
}
