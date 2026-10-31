package awsstorage

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
)

// TestConfigRedactsCredentials checks ordinary, Go-syntax, and structured-log
// formatting for credential leaks while retaining the useful region setting.
func TestConfigRedactsCredentials(t *testing.T) {
	t.Parallel()

	config := Config{
		Region:      "eu-west-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "supersecretkey", "sessiontoken"),
	}

	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).InfoContext(t.Context(), "config", "config", config)

	for _, out := range []string{fmt.Sprintf("%v %+v %#v", config, config, config), logged.String()} {
		for _, secret := range []string{"supersecretkey", "sessiontoken", "AKIDEXAMPLE"} {
			if strings.Contains(out, secret) {
				t.Fatalf("%s leaked: %s", secret, out)
			}
		}

		if !strings.Contains(out, "eu-west-1") {
			t.Fatalf("non-secret field missing: %s", out)
		}
	}
}
