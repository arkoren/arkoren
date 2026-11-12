package auth

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

// maxSubjectLength is the longest subject in bytes, matching the OpenID Connect
// "sub" claim limit (OpenID Connect Core section 2).
const maxSubjectLength = 255

// ErrNotFound reports that a user no longer exists. Loaders return it (or an
// error wrapping it) for a deleted user, and [SessionIdentity.Load] returns it
// for a subject that cannot be a valid identifier. It wraps [ErrNoCredentials],
// so session authentication treats a vanished user as anonymous.
//
// Return plain [ErrNoCredentials], or another error, for a user that exists but
// is disabled: code that cleans up data of deleted users treats only ErrNotFound
// as proof of deletion, so a suspended user's data is never removed.
//
// Example:
//
//	func loadUser(ctx context.Context, id int64) (User, error) {
//		user, err := users.Find(ctx, id)
//		if errors.Is(err, sql.ErrNoRows) {
//			return User{}, auth.ErrNotFound
//		}
//
//		return user, err
//	}
var ErrNotFound = fmt.Errorf("auth: user not found: %w", ErrNoCredentials)

// ErrInvalidSubject reports an identifier that cannot be used as a subject: an
// identifier type that is not a string, an integer or a text-marshaling type, or
// a subject that is not 1 to 255 printable ASCII bytes without spaces.
//
// Example:
//
//	if _, err := identity.Subject(user); errors.Is(err, auth.ErrInvalidSubject) {
//		return err
//	}
var ErrInvalidSubject = errors.New("auth: invalid subject")

// Subject returns the text reference of user for packages that store users
// without knowing the application's types. The subject is derived from
// user.AuthID() by identifier kind: a string kind is used as is, an integer kind
// becomes decimal digits and a type implementing [encoding.TextMarshaler] (and
// [encoding.TextUnmarshaler] through its pointer, as UUID, xid and ULID types
// do) becomes its text form, which takes precedence over the kind.
//
// A subject is 1 to 255 bytes of printable ASCII without spaces (the OpenID
// Connect "sub" rule, minus space because some database collations ignore
// trailing spaces). Store it in a column compared bytewise. It returns an error
// wrapping [ErrInvalidSubject] for any other identifier type, an empty or
// invalid result, or a marshaling failure. It does not look at the session or
// the loader, and works on definitions that fail [SessionIdentity.Err].
//
// Example:
//
//	subject, err := identity.Subject(user)
//	if err != nil {
//		return err
//	}
//	err = subscriptions.Forget(ctx, subject)
func (identity SessionIdentity[K, U]) Subject(user U) (string, error) {
	id := user.AuthID()

	var subject string

	if marshaler, ok := any(id).(encoding.TextMarshaler); ok {
		text, err := marshaler.MarshalText()

		if err != nil {
			return "", fmt.Errorf("%w: marshal %T: %w", ErrInvalidSubject, id, err)
		}

		subject = string(text)
	} else {
		value := reflect.ValueOf(id)

		switch {
		case value.Kind() == reflect.String:
			subject = value.String()
		case value.CanInt():
			subject = strconv.FormatInt(value.Int(), 10)
		case value.CanUint():
			subject = strconv.FormatUint(value.Uint(), 10)
		default:
			return "", fmt.Errorf("%w: %T is not a string, an integer or a text type", ErrInvalidSubject, id)
		}
	}

	if err := checkSubject(subject); err != nil {
		return "", err
	}

	return subject, nil
}

// Load resolves a subject produced by [SessionIdentity.Subject] back to the user
// through the definition's loader. It returns [ErrInvalidSubject] (wrapped) for
// an unsupported identifier type or a subject that is not 1 to 255 printable
// ASCII bytes, [ErrInvalidSessionIdentity] for an invalid definition, and
// [ErrNotFound] (wrapped, with the parse cause) when the subject cannot be
// parsed as an identifier, such as letters for an integer or a value that
// overflows its bit size. Loader errors pass through unchanged, so a loader
// reporting a deleted user with [ErrNotFound] reaches the caller as is.
//
// Load needs no session or request. Use it from jobs and sweeps.
//
// Example:
//
//	user, err := identity.Load(ctx, subject)
//	if errors.Is(err, auth.ErrNotFound) {
//		return nil // The user was deleted.
//	}
func (identity SessionIdentity[K, U]) Load(ctx context.Context, subject string) (user U, err error) {
	if err := identity.Err(); err != nil {
		return user, err
	}

	if err := checkSubject(subject); err != nil {
		return user, err
	}

	var id K

	if unmarshaler, ok := any(&id).(encoding.TextUnmarshaler); ok {
		if err := unmarshaler.UnmarshalText([]byte(subject)); err != nil {
			return user, fmt.Errorf("%w: parse subject: %w", ErrNotFound, err)
		}

		return identity.load(ctx, id)
	}

	value := reflect.ValueOf(&id).Elem()

	switch {
	case value.Kind() == reflect.String:
		value.SetString(subject)
	case value.CanInt():
		number, err := strconv.ParseInt(subject, 10, value.Type().Bits())

		if err != nil {
			return user, fmt.Errorf("%w: parse subject: %w", ErrNotFound, err)
		}

		value.SetInt(number)
	case value.CanUint():
		number, err := strconv.ParseUint(subject, 10, value.Type().Bits())

		if err != nil {
			return user, fmt.Errorf("%w: parse subject: %w", ErrNotFound, err)
		}

		value.SetUint(number)
	default:
		return user, fmt.Errorf("%w: %T is not a string, an integer or a text type", ErrInvalidSubject, id)
	}

	return identity.load(ctx, id)
}

