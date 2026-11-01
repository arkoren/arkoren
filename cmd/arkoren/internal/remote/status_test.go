package remote

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"testing"
)

// TestStatusHealthUsesCounts bounds large check sets without changing the
// complete health data returned when verbose mode is explicit.
func TestStatusHealthUsesCounts(t *testing.T) {
	t.Parallel()

	checks := make([]map[string]string, 100)

	for index := range checks {
		checks[index] = map[string]string{"name": fmt.Sprintf("check-%d", index), "status": "ok"}
	}

	checks[0]["status"] = "failing"
	raw, err := json.Marshal(map[string]any{"status": "failing", "items": checks})

	if err != nil {
		t.Fatal(err)
	}

	brief := SummarizeHealth(raw, false)

	var document map[string]any

	if err := json.Unmarshal(brief, &document); err != nil {
		t.Fatal(err)
	}

	if document["check_count"] != float64(100) || document["failing_count"] != float64(1) || len(brief) > 300 {
		t.Fatalf("health summary = %s", brief)
	}

	if _, ok := document["items"]; ok {
		t.Fatal("default status listed every check")
	}

	if !bytes.Equal(SummarizeHealth(raw, true), raw) {
		t.Fatal("verbose status altered the full health result")
	}
}
