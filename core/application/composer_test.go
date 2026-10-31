package application_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/application"
	"arkoren.dev/core/event"
)

// TestComposerIdentityIsSharedAcrossCallbacks preserves one composition scope
// and one public handle throughout an execution.
func TestComposerIdentityIsSharedAcrossCallbacks(t *testing.T) {
	t.Parallel()

	app := application.New()
	var first *application.Composer

	err := app.Start(t.Context(), func(compose *application.Composer) {
		first = compose
	}, func(compose *application.Composer) {
		if compose != first {
			t.Fatal("composition callbacks received different composers")
		}
	})

	mustNil(t, err)
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestCompositionCompletesBeforeStartup checks ordered callback execution and
// alias-slice compatibility. No startup hook may interrupt composition.
func TestCompositionCompletesBeforeStartup(t *testing.T) {
	t.Parallel()

	app := application.New()
	var order []string
	composers := []func(*application.Composer){
		func(scope *application.Composer) {
			order = append(order, "compose first")
			scope.Startup(func(context.Context) error {
				order = append(order, "startup first")

				return nil
			})
		},
		func(scope *application.Composer) {
			order = append(order, "compose second")
			scope.Startup(func(context.Context) error {
				order = append(order, "startup second")

				return nil
			})
		},
	}

	mustNil(t, app.Start(t.Context(), composers...))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	want := []string{"compose first", "compose second", "startup first", "startup second"}

	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestComposerServicesAndContext checks that composition uses the caller's
// application and startup values without introducing a dependency container.
func TestComposerServicesAndContext(t *testing.T) {
	t.Parallel()

	app := application.New()
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "tenant")

	mustNil(t, app.Start(ctx, func(scope *application.Composer) {
		if scope.App() != app || scope.Context().Value(contextKey{}) != "tenant" {
			t.Fatal("composer lost application or startup context")
		}

		scope.Startup(func(ctx context.Context) error {
			if ctx != scope.Context() {
				t.Fatal("startup and composition contexts differ")
			}

			return nil
		})
	}))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})
}

// TestMustSuccessAndRequireNilContinue checks successful constructor unwrapping
// without copying a dependency or stopping subsequent composition callbacks.
func TestMustSuccessAndRequireNilContinue(t *testing.T) {
	t.Parallel()

	app := application.New()
	dependency := new(int)
	continued := false

	mustNil(t, app.Start(t.Context(), func(scope *application.Composer) {
		if got := scope.Must(dependency, nil); got != dependency {
			t.Fatal("Must changed dependency identity")
		}

		scope.Require(nil)

		continued = true
	}))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	if !continued {
		t.Fatal("nil error stopped composition")
	}
}

// TestMustFailureReturnsOriginalAndCleansUp checks exact error identity and
// immediately active releases, including cleanup before any startup runs.
// Comparing interface values requires identity, not merely errors.Is matching.
func TestMustFailureReturnsOriginalAndCleansUp(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("constructor failed")
	var order []int

	err := app.Start(t.Context(), func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			order = append(order, 1)

			return nil
		})
		scope.Cleanup(func(context.Context) error {
			order = append(order, 2)

			return nil
		})

		_ = scope.Must(0, failure)
		t.Fatal("composition continued after Must failure")
	})

	if any(err) != any(failure) {
		t.Fatalf("error = %v, want original error %v", err, failure)
	}

	mustNil(t, app.Shutdown(context.Background()))

	if !slices.Equal(order, []int{2, 1}) {
		t.Fatalf("cleanup order = %v, want [2 1] exactly once", order)
	}
}

