package auth_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/event"
)

// ctxKey keys the test value carried in contexts.
type ctxKey struct{}

// forgottenLog listens for [auth.UserForgotten] on bus and returns the payloads
// published so far.
func forgottenLog(t *testing.T, bus *event.Bus) func() []string {
	t.Helper()

	var (
		mu  sync.Mutex
		got []string
	)

	if _, err := bus.Listen(auth.UserForgotten, func(_ context.Context, forgotten auth.UserForgottenEvent) error {
		mu.Lock()
		defer mu.Unlock()

		got = append(got, forgotten.Subject)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return func() []string {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(got)
	}
}

// newProvider returns a provider over an empty memory driver.
func newProvider(t *testing.T, configure ...auth.UserProviderBuilderFunc) *auth.UserProvider {
	t.Helper()

	users, err := auth.NewUserProvider(memoryauth.New(), configure...)

	if err != nil {
		t.Fatal(err)
	}

	return users
}

// TestNewUserProviderRejectsNilDriver verifies a nil driver fails construction.
func TestNewUserProviderRejectsNilDriver(t *testing.T) {
	t.Parallel()

	if _, err := auth.NewUserProvider(nil); err == nil {
		t.Fatal("nil driver accepted")
	}
}

// TestFindDelegatesToDriver verifies Find returns the driver's record.
func TestFindDelegatesToDriver(t *testing.T) {
	t.Parallel()

	driver := memoryauth.New()

	if err := driver.Put(auth.UserRecord{Subject: "7", Version: "v2"}); err != nil {
		t.Fatal(err)
	}

	users, err := auth.NewUserProvider(driver)

	if err != nil {
		t.Fatal(err)
	}

	if record, err := users.Find(t.Context(), "7"); err != nil || record.Version != "v2" {
		t.Fatalf("record = %v, error = %v", record, err)
	}

	if _, err := users.Find(t.Context(), "8"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestForgetRunsHooksInOrder verifies registration order and the subject.
func TestForgetRunsHooksInOrder(t *testing.T) {
	t.Parallel()

	users := newProvider(t)

	var calls []string

	for _, name := range []string{"a", "b", "c"} {
		users.OnForget(func(_ context.Context, subject string) error {
			calls = append(calls, name+":"+subject)

			return nil
		})
	}

	if err := users.Forget(t.Context(), "7"); err != nil {
		t.Fatal(err)
	}

	if want := []string{"a:7", "b:7", "c:7"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

// TestForgetErrorStopsAndSkipsEvent verifies the first error is returned, later
// hooks do not run and nothing is emitted.
func TestForgetErrorStopsAndSkipsEvent(t *testing.T) {
	t.Parallel()

	bus := event.New()
	emitted := forgottenLog(t, bus)
	users := newProvider(t, func(users *auth.UserProviderBuilder) { users.Events(bus) })
	failure := errors.New("delete failed")

	var calls []string

	users.OnForget(func(context.Context, string) error {
		calls = append(calls, "first")

		return nil
	})
	users.OnForget(func(context.Context, string) error {
		calls = append(calls, "second")

		return failure
	})
	users.OnForget(func(context.Context, string) error {
		calls = append(calls, "third")

		return nil
	})

	if err := users.Forget(t.Context(), "7"); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the hook's error", err)
	}

	if want := []string{"first", "second"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}

	if got := emitted(); len(got) != 0 {
		t.Fatalf("emitted %v after failure", got)
	}
}

// TestForgetEmitsOnceOnSuccess verifies one event carrying the subject.
func TestForgetEmitsOnceOnSuccess(t *testing.T) {
	t.Parallel()

	bus := event.New()
	emitted := forgottenLog(t, bus)
	users := newProvider(t, func(users *auth.UserProviderBuilder) { users.Events(bus) })

	users.OnForget(func(context.Context, string) error { return nil })

	if err := users.Forget(t.Context(), "7"); err != nil {
		t.Fatal(err)
	}

	if got := emitted(); !slices.Equal(got, []string{"7"}) {
		t.Fatalf("emitted %v, want [7]", got)
	}
}

// TestForgetEmitsWithoutHooks verifies a provider with no hooks still reports
// completion.
func TestForgetEmitsWithoutHooks(t *testing.T) {
	t.Parallel()

	bus := event.New()
	emitted := forgottenLog(t, bus)
	users := newProvider(t, func(users *auth.UserProviderBuilder) { users.Events(bus) })

	if err := users.Forget(t.Context(), "7"); err != nil {
		t.Fatal(err)
	}

	if got := emitted(); len(got) != 1 {
		t.Fatalf("emitted %v, want one event", got)
	}
}

// TestForgetWithoutBus verifies a nil bus emits nothing and does not fail.
func TestForgetWithoutBus(t *testing.T) {
	t.Parallel()

	users := newProvider(t)
	ran := false

	users.OnForget(func(context.Context, string) error {
		ran = true

		return nil
	})

	if err := users.Forget(t.Context(), "7"); err != nil || !ran {
		t.Fatalf("error = %v, ran = %t", err, ran)
	}
}

// TestForgetPassesCallerContext verifies a hook sees values the caller put in
// ctx, which is what lets it join a transaction carried there.
func TestForgetPassesCallerContext(t *testing.T) {
	t.Parallel()

	users := newProvider(t)
	ctx := context.WithValue(t.Context(), ctxKey{}, "tx-1")

	var seen any

	users.OnForget(func(ctx context.Context, _ string) error {
		seen = ctx.Value(ctxKey{})

		return nil
	})

	if err := users.Forget(ctx, "7"); err != nil || seen != "tx-1" {
		t.Fatalf("seen = %v, error = %v", seen, err)
	}
}

// TestForgetRejectsInvalidSubject verifies hooks do not run for an invalid
// subject.
func TestForgetRejectsInvalidSubject(t *testing.T) {
	t.Parallel()

	users := newProvider(t)

	users.OnForget(func(context.Context, string) error {
		t.Error("hook ran for invalid subject")

		return nil
	})

	if err := users.Forget(t.Context(), ""); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("error = %v", err)
	}
}

// TestOnForgetRegistersConcurrently verifies registration races with Forget
// safely; run under the race detector.
func TestOnForgetRegistersConcurrently(t *testing.T) {
	t.Parallel()

	users := newProvider(t)

	var wait sync.WaitGroup

	for range 8 {
		wait.Go(func() {
			users.OnForget(func(context.Context, string) error { return nil })

			if err := users.Forget(t.Context(), "7"); err != nil {
				t.Error(err)
			}
		})
	}

	wait.Wait()
}

// TestOnForgetRejectsNilHook verifies a nil hook panics at registration.
func TestOnForgetRejectsNilHook(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil hook accepted")
		}
	}()

	newProvider(t).OnForget(nil)
}
