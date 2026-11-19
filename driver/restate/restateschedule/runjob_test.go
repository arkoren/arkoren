package restateschedule

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
)

// TestRunJobPayloadKeepsExplicitNull verifies an absent payload is omitted from
// the wire (a recurring entry) while an explicit JSON null stays on it (task
// data), as the field's documentation promises.
func TestRunJobPayloadKeepsExplicitNull(t *testing.T) {
	t.Parallel()

	recurring, err := json.Marshal(runJob{Name: "hourly"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(recurring) != `{"Name":"hourly"}` {
		t.Fatalf("recurring = %s, want the payload omitted", recurring)
	}
	task, err := json.Marshal(runJob{Name: "remind", Payload: jsontext.Value(`null`)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(task) != `{"Name":"remind","Payload":null}` {
		t.Fatalf("task = %s, want the explicit null kept", task)
	}
}
