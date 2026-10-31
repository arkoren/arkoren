package application_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"arkoren.dev/core/application"
)

// TestUseSharesScopeAndOrdersChildren checks typed action slices, nested
// composers, the startup barrier, and global reverse cleanup ordering.
func TestUseSharesScopeAndOrdersChildren(t *testing.T) {
	t.Parallel()

	app := application.New()
	var order []string

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		children := []application.Action{
			func(child *application.Composer) {
				if child != composer {
					t.Fatal("Use created a separate composer")
				}

				order = append(order, "first")
				child.Cleanup(func(context.Context) error {
					order = append(order, "cleanup first")

					return nil
				})
				child.Startup(func(context.Context) error {
					order = append(order, "startup")

					return nil
				})
			},
			func(child *application.Composer) {
				child.Use(application.Action(func(nested *application.Composer) {
					order = append(order, "nested")
					nested.Cleanup(func(context.Context) error {
						order = append(order, "cleanup nested")

						return nil
					})
				}))
			},
		}

		for _, child := range children {
			composer.Use(child)
		}

		order = append(order, "parent")
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if !slices.Equal(order, []string{"first", "nested", "parent", "startup"}) {
		t.Fatalf("composition/startup order = %v", order)
	}

	mustNil(t, app.Shutdown(context.Background()))

	if !slices.Equal(order, []string{"first", "nested", "parent", "startup", "cleanup nested", "cleanup first"}) {
		t.Fatalf("cleanup order = %v", order)
	}
}

// TestUseSequentialActionsPreserveComposition checks each action executes once
// without sealing the enclosing composer or requiring a result placeholder.
func TestUseSequentialActionsPreserveComposition(t *testing.T) {
	t.Parallel()

	app := application.New()
	calls := 0

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		action := application.Action(func(*application.Composer) {
			calls++
		})
		composer.Use(action)
		composer.Use(action)
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 2 {
		t.Fatalf("action calls = %d, want 2", calls)
	}
}

// TestUseFailureSkipsLaterChildren checks constructor failures abort both child
// and parent composition before startup, while releasing earlier acquisitions.
func TestUseFailureSkipsLaterChildren(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("child construction failed")
	cleaned := false

	err := app.Start(t.Context(), func(composer *application.Composer) {
		composer.Use(application.Action(func(child *application.Composer) {
			child.Cleanup(func(context.Context) error {
				cleaned = true

				return nil
			})
			child.Startup(func(context.Context) error {
				t.Fatal("startup executed after child failure")

				return nil
			})

			_ = child.Must(0, failure)
		}))
		composer.Use(application.Action(func(*application.Composer) {
			t.Fatal("later child executed after failure")
		}))
		t.Fatal("parent continued after child failure")
	})

	if !errors.Is(err, failure) || !cleaned {
		t.Fatalf("error = %v, cleaned = %v", err, cleaned)
	}
}

// TestUseStopsAfterRecoveredAbort checks a child cannot erase a recorded
// failure by recovering it and allow a later child to construct resources.
func TestUseStopsAfterRecoveredAbort(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("child failure")

	err := app.Start(t.Context(), func(composer *application.Composer) {
		composer.Use(application.Action(func(child *application.Composer) {
			defer func() {
				_ = recover()
			}()
			child.Require(failure)
		}))
		composer.Use(application.Action(func(*application.Composer) {
			t.Fatal("later child executed after recovered abort")
		}))
	})

	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want recorded child failure", err)
	}
}

// TestUseNilCallbackCleansEarlierChildren checks a nil entry fails when reached,
// with acquired cleanup retained from previously executed children.
func TestUseNilCallbackCleansEarlierChildren(t *testing.T) {
	t.Parallel()

	app := application.New()
	cleaned := false

	mustPanic(t, application.ErrNilFunc, func() {
		_ = app.Start(t.Context(), func(composer *application.Composer) {
			composer.Use(application.Action(func(child *application.Composer) {
				child.Cleanup(func(context.Context) error {
					cleaned = true

					return nil
				})
			}))
			composer.Use[int](nil)
			composer.Use(application.Action(func(*application.Composer) {
				t.Fatal("child after nil entry executed")
			}))
		})
	})

	if !cleaned {
		t.Fatal("nil child lost earlier cleanup")
	}
}

