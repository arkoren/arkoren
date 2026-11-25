package secretmux

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"arkoren.dev/core/service/secret"
)

// ErrInvalidConfig identifies a construction failure of [New]: no sources, an
// empty scheme, a scheme containing a colon, a nil driver or a duplicate
// scheme. The wrapping error names the offending scheme and never a reference.
//
// Example:
//
//	_, err := secretmux.New()
//	if errors.Is(err, secretmux.ErrInvalidConfig) {
//		// Register at least one source.
//	}
var ErrInvalidConfig = errors.New("secretmux: invalid config")

// BuilderFunc configures one fresh construction draft directly, synchronously
// and once in order. An expanded nil or empty slice leaves the draft empty,
// which [New] rejects; a supplied nil entry panics.
//
// Example:
//
//	var files secretmux.BuilderFunc = func(sources *secretmux.Builder) {
//		sources.Source("file", filesecret.New())
//	}
//	driver, err := secretmux.New(files)
type BuilderFunc = func(*Builder)

// Builder registers the sources a [Driver] routes to. Callbacks run once,
// synchronously in order on one draft; retaining the draft cannot change a
// published driver. Methods return the same draft.
//
// Example:
//
//	secretmux.New(func(sources *secretmux.Builder) {
//		sources.Source("file", filesecret.New())
//		sources.Source("env", envsecret.New(os.LookupEnv))
//	})
type Builder struct {
	// registrations lists every Source call in order, including invalid ones,
	// so New can report the first problem by scheme.
	registrations []registration
}

// registration is one Source call.
type registration struct {
	// scheme is the routing prefix as supplied.
	scheme string

	// driver is the source the scheme routes to.
	driver secret.Driver
}

// Source registers driver for references that start with "scheme:". The scheme
// is matched exactly (case-sensitively) against the text before the first
// colon of a reference. It must be nonempty, contain no colon and be unique,
// and driver must be non-nil; [New] reports violations as [ErrInvalidConfig].
// The driver is shared, not copied, and its lifecycle (Start, Stop, Ping) stays
// with the caller.
//
// Example:
//
//	secretmux.New(func(sources *secretmux.Builder) {
//		sources.Source("aws", awsDriver)
//	})
func (sources *Builder) Source(scheme string, driver secret.Driver) *Builder {
	sources.registrations = append(sources.registrations, registration{scheme: scheme, driver: driver})

	return sources
}

// validate checks the completed draft and returns the routing table. It
// reports the first problem in registration order.
func (sources *Builder) validate() (map[string]secret.Driver, error) {
	if len(sources.registrations) == 0 {
		return nil, fmt.Errorf("%w: no sources registered", ErrInvalidConfig)
	}

	routes := make(map[string]secret.Driver, len(sources.registrations))

	for _, source := range sources.registrations {
		switch {
		case source.scheme == "":
			return nil, fmt.Errorf("%w: empty scheme", ErrInvalidConfig)
		case strings.Contains(source.scheme, ":"):
			return nil, fmt.Errorf("%w: scheme %q contains a colon", ErrInvalidConfig, source.scheme)
		case source.driver == nil:
			return nil, fmt.Errorf("%w: scheme %q has a nil driver", ErrInvalidConfig, source.scheme)
		}

		if _, duplicate := routes[source.scheme]; duplicate {
			return nil, fmt.Errorf("%w: duplicate scheme %q", ErrInvalidConfig, source.scheme)
		}

		routes[source.scheme] = source.driver
	}

	return routes, nil
}

// schemes returns the registered schemes in sorted order, joined for error
// messages.
func schemes(routes map[string]secret.Driver) string {
	return strings.Join(slices.Sorted(maps.Keys(routes)), ", ")
}
