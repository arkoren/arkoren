package application_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/application"
)

// captureStderr redirects os.Stderr to a pipe for the rest of the test and
// returns a function that closes the writer and returns everything written.
// Tests using it must not run in parallel because os.Stderr is process-wide.
func captureStderr(t *testing.T) func() string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	original := os.Stderr
	os.Stderr = writer

	t.Cleanup(func() { os.Stderr = original })

	return func() string {
		os.Stderr = original

		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}

		output, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}

		return string(output)
	}
}

// TestRootApplicationSettingsComeFromProviders builds a log handler through a
// provider and checks that the same value is returned to later Use calls.
func TestRootApplicationSettingsComeFromProviders(t *testing.T) {
	t.Parallel()

	var builds int

	handler := application.Provide(func(*application.Composer) slog.Handler {
		builds++

		return slog.DiscardHandler
	})
	root := application.Provide(func(compose *application.Composer) *application.Application {
		return application.New(func(app *application.Builder) {
			app.Name("shop")
			app.LogHandler(func(slog.Leveler) slog.Handler { return compose.Use(handler) })
		})
	})

	var used slog.Handler

	app, err := application.Start(t.Context(), root, func(compose *application.Composer) {
		used = compose.Use(handler)

		if compose.App().Environment() != application.Development {
			t.Error("compose.App is not the built application")
		}
	})
	mustNil(t, err)
	t.Cleanup(func() { mustNil(t, app.Shutdown(context.Background())) })

	if builds != 1 || used == nil {
		t.Fatalf("handler built %d times, used %v", builds, used)
	}
}

// TestRootApplicationAppDuringBuildIsACycle panics when a provider the root
// depends on asks for the application that does not exist yet.
func TestRootApplicationAppDuringBuildIsACycle(t *testing.T) {
	t.Parallel()

	needsApp := application.Provide(func(compose *application.Composer) *slog.Logger {
		return compose.App().Logger()
	})
	root := application.Provide(func(compose *application.Composer) *application.Application {
		return application.New(func(app *application.Builder) {
			app.LogHandler(func(slog.Leveler) slog.Handler {
				return compose.Use(needsApp).Handler()
			})
		})
	})

	defer func() {
		recovered, _ := recover().(error)
		if !errors.Is(recovered, application.ErrProviderCycle) {
			t.Fatalf("recovered %v, want ErrProviderCycle", recovered)
		}

		want := "*application.Application -> *slog.Logger -> *application.Application"
		if !strings.Contains(recovered.Error(), want) {
			t.Fatalf("message %q does not name the chain %q", recovered, want)
		}
	}()

	_, _ = application.Start(t.Context(), root)

	t.Fatal("Start did not panic")
}

// TestRootApplicationCleanupFromBuildRunsOnShutdown checks that a release
// registered while the settings were built is owned by the application, and runs
// after later registrations.
func TestRootApplicationCleanupFromBuildRunsOnShutdown(t *testing.T) {
	t.Parallel()

	var order []string

	exporter := application.Provide(func(compose *application.Composer) slog.Handler {
		compose.Defer(func() { order = append(order, "exporter") })

		return slog.DiscardHandler
	})
	root := application.Provide(func(compose *application.Composer) *application.Application {
		return application.New(func(app *application.Builder) {
			app.LogHandler(func(slog.Leveler) slog.Handler { return compose.Use(exporter) })
		})
	})

	app, err := application.Start(t.Context(), root, func(compose *application.Composer) {
		compose.Defer(func() { order = append(order, "compose") })
	})
	mustNil(t, err)
	mustNil(t, app.Shutdown(context.Background()))

	if want := []string{"compose", "exporter"}; !slices.Equal(order, want) {
		t.Fatalf("cleanup order %v, want %v", order, want)
	}
}

// TestRootApplicationCleanupRunsWhenComposeFails rolls back releases made by the
// build when a later compose callback aborts.
func TestRootApplicationCleanupRunsWhenComposeFails(t *testing.T) {
	t.Parallel()

	var order []string

	failure := errors.New("compose failed")
	exporter := application.Provide(func(compose *application.Composer) slog.Handler {
		compose.Defer(func() { order = append(order, "exporter") })

		return slog.DiscardHandler
	})
	root := application.Provide(func(compose *application.Composer) *application.Application {
		return application.New(func(app *application.Builder) {
			app.LogHandler(func(slog.Leveler) slog.Handler { return compose.Use(exporter) })
		})
	})

	app, err := application.Start(t.Context(), root, func(compose *application.Composer) {
		compose.Defer(func() { order = append(order, "compose") })
		compose.Require(failure)
	})

	if !errors.Is(err, failure) || app != nil {
		t.Fatalf("Start = %v, %v", app, err)
	}

	if want := []string{"compose", "exporter"}; !slices.Equal(order, want) {
		t.Fatalf("cleanup order %v, want %v", order, want)
	}
}