// TestRequireFailureSkipsAllStartupAndWorkers checks composition's failure
// barrier, including later composers and background readiness checks.
func TestRequireFailureSkipsAllStartupAndWorkers(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("configuration failed")
	var executed atomic.Bool
	callback := func(context.Context) error {
		executed.Store(true)

		return nil
	}

	var order []int

	err := app.Start(t.Context(), func(scope *application.Composer) {
		scope.Startup(callback)
		scope.Run(callback)
		scope.Check("dependency", callback)
		scope.Cleanup(func(context.Context) error {
			order = append(order, 1)

			return nil
		})
		scope.Cleanup(func(context.Context) error {
			order = append(order, 2)

			return nil
		})
		scope.Require(failure)
		t.Fatal("composition continued after Require failure")
	}, func(*application.Composer) {
		t.Fatal("later composer ran after failure")
	})

	if any(err) != any(failure) {
		t.Fatalf("error = %v, want original error %v", err, failure)
	}

	mustNil(t, app.Shutdown(context.Background()))

	if executed.Load() || !slices.Equal(order, []int{2, 1}) {
		t.Fatalf("executed = %v, cleanup order = %v", executed.Load(), order)
	}
}

// TestStartupSuccessGatesWorkersAndChecks uses virtual scheduling to prove
// registered work remains dormant throughout a blocked startup hook.
func TestStartupSuccessGatesWorkersAndChecks(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app := application.New()
		var executed atomic.Bool
		callback := func(context.Context) error {
			executed.Store(true)

			return nil
		}

		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)

		go func() {
			done <- app.Start(t.Context(), func(scope *application.Composer) {
				scope.Run(callback)
				scope.Check("dependency", callback)
				scope.Startup(func(context.Context) error {
					close(entered)
					<-release

					return nil
				})
			})
		}()

		<-entered
		synctest.Wait()

		if executed.Load() {
			t.Fatal("worker or readiness check ran during startup")
		}

		close(release)

		mustNil(t, <-done)
		synctest.Wait()

		if !executed.Load() {
			t.Fatal("registered work never ran after startup")
		}

		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestCompositionProgrammerPanicCleansUpAndRepanics checks that a normal
// error-valued panic is not mistaken for Must's private abort marker.
func TestCompositionProgrammerPanicCleansUpAndRepanics(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("programmer panic")
	var order []int
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = app.Start(t.Context(), func(scope *application.Composer) {
			scope.Cleanup(func(context.Context) error {
				order = append(order, 1)

				return nil
			})
			scope.Cleanup(func(context.Context) error {
				order = append(order, 2)

				return nil
			})
			panic(failure)
		})
	}()

	if recovered != any(failure) || !slices.Equal(order, []int{2, 1}) {
		t.Fatalf("panic = %v, cleanup order = %v", recovered, order)
	}

	mustNil(t, app.Shutdown(context.Background()))

	if len(order) != 2 {
		t.Fatalf("cleanup ran again: %v", order)
	}
}

// TestRequireRecoveredAbortStillFails prevents application code from turning
// a recorded composition failure into a successful startup by recovering it.
func TestRequireRecoveredAbortStillFails(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("required dependency unavailable")

	err := app.Start(t.Context(), func(scope *application.Composer) {
		defer func() {
			_ = recover() // Deliberately exercise recovery of the private abort.
		}()
		scope.Require(failure)
	})

	if any(err) != any(failure) {
		t.Fatalf("error = %v, want original error %v", err, failure)
	}
}

// TestRequireAbortCannotEscapeItsScope checks that replaying an abort from a
// different application is a programmer panic, not the new app's startup error.
func TestRequireAbortCannotEscapeItsScope(t *testing.T) {
	t.Parallel()

	first := application.New()
	failure := errors.New("first composition failed")
	var marker any

	err := first.Start(t.Context(), func(scope *application.Composer) {
		defer func() {
			marker = recover()
		}()
		scope.Require(failure)
	})

	if any(err) != any(failure) || marker == nil {
		t.Fatalf("error = %v, abort marker = %v", err, marker)
	}

	second := application.New()
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = second.Start(t.Context(), func(*application.Composer) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want original scoped marker %v", recovered, marker)
	}
}

