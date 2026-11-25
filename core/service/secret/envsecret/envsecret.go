package envsecret

import (
	"context"
	"fmt"
	"unicode/utf8"

	"arkoren.dev/core/service/secret"
)

// Driver reads secrets from environment variables through a lookup function.
// The reference name is the variable name, so "env:API_TOKEN" reaches it as
// "API_TOKEN" once a secretmux driver has routed on the scheme. The source is
// read-only and text-only: Put, Update and Delete fail with
// [secret.ErrUnsupported], and a value that is not valid UTF-8 is an error. It
// has no versions, so asking for a concrete version is [secret.ErrNotFound].
// Every Get calls the lookup again, so a lookup over a changing source returns
// current values. It is safe for concurrent use when the lookup is, and needs
// no Start or Stop. Construct it with [New].
//
// Example:
//
//	driver := envsecret.New(os.LookupEnv)
//	value, err := driver.Get(ctx, secret.Reference{Name: "API_TOKEN"})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
type Driver struct {
	// lookup returns the value of a variable and whether it is set.
	lookup func(string) (string, bool)
}

// New creates a driver reading variables through lookup, normally os.LookupEnv.
// Tests pass a function over a map so no process state changes. The lookup
// must be non-nil: a nil lookup panics on the first Get. The function is
// retained and shared, not copied. New performs no I/O.
//
// Example:
//
//	driver := envsecret.New(func(name string) (string, bool) {
//		value, found := fixtures[name]
//
//		return value, found
//	})
func New(lookup func(string) (string, bool)) *Driver {
	return &Driver{lookup: lookup}
}

// Get returns the variable named by reference.Name as bytes owned by the
// caller. An invalid name (not a POSIX name, see [Driver]) and a value that is
// not UTF-8 are errors; an unset variable returns an error wrapping
// [secret.ErrNotFound]; a variable set to the empty string returns empty data.
// A non-empty reference.Version returns [secret.ErrNotFound], because
// variables have no versions. An already-canceled context returns its error
// before the lookup. Errors name the variable, never its value.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "API_TOKEN"})
//	if errors.Is(err, secret.ErrNotFound) {
//		return errors.New("API_TOKEN is not set")
//	}
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	if !validName(reference.Name) {
		return secret.Value{}, fmt.Errorf("envsecret: %q is not a POSIX environment variable name", reference.Name)
	}

	if reference.Version != "" {
		return secret.Value{}, fmt.Errorf("envsecret: variable %s has no versions: %w", reference.Name, secret.ErrNotFound)
	}

	value, found := driver.lookup(reference.Name)

	if !found {
		return secret.Value{}, fmt.Errorf("envsecret: variable %s is not set: %w", reference.Name, secret.ErrNotFound)
	}

	if !utf8.ValidString(value) {
		return secret.Value{}, fmt.Errorf("envsecret: variable %s is not valid UTF-8", reference.Name)
	}

	return secret.Value{Data: []byte(value)}, nil
}

// Put fails with [secret.ErrUnsupported]: environment variables are read-only.
// An already-canceled context returns its error instead.
//
// Example:
//
//	_, err := driver.Put(ctx, "API_TOKEN", []byte(token))
//	_ = errors.Is(err, secret.ErrUnsupported) // true
func (driver *Driver) Put(ctx context.Context, _ string, _ []byte) (secret.Value, error) {
	return secret.Value{}, unsupported(ctx, "put")
}

// Update fails with [secret.ErrUnsupported]: environment variables are
// read-only. An already-canceled context returns its error instead.
//
// Example:
//
//	_, err := driver.Update(ctx, "API_TOKEN", []byte(token))
//	_ = errors.Is(err, secret.ErrUnsupported) // true
func (driver *Driver) Update(ctx context.Context, _ string, _ []byte) (secret.Value, error) {
	return secret.Value{}, unsupported(ctx, "update")
}

// Delete fails with [secret.ErrUnsupported]: environment variables are
// read-only. An already-canceled context returns its error instead.
//
// Example:
//
//	err := driver.Delete(ctx, "API_TOKEN")
//	_ = errors.Is(err, secret.ErrUnsupported) // true
func (driver *Driver) Delete(ctx context.Context, _ string) error {
	return unsupported(ctx, "delete")
}

// unsupported returns the context's error when it is done and otherwise the
// read-only failure for operation, so a canceled context takes precedence as
// the Driver contract requires.
func unsupported(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return fmt.Errorf("envsecret: %s: environment variables are read-only: %w", operation, secret.ErrUnsupported)
}

// validName reports whether name is a POSIX environment variable name:
// [A-Za-z_][A-Za-z0-9_]*. It rejects "=" and NUL, which no variable name can
// contain, and every name that would make a reference ambiguous.
func validName(name string) bool {
	if name == "" {
		return false
	}

	for index := range len(name) {
		char := name[index]

		letter := char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char == '_'
		digit := char >= '0' && char <= '9'

		if !letter && (!digit || index == 0) {
			return false
		}
	}

	return true
}

var _ secret.Driver = (*Driver)(nil)
