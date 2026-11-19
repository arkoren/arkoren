package oauthauth

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// TestLoginStateWritesFreshOnlyWhenSet verifies the sealed state omits the
// "fresh" member for an ordinary login and records it for fresh=1, and that a
// login cookie carries no hand-off identity.
func TestLoginStateWritesFreshOnlyWhenSet(t *testing.T) {
	t.Parallel()

	ordinary, err := json.Marshal(loginState{Cookie: "c", State: "s"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(ordinary), "fresh") || strings.Contains(string(ordinary), "identity") {
		t.Fatalf("state = %s, want no fresh or identity member", ordinary)
	}
	fresh, err := json.Marshal(loginState{Cookie: "c", State: "s", Fresh: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(fresh), `"fresh":true`) {
		t.Fatalf("state = %s, want fresh true", fresh)
	}
}
