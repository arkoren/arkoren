package certificate_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/foundation/certificate"
)

// holder keeps an identity in an unexported field, the shape in which fmt
// cannot call String, GoString or Format on it and reflection prints the raw
// private field.
type holder struct {
	secret certificate.Identity
}

// privateSecrets generates an identity and lists every plausible textual form
// of its PKCS #8 DER: the bytes, their escaped form as printed by text log
// handlers, padded base64 and lowercase hex.
func privateSecrets(t *testing.T) (certificate.Identity, []string) {
	t.Helper()

	identity, err := certificate.GenerateRoot("Redaction test")
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(identity.PrivatePEM())
	if block == nil {
		t.Fatal("missing private PEM block")
	}

	quoted := strconv.Quote(string(block.Bytes))

	return identity, []string{
		string(block.Bytes),
		quoted[1 : len(quoted)-1],
		base64.StdEncoding.EncodeToString(block.Bytes),
		hex.EncodeToString(block.Bytes),
	}
}

// assertHidden fails when output contains any form of the private key.
func assertHidden(t *testing.T, what, output string, secrets []string) {
	t.Helper()

	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatalf("%s leaked private key: %q", what, output)
		}
	}
}

func TestIdentityNestedFormattingRedacted(t *testing.T) {
	t.Parallel()

	identity, secrets := privateSecrets(t)

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%q", "%d"} {
		assertHidden(t, "nested "+verb, fmt.Sprintf(verb, holder{secret: identity}), secrets)
		assertHidden(t, "nested pointer "+verb, fmt.Sprintf(verb, &holder{secret: identity}), secrets)
	}

	// encoding/json/v2 rejects the type with an error; the output must still be
	// free of key material.
	encoded, _ := json.Marshal(struct{ Identity certificate.Identity }{identity})

	assertHidden(t, "json", string(encoded), secrets)
}

func TestIdentityNestedLoggingRedacted(t *testing.T) {
	t.Parallel()

	identity, secrets := privateSecrets(t)

	var text, jsonOut bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).InfoContext(t.Context(), "x", "cfg", holder{secret: identity})
	slog.New(slog.NewJSONHandler(&jsonOut, nil)).InfoContext(t.Context(), "x", "cfg", holder{secret: identity})
	assertHidden(t, "text handler", text.String(), secrets)
	assertHidden(t, "json handler", jsonOut.String(), secrets)
}

func TestIdentityVerbsRedacted(t *testing.T) {
	t.Parallel()

	identity, secrets := privateSecrets(t)
	const marker = "certificate.Identity([REDACTED])"

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x", "%X", "%t", "%08.3f"} {
		for _, value := range []certificate.Identity{identity, {}} {
			output := fmt.Sprintf(verb, value)
			assertHidden(t, verb, output, secrets)

			if !strings.Contains(output, marker) {
				t.Fatalf("%s = %q, want marker %q", verb, output, marker)
			}
		}
	}
}
