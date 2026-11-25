package secretmux

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"arkoren.dev/core/service/secret"
)

// ErrUnknownScheme identifies a reference whose scheme no source is registered
// for, or that has no scheme at all. The wrapping error lists the registered
// schemes in sorted order and the unknown scheme, never the rest of the
// reference. It is a configuration error, not [secret.ErrNotFound].
//
// Example:
//
//	_, err := secrets.Raw(ctx, "gcp:billing/key")
//	if errors.Is(err, secretmux.ErrUnknownScheme) {
//		return fmt.Errorf("register a source for the gcp scheme: %w", err)
//	}
var ErrUnknownScheme = errors.New("secretmux: unknown scheme")

// Driver is a [secret.Driver] that routes each reference to the source
// registered for its scheme. A reference is "scheme:name"; the Driver splits it
// at the first colon, so names may themselves contain colons ("vault:app:db"
// routes to "vault" with the name "app:db"), and hands the name to the source
// unchanged. Every operation (Get, Put, Update, Delete) routes the same way and
// returns the source's result and errors untouched, so [secret.ErrNotFound],
// [secret.ErrUnsupported] and context errors keep their meaning. Versions pass
// through: Get forwards Reference.Version to the source.
//
// The Driver owns no lifecycle. It never starts, stops or pings its sources;
// callers start and stop each source driver themselves. It is immutable and
// safe for concurrent use when its sources are. Construct it with [New].
//
// Example:
//
//	sources, err := secretmux.New(func(sources *secretmux.Builder) {
//		sources.Source("file", filesecret.New())
//		sources.Source("env", envsecret.New(os.LookupEnv))
//	})
//	if err != nil {
//		return err
//	}
//	secrets := secret.New(sources)
//	token, err := secrets.Get(ctx, secret.NewSecret[string]("env:API_TOKEN"))
type Driver struct {
	// routes maps each registered scheme to its source.
	routes map[string]secret.Driver
}

// New builds a multiplexing driver from the sources registered by configure.
// Callbacks run directly once, synchronously in order on one fresh draft; nil
// entries panic. New performs no I/O and does not start the sources. It returns
// an error wrapping [ErrInvalidConfig] when no source is registered, a scheme is
// empty or contains a colon, a driver is nil, or a scheme is registered twice.
//
// Example:
//
//	sources, err := secretmux.New(func(sources *secretmux.Builder) {
//		sources.Source("file", filesecret.New())
//	})
//	if err != nil {
//		return err
//	}
//	secrets := secret.New(sources)
func New(configure ...BuilderFunc) (*Driver, error) {
	sources := &Builder{}

	for _, configure := range configure {
		configure(sources)
	}

	routes, err := sources.validate()

	if err != nil {
		return nil, err
	}

	return &Driver{routes: routes}, nil
}

// route splits reference at its first colon and returns the source registered
// for the scheme with the remaining name. A reference without a colon or with
// an unregistered scheme returns [ErrUnknownScheme] listing the registered
// schemes.
func (driver *Driver) route(reference string) (secret.Driver, string, error) {
	scheme, name, found := strings.Cut(reference, ":")

	if !found {
		return nil, "", fmt.Errorf("%w: reference has no scheme; registered schemes: %s",
			ErrUnknownScheme, schemes(driver.routes))
	}

	source, registered := driver.routes[scheme]

	if !registered {
		return nil, "", fmt.Errorf("%w %q; registered schemes: %s",
			ErrUnknownScheme, scheme, schemes(driver.routes))
	}

	return source, name, nil
}

// Get routes reference.Name to its source and reads it there, passing
// reference.Version along. An unknown or missing scheme returns
// [ErrUnknownScheme] without calling any source; every other result is the
// source's.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "file:/run/secrets/token"})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	source, name, err := driver.route(reference.Name)

	if err != nil {
		return secret.Value{}, err
	}

	return source.Get(ctx, secret.Reference{Name: name, Version: reference.Version})
}

// Put routes reference to its source and writes there. An unknown or missing
// scheme returns [ErrUnknownScheme]; a read-only source returns an error
// wrapping [secret.ErrUnsupported]. Every other result is the source's.
//
// Example:
//
//	written, err := driver.Put(ctx, "aws:billing/key", []byte(token))
//	if err != nil {
//		return err
//	}
//	rememberVersion(written.Version)
func (driver *Driver) Put(ctx context.Context, reference string, data []byte) (secret.Value, error) {
	source, name, err := driver.route(reference)

	if err != nil {
		return secret.Value{}, err
	}

	return source.Put(ctx, name, data)
}

// Update routes reference to its source and updates it there, with the errors
// of [Driver.Put] plus the source's [secret.ErrNotFound] for a missing secret.
//
// Example:
//
//	written, err := driver.Update(ctx, "aws:billing/key", []byte(replacement))
//	if err != nil {
//		return err
//	}
//	rememberVersion(written.Version)
func (driver *Driver) Update(ctx context.Context, reference string, data []byte) (secret.Value, error) {
	source, name, err := driver.route(reference)

	if err != nil {
		return secret.Value{}, err
	}

	return source.Update(ctx, name, data)
}

// Delete routes reference to its source and deletes it there, with the errors
// of [Driver.Update].
//
// Example:
//
//	if err := driver.Delete(ctx, "aws:billing/retired-key"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, reference string) error {
	source, name, err := driver.route(reference)

	if err != nil {
		return err
	}

	return source.Delete(ctx, name)
}

var _ secret.Driver = (*Driver)(nil)
