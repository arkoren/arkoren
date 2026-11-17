package federation

import (
	"testing"
	"time"
)

// TestConnectIntentExpires verifies an intent stops working at its expiry and
// only for the user who started it.
func TestConnectIntentExpires(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_000_000, 0)
	intent := connectIntent{Subject: "1", ExpiresAt: now.Add(ConnectIntentTTL).Unix()}
	if !intent.validFor("1", now) {
		t.Error("a fresh intent is not valid for its user")
	}
	if !intent.validFor("1", now.Add(ConnectIntentTTL-time.Second)) {
		t.Error("the intent expired early")
	}
	if intent.validFor("1", now.Add(ConnectIntentTTL)) {
		t.Error("the intent outlived its time to live")
	}
	if intent.validFor("2", now) {
		t.Error("the intent is valid for another user")
	}
}
