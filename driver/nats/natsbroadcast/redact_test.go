package natsbroadcast_test

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/driver/nats/natsbroadcast"
)

// TestConfigDoesNotPrintSecrets checks ordinary value/Go-syntax fmt and JSON
// slog output for explicit secret fields; it does not claim all fmt error paths.
func TestConfigDoesNotPrintSecrets(t *testing.T) {
	t.Parallel()

	config := natsbroadcast.Config{
		Password: "secret-password-value",
		Token:    "secret-token-value",
		NKeySeed: "secret-nkeyseed-value",
	}
	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)

	output := fmt.Sprintf("%v %+v %#v %s", config, config, config, logs.String())

	for _, secret := range []string{"secret-password-value", "secret-token-value", "secret-nkeyseed-value"} {
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

	if strings.Contains(fmt.Sprintf("%+v", natsbroadcast.Config{}), "[REDACTED]") {
		t.Fatal("empty secret was redacted")
	}
}

// TestConfigRedactsURLCredentials covers passwords and NATS token userinfo in
// every supported diagnostic formatter without mutating the original record.
func TestConfigRedactsURLCredentials(t *testing.T) {
	t.Parallel()

	config := natsbroadcast.Config{URLs: []string{
		"nats://alice:private-password@broker:4222",
		"nats://private-token@broker:4222",
	}}
	original := slices.Clone(config.URLs)

	for _, output := range configDiagnostics(t, config) {
		if strings.Contains(output, "private-") || strings.Contains(output, "alice") {
			t.Fatal("URL credential exposed")
		}

		if !strings.Contains(output, "broker:4222") {
			t.Fatal("qualified server authority disappeared from diagnostics")
		}
	}

	if !slices.Equal(config.URLs, original) {
		t.Fatal("diagnostic formatting mutated configuration")
	}
}

// TestConfigRedactsSchemelessURLCredentials checks native server spellings that
// net/url treats as opaque data or paths. Both value and pointer diagnostics
// must hide their userinfo without modifying the caller's URL slice.
func TestConfigRedactsSchemelessURLCredentials(t *testing.T) {
	t.Parallel()

	urls := []string{
		"url-private-user:url-private-password@broker:4222",
		"url-private-token@broker:4222",
		"url-private-token@broker",
		"nats://public-broker:4222, url-private-token@other:4222",
	}
	original := slices.Clone(urls)
	config := natsbroadcast.Config{URLs: urls}

	for _, output := range configDiagnostics(t, config) {
		if strings.Contains(output, "url-private") {
			t.Fatalf("schemeless URL credential exposed: %s", output)
		}
	}

	if !slices.Equal(urls, original) {
		t.Fatal("diagnostics mutated caller-owned URLs")
	}
}

// TestConfigRedactsMalformedURLCredentials covers opaque, path-only, hostless,
// and parse-error forms. An ambiguous entry must disappear as a whole rather
// than preserve credential-bearing text outside parsed URL userinfo.
func TestConfigRedactsMalformedURLCredentials(t *testing.T) {
	t.Parallel()

	urls := []string{
		"nats:url-private-user:url-private-password@broker:4222",
		"nats:///url-private-token@broker:4222",
		"nats://url-private-user:url-private-password@",
		"nats://url-private-user:url-private-password@:4222",
		"nats://url-private-user:url-private-password@broker:badport",
		"nats://url-private-token%zz@broker:4222",
		"nats://url-private-token@[broker",
		"/url-private-token@broker",
	}
	original := slices.Clone(urls)
	config := natsbroadcast.Config{URLs: urls}

	for _, output := range configDiagnostics(t, config) {
		if strings.Contains(output, "url-private") || strings.Contains(output, "broker") {
			t.Fatalf("malformed URL was not redacted wholesale: %s", output)
		}
	}

	if !slices.Equal(urls, original) {
		t.Fatal("diagnostics mutated caller-owned URLs")
	}
}

// TestConfigRedactsUnsupportedFormatVerbs checks fmt's fallback diagnostics,
// which bypass Stringer for numeric, boolean, and unknown verbs. These calls
// format Config itself; they do not extract fields or convert its record type.
func TestConfigRedactsUnsupportedFormatVerbs(t *testing.T) {
	t.Parallel()

	config := natsbroadcast.Config{
		URLs:     []string{"fmt-private-user:fmt-private-password@broker:4222"},
		Password: "fmt-private-password",
		Token:    "fmt-private-token",
		NKeySeed: "fmt-private-seed",
	}

	for _, format := range []string{"%d", "%t", "%f", "%b", "%c", "%e", "%g", "%o", "%O", "%U", "%z"} {
		output := fmt.Sprintf(format, config) + fmt.Sprintf(format, &config)

		if strings.Contains(output, "fmt-private") {
			t.Errorf("%s exposed owned credentials", format)
		}
	}
}

// TestConfigStringProtectsExternallyChosenFormats checks the documented escape
// hatch for fmt paths such as invalid %p and %w, which Go handles before it can
// invoke any Config method. Only the already-redacted string reaches fmt here.
func TestConfigStringProtectsExternallyChosenFormats(t *testing.T) {
	t.Parallel()

	config := natsbroadcast.Config{
		URLs:     []string{"format-private-token@broker"},
		Password: "format-private-password",
		Token:    "format-private-token",
		NKeySeed: "format-private-seed",
	}

	for _, format := range []string{"%p", "%w", "%d", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(format, config.String()), "format-private") {
			t.Fatalf("%s exposed credentials from the redacted string", format)
		}
	}
}

// TestConfigPreservesStringFormatting checks the public textual representation,
// flags, width, precision, quoting, and hexadecimal encoding after Format takes
// over from Stringer. The expected text is an independent diagnostic fixture.
func TestConfigPreservesStringFormatting(t *testing.T) {
	t.Parallel()

	config := natsbroadcast.Config{Token: "private-token"}
	const text = "{URLs:[] Name: MaxReconnects:0 NoReconnect:false ReconnectWait:0s Timeout:0s " +
		"Username: Password: Token:[REDACTED] NKeySeed: CredentialsFile: RootCAs:[] " +
		"Prefix: QueueGroup: TLS:<nil> Logger:<nil>}"

	for _, format := range []string{
		"%v", "%+v", "%20.12v", "%s", "%q", "%+q", "%#q", "%x", "%X", "%300s", "%.80s",
	} {
		if fmt.Sprintf(format, config) != fmt.Sprintf(format, text) {
			t.Fatalf("%s changed string formatting", format)
		}
	}

	if fmt.Sprintf("%#v", config) != config.GoString() {
		t.Fatal("Go syntax formatting changed its representation")
	}
}

// configDiagnostics exercises the public string methods, fmt dispatch for
// values and pointers, and both standard slog handlers. Each entry is an
// independently observable diagnostic, not a converted method-free record.
func configDiagnostics(t *testing.T, config natsbroadcast.Config) []string {
	t.Helper()

	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)
	slog.New(slog.NewTextHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", &config)

	return []string{
		config.String(), config.GoString(), config.LogValue().String(),
		fmt.Sprintf("%v", config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config),
		fmt.Sprintf("%s", config), fmt.Sprintf("%q", config),
		fmt.Sprintf("%v", &config), fmt.Sprintf("%+v", &config), fmt.Sprintf("%#v", &config),
		fmt.Sprintf("%s", &config), fmt.Sprintf("%q", &config),
		logs.String(),
	}
}
