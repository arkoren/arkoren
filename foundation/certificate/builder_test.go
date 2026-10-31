package certificate_test

import (
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/foundation/certificate"
)

// TestRootCallbacksOrderedOnce uses two policies to check callback ordering and
// final validity, without relying on callback replay through another constructor.
func TestRootCallbacksOrderedOnce(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var calls []int
		root, err := certificate.GenerateRoot("Example development",
			func(authority *certificate.RootBuilder) {
				calls = append(calls, 1)
				authority.Validity(time.Hour)
			},
			func(authority *certificate.RootBuilder) {
				calls = append(calls, 2)
				authority.Validity(2 * time.Hour)
			})

		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(calls, []int{1, 2}) {
			t.Fatalf("callback calls: %v", calls)
		}

		if !root.Bundle().Certificates()[0].NotAfter.Equal(time.Now().Add(2 * time.Hour)) {
			t.Fatal("last callback did not win")
		}
	})
}

// TestServerCallbacksOrderedOnce checks shared-draft edits and their order
// before the server identity is published.
func TestServerCallbacksOrderedOnce(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		root, err := certificate.GenerateRoot("Example development")

		if err != nil {
			t.Fatal(err)
		}

		var calls []int
		leaf, err := certificate.GenerateServer(root, []string{"localhost"},
			func(leaf *certificate.ServerBuilder) {
				calls = append(calls, 1)
				leaf.Validity(time.Hour)
			},
			func(leaf *certificate.ServerBuilder) {
				calls = append(calls, 2)
				leaf.Validity(2 * time.Hour)
			})

		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(calls, []int{1, 2}) {
			t.Fatalf("callback calls: %v", calls)
		}

		if !leaf.Bundle().Certificates()[0].NotAfter.Equal(time.Now().Add(2 * time.Hour)) {
			t.Fatal("last callback did not win")
		}
	})
}

// TestRootCallbacksRunOnInvalidName preserves construction callback semantics
// even when generation will fail validation of a required positional input.
func TestRootCallbacksRunOnInvalidName(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := certificate.GenerateRoot("", func(authority *certificate.RootBuilder) {
		calls++
		authority.Validity(time.Hour)
	})

	if !errors.Is(err, certificate.ErrInvalid) || calls != 1 {
		t.Fatalf("invalid-name callbacks: calls=%d err=%v", calls, err)
	}
}

// TestServerCallbacksRunOnInvalidIssuer verifies that callback invocation does
// not depend on issuer or host validation succeeding first.
func TestServerCallbacksRunOnInvalidIssuer(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := certificate.GenerateServer(certificate.Identity{}, nil, func(leaf *certificate.ServerBuilder) {
		calls++
		leaf.Validity(time.Hour)
	})

	if !errors.Is(err, certificate.ErrInvalid) || calls != 1 {
		t.Fatalf("invalid-issuer callbacks: calls=%d err=%v", calls, err)
	}
}

// TestRootRetainedDraftDetached checks that mutating a captured construction
// draft does not change the completed identity or its validity period.
func TestRootRetainedDraftDetached(t *testing.T) {
	t.Parallel()

	var retained *certificate.RootBuilder
	root, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
		retained = authority.Validity(time.Hour)
	})

	if err != nil {
		t.Fatal(err)
	}

	want := root.Bundle().Certificates()[0].NotAfter

	retained.Validity(2 * time.Hour)

	if !root.Bundle().Certificates()[0].NotAfter.Equal(want) {
		t.Fatal("retained root draft changed identity")
	}
}

// TestServerRetainedDraftDetached keeps both the draft and input hosts after
// generation and confirms that later edits cannot modify the publication.
func TestServerRetainedDraftDetached(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	var retained *certificate.ServerBuilder
	hosts := []string{"localhost"}
	leaf, err := certificate.GenerateServer(root, hosts, func(leaf *certificate.ServerBuilder) {
		retained = leaf.Validity(time.Hour)
	})

	if err != nil {
		t.Fatal(err)
	}

	want := leaf.Bundle().Certificates()[0].NotAfter

	retained.Validity(2 * time.Hour)
	hosts[0] = "mutated.test"

	cert := leaf.Bundle().Certificates()[0]

	if !cert.NotAfter.Equal(want) || cert.DNSNames[0] != "localhost" {
		t.Fatal("retained inputs changed server identity")
	}
}

