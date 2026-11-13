package auth

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

// maxSubjectLength is the longest subject in bytes, matching the OpenID Connect
// "sub" claim limit (OpenID Connect Core section 2).
const maxSubjectLength = 255

// ErrInvalidSubject reports a subject that is not 1 to 255 printable ASCII
// bytes without spaces, a subject that does not parse as the driver's
// identifier type or does not render back to itself, and an identifier type
// that is not a string, an integer or a text-marshaling type.
//
// Example:
//
//	if _, err := users.Find(ctx, subject); errors.Is(err, auth.ErrInvalidSubject) {
//		return err
//	}
var ErrInvalidSubject = errors.New("auth: invalid subject")

// ValidateSubject returns nil when subject is 1 to 255 bytes of printable ASCII
// without spaces, the OpenID Connect "sub" rule minus space because some
// database collations ignore trailing spaces. Store subjects in a column
// compared bytewise. Otherwise it returns an error wrapping [ErrInvalidSubject].
//
// Example:
//
//	if err := auth.ValidateSubject("usr_9f1c"); err != nil {
//		return err
//	}
func ValidateSubject(subject string) error {
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

// checkSubjectType reports whether K can be converted to and from subjects:
// a string kind, an integer kind, or a type that both marshals text and
// (through its pointer) unmarshals it. Implementing only one of the two text
// interfaces is rejected, because the two directions would disagree.
func checkSubjectType[K any]() error {
	keyType := reflect.TypeFor[K]()
	marshals := keyType.Implements(reflect.TypeFor[encoding.TextMarshaler]())
	unmarshals := reflect.PointerTo(keyType).Implements(reflect.TypeFor[encoding.TextUnmarshaler]())

	switch kind := keyType.Kind(); {
	case marshals != unmarshals:
		return fmt.Errorf("%w: %v must implement both TextMarshaler and TextUnmarshaler", ErrInvalidSubject, keyType)
	case marshals, kind == reflect.String, kind >= reflect.Int && kind <= reflect.Uint64:
		return nil
	}

	return fmt.Errorf("%w: %v is not a string, an integer or a text type", ErrInvalidSubject, keyType)
}

// formatSubject renders id as a subject: text form for text marshalers (which
// take precedence over the kind), the string itself, or decimal digits. The
// result must satisfy [ValidateSubject]. K must have passed [checkSubjectType].
func formatSubject[K any](id K) (string, error) {
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
		default:
			subject = strconv.FormatUint(value.Uint(), 10)
		}
	}

	if err := ValidateSubject(subject); err != nil {
		return "", err
	}

	return subject, nil
}

// parseSubject converts subject back to K and requires the result to render to
// exactly subject, so "007" or "+1" never alias the user "7" or "1". Every
// failure wraps [ErrInvalidSubject]: an unparseable subject is not proof that a
// user was deleted. K must have passed [checkSubjectType].
func parseSubject[K any](subject string) (K, error) {
	var id K

	if err := ValidateSubject(subject); err != nil {
		return id, err
	}

	if unmarshaler, ok := any(&id).(encoding.TextUnmarshaler); ok {
		if err := unmarshaler.UnmarshalText([]byte(subject)); err != nil {
			return id, fmt.Errorf("%w: parse subject: %w", ErrInvalidSubject, err)
		}
	} else {
		value := reflect.ValueOf(&id).Elem()

		switch {
		case value.Kind() == reflect.String:
			value.SetString(subject)
		case value.CanInt():
			number, err := strconv.ParseInt(subject, 10, value.Type().Bits())

			if err != nil {
				return id, fmt.Errorf("%w: parse subject: %w", ErrInvalidSubject, err)
			}

			value.SetInt(number)
		default:
			number, err := strconv.ParseUint(subject, 10, value.Type().Bits())

			if err != nil {
				return id, fmt.Errorf("%w: parse subject: %w", ErrInvalidSubject, err)
			}

			value.SetUint(number)
		}
	}

	if canonical, err := formatSubject(id); err != nil || canonical != subject {
		return id, fmt.Errorf("%w: subject is not in canonical form", ErrInvalidSubject)
	}

	return id, nil
}