// checkSubject reports whether subject is 1 to 255 bytes of printable ASCII
// without spaces. Space is excluded because trailing spaces compare equal under
// PAD SPACE collations, including MySQL's ascii_bin.
func checkSubject(subject string) error {
	if subject == "" || len(subject) > maxSubjectLength {
		return fmt.Errorf("%w: length %d is outside 1 to %d", ErrInvalidSubject, len(subject), maxSubjectLength)
	}

	for index := range len(subject) {
		if character := subject[index]; character <= ' ' || character > '~' {
			return fmt.Errorf("%w: byte %d is not printable ASCII", ErrInvalidSubject, index)
		}
	}

	return nil
}

// Directory is a non-generic view of a [SessionIdentity] for packages that
// attach data to users but must not depend on the application's user types. It
// exposes only text subjects. Obtain it with [SessionIdentity.Directory]; the
// zero value is invalid and its methods panic.
//
// Example:
//
//	directory, err := identity.Directory()
//	if err != nil {
//		return err
//	}
//	subject, err := directory.Subject(r.Context())
type Directory struct {
	// subject resolves the current request's user to its subject.
	subject func(context.Context) (string, error)

	// exists loads a subject through the identity's loader.
	exists func(context.Context, string) error
}

// Directory returns the non-generic view of the identity. It returns an error
// wrapping [ErrInvalidSessionIdentity] for an invalid definition and one
// wrapping [ErrInvalidSubject] when K is not a string kind, an integer kind or a
// type implementing both [encoding.TextMarshaler] and (through its pointer)
// [encoding.TextUnmarshaler], so the failure surfaces during setup rather than
// on a request. The returned value shares the loader and its captures.
//
// Example:
//
//	directory, err := identity.Directory()
//	if err != nil {
//		return err
//	}
//	subscriptions := billing.New(directory, db)
func (identity SessionIdentity[K, U]) Directory() (Directory, error) {
	if err := identity.Err(); err != nil {
		return Directory{}, err
	}

	kind := reflect.TypeFor[K]().Kind()
	text := reflect.TypeFor[K]().Implements(reflect.TypeFor[encoding.TextMarshaler]()) &&
		reflect.PointerTo(reflect.TypeFor[K]()).Implements(reflect.TypeFor[encoding.TextUnmarshaler]())

	if !text && kind != reflect.String && (kind < reflect.Int || kind > reflect.Uint64) {
		return Directory{}, fmt.Errorf("%w: %v is not a string, an integer or a text type", ErrInvalidSubject, reflect.TypeFor[K]())
	}

	return Directory{
		subject: func(ctx context.Context) (string, error) {
			user, ok := FromContext[U](ctx)

			if !ok {
				return "", ErrUnauthenticated
			}

			return identity.Subject(user)
		},
		exists: func(ctx context.Context, subject string) error {
			_, err := identity.Load(ctx, subject)

			return err
		},
	}, nil
}

// Subject returns the subject of the user that [Middleware] stored in ctx. It
// returns [ErrUnauthenticated] when ctx carries no user of the identity's exact
// user type (a pointer stored where the identity expects a value counts as no
// user) and the errors of [SessionIdentity.Subject] otherwise.
//
// Example:
//
//	subject, err := directory.Subject(r.Context())
//	if err != nil {
//		return err
//	}
func (directory Directory) Subject(ctx context.Context) (string, error) {
	return directory.subject(ctx)
}

// Exists reports whether subject still identifies a user by loading it. It
// returns nil when the loader finds the user, an error wrapping [ErrNotFound]
// when the user is gone or the subject cannot be parsed as an identifier, an
// error wrapping [ErrInvalidSubject] for a malformed subject, and any other
// error for failures and for disabled users, which must not be treated as
// deleted. Callers that delete data must act only on [ErrNotFound].
//
// Example:
//
//	err := directory.Exists(ctx, subject)
//	if errors.Is(err, auth.ErrNotFound) {
//		return forget(ctx, subject)
//	}
func (directory Directory) Exists(ctx context.Context, subject string) error {
	return directory.exists(ctx, subject)
}
