package pagination_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/foundation/key"
)

// ExampleCursorCodec demonstrates typed construction and signed round-tripping
// without provisioning random material or accessing a network or secret file.
func ExampleCursorCodec() {
	// Fixed example material only; provision a private key for real applications.
	material, err := key.NewShared([]byte("0123456789abcdef0123456789abcdef"))

	if err != nil {
		fmt.Println(err)

		return
	}

	codec, err := pagination.NewCursorCodec(material)

	if err != nil {
		fmt.Println(err)

		return
	}

	cursor, err := codec.Encode(42)

	if err != nil {
		fmt.Println(err)

		return
	}

	value, err := codec.Decode[int](cursor)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value)
	// Output: 42
}

// position is a cursor payload for codec tests.
type position struct {
	// ID identifies the item at the cursor boundary after signature verification.
	ID int `json:"id"`
}

// secret returns a valid secret of repeated b.
func secret(b byte) key.Shared {
	material, _ := key.NewShared(bytes.Repeat([]byte{b}, pagination.MinSecretLength))

	return material
}

// newCodec builds a codec or fails the test.
func newCodec(t *testing.T, current key.Shared, previous ...key.Shared) *pagination.CursorCodec {
	t.Helper()

	codec, err := pagination.NewCursorCodec(current, previous...)

	if err != nil {
		t.Fatalf("NewCursorCodec() error = %v", err)
	}

	return codec
}

// TestCursorCodecRoundTrip verifies a signed cursor decodes to its value.
func TestCursorCodecRoundTrip(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, secret(1))

	cursor, err := codec.Encode(position{ID: 7})

	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	got, err := codec.Decode[position](cursor)

	if err != nil || got.ID != 7 {
		t.Fatalf("Decode() = %v, %v, want ID 7", got, err)
	}
}

// TestCursorCodecRejectsTamperedPayload verifies an altered payload fails.
func TestCursorCodecRejectsTamperedPayload(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, secret(1))
	cursor, _ := codec.Encode(position{ID: 7})
	forged, _ := pagination.CursorEncode(position{ID: 8})
	_, mac, _ := strings.Cut(cursor, ".")

	if _, err := codec.Decode[position](forged + "." + mac); !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("Decode() error = %v, want ErrCursorDecode", err)
	}
}

// TestCursorCodecRejectsTamperedMAC verifies an altered signature fails.
func TestCursorCodecRejectsTamperedMAC(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, secret(1))
	cursor, _ := codec.Encode(position{ID: 7})
	payload, mac, _ := strings.Cut(cursor, ".")
	flipped := "A"

	if strings.HasPrefix(mac, "A") {
		flipped = "B"
	}

	for _, bad := range []string{flipped + mac[1:], mac[:len(mac)-2], "!!"} {
		if _, err := codec.Decode[position](payload + "." + bad); !errors.Is(err, pagination.ErrCursorDecode) {
			t.Fatalf("Decode(mac %q) error = %v, want ErrCursorDecode", bad, err)
		}
	}
}

// TestCursorCodecRotation verifies old secrets verify and the new one signs.
func TestCursorCodecRotation(t *testing.T) {
	t.Parallel()

	oldCodec := newCodec(t, secret(1))
	rotated := newCodec(t, secret(2), secret(1))

	oldCursor, _ := oldCodec.Encode(position{ID: 1})

	if got, err := rotated.Decode[position](oldCursor); err != nil || got.ID != 1 {
		t.Fatalf("Decode(old cursor) = %v, %v", got, err)
	}

	newCursor, _ := rotated.Encode(position{ID: 2})

	if _, err := oldCodec.Decode[position](newCursor); !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("old codec Decode(new cursor) error = %v, want ErrCursorDecode", err)
	}

	if got, err := newCodec(t, secret(2)).Decode[position](newCursor); err != nil || got.ID != 2 {
		t.Fatalf("new codec Decode(new cursor) = %v, %v", got, err)
	}
}

// TestNewCursorCodecRejectsShortSecret verifies secrets under the minimum fail.
func TestNewCursorCodecRejectsShortSecret(t *testing.T) {
	t.Parallel()

	short := key.Shared{}

	if _, err := pagination.NewCursorCodec(short); !errors.Is(err, pagination.ErrInvalidSecret) {
		t.Fatalf("short secret error = %v, want ErrInvalidSecret", err)
	}

	if _, err := pagination.NewCursorCodec(secret(1), short); !errors.Is(err, pagination.ErrInvalidSecret) {
		t.Fatalf("short previous error = %v, want ErrInvalidSecret", err)
	}
}

// TestCursorCodecRejectsUnsignedCursor verifies plain cursors are refused.
func TestCursorCodecRejectsUnsignedCursor(t *testing.T) {
	t.Parallel()

	plain, _ := pagination.CursorEncode(position{ID: 7})

	if _, err := newCodec(t, secret(1)).Decode[position](plain); !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("Decode() error = %v, want ErrCursorDecode", err)
	}
}

// TestCursorCodecRejectsOversizedCursor verifies the length cap applies.
func TestCursorCodecRejectsOversizedCursor(t *testing.T) {
	t.Parallel()

	if _, err := newCodec(t, secret(1)).Decode[position](strings.Repeat("a", 5000) + ".x"); !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("Decode() error = %v, want ErrCursorDecode", err)
	}
}

// TestCursorCodecEncodeRejectsUndecodableLength includes the signature overhead
// in the existing decode limit, so a successfully issued cursor can be reused.
func TestCursorCodecEncodeRejectsUndecodableLength(t *testing.T) {
	t.Parallel()

	_, err := newCodec(t, secret(1)).Encode(strings.Repeat("a", 3040))

	if !errors.Is(err, pagination.ErrCursorEncode) {
		t.Fatalf("Encode() = %v, want ErrCursorEncode", err)
	}
}

// TestCursorCodecEncodeAcceptsMaximumLength checks the inclusive token bound
// with a real signature and verifies the largest accepted token can be reused.
func TestCursorCodecEncodeAcceptsMaximumLength(t *testing.T) {
	t.Parallel()

	codec := newCodec(t, secret(1))
	value := strings.Repeat("a", 3037)

	cursor, err := codec.Encode(value)

	if err != nil {
		t.Fatal(err)
	}

	if len(cursor) != 4096 {
		t.Fatalf("token length = %d, want 4096", len(cursor))
	}

	decoded, err := codec.Decode[string](cursor)

	if err != nil || decoded != value {
		t.Fatalf("Decode() = %q, %v", decoded, err)
	}
}

// TestZeroCursorCodecEncodeReturnsError verifies an unconstructed codec cannot
// panic or silently create a token signed with missing application key material.
func TestZeroCursorCodecEncodeReturnsError(t *testing.T) {
	t.Parallel()

	var codec pagination.CursorCodec

	_, err := codec.Encode(42)

	if !errors.Is(err, pagination.ErrCursorEncode) || !errors.Is(err, pagination.ErrInvalidSecret) {
		t.Fatalf("Encode() = %v, want ErrCursorEncode and ErrInvalidSecret", err)
	}
}

// TestZeroCursorCodecRejectsSignedToken verifies missing application material
// cannot verify another codec's otherwise valid signed token.
func TestZeroCursorCodecRejectsSignedToken(t *testing.T) {
	t.Parallel()

	encoded, err := newCodec(t, secret(1)).Encode(42)

	if err != nil {
		t.Fatal(err)
	}

	var codec pagination.CursorCodec

	_, err = codec.Decode[int](encoded)

	if !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("Decode() = %v, want ErrCursorDecode", err)
	}
}
