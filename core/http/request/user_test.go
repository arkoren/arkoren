package request_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/foundation/problem"
)

// authenticated returns a request whose identity has the given subject and
// carries value as the application user.
func authenticated(subject string, value any) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	identity := auth.NewIdentity(subject, func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Subject: subject, Value: value})
	})

	return r.WithContext(auth.NewContext(r.Context(), identity))
}

func ExampleIdentity() {
	r := authenticated("42", nil)

	identity, err := request.Identity(r)

	fmt.Println(identity.Subject(), err)
	// Output: 42 <nil>
}

func ExampleUser() {
	type User struct{ Name string }

	r := authenticated("42", User{Name: "ann"})

	user, err := request.User[User](r)

	fmt.Println(user.Name, err)
	// Output: ann <nil>
}

// TestIdentityReturnsStoredIdentity verifies the identity is read from the context.
func TestIdentityReturnsStoredIdentity(t *testing.T) {
	t.Parallel()

	if got, err := request.Identity(authenticated("ann", nil)); err != nil || got.Subject() != "ann" {
		t.Fatalf("got %q, %v", got.Subject(), err)
	}
}

// TestIdentityReturnsErrUnauthenticatedWhenAbsent verifies the anonymous case.
func TestIdentityReturnsErrUnauthenticatedWhenAbsent(t *testing.T) {
	t.Parallel()

	if _, err := request.Identity(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}

// TestUserReturnsStoredUser verifies the user is read from the identity record.
func TestUserReturnsStoredUser(t *testing.T) {
	t.Parallel()

	if got, err := request.User[string](authenticated("1", "ann")); err != nil || got != "ann" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestUserReturnsErrUnauthenticatedWhenAbsent verifies the missing case.
func TestUserReturnsErrUnauthenticatedWhenAbsent(t *testing.T) {
	t.Parallel()

	if _, err := request.User[string](httptest.NewRequest(http.MethodGet, "/", nil)); !errors.As(err, new(problem.Details)) {
		t.Fatalf("err = %v", err)
	}
}

// TestUserFailsClosedOnTypeMismatch verifies a record value of another type, or
// none, is ErrUnauthenticated.
func TestUserFailsClosedOnTypeMismatch(t *testing.T) {
	t.Parallel()

	for _, r := range []*http.Request{authenticated("1", 7), authenticated("1", nil)} {
		if _, err := request.User[string](r); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	}
}