// TestExpiredComposerRequireRejectsNil checks that even no-op validation must
// not accidentally reactivate a composer after application startup.
func TestExpiredComposerRequireRejectsNil(t *testing.T) {
	t.Parallel()

	app := application.New()
	var retained *application.Composer

	mustNil(t, app.Start(t.Context(), func(scope *application.Composer) {
		retained = scope
	}))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.Require(nil)
	})
}

// TestExpiredComposerMustRejectsValues checks that successful unwrapping and
// failed unwrapping both require an active synchronous composition scope.
func TestExpiredComposerMustRejectsValues(t *testing.T) {
	t.Parallel()

	app := application.New()
	var retained *application.Composer

	mustNil(t, app.Start(t.Context(), func(scope *application.Composer) {
		retained = scope
	}))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	mustPanic(t, application.ErrInvalidPhase, func() {
		_ = retained.Must(42, nil)
	})
	mustPanic(t, application.ErrInvalidPhase, func() {
		_ = retained.Must(0, errors.New("late constructor failure"))
	})
}

// TestMustRecoveredAbortStillFails checks sticky failure after application
// code catches Must's private marker, including preservation of error identity.
func TestMustRecoveredAbortStillFails(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("required constructor failed")

	err := app.Start(t.Context(), func(scope *application.Composer) {
		defer func() {
			_ = recover() // Deliberately exercise recovery of the private abort.
		}()
		_ = scope.Must(0, failure)
	})

	if any(err) != any(failure) {
		t.Fatalf("error = %v, want original error %v", err, failure)
	}
}

// TestMustAbortCannotEscapeItsScope checks that a captured constructor abort
// cannot impersonate an intentional failure in another application's composer.
func TestMustAbortCannotEscapeItsScope(t *testing.T) {
	t.Parallel()

	first := application.New()
	failure := errors.New("first constructor failed")
	var marker any

	err := first.Start(t.Context(), func(scope *application.Composer) {
		defer func() {
			marker = recover()
		}()
		_ = scope.Must(0, failure)
	})

	if any(err) != any(failure) || marker == nil {
		t.Fatalf("error = %v, abort marker = %v", err, marker)
	}

	second := application.New()
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = second.Start(t.Context(), func(*application.Composer) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want original scoped marker %v", recovered, marker)
	}
}

// TestRunCompositionFailureReturnsOriginal checks the combined lifecycle's
// failure path without requiring workers, signals, or wall-clock cancellation.
func TestRunCompositionFailureReturnsOriginal(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("construction failed")
	cleaned := false

	err := app.Run(t.Context(), func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			cleaned = true

			return nil
		})
		scope.Require(failure)
	})

	if any(err) != any(failure) || !cleaned {
		t.Fatalf("error = %v, cleaned = %v", err, cleaned)
	}
}

// TestStartupCleanupJoinsReverseRegistrationOrder checks that resources
// acquired inside a failing startup join the same cleanup stack as composition.
func TestStartupCleanupJoinsReverseRegistrationOrder(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("startup failed after acquisition")
	var order []int

	err := app.Start(t.Context(), func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			order = append(order, 1)

			return nil
		})
		scope.Startup(func(context.Context) error {
			scope.Cleanup(func(context.Context) error {
				order = append(order, 3)

				return nil
			})

			return failure
		})
		scope.Cleanup(func(context.Context) error {
			order = append(order, 2)

			return nil
		})
		scope.Startup(func(context.Context) error {
			t.Fatal("later startup ran after failure")

			return nil
		})
	})

	if !errors.Is(err, failure) || !slices.Equal(order, []int{3, 2, 1}) {
		t.Fatalf("error = %v, cleanup order = %v", err, order)
	}

	mustNil(t, app.Shutdown(context.Background()))

	if len(order) != 3 {
		t.Fatalf("cleanup ran twice: %v", order)
	}
}