// TestRootApplicationCleanupRunsWhenStartupFails rolls back releases made by the
// build when a Startup callback fails.
func TestRootApplicationCleanupRunsWhenStartupFails(t *testing.T) {
	t.Parallel()

	var released bool

	failure := errors.New("startup failed")
	exporter := application.Provide(func(compose *application.Composer) slog.Handler {
		compose.Defer(func() { released = true })

		return slog.DiscardHandler
	})
	root := application.Provide(func(compose *application.Composer) *application.Application {
		return application.New(func(app *application.Builder) {
			app.LogHandler(func(slog.Leveler) slog.Handler { return compose.Use(exporter) })
		})
	})

	err := application.Run(t.Context(), root, func(compose *application.Composer) {
		compose.Startup(func(context.Context) error { return failure })
	})

	if !errors.Is(err, failure) || !released {
		t.Fatalf("Run = %v, released %v", err, released)
	}
}

// TestRootApplicationBuildFailureReportsOnStderr returns the abort, runs cleanup
// registered before it and reports once on stderr.
func TestRootApplicationBuildFailureReportsOnStderr(t *testing.T) {
	read := captureStderr(t)

	var released bool

	failure := errors.New("exporter unavailable")
	root := application.Provide(func(compose *application.Composer) *application.Application {
		compose.Defer(func() { released = true })
		compose.Require(failure)

		return application.New()
	})

	err := application.Run(t.Context(), root, func(*application.Composer) {
		t.Error("compose ran after a failed build")
	})
	output := read()

	if !errors.Is(err, failure) || !released {
		t.Fatalf("Run = %v, released %v", err, released)
	}

	if strings.Count(output, "exporter unavailable") != 1 || !strings.Contains(output, "app failed") {
		t.Fatalf("stderr %q does not report the failure once", output)
	}
}

// TestRootApplicationNilResult rejects a provider that builds no application.
func TestRootApplicationNilResult(t *testing.T) {
	read := captureStderr(t)

	root := application.Provide(func(*application.Composer) *application.Application { return nil })

	app, err := application.Start(t.Context(), root)
	_ = read()

	if !errors.Is(err, application.ErrNilApplication) || app != nil {
		t.Fatalf("Start = %v, %v", app, err)
	}
}

// TestRootApplicationNilProvider panics for a nil root provider.
func TestRootApplicationNilProvider(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]func(){
		"Run":   func() { _ = application.Run(t.Context(), nil) },
		"Start": func() { _, _ = application.Start(t.Context(), nil) },
	} {
		func() {
			defer func() {
				recovered, _ := recover().(error)
				if !errors.Is(recovered, application.ErrNilFunc) {
					t.Fatalf("%s recovered %v, want ErrNilFunc", name, recovered)
				}
			}()

			call()
		}()
	}
}

// TestRootApplicationStartedApplicationIsRejected refuses a provider that hands
// back an application that already started.
func TestRootApplicationStartedApplicationIsRejected(t *testing.T) {
	read := captureStderr(t)

	started := application.New()
	mustNil(t, started.Start(t.Context()))
	t.Cleanup(func() { mustNil(t, started.Shutdown(context.Background())) })

	root := application.Provide(func(*application.Composer) *application.Application { return started })

	_, err := application.Start(t.Context(), root)
	_ = read()

	if !errors.Is(err, application.ErrInvalidPhase) {
		t.Fatalf("Start = %v, want ErrInvalidPhase", err)
	}
}

// TestRootStartReturnsRunningApplication starts, observes and shuts down.
func TestRootStartReturnsRunningApplication(t *testing.T) {
	t.Parallel()

	root := application.Provide(func(*application.Composer) *application.Application {
		return application.New()
	})

	app, err := application.Start(t.Context(), root)
	mustNil(t, err)

	if app.State() != application.StateRunning {
		t.Fatalf("state %s, want running", app.State())
	}

	mustNil(t, app.Shutdown(context.Background()))

	if app.State() != application.StateStopped {
		t.Fatalf("state %s, want stopped", app.State())
	}
}

// TestRootRunReturnsOnContextCancel blocks until the context ends and stops.
func TestRootRunReturnsOnContextCancel(t *testing.T) {
	t.Parallel()

	root := application.Provide(func(*application.Composer) *application.Application {
		return application.New()
	})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	running := make(chan struct{})

	go func() {
		result <- application.Run(ctx, root, func(compose *application.Composer) {
			compose.Run(func(ctx context.Context) error {
				close(running)
				<-ctx.Done()

				return nil
			})
		})
	}()

	<-running
	cancel()

	mustNil(t, <-result)
}

// ExampleRun builds the application from a root provider and runs it until the
// runner returns.
func ExampleRun() {
	root := application.Provide(func(*application.Composer) *application.Application {
		return application.New(func(app *application.Builder) {
			app.Name("shop")
		})
	})

	err := application.Run(context.Background(), root, func(compose *application.Composer) {
		compose.Run(func(context.Context) error {
			fmt.Println("serving", compose.App().Environment())

			return nil
		})
	})
	fmt.Println(err)
	// Output:
	// serving development
	// <nil>
}

// ExampleStart returns the running application for the caller to stop.
func ExampleStart() {
	root := application.Provide(func(*application.Composer) *application.Application {
		return application.New()
	})

	app, err := application.Start(context.Background(), root)
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(app.State())
	fmt.Println(app.Shutdown(context.Background()))
	// Output:
	// running
	// <nil>
}
