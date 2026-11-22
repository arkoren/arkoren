package application

import (
	"fmt"
	"reflect"
	"strings"
)

// Provider declares a value that exists exactly once per composition. Create one
// with [Provide], usually as a package-level variable, and obtain its value with
// [Composer.Use]. The first Use during a composition runs the build function and
// caches the result; every later Use in the same composition, from any nested
// provider, returns that same value.
//
// Use a Provider only for values the process must hold once, such as the router
// or a database. For anything that may exist several times, write an ordinary
// constructor, or an exported New function that takes the composer and registers
// its own lifecycle.
//
// A Provider holds no state of its own: the cache lives on the [Composer] of one
// Start or Run, so separate applications and compositions never share values,
// and a Provider is safe to declare globally and to reuse.
//
//	var Router = application.Provide(func(composer *application.Composer) *router.Router {
//		return router.New()
//	})
type Provider[T any] struct {
	// build constructs the value; it runs at most once per composition.
	build func(*Composer) T
	// name is the value's type name, used when reporting a provider cycle.
	name string
}

// providerKey identifies a provider under construction in the composer's
// building stack, so a cycle can be reported with the chain of names.
type providerKey struct {
	// provider is the *Provider[T] pointer, comparable across instantiations.
	provider any
	// name is the provider's display name.
	name string
}

// Provide declares a memoized provider whose value is constructed by build the
// first time [Composer.Use] asks for it in a composition. A nil build panics
// immediately with [ErrNilFunc], at declaration rather than at first Use.
//
// The build function runs synchronously on the composing composer. It may call
// Use on other providers first and then register its own Startup, Cleanup, Run
// and Check callbacks: because startup runs in registration order and cleanup in
// reverse, dependencies start first and stop last. Failures raised through
// [Composer.Must] or [Composer.Require] abort composition and are never cached.
//
//	var Database = application.Provide(func(composer *application.Composer) *sql.DB {
//		db := composer.Must(sql.Open("pgx", dsn))
//		composer.Defer(func() { _ = db.Close() })
//		return db
//	})
func Provide[T any](build func(*Composer) T) *Provider[T] {
	if build == nil {
		panic(fmt.Errorf("app: Provide: %w", ErrNilFunc))
	}

	return &Provider[T]{build: build, name: reflect.TypeFor[T]().String()}
}

// Use returns the provider's value for this composition. The first call runs the
// provider's build function inline, sharing this composer's context, cleanup
// ownership and error handling, and caches the result keyed by the provider
// pointer; later calls, including from nested providers, return the cached value
// without building again. The cache is private to one Start or Run.
//
// Use is composition-only. It panics with [ErrInvalidPhase] outside synchronous
// composition, with [ErrNilFunc] for a nil provider, and with [ErrProviderCycle]
// when provider is still being built, naming the chain of value types, for
// example "app: provider cycle: *router.Router -> *iam.IAM -> *router.Router".
// A recorded composition failure aborts before building and prevents a value
// from being cached, even if the build recovered the private abort. Because
// composition is synchronous, the cache needs no locking.
//
//	err := app.Start(ctx, func(composer *application.Composer) {
//		routes := composer.Use(Router)
//		routes.Get(application.LivePath, composer.App().LiveHandler())
//	})
func (composer *Composer) Use[T any](provider *Provider[T]) T {
	if provider == nil {
		panic(fmt.Errorf("app: Use: %w", ErrNilFunc))
	}

	composer.scope.Validate()

	if value, ok := composer.provided[provider]; ok {
		return value.(T)
	}

	for index, entry := range composer.building {
		if entry.provider != any(provider) {
			continue
		}

		panic(composer.cycleError(index, provider.name))
	}

	composer.building = append(composer.building, providerKey{provider: provider, name: provider.name})
	defer func() { composer.building = composer.building[:len(composer.building)-1] }()

	value := provider.build(composer)
	composer.scope.Validate()

	if composer.provided == nil {
		composer.provided = map[any]any{}
	}

	composer.provided[provider] = value

	return value
}

// cycleError builds the [ErrProviderCycle] error for a provider chain that
// starts at composer.building[from] and returns to name, for example
// "app: provider cycle: *router.Router -> *iam.IAM -> *router.Router".
func (composer *Composer) cycleError(from int, name string) error {
	chain := make([]string, 0, len(composer.building)-from+1)
	for _, member := range composer.building[from:] {
		chain = append(chain, member.name)
	}

	chain = append(chain, name)

	return fmt.Errorf("app: %w: %s", ErrProviderCycle, strings.Join(chain, " -> "))
}