// TestCompositionPanicSurvivesCleanupPanic checks best-effort cleanup without
// losing the original programmer panic when one release also panics.
func TestCompositionPanicSurvivesCleanupPanic(t *testing.T) {
	t.Parallel()

	app := application.New()
	panicValue := new(int)
	cleaned := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = app.Start(t.Context(), func(scope *application.Composer) {
			scope.Cleanup(func(context.Context) error {
				cleaned = true

				return nil
			})
			scope.Cleanup(func(context.Context) error {
				panic("cleanup panic")
			})
			panic(panicValue)
		})
	}()

	if recovered != panicValue || !cleaned {
		t.Fatalf("panic = %v, cleaned = %v", recovered, cleaned)
	}
}

// TestCleanupRejectsFurtherRegistration checks that rollback cannot extend its
// own stack or add work through the retained startup composition scope.
func TestCleanupRejectsFurtherRegistration(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("composition failed")

	err := app.Start(t.Context(), func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			mustPanic(t, application.ErrInvalidPhase, func() {
				scope.Cleanup(func(context.Context) error {
					t.Fatal("cleanup registered during rollback ran")

					return nil
				})
			})

			return nil
		})
		scope.Require(failure)
	})

	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}
}

// TestNilComposerCleansUpBeforePanic checks that a supplied nil callback is
// programmer misuse, but releases acquired by earlier composers remain owned.
func TestNilComposerCleansUpBeforePanic(t *testing.T) {
	t.Parallel()

	app := application.New()
	cleaned := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = app.Start(t.Context(), func(scope *application.Composer) {
			scope.Cleanup(func(context.Context) error {
				cleaned = true

				return nil
			})
		}, nil)
	}()

	if recovered == nil || !cleaned {
		t.Fatalf("panic = %v, cleaned = %v", recovered, cleaned)
	}
}

