package protocol

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// TestSignalRequestForceIsWrittenOnlyWhenSet verifies the "force" member is
// omitted for the default false and present when true, so a request that never
// forces stays minimal on the wire.
func TestSignalRequestForceIsWrittenOnlyWhenSet(t *testing.T) {
	t.Parallel()

	plain, err := json.Marshal(SignalRequest{Name: "approve", Payload: Payload(`1`)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(plain), "force") {
		t.Fatalf("request = %s, want no force member", plain)
	}
	forced, err := json.Marshal(SignalRequest{Name: "approve", Payload: Payload(`1`), Force: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(forced), `"force":true`) {
		t.Fatalf("request = %s, want force true", forced)
	}
}
