package web_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/ark/iam/internal/web"
)

// TestThenRunsBothAndJoinsErrors verifies a failing first hook does not skip the
// second, and both errors are reported.
func TestThenRunsBothAndJoinsErrors(t *testing.T) {
	t.Parallel()

	first, second := errors.New("first"), errors.New("second")
	var ran bool
	hook := web.Then(
		func(context.Context, string) error { return first },
		func(context.Context, string) error { ran = true; return second },
	)

	err := hook(t.Context(), "subject")

	if !ran || !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("ran = %v, error = %v; want both hooks to run and both errors joined", ran, err)
	}
}

// TestThenSkipsNilSides verifies a nil side leaves the other hook as it is.
func TestThenSkipsNilSides(t *testing.T) {
	t.Parallel()

	if web.Then[string](nil, nil) != nil {
		t.Fatal("Then of two nil hooks is not nil")
	}
	if err := web.Then(nil, func(context.Context, string) error { return nil })(t.Context(), "x"); err != nil {
		t.Fatalf("Then(nil, hook) = %v", err)
	}
}

// TestAllNeedsEveryCheck verifies the combined check is false when either side
// is false, and does not call the second after a false first.
func TestAllNeedsEveryCheck(t *testing.T) {
	t.Parallel()

	yes := func(context.Context, string) (bool, error) { return true, nil }
	no := func(context.Context, string) (bool, error) { return false, nil }
	called := false
	spy := func(context.Context, string) (bool, error) { called = true; return true, nil }

	if ok, _ := web.All(yes, no)(t.Context(), "x"); ok {
		t.Fatal("All(yes, no) = true")
	}
	if ok, _ := web.All(no, spy)(t.Context(), "x"); ok || called {
		t.Fatalf("All(no, spy) = %v, spy called = %v; want false and not called", ok, called)
	}
	if ok, _ := web.All(yes, yes)(t.Context(), "x"); !ok {
		t.Fatal("All(yes, yes) = false")
	}
}

// TestAnyNeedsOneCheck verifies the combined check is true when either side is.
func TestAnyNeedsOneCheck(t *testing.T) {
	t.Parallel()

	yes := func(context.Context, string) (bool, error) { return true, nil }
	no := func(context.Context, string) (bool, error) { return false, nil }

	if ok, _ := web.Any(no, yes)(t.Context(), "x"); !ok {
		t.Fatal("Any(no, yes) = false")
	}
	if ok, _ := web.Any(no, no)(t.Context(), "x"); ok {
		t.Fatal("Any(no, no) = true")
	}
}

// TestFirstReturnsTheFirstSubject verifies the second function runs only when
// the first declines with an empty subject and no error.
func TestFirstReturnsTheFirstSubject(t *testing.T) {
	t.Parallel()

	decline := func(context.Context, string) (string, error) { return "", nil }
	give := func(context.Context, string) (string, error) { return "u1", nil }
	fail := func(context.Context, string) (string, error) { return "", errors.New("down") }

	if subject, _ := web.First(decline, give)(t.Context(), "x"); subject != "u1" {
		t.Fatalf("First(decline, give) = %q, want u1", subject)
	}
	if subject, _ := web.First(give, decline)(t.Context(), "x"); subject != "u1" {
		t.Fatalf("First(give, decline) = %q, want u1", subject)
	}
	if _, err := web.First(fail, give)(t.Context(), "x"); err == nil {
		t.Fatal("First(fail, give) hid the error of the first function")
	}
}
