package key_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/foundation/key"
)

// ExampleGenerateShared demonstrates provisioning without printing a secret.
func ExampleGenerateShared() {
	material, err := key.GenerateShared()

	if err != nil {
		fmt.Println("generation failed")

		return
	}

	restored, err := key.ParseShared(material.Base64())

	if err != nil {
		fmt.Println("parsing failed")

		return
	}

	fmt.Println(len(restored.Bytes()))
	// Output: 32
}

// TestSharedRoundTrip verifies generation, standard encoding, and parsing.
func TestSharedRoundTrip(t *testing.T) {
	t.Parallel()

	first, err := key.GenerateShared()

	if err != nil {
		t.Fatal(err)
	}

	second, err := key.GenerateShared()

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := key.ParseShared(first.Base64() + "\n")

	if err != nil || !bytes.Equal(first.Bytes(), parsed.Bytes()) || len(parsed.Bytes()) != 32 {
		t.Fatalf("round trip failed: %v", err)
	}

	if bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("independent generations repeated")
	}
}

// TestSharedInvalidLengths rejects every length other than 32 bytes.
func TestSharedInvalidLengths(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 16, 24, 31, 33, 64, 32768} {
		raw := make([]byte, size)

		if _, err := key.NewShared(raw); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("size %d accepted", size)
		}

		if _, err := key.ParseShared(base64.StdEncoding.EncodeToString(raw)); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("encoded size %d accepted", size)
		}
	}
}

// TestSharedImports32Bytes accepts the one supported symmetric key length.
func TestSharedImports32Bytes(t *testing.T) {
	t.Parallel()

	material, err := key.NewShared(bytes.Repeat([]byte{42}, 32))

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := key.ParseShared(material.Base64())

	if err != nil || parsed.Err() != nil || !bytes.Equal(parsed.Bytes(), material.Bytes()) {
		t.Fatalf("round trip failed: %v", err)
	}

	generated, err := key.GenerateShared()

	if err != nil || len(generated.Bytes()) != 32 {
		t.Fatalf("generation failed: %v", err)
	}
}

// TestSharedRejectsShortInput prevents low-length symmetric material publication.
func TestSharedRejectsShortInput(t *testing.T) {
	t.Parallel()

	if _, err := key.NewShared(make([]byte, 31)); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("short secret: %v", err)
	}
}

// TestSymmetricOwnership detaches both constructor input and explicit exports.
func TestSymmetricOwnership(t *testing.T) {
	t.Parallel()

	raw := bytes.Repeat([]byte{42}, 32)

	secret, err := key.NewShared(raw)

	if err != nil {
		t.Fatal(err)
	}

	clear(raw)
	clear(secret.Bytes())

	if secret.Bytes()[0] != 42 {
		t.Fatal("key aliases mutable state")
	}
}

// TestParseRejectsMalformedBase64 prevents decoder leniency or secret disclosure.
func TestParseRejectsMalformedBase64(t *testing.T) {
	t.Parallel()

	valid := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))

	for _, encoded := range []string{
		"", "private-secret-not-base64", valid[:10] + "\n" + valid[10:],
		valid[:len(valid)-1], strings.Repeat("a", 65537),
	} {
		if _, err := key.ParseShared(encoded); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("invalid signing encoding accepted: %v", err)
		}
	}
}

// TestLoads verifies explicit files and missing files without implicit generation.
func TestLoads(t *testing.T) {
	t.Parallel()

	material, err := key.GenerateShared()

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "secret")

	if err := os.WriteFile(path, []byte(material.Base64()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := key.LoadShared(path)

	if err != nil || !bytes.Equal(loaded.Bytes(), material.Bytes()) {
		t.Fatalf("load secret: %v", err)
	}

	if _, err := key.LoadShared(path + ".missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

// TestLoadRejectsOversize bounds file processing independently of parser checks.
func TestLoadRejectsOversize(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "oversize")

	if err := os.WriteFile(path, bytes.Repeat([]byte{'a'}, 65537), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := key.LoadShared(path); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("oversize file: %v", err)
	}
}

// TestZeroValuesAreInvalid prevents missing settings from becoming usable keys.
func TestZeroValuesAreInvalid(t *testing.T) {
	t.Parallel()

	if keyErr := (key.Shared{}).Err(); !errors.Is(keyErr, key.ErrInvalid) {
		t.Fatal("zero secret accepted")
	}

	if keyErr := (key.Signing{}).Err(); !errors.Is(keyErr, key.ErrInvalid) {
		t.Fatal("zero P-256 accepted")
	}

	if (key.Signing{}).PrivateKey() != nil || (key.Signing{}).PEM() != nil ||
		(key.Signing{}).VAPIDPublicKey() != "" {
		t.Fatal("zero identity exported material")
	}
}

// TestSharedZeroExports returns no usable material when configuration omits
// a shared key. Encoding must not synthesize bytes for the invalid value.
func TestSharedZeroExports(t *testing.T) {
	t.Parallel()

	if len((key.Shared{}).Bytes()) != 0 || (key.Shared{}).Base64() != "" {
		t.Fatal("zero shared key exported material")
	}
}

// TestFormattingRedactsMaterial protects common formatting and logging paths.
func TestFormattingRedactsMaterial(t *testing.T) {
	t.Parallel()

	material, err := key.NewShared(bytes.Repeat([]byte{'x'}, 32))

	if err != nil {
		t.Fatal(err)
	}

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []any{material, identity, exchangeFixture(t)} {
		var output strings.Builder

		slog.New(slog.NewJSONHandler(&output, nil)).InfoContext(t.Context(), "key", "value", value)

		formatted := fmt.Sprintf("%v %+v %#v %s", value, value, value, output.String())

		if strings.Contains(formatted, strings.Repeat("x", 32)) ||
			strings.Contains(formatted, material.Base64()) || !strings.Contains(formatted, "REDACTED") {
			t.Fatal("formatting leaked key material")
		}

		encoded, err := json.Marshal(value)

		if err == nil || len(encoded) != 0 {
			t.Fatalf("unexpected implicit serialization: %s, %v", encoded, err)
		}
	}
}
