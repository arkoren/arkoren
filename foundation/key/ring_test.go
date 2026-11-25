package key_test

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/foundation/key"
)

// ringMarker is the redaction text every Ring formatting path must print.
const ringMarker = "[REDACTED shared key ring]"

// mustShared returns a fresh random shared key or fails the test.
func mustShared(t *testing.T) key.Shared {
	t.Helper()

	material, err := key.GenerateShared()

	if err != nil {
		t.Fatal(err)
	}

	return material
}

// ExampleParseRing demonstrates reading a rotated key list from configuration.
func ExampleParseRing() {
	current, _ := key.GenerateShared()
	previous, _ := key.GenerateShared()

	ring, err := key.ParseRing[key.Shared](current.Base64() + "," + previous.Base64())

	if err != nil {
		fmt.Println("parsing failed")

		return
	}

	fmt.Println(ring.Len(), len(ring.Previous()))
	// Output: 2 1
}

// ExampleNewRing demonstrates building a ring for a key rotation.
func ExampleNewRing() {
	current, _ := key.GenerateShared()
	previous, _ := key.GenerateShared()

	ring, err := key.NewRing(current, previous)

	if err != nil {
		fmt.Println("invalid ring")

		return
	}

	fmt.Println(ring.Current().Fingerprint() == current.Fingerprint())
	// Output: true
}

// TestNewRingOrdersKeys verifies the first key is current and the rest previous.
func TestNewRingOrdersKeys(t *testing.T) {
	t.Parallel()

	current, older, oldest := mustShared(t), mustShared(t), mustShared(t)
	ring, err := key.NewRing(current, older, oldest)

	if err != nil {
		t.Fatal(err)
	}

	previous := ring.Previous()

	if ring.Len() != 3 || ring.Current().Base64() != current.Base64() ||
		len(previous) != 2 || previous[0].Base64() != older.Base64() || previous[1].Base64() != oldest.Base64() {
		t.Fatal("ring order is wrong")
	}
}

// TestNewRingRejectsZeroCurrent verifies a zero current key is invalid.
func TestNewRingRejectsZeroCurrent(t *testing.T) {
	t.Parallel()

	if _, err := key.NewRing(key.Shared{}); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
}

// TestNewRingRejectsZeroPrevious verifies a zero previous key is invalid.
func TestNewRingRejectsZeroPrevious(t *testing.T) {
	t.Parallel()

	if _, err := key.NewRing(mustShared(t), key.Shared{}); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
}

// TestNewRingRejectsDuplicates verifies repeated material is invalid.
func TestNewRingRejectsDuplicates(t *testing.T) {
	t.Parallel()

	material := mustShared(t)

	if _, err := key.NewRing(material, mustShared(t), material); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
}

// TestNewRingCopiesInput verifies later edits to the input slice do not change the ring.
func TestNewRingCopiesInput(t *testing.T) {
	t.Parallel()

	older := mustShared(t)
	input := []key.Shared{older}
	ring, err := key.NewRing(mustShared(t), input...)

	if err != nil {
		t.Fatal(err)
	}

	input[0] = key.Shared{}

	if ring.Previous()[0].Base64() != older.Base64() {
		t.Fatal("ring shares the input slice")
	}
}

// TestRingPreviousReturnsCopy verifies mutating the result cannot change the ring.
func TestRingPreviousReturnsCopy(t *testing.T) {
	t.Parallel()

	older := mustShared(t)
	ring, err := key.NewRing(mustShared(t), older)

	if err != nil {
		t.Fatal(err)
	}

	ring.Previous()[0] = key.Shared{}

	if ring.Previous()[0].Base64() != older.Base64() {
		t.Fatal("Previous exposes internal state")
	}
}

// TestRingZeroValueIsInvalid verifies the zero ring reports ErrInvalid and is empty.
func TestRingZeroValueIsInvalid(t *testing.T) {
	t.Parallel()

	var ring key.Ring[key.Shared]

	if !errors.Is(ring.Err(), key.ErrInvalid) || ring.Len() != 0 || len(ring.Previous()) != 0 ||
		ring.Current().Err() == nil || ring.Encode() != "" {
		t.Fatal("zero ring is not invalid and empty")
	}
}

// TestParseRingSingleKey verifies a bare Shared encoding is a one-key ring.
func TestParseRingSingleKey(t *testing.T) {
	t.Parallel()

	material := mustShared(t)
	ring, err := key.ParseRing[key.Shared](material.Base64() + "\n")

	if err != nil {
		t.Fatal(err)
	}

	if ring.Len() != 1 || ring.Current().Base64() != material.Base64() {
		t.Fatal("single key ring is wrong")
	}
}

