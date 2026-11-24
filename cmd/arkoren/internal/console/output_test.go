package console

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestJSONEncodingFailureWritesNothing protects the document boundary when a
// local report contains a value JSON cannot represent.
func TestJSONEncodingFailureWritesNothing(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	err := WriteJSON(&output, make(chan int), Style{})

	if err == nil || output.Len() != 0 {
		t.Fatalf("output = %q; error = %v", output.String(), err)
	}
}

// TestOutputRetainsFirstFailure checks subsequent rendering cannot retry a
// failed sink or replace its cause. Callers can still use errors.Is.
func TestOutputRetainsFirstFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("stdout failed")
	output := &Output{Writer: &failingOutput{err: cause}}

	if _, err := output.Write([]byte("first")); !errors.Is(err, cause) {
		t.Fatalf("first write = %v", err)
	}

	output.Writer = io.Discard

	if written, err := output.Write([]byte("second")); written != 0 || !errors.Is(err, cause) {
		t.Fatalf("second write = %d, %v", written, err)
	}
}

// TestOutputStyleTranslatesOnlyOnTerminals keeps JSON for pipes and files.
func TestOutputStyleTranslatesOnlyOnTerminals(t *testing.T) {
	t.Parallel()

	if !(Environment{IsTerminal: true}).OutputStyle().Text {
		t.Fatal("terminal output is not text")
	}

	if (Environment{}).OutputStyle().Text {
		t.Fatal("non-terminal output is text")
	}
}

// TestOutputStyleJSONFlagsKeepJSONOnTerminals honors -json and -compact.
func TestOutputStyleJSONFlagsKeepJSONOnTerminals(t *testing.T) {
	t.Parallel()

	for _, flags := range []Globals{{JSON: true}, {Compact: true}} {
		if (Environment{IsTerminal: true, Presentation: flags}).OutputStyle().Text {
			t.Fatalf("flags %+v printed text", flags)
		}
	}
}

// TestResultPrintsTextOnTerminal translates local results on a terminal.
func TestResultPrintsTextOnTerminal(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	env := Environment{Stdout: &output, IsTerminal: true, Presentation: Globals{NoColor: true}}

	if err := env.Result(map[string]any{"created": true}); err != nil || output.String() != "Created  true\n" {
		t.Fatalf("output = %q; error = %v", output.String(), err)
	}
}

// failingOutput accepts a fixed number of writes before failing. It can also
// simulate an invalid short write without an error from an injected writer.
type failingOutput struct {
	// remaining is the number of successful writes before failure.
	remaining int
	// err is the failure returned after remaining reaches zero; nil short-writes.
	err error
}

// Write preserves complete writes until the configured failure point. A nil
// failure deliberately violates io.Writer's contract to exercise normalization.
func (writer *failingOutput) Write(data []byte) (int, error) {
	if writer.remaining > 0 {
		writer.remaining--

		return len(data), nil
	}

	return 0, writer.err
}
