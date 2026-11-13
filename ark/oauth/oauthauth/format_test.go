package oauthauth

import (
	"fmt"
	"testing"
)

// TestOwnedClientFormattingRedactsCredentials covers the client returned by
// New, not arbitrary caller-supplied native clients or captured functions.
func TestOwnedClientFormattingRedactsCredentials(t *testing.T) {
	t.Parallel()

	client, err := New(GitHub(), testCipher(t), "cid", func(client *ClientBuilder) {
		client.ClientSecret("synthetic-owned-secret")
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, client); got != "[REDACTED OAuth client]" {
			t.Fatalf("owned client not redacted for %s", format)
		}
	}
}

// TestRetainedDraftFormattingRedactsCredentials protects a retained public
// construction draft as well as the completed client, including draft copies.
func TestRetainedDraftFormattingRedactsCredentials(t *testing.T) {
	t.Parallel()

	var draft *ClientBuilder
	_, err := New(GitHub(), testCipher(t), "cid", func(client *ClientBuilder) {
		client.ClientSecret("synthetic-owned-secret")
		draft = client
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []any{draft, *draft} {
		if got := fmt.Sprintf("%#v", value); got != "[REDACTED OAuth draft]" {
			t.Fatal("owned draft was not redacted")
		}
	}
}
