package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// logoutHandler wraps Guard.LogoutHandler in the authentication middleware of login, as
// an application wires it.
func logoutHandler(guard *auth.Guard, configure ...auth.LogoutBuilderFunc) router.Handler {
	return auth.Middleware(guard.Authenticator())(guard.LogoutHandler(configure...))
}

// listenLoggedOut returns the payloads published on bus so far.
func listenLoggedOut(t *testing.T, bus *event.Bus) *[]auth.LoggedOutEvent {
	t.Helper()

	var got []auth.LoggedOutEvent

	if _, err := bus.Listen(auth.LoggedOut, func(_ context.Context, payload auth.LoggedOutEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return &got
}

// TestLogoutEndsLoginAndPublishesOnce verifies a logged-in user gets 204, loses
// the session and the login record, rotates the ID and is published once.
func TestLogoutEndsLoginAndPublishesOnce(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	bus := event.New()
	got := listenLoggedOut(t, bus)
	current, r := device(t, guard, "7", "Firefox")
	before := current.ID()
	w := httptest.NewRecorder()

	err := logoutHandler(guard, func(logout *auth.LogoutBuilder) { logout.Events(bus) })(w, r)

	if err != nil || w.Code != http.StatusNoContent {
		t.Fatalf("logout = %v, %d", err, w.Code)
	}

	if current.ID() == before {
		t.Fatal("session ID was not regenerated")
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("still logged in: %v", err)
	}

	if len(loginsOf(t, guard, "7")) != 0 {
		t.Fatal("login record survived")
	}

	if len(*got) != 1 || (*got)[0].Subject != "7" {
		t.Fatalf("events = %+v", *got)
	}
}

// TestLogoutAnonymousSucceedsWithoutEvent verifies logging out a session
// without a logged-in user answers 204 and publishes nothing.
func TestLogoutAnonymousSucceedsWithoutEvent(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	bus := event.New()
	got := listenLoggedOut(t, bus)
	w := httptest.NewRecorder()

	err := logoutHandler(guard, func(logout *auth.LogoutBuilder) { logout.Events(bus) })(w, sessionRequest(newSession()))

	if err != nil || w.Code != http.StatusNoContent || len(*got) != 0 {
		t.Fatalf("logout = %v, %d, events %+v", err, w.Code, *got)
	}
}

// TestLogoutClearsPendingLogin verifies a pending second-factor login is
// discarded without an event.
func TestLogoutClearsPendingLogin(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	bus := event.New()
	got := listenLoggedOut(t, bus)
	r := sessionRequest(newSession())

	if begin(t, guard, r, "7") {
		t.Fatal("login completed without its second factor")
	}

	err := logoutHandler(guard, func(logout *auth.LogoutBuilder) { logout.Events(bus) })(httptest.NewRecorder(), r)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending login survived: %v", err)
	}

	if len(*got) != 0 {
		t.Fatalf("events = %+v", *got)
	}
}

// TestLogoutWithoutSessionIsWiringError verifies a request without session
// middleware returns session.ErrNoSession.
func TestLogoutWithoutSessionIsWiringError(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)

	err := guard.LogoutHandler()(httptest.NewRecorder(), r)

	if !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("error = %v", err)
	}
}

// TestLogoutRedirectAnswersSeeOther verifies Redirect answers 303 with Location.
func TestLogoutRedirectAnswersSeeOther(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	_, r := device(t, guard, "7", "Firefox")
	w := httptest.NewRecorder()

	err := logoutHandler(guard, func(logout *auth.LogoutBuilder) { logout.Redirect("/") })(w, r)

	if err != nil || w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("logout = %v, %d, %q", err, w.Code, w.Header().Get("Location"))
	}
}

// TestLogoutUnsafeRedirectIsConfigurationError verifies an unsafe target fails
// every request before the session changes.
func TestLogoutUnsafeRedirectIsConfigurationError(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current, r := device(t, guard, "7", "Firefox")

	err := logoutHandler(guard, func(logout *auth.LogoutBuilder) { logout.Redirect("https://evil.example") })(httptest.NewRecorder(), r)

	if !errors.Is(err, response.ErrUnsafeRedirect) {
		t.Fatalf("error = %v", err)
	}

	if _, err := authenticate(guard, current); err != nil {
		t.Fatalf("session changed: %v", err)
	}
}

// TestLogoutNilLoginIsConfigurationError verifies a nil login returns an error
// per request instead of panicking.
func TestLogoutNilLoginIsConfigurationError(t *testing.T) {
	t.Parallel()

	handler := (*auth.Guard)(nil).LogoutHandler()

	for range 2 {
		if err := handler(httptest.NewRecorder(), sessionRequest(newSession())); err == nil {
			t.Fatal("nil login was accepted")
		}
	}
}

// TestLogoutWithoutBusPublishesNothing verifies the default publishes nothing.
func TestLogoutWithoutBusPublishesNothing(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	_, r := device(t, guard, "7", "Firefox")
	w := httptest.NewRecorder()

	if err := logoutHandler(guard)(w, r); err != nil || w.Code != http.StatusNoContent {
		t.Fatalf("logout = %v, %d", err, w.Code)
	}
}

func ExampleGuard_LogoutHandler() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
	w := httptest.NewRecorder()

	err := auth.Middleware(guard.Authenticator())(guard.LogoutHandler())(w, r)
	_, authErr := guard.Authenticator()(r)

	fmt.Println(err, w.Code, errors.Is(authErr, auth.ErrNoCredentials))
	// Output: <nil> 204 true
}

func ExampleLogoutBuilder() {
	guard := exampleGuard()
	handler := guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
		logout.Redirect("/").Events(event.New())
	})
	w := httptest.NewRecorder()

	err := handler(w, exampleRequest())

	fmt.Println(err, w.Code, w.Header().Get("Location"))
	// Output: <nil> 303 /
}

func ExampleLogoutBuilder_Redirect() {
	handler := exampleGuard().LogoutHandler(func(logout *auth.LogoutBuilder) {
		logout.Redirect("/goodbye")
	})
	w := httptest.NewRecorder()

	err := handler(w, exampleRequest())

	fmt.Println(err, w.Code, w.Header().Get("Location"))
	// Output: <nil> 303 /goodbye
}

func ExampleLogoutBuilder_Events() {
	bus := event.New()
	_, _ = bus.Listen(auth.LoggedOut, func(_ context.Context, out auth.LoggedOutEvent) error {
		fmt.Println("logged out", out.Subject)

		return nil
	})
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	handler := auth.Middleware(guard.Authenticator())(guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
		logout.Events(bus)
	}))
	_ = handler(httptest.NewRecorder(), r)
	// Output: logged out 1
}

func ExampleLogoutBuilderFunc() {
	policy := auth.LogoutBuilderFunc(func(logout *auth.LogoutBuilder) {
		logout.Redirect("/")
	})
	w := httptest.NewRecorder()

	err := exampleGuard().LogoutHandler(policy)(w, exampleRequest())

	fmt.Println(err, w.Code)
	// Output: <nil> 303
}

func ExampleLoggedOut() {
	bus := event.New()
	_, _ = bus.Listen(auth.LoggedOut, func(_ context.Context, out auth.LoggedOutEvent) error {
		fmt.Println(out.Subject)

		return nil
	})

	bus.Emit(context.Background(), auth.LoggedOut, auth.LoggedOutEvent{Subject: "1"})
	// Output: 1
}

func ExampleLoggedOutEvent() {
	fmt.Println(auth.LoggedOutEvent{Subject: "1"}.Subject)
	// Output: 1
}
