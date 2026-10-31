package network

import (
	"testing"
)

// TestEndpointRejectsUnsafeAuthorities keeps URI parser ambiguities outside
// the destination policy, independent of the approved provider namespace.
func TestEndpointRejectsUnsafeAuthorities(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"http://fcm.googleapis.com/path", "https://user@fcm.googleapis.com/path",
		"https://fcm.googleapis.com/path#", "https://fcm.googleapis.com:444/path",
		"https://fcm.googleapis.com:/path", "https://fcm.googleapis.com./path",
		"https://FCM.googleapis.com/path", "https://127.0.0.1/path",
		"https://[::1]/path", "https://fcm.googleapis.com%2f.evil/path",
		"https://fcm.googleapis.com\\@evil/path", "https://-bad.example/path",
		"https://fcm..googleapis.com/path", "https://fcm.googleapis.com:0443/path",
	} {
		if _, err := parseEndpoint(endpoint); err == nil {
			t.Fatal("accepted unsafe endpoint")
		}
	}
}
