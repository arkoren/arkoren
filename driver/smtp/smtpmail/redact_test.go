package smtpmail_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/driver/smtp/smtpmail"
)

// ExampleDriver_Format demonstrates that fmt never recursively inspects SMTP
// credentials, including through copied values and the Go-syntax formatting verb.
func ExampleDriver_Format() {
	driver, err := smtpmail.New("smtp.example.com", 587)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Printf("%#v\n", driver)
	// Output: smtpmail.Driver
}

// TestDriverFormattingDoesNotExposePassword exercises Go's public formatting
// protocol on the driver pointer and value. Config's own redaction must not be
// bypassed by reflection into the driver's unexported configuration field.
func TestDriverFormattingDoesNotExposePassword(t *testing.T) {
	t.Parallel()

	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
		transport.Username("synthetic-user")
		transport.Password("synthetic-password-marker")
	})

	if err != nil {
		t.Fatal(err)
	}

	output := fmt.Sprintf("%v %+v %#v | %v %+v %#v", driver, driver, driver, *driver, *driver, *driver)

	if strings.Contains(output, "synthetic-password-marker") {
		t.Fatal("driver formatting exposed the SMTP password")
	}
}

// TestConfigDoesNotPrintSecrets verifies fmt and slog never show secret fields.
func TestConfigDoesNotPrintSecrets(t *testing.T) {
	t.Parallel()

	config := smtpmail.Config{
		Password: "secret-password-value",
	}
	var logs strings.Builder

	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)

	output := fmt.Sprintf("%v %+v %#v %s", config, config, config, logs.String())

	for _, secret := range []string{"secret-password-value"} {
		if strings.Contains(output, secret) {
			t.Fatalf("output contains %q: %s", secret, output)
		}
	}

	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("output has no placeholder: %s", output)
	}
}

// TestConfigPrintsEmptySecretsEmpty verifies unset secrets are not redacted.
func TestConfigPrintsEmptySecretsEmpty(t *testing.T) {
	t.Parallel()

	if strings.Contains(fmt.Sprintf("%+v", smtpmail.Config{}), "[REDACTED]") {
		t.Fatal("empty secret was redacted")
	}
}
