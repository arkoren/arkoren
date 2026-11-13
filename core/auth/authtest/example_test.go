package authtest_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/authtest"
	"arkoren.dev/core/auth/memoryauth"
)

// seededDriver returns a memory driver containing users.
func seededDriver(users []authtest.User) auth.UserDriver {
	driver := memoryauth.New()

	for _, user := range users {
		_ = driver.Put(auth.UserRecord{Subject: user.Subject, Version: user.Version, Claims: user.Claims})

		if user.Disabled {
			_ = driver.Disable(user.Subject)
		}
	}

	return driver
}

func ExampleRunUserDriver() {
	// In a real package this is a Test function receiving its own *testing.T.
	_ = func(t *testing.T) {
		authtest.RunUserDriver(t, func(t *testing.T, users []authtest.User) auth.UserDriver {
			return seededDriver(users)
		})
	}

	fmt.Println("run the suite from a Test function")
	// Output: run the suite from a Test function
}

func ExampleWithTimeout() {
	option := authtest.WithTimeout(0) // Zero keeps the 30 second default.

	fmt.Println(option != nil)
	// Output: true
}

func ExampleUser() {
	user := authtest.User{Subject: "1", Version: "v7", Claims: map[string]any{"role": "admin"}}

	fmt.Println(user.Subject, user.Version, user.Disabled)
	// Output: 1 v7 false
}

func ExampleOption() {
	option := authtest.Option(authtest.WithTimeout(1))

	fmt.Println(option != nil)
	// Output: true
}

func ExampleRun() {
	authenticate := auth.Bearer(func(_ context.Context, token string) (auth.Identity, error) {
		if token != "good" {
			return auth.Identity{}, errors.New("rejected token")
		}

		return auth.NewIdentity("42"), nil
	})
	request := func(header string) func(t *testing.T) *http.Request {
		return func(*testing.T) *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			if header != "" {
				r.Header.Set("Authorization", header)
			}

			return r
		}
	}

	// In a real package this is a Test function receiving its own *testing.T.
	_ = func(t *testing.T) {
		authtest.Run(t, authenticate, request(""), request("Bearer good"),
			authtest.WithInvalid(request("Bearer forged")))
	}

	fmt.Println("run the suite from a Test function")
	// Output: run the suite from a Test function
}

func ExampleWithInvalid() {
	option := authtest.WithInvalid(func(*testing.T) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/", nil)
	})

	fmt.Println(option != nil)
	// Output: true
}

func ExampleWithIdentity() {
	option := authtest.WithIdentity(func(t *testing.T, identity auth.Identity) {
		if identity.Subject() != "42" {
			t.Fatalf("subject = %q", identity.Subject())
		}
	})

	fmt.Println(option != nil)
	// Output: true
}
