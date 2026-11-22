package application_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/application"
)

// provided is a distinct value type so cycle messages name it predictably.
type provided struct{ name string }

// TestUseMemoizesWithinComposition checks that one provider builds once and
// every Use in the composition, including nested providers, sees that value.
func TestUseMemoizesWithinComposition(t *testing.T) {
	t.Parallel()

	builds := 0
	shared := application.Provide(func(*application.Composer) *provided {
		builds++

		return &provided{name: "shared"}
	})
	nested := application.Provide(func(composer *application.Composer) *provided {
		return composer.Use(shared)
	})

	app := application.New()
	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		first := composer.Use(shared)
		second := composer.Use(nested)

		if first != second || composer.Use(shared) != first {
			t.Fatal("Use returned different values for one provider")
		}
	}, func(composer *application.Composer) {
		if composer.Use(shared).name != "shared" {
			t.Fatal("later callback did not see the cached value")
		}
	}))
	t.Cleanup(func() { mustNil(t, app.Shutdown(context.Background())) })

	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
}

// TestUseIsolatesCompositions checks that two applications, and two Start
// calls, never share cached values for the same provider.
func TestUseIsolatesCompositions(t *testing.T) {
	t.Parallel()

	builds := 0
	shared := application.Provide(func(*application.Composer) *provided {
		builds++

		return &provided{}
	})

	var values []*provided

	for range 2 {
		app := application.New()
		mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
			values = append(values, composer.Use(shared))
		}))
		t.Cleanup(func() { mustNil(t, app.Shutdown(context.Background())) })
	}

	if builds != 2 || values[0] == values[1] {
		t.Fatalf("builds = %d, shared value = %v", builds, values[0] == values[1])
	}
}

// TestUseCycleNamesChain checks the panic for a provider re-entering itself
// through another provider names the whole chain and wraps ErrProviderCycle.
func TestUseCycleNamesChain(t *testing.T) {
	t.Parallel()

	type iam struct{}

	var routes *application.Provider[*provided]

	identity := application.Provide(func(composer *application.Composer) *iam {
		composer.Use(routes)

		return &iam{}
	})
	routes = application.Provide(func(composer *application.Composer) *provided {
		composer.Use(identity)

		return &provided{}
	})

	app := application.New()

	defer func() {
		err, ok := recover().(error)
		if !ok || !errors.Is(err, application.ErrProviderCycle) {
			t.Fatalf("panic = %v, want ErrProviderCycle", err)
		}

		want := "provider cycle: *application_test.provided -> *application_test.iam -> *application_test.provided"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message = %q, want it to contain %q", err, want)
		}
	}()

	_ = app.Start(t.Context(), func(composer *application.Composer) {
		composer.Use(routes)
	})
}

// TestUseOrdersLifecycleDependenciesFirst checks that nested providers which
// use their dependencies before registering start dependencies first and clean
// them up last.
func TestUseOrdersLifecycleDependenciesFirst(t *testing.T) {
	t.Parallel()

	var order []string

	register := func(composer *application.Composer, name string) {
		composer.Startup(func(context.Context) error {
			order = append(order, "start "+name)

			return nil
		})
		composer.Cleanup(func(context.Context) error {
			order = append(order, "stop "+name)

			return nil
		})
	}

	database := application.Provide(func(composer *application.Composer) *provided {
		register(composer, "database")

		return &provided{}
	})
	type cache struct{}
	store := application.Provide(func(composer *application.Composer) *cache {
		composer.Use(database)
		register(composer, "cache")

		return &cache{}
	})
	type server struct{}
	web := application.Provide(func(composer *application.Composer) *server {
		composer.Use(store)
		composer.Use(database)
		register(composer, "server")

		return &server{}
	})

	app := application.New()
	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.Use(web)
	}))
	mustNil(t, app.Shutdown(context.Background()))

	want := []string{
		"start database", "start cache", "start server",
		"stop server", "stop cache", "stop database",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestUseAfterCompositionPanics checks that a retained composer rejects Use
// once composition ended.
func TestUseAfterCompositionPanics(t *testing.T) {
	t.Parallel()

	shared := application.Provide(func(*application.Composer) *provided { return &provided{} })
	app := application.New()

	var retained *application.Composer

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		retained = composer
	}))
	t.Cleanup(func() { mustNil(t, app.Shutdown(context.Background())) })

	mustPanic(t, application.ErrInvalidPhase, func() { retained.Use(shared) })
}

// TestProvideNilBuildPanics checks that a nil build fails at declaration.
func TestProvideNilBuildPanics(t *testing.T) {
	t.Parallel()

	mustPanic(t, application.ErrNilFunc, func() { application.Provide[int](nil) })
}

// TestUseNilProviderPanics checks that a nil provider pointer is rejected.
func TestUseNilProviderPanics(t *testing.T) {
	t.Parallel()

	app := application.New()

	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, application.ErrNilFunc) {
			t.Fatalf("panic = %v, want ErrNilFunc", err)
		}
	}()

	_ = app.Start(t.Context(), func(composer *application.Composer) {
		composer.Use[int](nil)
	})
}

// TestUseFailedBuildIsNotCached checks that a build aborted through Must fails
// Start, that a recovered abort cannot make a later Use return a value, and
// that a later composition builds again instead of reusing the failure.
func TestUseFailedBuildIsNotCached(t *testing.T) {
	t.Parallel()

	failure := errors.New("open failed")
	builds := 0
	fail := true
	flaky := application.Provide(func(composer *application.Composer) *provided {
		builds++

		if fail {
			composer.Require(failure)
		}

		return &provided{}
	})

	tryUse := func(composer *application.Composer) (aborted bool) {
		defer func() { aborted = recover() != nil }()

		composer.Use(flaky)

		return false
	}

	err := application.New().Start(t.Context(), func(composer *application.Composer) {
		if !tryUse(composer) {
			t.Fatal("Use returned a value from a failing build")
		}

		if !tryUse(composer) {
			t.Fatal("Use returned a value after a recovered failed build")
		}
	})
	if !errors.Is(err, failure) || builds != 1 {
		t.Fatalf("err = %v, builds = %d", err, builds)
	}

	fail = false
	app := application.New()
	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.Use(flaky)
	}))
	t.Cleanup(func() { mustNil(t, app.Shutdown(context.Background())) })

	if builds != 2 {
		t.Fatalf("builds = %d, want 2", builds)
	}
}

// TestRunEndsWhenOnlyRunnerReturns checks that Run returns nil and leaves a
// stopped application once its only runner returns nil, as a one-shot command
// such as a migration needs.
func TestRunEndsWhenOnlyRunnerReturns(t *testing.T) {
	t.Parallel()

	ran := false
	app := application.New()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	err := app.Run(ctx, func(composer *application.Composer) {
		composer.Run(func(context.Context) error {
			ran = true

			return nil
		})
	})
	if err != nil || !ran || ctx.Err() != nil {
		t.Fatalf("err = %v, ran = %v, ctx = %v", err, ran, ctx.Err())
	}

	if app.State() != application.StateStopped {
		t.Fatalf("state = %v, want stopped", app.State())
	}
}