// TestUseRejectsStartupAndRetainedScope checks Use never reopens composition,
// including calls made while startup or shutdown is in progress.
func TestUseRejectsStartupAndRetainedScope(t *testing.T) {
	t.Parallel()

	app := application.New()
	var retained *application.Composer

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		retained = composer
		composer.Startup(func(context.Context) error {
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.Use(application.Action(func(*application.Composer) {}))
			})

			return nil
		})
		composer.Cleanup(func(context.Context) error {
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.Use(application.Action(func(*application.Composer) {
					t.Fatal("cleanup reopened composition")
				}))
			})

			return nil
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.Use(application.Action(func(*application.Composer) {}))
	})
	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.Use(application.Action(func(*application.Composer) {
			t.Fatal("retained composer executed a child")
		}))
	})
}

// TestFactoryTypedPointerFansOut checks inference through the private execution
// protocol from an importing package. Both consumers borrow the exact pointer,
// and all composition finishes before the owner's startup callback executes.
func TestFactoryTypedPointerFansOut(t *testing.T) {
	t.Parallel()

	app := application.New()
	var acquired *sql.DB
	var borrowers []*sql.DB
	var order []string
	owner := application.Factory[*sql.DB](func(composer *application.Composer) *sql.DB {
		acquired = new(sql.DB)
		composer.Cleanup(func(context.Context) error {
			order = append(order, "cleanup")

			return nil
		})
		composer.Startup(func(context.Context) error {
			order = append(order, "startup")

			return nil
		})

		return acquired
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		connection := composer.Use(owner)

		for range 2 {
			composer.Use(application.Action(func(*application.Composer) {
				borrowers = append(borrowers, connection)
				order = append(order, "borrow")
			}))
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if !slices.Equal(borrowers, []*sql.DB{acquired, acquired}) {
		t.Fatalf("borrowed pointers = %v, want two copies of %p", borrowers, acquired)
	}

	if !slices.Equal(order, []string{"borrow", "borrow", "startup"}) {
		t.Fatalf("composition/startup order = %v", order)
	}

	mustNil(t, app.Shutdown(context.Background()))
	mustNil(t, app.Shutdown(context.Background()))

	if !slices.Equal(order, []string{"borrow", "borrow", "startup", "cleanup"}) {
		t.Fatalf("cleanup order = %v", order)
	}
}

// TestFactoryReuseAcquiresPerApplication checks factories are reusable recipes,
// not memoized resource handles. Each application owns only its acquisition.
func TestFactoryReuseAcquiresPerApplication(t *testing.T) {
	t.Parallel()

	first := application.New()
	second := application.New()
	var released []*sql.DB
	owner := application.Factory[*sql.DB](func(composer *application.Composer) *sql.DB {
		connection := new(sql.DB)
		composer.Cleanup(func(context.Context) error {
			released = append(released, connection)

			return nil
		})

		return connection
	})

	var connections []*sql.DB
	root := func(composer *application.Composer) {
		connections = append(connections, composer.Use(owner))
	}

	mustNil(t, first.Start(t.Context(), root))
	t.Cleanup(func() {
		mustNil(t, first.Shutdown(context.Background()))
	})

	mustNil(t, second.Start(t.Context(), root))
	t.Cleanup(func() {
		mustNil(t, second.Shutdown(context.Background()))
	})

	if connections[0] == connections[1] {
		t.Fatal("factory output was memoized across applications")
	}

	mustNil(t, first.Shutdown(context.Background()))

	if !slices.Equal(released, connections[:1]) {
		t.Fatalf("first shutdown released %v", released)
	}

	mustNil(t, second.Shutdown(context.Background()))

	if !slices.Equal(released, connections) {
		t.Fatalf("shutdown releases = %v, want %v", released, connections)
	}
}

// TestFactoryRepeatedUseInvokesAgain checks a shared factory has no per-composer
// cache either. Explicit cleanup remains globally reverse ordered.
func TestFactoryRepeatedUseInvokesAgain(t *testing.T) {
	t.Parallel()

	app := application.New()
	var acquired []*sql.DB
	var released []*sql.DB
	owner := application.Factory[*sql.DB](func(composer *application.Composer) *sql.DB {
		connection := new(sql.DB)
		acquired = append(acquired, connection)
		composer.Cleanup(func(context.Context) error {
			released = append(released, connection)

			return nil
		})

		return connection
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		first := composer.Use(owner)
		second := composer.Use(owner)

		if first == second {
			t.Fatal("factory was not invoked on each Use")
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	mustNil(t, app.Shutdown(context.Background()))

	if !slices.Equal(released, []*sql.DB{acquired[1], acquired[0]}) {
		t.Fatalf("release order = %v, acquisitions = %v", released, acquired)
	}
}

// factoryResource has lifecycle-shaped methods solely to detect implicit
// adoption. Composition must call none of them merely because it returns this
// pointer; lifecycle registration always belongs to application code.
type factoryResource struct {
	// calls records any automatic lifecycle discovery attempt.
	calls int
}

// Start records an unwanted connection attempt without performing I/O.
func (resource *factoryResource) Start(context.Context) error {
	resource.calls++

	return nil
}

// Stop records an unwanted release of a resource that the application borrows.
func (resource *factoryResource) Stop(context.Context) error {
	resource.calls++

	return nil
}

// Ping records an unwanted health check of an unregistered resource.
func (resource *factoryResource) Ping(context.Context) error {
	resource.calls++

	return nil
}

// TestFactoryDoesNotAdoptLifecycle checks result shape never causes implicit
// startup, readiness registration, or cleanup of a borrowed dependency.
func TestFactoryDoesNotAdoptLifecycle(t *testing.T) {
	t.Parallel()

	app := application.New()
	resource := new(factoryResource)

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		got := composer.Use(application.Factory[*factoryResource](func(*application.Composer) *factoryResource {
			return resource
		}))

		if got != resource {
			t.Fatal("borrowed result pointer changed")
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	mustNil(t, app.Shutdown(context.Background()))

	if resource.calls != 0 {
		t.Fatalf("implicitly invoked lifecycle methods %d times", resource.calls)
	}
}

// TestFactoryFailureDoesNotPublishOutput checks Must failure preserves the
// original error, releases acquired resources, and prevents use of any result.
func TestFactoryFailureDoesNotPublishOutput(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("database constructor failed")
	var output *sql.DB
	cleaned := 0

	err := app.Start(t.Context(), func(composer *application.Composer) {
		output = composer.Use(application.Factory[*sql.DB](func(child *application.Composer) *sql.DB {
			child.Cleanup(func(context.Context) error {
				cleaned++

				return nil
			})
			child.Startup(func(context.Context) error {
				t.Fatal("startup ran after constructor failure")

				return nil
			})

			return child.Must(new(sql.DB), failure)
		}))
		t.Fatal("factory failure returned to its caller")
	})

	if any(err) != any(failure) || output != nil {
		t.Fatalf("error = %v, output = %p", err, output)
	}

	mustNil(t, app.Shutdown(context.Background()))

	if cleaned != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleaned)
	}
}

// TestFactorySwallowedAbortDoesNotPublishOutput checks a factory's own recover
// cannot turn Require failure into a successful typed result.
func TestFactorySwallowedAbortDoesNotPublishOutput(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("required database missing")
	output := 99

	err := app.Start(t.Context(), func(composer *application.Composer) {
		output = composer.Use(application.Factory[int](func(child *application.Composer) int {
			defer func() {
				_ = recover()
			}()
			child.Require(failure)

			return 42
		}))
		t.Fatal("swallowed factory abort returned to its caller")
	})

	if any(err) != any(failure) || output != 99 {
		t.Fatalf("error = %v, published output = %d", err, output)
	}
}

// TestConditionalFactoryFailureAbortsRun checks the presence pair is not an
// error channel. Run returns the original failure before publishing either
// result, and cleanup registered inside the factory is already active.
func TestConditionalFactoryFailureAbortsRun(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("conditional constructor failed")
	output, executed := 99, false
	cleaned := false

	err := app.Run(t.Context(), func(composer *application.Composer) {
		output, executed = composer.UseWhen(true, application.Factory[int](func(child *application.Composer) int {
			child.Cleanup(func(context.Context) error {
				cleaned = true

				return nil
			})
			child.Require(failure)

			return 42
		}))
		t.Fatal("conditional failure became a presence result")
	})

	if any(err) != any(failure) || !cleaned {
		t.Fatalf("error = %v, cleaned = %v", err, cleaned)
	}

	if output != 99 || executed {
		t.Fatalf("published result = (%d, %v)", output, executed)
	}
}

// TestUseWhenRunsInlineInParentScope checks callback identity and phase ordering.
func TestUseWhenRunsInlineInParentScope(t *testing.T) {
	t.Parallel()

	app := application.New()
	var order []string

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseWhen(true, application.Action(func(child *application.Composer) {
			if child != composer || child.Context() != composer.Context() {
				t.Fatal("conditional composition created a different scope")
			}

			order = append(order, "conditional")
			child.Startup(func(context.Context) error {
				order = append(order, "startup")

				return nil
			})
			child.Cleanup(func(context.Context) error {
				order = append(order, "cleanup")

				return nil
			})
		}))

		order = append(order, "parent")
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if !slices.Equal(order, []string{"conditional", "parent", "startup"}) {
		t.Fatalf("execution order = %v", order)
	}

	mustNil(t, app.Shutdown(context.Background()))

	if !slices.Equal(order, []string{"conditional", "parent", "startup", "cleanup"}) {
		t.Fatalf("cleanup order = %v", order)
	}
}

// TestUseWhenFalseSkipsAcquisition checks skipped callbacks have no effects.
func TestUseWhenFalseSkipsAcquisition(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseWhen(false, application.Action(func(*application.Composer) {
			t.Fatal("false condition executed composition")
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestUseDebugRunsNamedComposer checks reusable callback compatibility.
func TestUseDebugRunsNamedComposer(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Debug(true)
	})

	calls := 0
	diagnostics := func(*application.Composer) {
		calls++
	}

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseDebug(application.Action(diagnostics))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 1 {
		t.Fatalf("diagnostics calls = %d, want 1", calls)
	}
}

// TestUseDebugDisabledIgnoresRuntimeLogLevel checks debug policy is not inferred
// from the current logger threshold or the default development environment.
func TestUseDebugDisabledIgnoresRuntimeLogLevel(t *testing.T) {
	t.Parallel()

	app := application.New()
	app.SetLogLevel(slog.LevelDebug)

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseDebug(application.Action(func(*application.Composer) {
			t.Fatal("runtime logging or development environment enabled debug composition")
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestUseDebugEnabledInProduction checks debug and environment are independent.
func TestUseDebugEnabledInProduction(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Debug(true)
		app.Environment(application.Production)
	})

	calls := 0

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseDebug(application.Action(func(*application.Composer) {
			calls++
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 1 {
		t.Fatalf("debug calls = %d, want 1", calls)
	}
}

// TestUseInMatchesDefaultEnvironment checks New resolves the environment
// before conditional composition evaluates it.
func TestUseInMatchesDefaultEnvironment(t *testing.T) {
	t.Parallel()

	app := application.New()
	calls := 0

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseIn(application.Development, application.Action(func(*application.Composer) {
			calls++
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 1 {
		t.Fatalf("development calls = %d, want 1", calls)
	}
}

// TestUseInSkipsDifferentEnvironment checks matching rather than debug policy.
func TestUseInSkipsDifferentEnvironment(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Environment(application.Production)
		app.Debug(true)
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseIn(application.Development, application.Action(func(*application.Composer) {
			t.Fatal("different environment executed composition")
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestUseInMatchesCustomEnvironment checks application-defined names work.
func TestUseInMatchesCustomEnvironment(t *testing.T) {
	t.Parallel()

	environment := application.Environment("preview")
	app := application.New(func(app *application.Builder) {
		app.Environment(environment)
	})

	calls := 0

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseIn(environment, application.Action(func(*application.Composer) {
			calls++
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 1 {
		t.Fatalf("custom environment calls = %d, want 1", calls)
	}
}

// TestUseInAnyMatchesOnce checks matches and duplicates invoke one callback.
func TestUseInAnyMatchesOnce(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Environment(application.Staging)
	})

	calls := 0
	environments := []application.Environment{
		application.Development,
		application.Staging,
		application.Staging,
	}

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.UseInAny(environments, application.Action(func(*application.Composer) {
			calls++
		}))
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 1 {
		t.Fatalf("environment calls = %d, want 1", calls)
	}
}

// TestUseInAnySkipsUnmatchedEnvironments checks nonmatching and empty lists.
func TestUseInAnySkipsUnmatchedEnvironments(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		for _, environments := range [][]application.Environment{
			nil,
			{},
			{application.Staging, application.Production},
		} {
			composer.UseInAny(environments, application.Action(func(*application.Composer) {
				t.Fatal("nonmatching environment list executed composition")
			}))
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestConditionalCompositionFailureCleansParentScope checks nested aborts stop
// the outer composer and release all acquisitions in global reverse order.
func TestConditionalCompositionFailureCleansParentScope(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Debug(true)
	})

	failure := errors.New("conditional composition failed")
	var order []int

	err := app.Start(t.Context(), func(composer *application.Composer) {
		composer.Cleanup(func(context.Context) error {
			order = append(order, 1)

			return nil
		})
		composer.Startup(func(context.Context) error {
			t.Fatal("startup executed after conditional failure")

			return nil
		})
		composer.UseDebug(application.Action(func(child *application.Composer) {
			child.UseIn(application.Development, application.Action(func(nested *application.Composer) {
				nested.Cleanup(func(context.Context) error {
					order = append(order, 2)

					return nil
				})
				nested.Require(failure)
			}))
			t.Fatal("conditional callback continued after failure")
		}))
		t.Fatal("parent composition continued after failure")
	})

	if !errors.Is(err, failure) || !slices.Equal(order, []int{2, 1}) {
		t.Fatalf("error = %v, cleanup order = %v", err, order)
	}
}

// TestConditionalCompositionPanicCleansUp checks a selected callback preserves
// ordinary programmer panics while releasing acquisitions in the parent scope.
func TestConditionalCompositionPanicCleansUp(t *testing.T) {
	t.Parallel()

	app := application.New()
	panicValue := new(int)
	cleaned := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = app.Start(t.Context(), func(composer *application.Composer) {
			composer.Cleanup(func(context.Context) error {
				cleaned = true

				return nil
			})
			composer.UseWhen(true, application.Action(func(*application.Composer) {
				panic(panicValue)
			}))
		})
	}()

	if recovered != panicValue || !cleaned {
		t.Fatalf("panic = %v, cleaned = %v", recovered, cleaned)
	}
}

// TestConditionalCompositionRejectsNilCallbacks checks skipped branches still
// validate callbacks instead of hiding wiring mistakes.
func TestConditionalCompositionRejectsNilCallbacks(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		for _, invoke := range []func(){
			func() {
				composer.UseWhen[int](true, nil)
			},
			func() {
				composer.UseWhen[int](false, nil)
			},
			func() {
				composer.UseDebug[int](nil)
			},
			func() {
				composer.UseIn[int](application.Production, nil)
			},
			func() {
				composer.UseInAny[int](nil, nil)
			},
		} {
			mustPanic(t, application.ErrNilFunc, invoke)
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestConditionalCompositionRejectsExpiredScope checks even skipped callbacks
// cannot be used through a composer retained after composition finishes.
func TestConditionalCompositionRejectsExpiredScope(t *testing.T) {
	t.Parallel()

	app := application.New()
	var retained *application.Composer

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		retained = composer
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	callback := func(*application.Composer) {
		t.Fatal("expired conditional composition executed")
	}

	for _, invoke := range []func(){
		func() {
			retained.UseWhen(true, application.Action(callback))
		},
		func() {
			retained.UseWhen(false, application.Action(callback))
		},
		func() {
			retained.UseDebug(application.Action(callback))
		},
		func() {
			retained.UseIn(application.Development, application.Action(callback))
		},
		func() {
			retained.UseInAny(nil, application.Action(callback))
		},
	} {
		mustPanic(t, application.ErrInvalidPhase, invoke)
	}
}

// TestConditionalCompositionRejectsStartupPhase checks conditions do not reopen
// general composition while startup callbacks may only register cleanup.
func TestConditionalCompositionRejectsStartupPhase(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.Startup(func(context.Context) error {
			callback := func(*application.Composer) {
				t.Fatal("startup executed conditional composition")
			}

			for _, invoke := range []func(){
				func() {
					composer.UseWhen(false, application.Action(callback))
				},
				func() {
					composer.UseDebug(application.Action(callback))
				},
				func() {
					composer.UseIn(application.Development, application.Action(callback))
				},
				func() {
					composer.UseInAny(nil, application.Action(callback))
				},
			} {
				mustPanic(t, application.ErrInvalidPhase, invoke)
			}

			return nil
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestConditionalActionsReportExecution checks the common execution protocol
// accepts genuinely void callback bodies and exposes selection separately from
// the internal empty result. Selected zero-result actions are still present.
func TestConditionalActionsReportExecution(t *testing.T) {
	t.Parallel()

	app := application.New()
	calls := 0
	action := application.Action(func(*application.Composer) {
		calls++
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		_, executed := composer.UseWhen(true, action)

		if !executed {
			t.Fatal("selected void action reported absence")
		}

		_, executed = composer.UseWhen(false, action)

		if executed {
			t.Fatal("skipped void action reported execution")
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 1 {
		t.Fatalf("void action calls = %d, want 1", calls)
	}
}

// TestUseWhenFactoryPresenceDistinguishesZero checks an executed factory's zero
// value is present, while a skipped factory neither constructs nor publishes.
func TestUseWhenFactoryPresenceDistinguishesZero(t *testing.T) {
	t.Parallel()

	app := application.New()
	calls := 0
	zero := application.Factory[int](func(*application.Composer) int {
		calls++

		return 0
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		value, executed := composer.UseWhen(true, zero)

		if value != 0 || !executed {
			t.Fatalf("selected zero = (%d, %v)", value, executed)
		}

		value, executed = composer.UseWhen(false, zero)

		if value != 0 || executed {
			t.Fatalf("skipped zero = (%d, %v)", value, executed)
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1", calls)
	}
}

// TestUseWhenFactoryNilResultIsPresent checks nil is a valid selected result,
// distinct from a nil operation and from a skipped optional dependency.
func TestUseWhenFactoryNilResultIsPresent(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		value, executed := composer.UseWhen(true, application.Factory[*sql.DB](func(*application.Composer) *sql.DB {
			return nil
		}))

		if value != nil || !executed {
			t.Fatalf("selected nil = (%p, %v)", value, executed)
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestUseDebugFactoryReturnsTypedPointer checks inferred result identity and
// optional presence when the configured debug flag selects the factory.
func TestUseDebugFactoryReturnsTypedPointer(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Debug(true)
	})

	connection := new(sql.DB)

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		got, executed := composer.UseDebug(application.Factory[*sql.DB](func(*application.Composer) *sql.DB {
			return connection
		}))

		if got != connection || !executed {
			t.Fatalf("debug result = (%p, %v), want %p", got, executed, connection)
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestUseDebugFactorySkippedReturnsZero checks a disabled debug factory does
// not acquire anything and returns an absent typed pointer.
func TestUseDebugFactorySkippedReturnsZero(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		got, executed := composer.UseDebug(application.Factory[*sql.DB](func(*application.Composer) *sql.DB {
			t.Fatal("disabled debug factory acquired a resource")

			return new(sql.DB)
		}))

		if got != nil || executed {
			t.Fatalf("skipped debug result = (%p, %v)", got, executed)
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestEnvironmentFactoriesReturnPresence checks exact and list selection both
// preserve typed outputs, including zero, without invoking skipped factories.
func TestEnvironmentFactoriesReturnPresence(t *testing.T) {
	t.Parallel()

	app := application.New()
	zero := application.Factory[int](func(*application.Composer) int {
		return 0
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		value, executed := composer.UseIn(application.Development, zero)

		if value != 0 || !executed {
			t.Fatalf("selected environment = (%d, %v)", value, executed)
		}

		value, executed = composer.UseInAny([]application.Environment{application.Development}, zero)

		if value != 0 || !executed {
			t.Fatalf("selected environment list = (%d, %v)", value, executed)
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestEnvironmentFactoriesSkipAcquisition checks both environment selectors
// return absence rather than evaluating a constructor for an unmatched branch.
func TestEnvironmentFactoriesSkipAcquisition(t *testing.T) {
	t.Parallel()

	app := application.New()
	forbidden := application.Factory[int](func(*application.Composer) int {
		t.Fatal("unmatched environment acquired a resource")

		return 42
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		value, executed := composer.UseIn(application.Production, forbidden)

		if value != 0 || executed {
			t.Fatalf("skipped environment = (%d, %v)", value, executed)
		}

		value, executed = composer.UseInAny(nil, forbidden)

		if value != 0 || executed {
			t.Fatalf("skipped environment list = (%d, %v)", value, executed)
		}
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestConditionalTypedNilFactoriesPanic checks every selector validates typed
// nil factories on both selected and skipped paths, before attempting invocation.
func TestConditionalTypedNilFactoriesPanic(t *testing.T) {
	t.Parallel()

	for _, selected := range []bool{false, true} {
		app := application.New(func(app *application.Builder) {
			app.Debug(selected)
		})

		mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
			var factory application.Factory[int]

			mustPanic(t, application.ErrNilFunc, func() {
				composer.Use(factory)
			})
			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseWhen(selected, factory)
			})
			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseDebug(factory)
			})

			environment := application.Production

			if selected {
				environment = application.Development
			}

			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseIn(environment, factory)
			})
			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseInAny([]application.Environment{environment}, factory)
			})
		}))
		mustNil(t, app.Shutdown(context.Background()))
	}
}

// TestSelectedConditionsRejectNilInterfaces checks all selectors validate a nil
// protocol interface when selected. Explicit result arguments are necessary
// because an untyped nil carries no factory result for type inference.
func TestSelectedConditionsRejectNilInterfaces(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Debug(true)
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		mustPanic(t, application.ErrNilFunc, func() {
			composer.Use[int](nil)
		})
		mustPanic(t, application.ErrNilFunc, func() {
			composer.UseWhen[int](true, nil)
		})
		mustPanic(t, application.ErrNilFunc, func() {
			composer.UseDebug[int](nil)
		})
		mustPanic(t, application.ErrNilFunc, func() {
			composer.UseIn[int](application.Development, nil)
		})
		mustPanic(t, application.ErrNilFunc, func() {
			composer.UseInAny[int]([]application.Environment{application.Development}, nil)
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestConditionalTypedNilActionsPanic checks genuinely void operations reject
// typed nil values with the same policy as factories, even when skipped.
func TestConditionalTypedNilActionsPanic(t *testing.T) {
	t.Parallel()

	for _, selected := range []bool{false, true} {
		app := application.New(func(app *application.Builder) {
			app.Debug(selected)
		})

		mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
			var action application.Action

			mustPanic(t, application.ErrNilFunc, func() {
				composer.Use(action)
			})
			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseWhen(selected, action)
			})
			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseDebug(action)
			})

			environment := application.Production

			if selected {
				environment = application.Development
			}

			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseIn(environment, action)
			})
			mustPanic(t, application.ErrNilFunc, func() {
				composer.UseInAny([]application.Environment{environment}, action)
			})
		}))
		mustNil(t, app.Shutdown(context.Background()))
	}
}

// TestConditionalFactoryRejectsStartupPhase checks selected and skipped typed
// operations cannot reopen composition while a startup hook is executing.
func TestConditionalFactoryRejectsStartupPhase(t *testing.T) {
	t.Parallel()

	app := application.New()
	forbidden := application.Factory[int](func(*application.Composer) int {
		t.Fatal("startup phase executed a factory")

		return 0
	})

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		composer.Startup(func(context.Context) error {
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.Use(forbidden)
			})
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.UseWhen(true, forbidden)
			})
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.UseWhen(false, forbidden)
			})
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.UseDebug(forbidden)
			})
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.UseIn(application.Production, forbidden)
			})
			mustPanic(t, application.ErrInvalidPhase, func() {
				composer.UseInAny(nil, forbidden)
			})

			return nil
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})
}

// TestConditionalFactoryRejectsRetainedScope checks result-bearing operations
// cannot use a composer retained past composition, even if a branch is skipped.
func TestConditionalFactoryRejectsRetainedScope(t *testing.T) {
	t.Parallel()

	app := application.New()
	var retained *application.Composer

	mustNil(t, app.Start(t.Context(), func(composer *application.Composer) {
		retained = composer
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	forbidden := application.Factory[int](func(*application.Composer) int {
		t.Fatal("retained composer executed a factory")

		return 0
	})

	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.Use(forbidden)
	})
	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.UseWhen(true, forbidden)
	})
	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.UseWhen(false, forbidden)
	})
	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.UseDebug(forbidden)
	})
	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.UseIn(application.Production, forbidden)
	})
	mustPanic(t, application.ErrInvalidPhase, func() {
		retained.UseInAny(nil, forbidden)
	})
}

// TestConditionalSkipCannotEraseStickyFailure checks a recovered Require abort
// prevents even a skipped factory from returning an ordinary absence pair.
func TestConditionalSkipCannotEraseStickyFailure(t *testing.T) {
	t.Parallel()

	app := application.New()
	failure := errors.New("composition already failed")

	err := app.Start(t.Context(), func(composer *application.Composer) {
		func() {
			defer func() {
				_ = recover()
			}()
			composer.Require(failure)
		}()
		composer.UseWhen(false, application.Factory[int](func(*application.Composer) int {
			t.Fatal("skipped factory ran after failure")

			return 0
		}))
		t.Fatal("sticky failure returned a skipped presence result")
	})

	if any(err) != any(failure) {
		t.Fatalf("error = %v, want original failure", err)
	}
}
