package link

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/foundation/key"
)

// TestMiddlewareBindingIsStoredAndRootDetached checks the dedicated middleware
// role without running the request-time derivation at construction.
func TestMiddlewareBindingIsStoredAndRootDetached(t *testing.T) {
	t.Parallel()

	material, err := key.NewShared([]byte("current secret current secret 32"))

	if err != nil {
		t.Fatal(err)
	}

	signer, err := New(material)

	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	bindings := 0
	var retained *MiddlewareBuilder
	middleware := Middleware(signer, func(policy *MiddlewareBuilder) {
		retained = policy

		if policy.BindRequest(func(*http.Request) ([]byte, error) {
			bindings++

			return []byte("account revision"), nil
		}) != policy {
			t.Fatal("setting returned another draft")
		}

		calls++
	}, func(policy *MiddlewareBuilder) {
		if calls != 1 || retained != policy {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		calls++
	})
	retained.BindRequest(func(*http.Request) ([]byte, error) {
		t.Fatal("retained draft changed request binding")

		return nil, nil
	})

	if calls != 2 || bindings != 0 {
		t.Fatal("configuration replayed or runtime binding ran eagerly")
	}

	signed, err := signer.Sign("/reset", Bind([]byte("account revision")))

	if err != nil {
		t.Fatal(err)
	}

	handler := middleware(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	if err := handler(httptest.NewRecorder(), httptest.NewRequest("GET", signed, nil)); err != nil {
		t.Fatal(err)
	}

	if bindings != 1 {
		t.Fatalf("binding calls = %d, want 1 at request time", bindings)
	}
}

// TestMiddlewareOmissionAndNilPolicy keeps nil/empty expansions distinct from a
// supplied nil entry, without changing the preexisting nil-signer panic order.
func TestMiddlewareOmissionAndNilPolicy(t *testing.T) {
	t.Parallel()

	signer, err := New(signingFixture(t))

	if err != nil {
		t.Fatal(err)
	}

	var omitted []func(*MiddlewareBuilder)
	Middleware(signer, omitted...)
	Middleware(signer, []func(*MiddlewareBuilder){}...)
	defer func() {
		if recover() == nil {
			t.Fatal("supplied nil policy was filtered")
		}
	}()
	Middleware(signer, nil)
}

// TestMiddlewareNilSignerPrecedesPolicies preserves required-input validation
// timing: a nil signer is rejected before any construction callback is invoked.
func TestMiddlewareNilSignerPrecedesPolicies(t *testing.T) {
	t.Parallel()

	called := false
	defer func() {
		if value := recover(); value != "link: signer must not be nil" || called {
			t.Fatalf("panic = %v, callback ran = %v", value, called)
		}
	}()
	Middleware(nil, func(policy *MiddlewareBuilder) {
		called = true
	})
}
