package awssecret_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"arkoren.dev/driver/aws/awssecret"
	"arkoren.dev/driver/aws/awsstorage"
)

// TestConfigRedactsCredentials checks ordinary, Go-syntax, and structured-log
// formatting for credential leaks while retaining the useful region setting.
func TestConfigRedactsCredentials(t *testing.T) {
	t.Parallel()

	config := awssecret.Config{
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

// diagnosticProvider is a caller-owned native provider whose formatter and
// retrieval path would expose synthetic credentials if formatting invoked them.
type diagnosticProvider struct {
	// formatted counts calls to the native provider's unsafe diagnostic method.
	formatted int
	// retrieved counts native credential loads, which reporting must never trigger.
	retrieved int
}

// Retrieve returns dummy data only if reporting accidentally requests credentials.
// No environment, credential chain, or external identity provider is consulted.
func (diagnosticProvider *diagnosticProvider) Retrieve(context.Context) (aws.Credentials, error) {
	diagnosticProvider.retrieved++

	return aws.Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-provider-secret"}, nil
}

// String represents the native owner's formatter, outside Config's redaction
// contract. The public reporting records must use only its type, not this output.
func (diagnosticProvider *diagnosticProvider) String() string {
	diagnosticProvider.formatted++

	return "synthetic-provider-secret"
}

// TestConfigFormattingDoesNotInvokeCredentialOwner verifies both AWS reporting
// records through fmt and slog. Shared native ownership is preserved without
// recursively inspecting, formatting, or retrieving provider credentials.
func TestConfigFormattingDoesNotInvokeCredentialOwner(t *testing.T) {
	t.Parallel()

	provider := &diagnosticProvider{}

	for _, record := range []any{awssecret.Config{Credentials: provider}, awsstorage.Config{Credentials: provider}} {
		var output bytes.Buffer
		slog.New(slog.NewJSONHandler(&output, nil)).InfoContext(t.Context(), "config", "record", record)
		diagnostic := fmt.Sprintf("%v %+v %#v", record, record, record) + output.String()

		if strings.Contains(diagnostic, "synthetic-provider-secret") {
			t.Fatal("credential-owner diagnostics escaped Config redaction")
		}
	}

	if provider.formatted != 0 || provider.retrieved != 0 {
		t.Fatal("Config reporting invoked the shared credential provider")
	}
}
