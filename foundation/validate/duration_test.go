package validate_test

import (
	"encoding/json/v2"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// timedJob pairs an ordinary name with a text-encoded duration. It checks that
// Duration retains its string representation inside objects.
type timedJob struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string `json:"name"`
	// Elapsed holds a duration encoded through its text-marshaling adapter.
	Elapsed validate.Duration `json:"elapsed"`
}

// TestDurationJSONRoundTrip checks text-backed duration encoding and decoding
// through a JSON object field.
//
// Example:
//
//	go test -run "^TestDurationJSONRoundTrip$" .
func TestDurationJSONRoundTrip(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(timedJob{
		Name:    "sync",
		Elapsed: validate.Duration(182 * time.Millisecond),
	})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `{"name":"sync","elapsed":"182ms"}` {
		t.Fatalf("encoded = %s", data)
	}

	var decoded timedJob

	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Elapsed.Std() != 182*time.Millisecond {
		t.Fatalf("decoded = %v", decoded.Elapsed.Std())
	}
}

// TestDurationUnmarshalTextRejectsGarbage checks that invalid duration text
// returns an error without replacing the previous value.
//
// Example:
//
//	go test -run "^TestDurationUnmarshalTextRejectsGarbage$" .
func TestDurationUnmarshalTextRejectsGarbage(t *testing.T) {
	t.Parallel()

	duration := validate.Duration(time.Second)

	if err := duration.UnmarshalText([]byte("soon")); err == nil {
		t.Fatal("expected an error")
	}

	if duration.Std() != time.Second {
		t.Fatalf("duration changed to %v on error", duration.Std())
	}
}

// TestDurationFormat checks that a zero duration reports the duration format
// annotation.
//
// Example:
//
//	go test -run "^TestDurationFormat$" .
func TestDurationFormat(t *testing.T) {
	t.Parallel()

	if got := validate.Duration(0).JSONSchemaFormat(); got != "go-duration" {
		t.Fatalf("format = %q", got)
	}
}
