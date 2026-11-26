package workflow_test

import (
	"testing"

	"arkoren.dev/core/service/workflow"
)

// TestCodecSortsMapKeys pins deterministic map encoding, which replayed
// orchestration needs to re-encode identical bytes. Repetition defeats Go's
// randomized map iteration order.
func TestCodecSortsMapKeys(t *testing.T) {
	t.Parallel()

	value := map[string]any{"b": 2, "a": map[string]int{"d": 4, "c": 3, "f": 6, "e": 5}, "g": 7, "h": 8}

	for range 20 {
		data, err := workflow.JSON{}.Marshal(value)

		if err != nil {
			t.Fatal(err)
		}

		if want := `{"a":{"c":3,"d":4,"e":5,"f":6},"b":2,"g":7,"h":8}`; string(data) != want {
			t.Fatalf("Marshal = %s, want %s", data, want)
		}
	}
}
