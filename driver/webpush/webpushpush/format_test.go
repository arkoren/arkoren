package webpushpush_test

import (
	"fmt"
	"testing"

	"arkoren.dev/driver/webpush/webpushpush"
	"arkoren.dev/foundation/key"
)

// TestOwnedDriverFormattingRedactsIdentity uses a synthetic signing identity;
// no provider is contacted or real application credential loaded.
func TestOwnedDriverFormattingRedactsIdentity(t *testing.T) {
	t.Parallel()

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	driver, err := webpushpush.New(identity, "mailto:synthetic@example.com")

	if err != nil {
		t.Fatal(err)
	}

	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, driver); got != "[REDACTED Web Push driver]" {
			t.Fatalf("owned driver not redacted for %s", format)
		}
	}
}