// TestDeferSharesCleanupOrder checks mixed releases run once in reverse order.
func TestDeferSharesCleanupOrder(t *testing.T) {
	t.Parallel()

	app := application.New()
	var order []int

	mustNil(t, app.Start(t.Context(), func(compose *application.Composer) {
		compose.Defer(func() {
			order = append(order, 1)
		})
		compose.Cleanup(func(context.Context) error {
			order = append(order, 2)

			return nil
		})
		compose.MustDefer(func() {
			order = append(order, 3)
		}, nil)
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if len(order) != 0 {
		t.Fatalf("releases ran during composition: %v", order)
	}

	mustNil(t, app.Shutdown(context.Background()))
	mustNil(t, app.Shutdown(context.Background()))

	if !slices.Equal(order, []int{3, 2, 1}) {
		t.Fatalf("release order = %v", order)
	}
}

// TestDeferReleasesOnCompositionFailure checks registration is immediately active.
func TestDeferReleasesOnCompositionFailure(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("composition failed")
	released := false

	err := app.Start(t.Context(), func(compose *application.Composer) {
		compose.Defer(func() {
			released = true
		})
		compose.Require(failure)
	})

	if !errors.Is(err, failure) || !released {
		t.Fatalf("error = %v, released = %v", err, released)
	}
}

// TestDeferRegistersDuringStartup checks startup acquisitions share rollback.
func TestDeferRegistersDuringStartup(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("startup failed")
	released := false

	err := app.Start(t.Context(), func(compose *application.Composer) {
		compose.Startup(func(context.Context) error {
			compose.Defer(func() {
				released = true
			})

			return failure
		})
	})

	if !errors.Is(err, failure) || !released {
		t.Fatalf("error = %v, released = %v", err, released)
	}
}

// TestDeferPanicDoesNotSkipCleanup checks ordinary cleanup panic handling applies.
func TestDeferPanicDoesNotSkipCleanup(t *testing.T) {
	t.Parallel()

	app := application.New()
	released := false
	failure := errors.New("release panic")

	mustNil(t, app.Start(t.Context(), func(compose *application.Composer) {
		compose.Defer(func() {
			released = true
		})
		compose.Defer(func() {
			panic(failure)
		})
	}))

	err := app.Shutdown(context.Background())

	if err == nil || !released {
		t.Fatalf("error = %v, released = %v", err, released)
	}
}

// TestDeferRejectsNil checks that nil releases are wiring errors, not late panics.
func TestDeferRejectsNil(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(compose *application.Composer) {
		mustPanic(t, application.ErrNilFunc, func() {
			compose.Defer(nil)
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestDeferRejectsLateRegistration checks a retained composer cannot own new work.
func TestDeferRejectsLateRegistration(t *testing.T) {
	t.Parallel()

	app := application.New()
	var retained *application.Composer

	mustNil(t, app.Start(t.Context(), func(compose *application.Composer) {
		retained = compose
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.Defer(func() {
		})
	})
}

// TestMustDeferOwnsEventSubscription checks multi-result acquisition and removal.
func TestMustDeferOwnsEventSubscription(t *testing.T) {
	t.Parallel()

	app := application.New()
	count := 0

	mustNil(t, app.Start(t.Context(), func(compose *application.Composer) {
		compose.MustDefer(app.Events().Listen(application.Started, func(context.Context, event.Void) error {
			count++

			return nil
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if count != 1 {
		t.Fatalf("started calls = %d", count)
	}

	mustNil(t, app.Shutdown(context.Background()))
	app.Events().Signal(t.Context(), application.Started)

	if count != 1 {
		t.Fatalf("subscription survived cleanup: %d calls", count)
	}
}

// TestMustDeferFailureAbortsWithoutRegistering checks acquisition error priority.
func TestMustDeferFailureAbortsWithoutRegistering(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("registration failed")
	cleanupFailure := errors.New("cleanup failed")
	released := false

	err := app.Start(t.Context(), func(compose *application.Composer) {
		compose.Cleanup(func(context.Context) error {
			released = true

			return cleanupFailure
		})
		compose.MustDefer(func() {
			t.Error("failed acquisition registered its release")
		}, failure)
		t.Error("composition continued after acquisition failure")
	})

	if !errors.Is(err, failure) || !errors.Is(err, cleanupFailure) || !released {
		t.Fatalf("error = %v, released = %v", err, released)
	}
}

// TestMustDeferFailureDoesNotValidateRelease checks failed acquisition can return nil.
func TestMustDeferFailureDoesNotValidateRelease(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("registration failed")

	err := app.Start(t.Context(), func(compose *application.Composer) {
		compose.MustDefer(nil, failure)
	})

	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
}

// TestMustDeferRejectsSuccessfulNilRelease checks invalid successful acquisition.
func TestMustDeferRejectsSuccessfulNilRelease(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(compose *application.Composer) {
		mustPanic(t, application.ErrNilFunc, func() {
			compose.MustDefer(nil, nil)
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestMustDeferRejectsStartup checks acquisition aborts stay composition-only.
func TestMustDeferRejectsStartup(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(compose *application.Composer) {
		compose.Startup(func(context.Context) error {
			mustPanic(t, application.ErrInvalidPhase, func() {
				compose.MustDefer(func() {
				}, nil)
			})

			return nil
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// ExampleComposer_Defer registers release for application shutdown, not scope exit.
func ExampleComposer_Defer() {
	app := application.New()

	if err := app.Start(context.Background(), func(compose *application.Composer) {
		compose.Defer(func() {
			fmt.Println("released")
		})
	}); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("running")

	if err := app.Shutdown(context.Background()); err != nil {
		fmt.Println(err)
	}
	// Output:
	// running
	// released
}

// ExampleComposer_MustDefer owns the successful listener registration automatically.
func ExampleComposer_MustDefer() {
	app := application.New()

	if err := app.Start(context.Background(), func(compose *application.Composer) {
		events := compose.App().Events()

		compose.MustDefer(events.Listen(application.Started, func(context.Context, event.Void) error {
			fmt.Println("started")

			return nil
		}))
	}); err != nil {
		fmt.Println(err)

		return
	}

	if err := app.Shutdown(context.Background()); err != nil {
		fmt.Println(err)
	}

	app.Events().Signal(context.Background(), application.Started)
	// Output:
	// started
}
