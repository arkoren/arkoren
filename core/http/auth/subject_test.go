package auth_test

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/http/auth"
)

// textID mimics UUID, xid and ULID types: a byte array whose text form is
// hexadecimal and whose UnmarshalText has a pointer receiver.
type textID [12]byte

// MarshalText returns the hexadecimal form of the identifier.
func (id textID) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(id[:])), nil
}

// UnmarshalText parses the hexadecimal form and rejects any other length.
func (id *textID) UnmarshalText(text []byte) error {
	if len(text) != hex.EncodedLen(len(id)) {
		return errors.New("bad length")
	}

	_, err := hex.Decode(id[:], text)

	return err
}

// textUser is identified by a text-marshaling identifier.
type textUser struct{ ID textID }

// AuthID reports the text-marshaling identifier.
func (user textUser) AuthID() textID { return user.ID }

// userName is a named string identifier.
type userName string

// namedUser is identified by a named string.
type namedUser struct{ Name userName }

// AuthID reports the named string identifier.
func (user namedUser) AuthID() userName { return user.Name }

// smallUser is identified by an 8-bit unsigned integer.
type smallUser struct{ ID uint8 }

// AuthID reports the small integer identifier.
func (user smallUser) AuthID() uint8 { return user.ID }

// structID is an identifier type no subject can represent.
type structID struct{ X int }

// structUser is identified by an unsupported struct.
type structUser struct{ ID structID }

// AuthID reports the unsupported identifier.
func (user structUser) AuthID() structID { return user.ID }

// TestSubjectRoundTripsInt64 verifies integers become decimal digits and load
// back through the loader exactly, including values beyond float64 precision.
func TestSubjectRoundTripsInt64(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, id int64) (wide, error) {
		return wide(id), nil
	})

	subject, err := identity.Subject(wide(9007199254740993))

	if err != nil || subject != "9007199254740993" {
		t.Fatalf("subject = %q, error = %v", subject, err)
	}

	user, err := identity.Load(t.Context(), subject)

	if err != nil || user != wide(9007199254740993) {
		t.Fatalf("user = %v, error = %v", user, err)
	}
}

// TestSubjectRoundTripsNamedString verifies a named string is used as is.
func TestSubjectRoundTripsNamedString(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, name userName) (namedUser, error) {
		return namedUser{name}, nil
	})

	subject, err := identity.Subject(namedUser{"usr_9f1c"})

	if err != nil || subject != "usr_9f1c" {
		t.Fatalf("subject = %q, error = %v", subject, err)
	}

	user, err := identity.Load(t.Context(), subject)

	if err != nil || user.Name != "usr_9f1c" {
		t.Fatalf("user = %v, error = %v", user, err)
	}
}

// TestSubjectRoundTripsTextMarshaler verifies a 16-byte-class text type uses
// its text form and unmarshals through the pointer receiver.
func TestSubjectRoundTripsTextMarshaler(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, id textID) (textUser, error) {
		return textUser{id}, nil
	})
	want := textUser{textID{1, 2, 3, 250}}

	subject, err := identity.Subject(want)

	if err != nil || subject != "010203fa0000000000000000" {
		t.Fatalf("subject = %q, error = %v", subject, err)
	}

	user, err := identity.Load(t.Context(), subject)

	if err != nil || user != want {
		t.Fatalf("user = %v, error = %v", user, err)
	}
}

// TestLoadRespectsIntegerBitSize verifies a subject overflowing the identifier
// is not found rather than truncated into another user.
func TestLoadRespectsIntegerBitSize(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, id uint8) (smallUser, error) {
		return smallUser{id}, nil
	})

	if user, err := identity.Load(t.Context(), "255"); err != nil || user.ID != 255 {
		t.Fatalf("user = %v, error = %v", user, err)
	}

	for _, subject := range []string{"256", "-1", "abc"} {
		if _, err := identity.Load(t.Context(), subject); !errors.Is(err, auth.ErrNotFound) || !errors.Is(err, auth.ErrNoCredentials) {
			t.Fatalf("Load(%q) = %v, want ErrNotFound", subject, err)
		}
	}
}

// TestLoadUnparseableTextIsNotFound verifies a text identifier that fails to
// unmarshal is a missing user.
func TestLoadUnparseableTextIsNotFound(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, id textID) (textUser, error) {
		t.Fatal("loader reached for unparseable subject")

		return textUser{id}, nil
	})

	if _, err := identity.Load(t.Context(), "xyz"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// TestLoadPassesLoaderNotFound verifies the loader's own ErrNotFound and
// disabled-user errors reach callers unchanged and stay distinguishable.
func TestLoadPassesLoaderNotFound(t *testing.T) {
	t.Parallel()

	disabled := errors.New("disabled")
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		if id == 2 {
			return user{}, disabled
		}

		return user{}, auth.ErrNotFound
	})

	if _, err := identity.Load(t.Context(), "1"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal(err)
	}

	if _, err := identity.Load(t.Context(), "2"); !errors.Is(err, disabled) || errors.Is(err, auth.ErrNotFound) {
		t.Fatal(err)
	}
}