// TestParseRingTrimsEntries verifies whitespace around entries is ignored.
func TestParseRingTrimsEntries(t *testing.T) {
	t.Parallel()

	current, older := mustShared(t), mustShared(t)
	ring, err := key.ParseRing[key.Shared]("  " + current.Base64() + " ,\t" + older.Base64() + "  ")

	if err != nil {
		t.Fatal(err)
	}

	if ring.Len() != 2 || ring.Previous()[0].Base64() != older.Base64() {
		t.Fatal("entries were not trimmed")
	}
}

// TestParseRingRejectsMalformed verifies every malformed form returns ErrInvalid
// without echoing material.
func TestParseRingRejectsMalformed(t *testing.T) {
	t.Parallel()

	good := mustShared(t).Base64()
	short := "AAAA"
	inputs := []string{
		"",
		"   ",
		good + ",",
		"," + good,
		good + ",," + mustShared(t).Base64(),
		good + "," + good,
		good + "\n," + mustShared(t).Base64(),
		good + "," + "\n" + mustShared(t).Base64(),
		good + "," + short,
		short,
		strings.Repeat("A", 70*1024),
	}

	for _, input := range inputs {
		_, err := key.ParseRing[key.Shared](input)

		if !errors.Is(err, key.ErrInvalid) || strings.Contains(err.Error(), good) {
			t.Fatalf("input %.12q: error = %v", input, err)
		}
	}
}

// TestRingEncodeRoundTrips verifies Encode output parses back to the same keys.
func TestRingEncodeRoundTrips(t *testing.T) {
	t.Parallel()

	current, older := mustShared(t), mustShared(t)
	ring, err := key.NewRing(current, older)

	if err != nil {
		t.Fatal(err)
	}

	if ring.Encode() != current.Base64()+","+older.Base64() {
		t.Fatal("Encode is not canonical")
	}

	parsed, err := key.ParseRing[key.Shared](ring.Encode())

	if err != nil || parsed.Encode() != ring.Encode() {
		t.Fatalf("round trip failed: %v", err)
	}
}

// TestRingUnmarshalText verifies a ring decodes through the text interface.
func TestRingUnmarshalText(t *testing.T) {
	t.Parallel()

	material := mustShared(t)

	var ring key.Ring[key.Shared]

	if err := ring.UnmarshalText([]byte(material.Base64())); err != nil {
		t.Fatal(err)
	}

	if ring.Current().Base64() != material.Base64() {
		t.Fatal("UnmarshalText decoded the wrong key")
	}
}

// TestRingUnmarshalTextKeepsRingOnFailure verifies a failed decode changes nothing.
func TestRingUnmarshalTextKeepsRingOnFailure(t *testing.T) {
	t.Parallel()

	material := mustShared(t)
	ring, err := key.NewRing(material)

	if err != nil {
		t.Fatal(err)
	}

	if err := ring.UnmarshalText([]byte("nope")); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}

	if ring.Current().Base64() != material.Base64() {
		t.Fatal("failed decode replaced the ring")
	}
}

// TestRingRedactsFormatting verifies every formatting path prints the marker only.
func TestRingRedactsFormatting(t *testing.T) {
	t.Parallel()

	material := mustShared(t)
	ring, err := key.NewRing(material)

	if err != nil {
		t.Fatal(err)
	}

	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		output := fmt.Sprintf(format, ring)

		if !strings.Contains(output, ringMarker) || strings.Contains(output, material.Base64()) {
			t.Fatalf("%s printed %q", format, output)
		}
	}

	if ring.LogValue().String() != ringMarker || ring.String() != ringMarker || ring.GoString() != ringMarker {
		t.Fatal("redaction methods differ")
	}
}

// TestRingRedactsInStruct verifies a ring in an unexported field stays redacted.
func TestRingRedactsInStruct(t *testing.T) {
	t.Parallel()

	material := mustShared(t)
	ring, err := key.NewRing(material)

	if err != nil {
		t.Fatal(err)
	}

	holder := struct{ ring key.Ring[key.Shared] }{ring}
	output := fmt.Sprintf("%v %+v %#v", holder, holder, holder)

	if strings.Contains(output, material.Base64()) {
		t.Fatalf("struct output leaked material: %q", output)
	}
}

// TestRingRedactsLogs verifies structured logging never prints key material.
func TestRingRedactsLogs(t *testing.T) {
	t.Parallel()

	material := mustShared(t)
	ring, err := key.NewRing(material)

	if err != nil {
		t.Fatal(err)
	}

	var output strings.Builder

	logger := slog.New(slog.NewTextHandler(&output, nil))
	logger.InfoContext(t.Context(), "configuration", "keys", ring)

	if !strings.Contains(output.String(), ringMarker) || strings.Contains(output.String(), material.Base64()) {
		t.Fatalf("log output = %q", output.String())
	}
}
