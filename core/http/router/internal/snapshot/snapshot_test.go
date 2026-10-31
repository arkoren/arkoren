package snapshot_test

import (
	"encoding/json/jsontext"
	"testing"

	"arkoren.dev/core/http/router/internal/snapshot"
)

// TestCloneDetachesOwnedMetadata checks the independent containers at the
// publication and export boundary without interpreting raw schema numbers.
func TestCloneDetachesOwnedMetadata(t *testing.T) {
	t.Parallel()

	schema := jsontext.Value(`{"minimum":9007199254740993}`)
	tags := []string{"public"}
	parameters := []any{map[string]any{"schema": schema}}
	value := map[string]any{"tags": tags, "parameters": parameters}
	cloned := snapshot.Clone(value).(map[string]any)
	tags[0] = "changed"
	schema[0] = '!'
	parameters[0].(map[string]any)["other"] = true

	if cloned["tags"].([]string)[0] != "public" {
		t.Fatal("tags alias input")
	}

	parameter := cloned["parameters"].([]any)[0].(map[string]any)

	if string(parameter["schema"].(jsontext.Value)) != `{"minimum":9007199254740993}` || parameter["other"] != nil {
		t.Fatalf("parameter changed: %v", parameter)
	}
}
