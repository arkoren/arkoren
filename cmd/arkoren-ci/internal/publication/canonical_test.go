package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"strings"
	"testing"
)

func TestCanonicalV1PythonGoldenAllASCIIUnicodeAndNestedIntegers(t *testing.T) {
	var text strings.Builder
	for char := range 128 {
		text.WriteRune(rune(char))
	}
	text.WriteString("é中😀\u2028")
	value := map[string]any{"z": text.String(), "a": map[string]any{"b": []any{true, false, nil, -123, 0, int64(9007199254740993)}, "/": "</script>&"}, "😀": "value", "é": "x"}
	encoded, err := Canonical(value)
	if err != nil {
		t.Fatal(err)
	}
	// Generated independently with Python json.dumps(sort_keys=True,
	// separators=(',', ':'), ensure_ascii=True), not Go serialization.
	digest := sha256.Sum256([]byte(encoded))
	if hex.EncodeToString(digest[:]) != "5ff37e54383074e122e01df095904063ec786e9683e59a1f035b42fd528895f3" {
		t.Fatalf("signed-v1 byte protocol changed: %s", encoded)
	}
}

func TestCanonicalV1RejectsFloats(t *testing.T) {
	if _, err := Canonical(map[string]any{"number": float64(1)}); err == nil {
		t.Fatal("integer-valued float accepted into signed protocol")
	}
	if _, err := Canonical(jsontext.Value(`{"number":1.25}`)); err == nil {
		t.Fatal("float accepted into integer-only signed protocol")
	}
}