// TestRootInvalidValiditySticky rejects a negative edit even if a later edit
// would be valid, preserving the first invalid construction setting.
func TestRootInvalidValiditySticky(t *testing.T) {
	t.Parallel()

	_, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
		authority.Validity(-time.Hour).Validity(time.Hour)
	})

	if !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("sticky root validity: %v", err)
	}
}

// TestServerInvalidValiditySticky rejects invalid draft edits consistently with
// root construction, rather than using only the final duration value.
func TestServerInvalidValiditySticky(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	_, err = certificate.GenerateServer(root, []string{"localhost"}, func(leaf *certificate.ServerBuilder) {
		leaf.Validity(-time.Hour).Validity(time.Hour)
	})

	if !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("sticky server validity: %v", err)
	}
}

// TestRootSubsecondValidity rejects a lifetime too short for whole-second
// X.509 encoding instead of generating a certificate with no future lifetime.
func TestRootSubsecondValidity(t *testing.T) {
	t.Parallel()

	_, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
		authority.Validity(time.Millisecond)
	})

	if !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("subsecond root validity: %v", err)
	}
}

// TestServerSubsecondValidity rejects an unrepresentable leaf lifetime before
// randomness or signing is used.
func TestServerSubsecondValidity(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	_, err = certificate.GenerateServer(root, []string{"localhost"}, func(leaf *certificate.ServerBuilder) {
		leaf.Validity(time.Millisecond)
	})

	if !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("subsecond leaf validity: %v", err)
	}
}

// TestRootZeroValidityDefault checks the zero-duration convention rather than
// treating zero as an explicitly expired certificate.
func TestRootZeroValidityDefault(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		root, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
			authority.Validity(0)
		})

		if err != nil {
			t.Fatal(err)
		}

		if !root.Bundle().Certificates()[0].NotAfter.Equal(time.Now().Add(5 * 365 * 24 * time.Hour)) {
			t.Fatal("zero did not select root default")
		}
	})
}

// TestRootNilCallbackPanics preserves nil callback misuse instead of silently
// skipping a requested construction step.
func TestRootNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil root callback did not panic")
		}
	}()

	_, _ = certificate.GenerateRoot("Example development", nil)
}

// TestServerNilCallbackPanics checks nil callback misuse even when other
// required inputs are invalid.
func TestServerNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil server callback did not panic")
		}
	}()

	_, _ = certificate.GenerateServer(certificate.Identity{}, nil, nil)
}

// TestRootEmptyCallbackSlice checks alias identity with ordinary function slices
// and confirms that an expanded nil slice is not a supplied nil entry.
func TestRootEmptyCallbackSlice(t *testing.T) {
	t.Parallel()

	var callbacks []func(*certificate.RootBuilder)
	root, err := certificate.GenerateRoot("Example development", callbacks...)

	if err != nil {
		t.Fatal(err)
	}

	if err := root.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestServerZeroValidityDefault checks both alias slice compatibility and the
// explicit-zero default policy at a deterministic generation time.
func TestServerZeroValidityDefault(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		root, err := certificate.GenerateRoot("Example development")

		if err != nil {
			t.Fatal(err)
		}

		callbacks := []func(*certificate.ServerBuilder){
			func(leaf *certificate.ServerBuilder) {
				leaf.Validity(0)
			},
		}

		leaf, err := certificate.GenerateServer(root, []string{"localhost"}, callbacks...)

		if err != nil {
			t.Fatal(err)
		}

		if !leaf.Bundle().Certificates()[0].NotAfter.Equal(time.Now().Add(90 * 24 * time.Hour)) {
			t.Fatal("zero did not select server default")
		}
	})
}

// TestRootCallbacksContinueAfterInvalidEdit ensures all policies still execute
// once when the draft records an error before later callbacks run.
func TestRootCallbacksContinueAfterInvalidEdit(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := certificate.GenerateRoot("Example development",
		func(authority *certificate.RootBuilder) {
			calls++
			authority.Validity(-time.Hour)
		},
		func(authority *certificate.RootBuilder) {
			calls++
			authority.Validity(time.Hour)
		})

	if !errors.Is(err, certificate.ErrInvalid) || calls != 2 {
		t.Fatalf("invalid draft callbacks: calls=%d err=%v", calls, err)
	}
}
