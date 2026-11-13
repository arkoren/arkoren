package auth_test

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
)

// textID mimics UUID types: a byte array whose text form is lowercase
// hexadecimal and whose UnmarshalText has a pointer receiver that, like
// uuid.UUID, also accepts a braced form.
type textID [4]byte

// MarshalText returns the lowercase hexadecimal form.
func (id textID) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(id[:])), nil
}

// UnmarshalText parses the hexadecimal form, with or without braces.
func (id *textID) UnmarshalText(text []byte) error {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(string(text), "{"), "}")

	if len(trimmed) != hex.EncodedLen(len(id)) {
		return errors.New("bad length")
	}

	_, err := hex.Decode(id[:], []byte(trimmed))

	return err
}

// userName is a named string identifier.
type userName string

// structID is an identifier type no subject can represent.
type structID struct{ X int }

// onlyMarshals implements TextMarshaler but not TextUnmarshaler.
type onlyMarshals string

// MarshalText returns the value.
func (value onlyMarshals) MarshalText() ([]byte, error) { return []byte(value), nil }

// entity is the loaded value used by the typed driver tests.
type entity[K comparable] struct{ ID K }

// loadEntity returns a loader that echoes the identifier and an identifier function.
func newEntityDriver[K comparable](t *testing.T) *auth.TypedUserDriver[K, entity[K]] {
	t.Helper()

	driver, err := auth.NewUserDriver(
		func(_ context.Context, id K) (entity[K], error) { return entity[K]{id}, nil },
		func(user entity[K]) K { return user.ID },
	)

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// roundTrip asserts id renders to want and finds the same entity again.
func roundTrip[K comparable](t *testing.T, id K, want string) {
	t.Helper()

	driver := newEntityDriver[K](t)

	subject, err := driver.Subject(entity[K]{id})

	if err != nil || subject != want {
		t.Fatalf("subject = %q, error = %v, want %q", subject, err, want)
	}

	record, err := driver.Find(t.Context(), subject)

	if err != nil || record.Value != any(entity[K]{id}) || record.Subject != want {
		t.Fatalf("record = %v, error = %v", record, err)
	}
}

// TestSubjectRoundTripsInt64 verifies integers use decimal digits, including
// values beyond float64 precision.
func TestSubjectRoundTripsInt64(t *testing.T) {
	t.Parallel()

	roundTrip(t, int64(9007199254740993), "9007199254740993")
}

// TestSubjectRoundTripsUint64Max verifies the largest unsigned value.
func TestSubjectRoundTripsUint64Max(t *testing.T) {
	t.Parallel()

	roundTrip(t, uint64(math.MaxUint64), strconv.FormatUint(math.MaxUint64, 10))
}

// TestSubjectRoundTripsNamedString verifies a named string is used as is.
func TestSubjectRoundTripsNamedString(t *testing.T) {
	t.Parallel()

	roundTrip(t, userName("usr_9f1c"), "usr_9f1c")
}

// TestSubjectRoundTripsTextMarshaler verifies a UUID-like type uses its text
// form and unmarshals through the pointer receiver.
func TestSubjectRoundTripsTextMarshaler(t *testing.T) {
	t.Parallel()

	roundTrip(t, textID{1, 2, 3, 250}, "010203fa")
}

// TestFindRejectsOverflowingInteger verifies a subject beyond the identifier's
// bit size is invalid rather than truncated into another user.
func TestFindRejectsOverflowingInteger(t *testing.T) {
	t.Parallel()

	driver := newEntityDriver[uint8](t)

	if record, err := driver.Find(t.Context(), "255"); err != nil || record.Value != any(entity[uint8]{255}) {
		t.Fatalf("record = %v, error = %v", record, err)
	}

	for _, subject := range []string{"256", "-1", "abc"} {
		if _, err := driver.Find(t.Context(), subject); !errors.Is(err, auth.ErrInvalidSubject) || errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Find(%q) = %v, want ErrInvalidSubject only", subject, err)
		}
	}
}

// TestFindRejectsNonCanonicalInteger verifies subjects that parse but do not
// render back to themselves are invalid.
func TestFindRejectsNonCanonicalInteger(t *testing.T) {
	t.Parallel()

	driver := newEntityDriver[int64](t)

	for _, subject := range []string{"007", "+1", "-0"} {
		if _, err := driver.Find(t.Context(), subject); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Fatalf("Find(%q) = %v, want ErrInvalidSubject", subject, err)
		}
	}
}

// TestFindRejectsBracedText verifies a text form the type accepts but does not
// produce is invalid.
func TestFindRejectsBracedText(t *testing.T) {
	t.Parallel()

	driver := newEntityDriver[textID](t)

	for _, subject := range []string{"{010203fa}", "010203FA", "xyz"} {
		if _, err := driver.Find(t.Context(), subject); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Fatalf("Find(%q) = %v, want ErrInvalidSubject", subject, err)
		}
	}
}

// TestNewUserDriverRejectsUnsupportedIdentifier verifies unsupported identifier
// types fail at setup.
func TestNewUserDriverRejectsUnsupportedIdentifier(t *testing.T) {
	t.Parallel()

	_, err := auth.NewUserDriver(
		func(_ context.Context, id structID) (entity[structID], error) { return entity[structID]{id}, nil },
		func(user entity[structID]) structID { return user.ID },
	)

	if !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("struct error = %v", err)
	}

	_, err = auth.NewUserDriver(
		func(_ context.Context, id onlyMarshals) (entity[onlyMarshals], error) {
			return entity[onlyMarshals]{id}, nil
		},
		func(user entity[onlyMarshals]) onlyMarshals { return user.ID },
	)

	if !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("one-way text error = %v", err)
	}
}

// TestSubjectRejectsEmptyOverlongAndUnprintable verifies the 1 to 255 byte
// printable ASCII rule on produced and looked-up subjects, and that the
// loader is never reached.
func TestSubjectRejectsEmptyOverlongAndUnprintable(t *testing.T) {
	t.Parallel()

	driver, err := auth.NewUserDriver(
		func(_ context.Context, name userName) (entity[userName], error) {
			t.Error("loader reached for invalid subject")

			return entity[userName]{name}, nil
		},
		func(user entity[userName]) userName { return user.ID },
	)

	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"", strings.Repeat("a", 256), "a b", "café", "tab\t", "del\x7f"} {
		if _, err := driver.Subject(entity[userName]{userName(name)}); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Fatalf("Subject(%q) = %v", name, err)
		}

		if _, err := driver.Find(t.Context(), name); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Fatalf("Find(%q) = %v", name, err)
		}
	}

	if err := auth.ValidateSubject(strings.Repeat("a", 255)); err != nil {
		t.Fatalf("255-byte subject: %v", err)
	}
}
