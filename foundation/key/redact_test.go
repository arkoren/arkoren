package key_test

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/foundation/key"
)

// holder keeps a value in an unexported field, the shape in which fmt cannot
// call String, GoString or Format on it and reflection prints the raw field.
type holder[T any] struct {
	secret T
}

// leaks lists every plausible textual form of raw: the bytes as a string,
// its escaped form as printed by text log handlers, padded base64 and
// lowercase hex.
func leaks(raw []byte) []string {
	quoted := strconv.Quote(string(raw))

	return []string{string(raw), quoted[1 : len(quoted)-1], base64.StdEncoding.EncodeToString(raw), hex.EncodeToString(raw)}
}

// assertHidden fails when output contains any form of the secret.
func assertHidden(t *testing.T, what, output string, secrets []string) {
	t.Helper()

	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatalf("%s leaked key material: %q", what, output)
		}
	}
}

// checkNestedFormatting formats value held in an unexported struct field.
func checkNestedFormatting[T any](t *testing.T, value T, secrets []string) {
	t.Helper()

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%q", "%d"} {
		assertHidden(t, "nested "+verb, fmt.Sprintf(verb, holder[T]{secret: value}), secrets)
		assertHidden(t, "nested pointer "+verb, fmt.Sprintf(verb, &holder[T]{secret: value}), secrets)
	}

	// encoding/json/v2 has no hook on these types and rejects them with an
	// error; either way the output must not contain the material.
	for name, v := range map[string]any{"field": struct{ Secret T }{value}, "pointer": &struct{ Secret T }{value}, "key": value} {
		encoded, _ := json.Marshal(v)
		assertHidden(t, "json "+name, string(encoded), secrets)
	}
}

// checkNestedLogging logs value held in an unexported struct field through
// both slog handlers.
func checkNestedLogging[T any](t *testing.T, value T, secrets []string) {
	t.Helper()

	var text, jsonOut bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).InfoContext(t.Context(), "x", "cfg", holder[T]{secret: value})
	slog.New(slog.NewJSONHandler(&jsonOut, nil)).InfoContext(t.Context(), "x", "cfg", holder[T]{secret: value})
	assertHidden(t, "text handler", text.String(), secrets)
	assertHidden(t, "json handler", jsonOut.String(), secrets)
}

// checkVerbs applies every verb directly and requires the redaction marker.
func checkVerbs[T any](t *testing.T, value T, secrets []string, marker string) {
	t.Helper()

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x", "%X", "%t", "%08.3f"} {
		output := fmt.Sprintf(verb, value)
		assertHidden(t, verb, output, secrets)

		if !strings.Contains(output, marker) {
			t.Fatalf("%s = %q, want marker %q", verb, output, marker)
		}
	}
}

func sharedSecrets(t *testing.T) (key.Shared, []string) {
	t.Helper()

	material, err := key.GenerateShared()
	if err != nil {
		t.Fatal(err)
	}

	return material, leaks(material.Bytes())
}

func signingSecrets(t *testing.T) (key.Signing, []string) {
	t.Helper()

	identity, err := key.GenerateSigning()
	if err != nil {
		t.Fatal(err)
	}

	raw, err := identity.PrivateKey().Bytes()
	if err != nil {
		t.Fatal(err)
	}

	return identity, leaks(raw)
}

func exchangeSecrets(t *testing.T) (key.Exchange, []string) {
	t.Helper()

	identity, err := key.GenerateExchange()
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(identity.PEM())
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		t.Fatal(err)
	}

	return identity, append(leaks(identity.PrivateKey().Bytes()), leaks(block.Bytes)...)
}

func TestSharedNestedFormattingRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := sharedSecrets(t)
	checkNestedFormatting(t, value, secrets)
}

func TestSharedNestedLoggingRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := sharedSecrets(t)
	checkNestedLogging(t, value, secrets)
}

func TestSharedVerbsRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := sharedSecrets(t)
	checkVerbs(t, value, secrets, "[REDACTED shared key]")
}

func TestSigningNestedFormattingRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := signingSecrets(t)
	checkNestedFormatting(t, value, secrets)
}

func TestSigningNestedLoggingRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := signingSecrets(t)
	checkNestedLogging(t, value, secrets)
}

func TestSigningVerbsRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := signingSecrets(t)
	checkVerbs(t, value, secrets, "[REDACTED signing key]")
}

func TestExchangeNestedFormattingRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := exchangeSecrets(t)
	checkNestedFormatting(t, value, secrets)
}

func TestExchangeNestedLoggingRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := exchangeSecrets(t)
	checkNestedLogging(t, value, secrets)
}

func TestExchangeVerbsRedacted(t *testing.T) {
	t.Parallel()

	value, secrets := exchangeSecrets(t)
	checkVerbs(t, value, secrets, "[REDACTED exchange key]")
}

func TestZeroValuesRedactedInVerbs(t *testing.T) {
	t.Parallel()

	checkVerbs(t, key.Shared{}, nil, "[REDACTED shared key]")
	checkVerbs(t, key.Signing{}, nil, "[REDACTED signing key]")
	checkVerbs(t, key.Exchange{}, nil, "[REDACTED exchange key]")
}