// TestSubjectRejectsUnsupportedIdentifier verifies struct identifiers are
// refused by Subject and Load, while Err and sessions are unaffected.
func TestSubjectRejectsUnsupportedIdentifier(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, id structID) (structUser, error) {
		return structUser{id}, nil
	})

	if err := identity.Err(); err != nil {
		t.Fatal(err)
	}

	if _, err := identity.Subject(structUser{}); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("Subject error = %v", err)
	}

	if _, err := identity.Load(t.Context(), "1"); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("Load error = %v", err)
	}

	if _, err := identity.Directory(); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("Directory error = %v", err)
	}
}

// TestSubjectRejectsEmptyOverlongAndUnprintable verifies the 1 to 255 byte
// printable ASCII rule on both the produced and the loaded subject.
func TestSubjectRejectsEmptyOverlongAndUnprintable(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, name userName) (namedUser, error) {
		t.Fatal("loader reached for invalid subject")

		return namedUser{name}, nil
	})

	for _, name := range []string{"", strings.Repeat("a", 256), "a b", "café", "tab\t", "del\x7f"} {
		if _, err := identity.Subject(namedUser{userName(name)}); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Fatalf("Subject(%q) = %v", name, err)
		}

		if _, err := identity.Load(t.Context(), name); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Fatalf("Load(%q) = %v", name, err)
		}
	}

	if _, err := identity.Subject(namedUser{userName(strings.Repeat("a", 255))}); err != nil {
		t.Fatalf("255-byte subject: %v", err)
	}
}

// TestSubjectInvalidDefinition verifies Load and Directory report an invalid
// definition before touching the loader, and Subject needs no loader.
func TestSubjectInvalidDefinition(t *testing.T) {
	t.Parallel()

	var identity auth.SessionIdentity[int, user]

	if _, err := identity.Load(t.Context(), "1"); !errors.Is(err, auth.ErrInvalidSessionIdentity) {
		t.Fatal(err)
	}

	if _, err := identity.Directory(); !errors.Is(err, auth.ErrInvalidSessionIdentity) {
		t.Fatal(err)
	}

	if subject, err := identity.Subject(user{ID: 3}); err != nil || subject != "3" {
		t.Fatalf("subject = %q, error = %v", subject, err)
	}
}

// TestDirectorySubjectWithoutUser verifies an anonymous context is
// unauthenticated, including a user stored under the pointer type.
func TestDirectorySubjectWithoutUser(t *testing.T) {
	t.Parallel()

	directory, err := auth.NewSessionIdentity(loadByID).Directory()

	if err != nil {
		t.Fatal(err)
	}

	if _, err := directory.Subject(t.Context()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous error = %v", err)
	}

	ctx := auth.NewContext(t.Context(), &user{ID: 7})

	if _, err := directory.Subject(ctx); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pointer-stored error = %v", err)
	}
}

// TestDirectorySubjectWithUser verifies the current request's user is mapped.
func TestDirectorySubjectWithUser(t *testing.T) {
	t.Parallel()

	directory, err := auth.NewSessionIdentity(loadByID).Directory()

	if err != nil {
		t.Fatal(err)
	}

	subject, err := directory.Subject(auth.NewContext(t.Context(), user{ID: 7}))

	if err != nil || subject != "7" {
		t.Fatalf("subject = %q, error = %v", subject, err)
	}
}

// TestDirectoryExists verifies nil, ErrNotFound and other errors are reported
// as the loader produced them.
func TestDirectoryExists(t *testing.T) {
	t.Parallel()

	down := errors.New("db down")
	directory, err := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		switch id {
		case 1:
			return user{ID: 1}, nil
		case 3:
			return user{}, down
		}

		return user{}, auth.ErrNotFound
	}).Directory()

	if err != nil {
		t.Fatal(err)
	}

	if err := directory.Exists(t.Context(), "1"); err != nil {
		t.Fatal(err)
	}

	if err := directory.Exists(t.Context(), "2"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal(err)
	}

	if err := directory.Exists(t.Context(), "3"); !errors.Is(err, down) || errors.Is(err, auth.ErrNotFound) {
		t.Fatal(err)
	}

	if err := directory.Exists(t.Context(), "x"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal(err)
	}
}

// TestIdentityInfersTypeArguments verifies construction needs no explicit type
// arguments when the user implements Identifiable for the loader's identifier.
func TestIdentityInfersTypeArguments(t *testing.T) {
	t.Parallel()

	expect := func(identity auth.SessionIdentity[int, user]) error {
		return identity.Err()
	}

	if err := expect(auth.NewSessionIdentity(loadByID)); err != nil {
		t.Fatal(err)
	}
}
