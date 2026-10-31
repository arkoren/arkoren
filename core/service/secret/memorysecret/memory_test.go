package memorysecret_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
	"arkoren.dev/core/service/secret/secrettest"
)

// TestConformance runs the secret conformance suite against the memory driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	secrettest.Run(t, func(*testing.T) secret.Driver {
		return memorysecret.New()
	})
}

// TestValuesCopiesMapAndEachByteSlice verifies capture precedes subsequent
// callbacks and the seeded driver detaches the completed retained draft.
func TestValuesCopiesMapAndEachByteSlice(t *testing.T) {
	t.Parallel()

	input := map[string][]byte{"database": []byte("original")}
	var retained *memorysecret.Builder
	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		retained = secrets
		secrets.Values(input)

		input["database"][0] = 'X'
		input["later"] = []byte("not-seeded")
	})
	retained.Values(map[string][]byte{"database": []byte("draft-change")})

	value, err := driver.Get(t.Context(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	if string(value.Data) != "original" {
		t.Fatalf("seed = %q", value.Data)
	}

	value.Data[0] = 'Y'

	again, err := driver.Get(t.Context(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	if string(again.Data) != "original" {
		t.Fatal("returned bytes changed retained secret")
	}

	if _, err := driver.Get(t.Context(), secret.Reference{Name: "later"}); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("later map edit was seeded: %v", err)
	}
}

// TestNewCopiesInitialSecretValues verifies that New copies supplied data.
func TestNewCopiesInitialSecretValues(t *testing.T) {
	initial := []byte("original")
	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": initial})
	})

	initial[0] = 'x'

	value, err := driver.Get(context.Background(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	if string(value.Data) != "original" || value.Version != "1" {
		t.Fatalf("value = %#v", value)
	}
}

// TestGetReturnsSecretValueCopy verifies that Get does not expose stored data.
func TestGetReturnsSecretValueCopy(t *testing.T) {
	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": []byte("original")})
	})

	value, err := driver.Get(context.Background(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	value.Data[0] = 'x'

	second, err := driver.Get(context.Background(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	if string(second.Data) != "original" {
		t.Fatalf("second = %#v", second)
	}
}

// TestGetReadsSpecificVersion verifies that Get selects a requested version.
func TestGetReadsSpecificVersion(t *testing.T) {
	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": []byte("first")})
	})

	second, err := driver.Put(context.Background(), "database", []byte("second"))

	if err != nil {
		t.Fatal(err)
	}

	value, err := driver.Get(context.Background(), secret.Reference{Name: "database", Version: "1"})

	assertSpecificSecretVersion(t, value, second, err)
}

// assertSpecificSecretVersion verifies a specific version returns the first value.
func assertSpecificSecretVersion(t *testing.T, value, second secret.Value, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("value = %#v, second = %#v, err = %v", value, second, err)
	}

	if string(value.Data) != "first" || second.Version != "2" {
		t.Fatalf("value = %#v, second = %#v, err = %v", value, second, err)
	}
}

// TestUpdateRequiresExistingSecret verifies that Update rejects a missing secret.
func TestUpdateRequiresExistingSecret(t *testing.T) {
	driver := memorysecret.New()

	_, err := driver.Update(context.Background(), "missing", []byte("value"))

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestDeleteRemovesAllVersions verifies that Delete removes each secret version.
func TestDeleteRemovesAllVersions(t *testing.T) {
	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": []byte("first")})
	})

	_, err := driver.Put(context.Background(), "database", []byte("second"))

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Delete(context.Background(), "database"); err != nil {
		t.Fatal(err)
	}

	_, err = driver.Get(context.Background(), secret.Reference{Name: "database", Version: "1"})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestRecreatedSecretReusesVersionSelector characterizes the memory driver's
// existing lifecycle: deletion removes one secret, and a new secret at that
// name can reuse a version string. An old selector then selects the new data.
// This characterizes memory behavior, not a portable conformance requirement.
func TestRecreatedSecretReusesVersionSelector(t *testing.T) {
	t.Parallel()

	driver := memorysecret.New()

	first, err := driver.Put(t.Context(), "credential", []byte("first-fixture"))

	if err != nil {
		t.Fatal(err)
	}

	reference := secret.Reference{Name: "credential", Version: first.Version}

	if err := driver.Delete(t.Context(), "credential"); err != nil {
		t.Fatal(err)
	}

	recreated, err := driver.Put(t.Context(), "credential", []byte("replacement-fixture"))

	if err != nil {
		t.Fatal(err)
	}

	if recreated.Version != reference.Version {
		t.Fatal("memory recreation did not reuse the original version selector")
	}

	selected, err := driver.Get(t.Context(), reference)

	if err != nil {
		t.Fatal(err)
	}

	if string(selected.Data) != "replacement-fixture" {
		t.Fatal("old selector did not resolve to the recreated secret's payload")
	}
}

// TestGetCanceledContextReturnsContextError verifies that Get observes cancellation.
func TestGetCanceledContextReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": []byte("value")})
	})

	_, err := driver.Get(ctx, secret.Reference{Name: "database"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

// TestSetCanceledContextDoesNotMutate verifies that canceled Put calls do not write.
func TestSetCanceledContextDoesNotMutate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	driver := memorysecret.New()

	_, err := driver.Put(ctx, "database", []byte("value"))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	_, err = driver.Get(context.Background(), secret.Reference{Name: "database"})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestUpdateCanceledContextDoesNotMutate verifies that canceled Update calls do not write.
func TestUpdateCanceledContextDoesNotMutate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": []byte("first")})
	})

	_, err := driver.Update(ctx, "database", []byte("second"))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	value, err := driver.Get(context.Background(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	if string(value.Data) != "first" || value.Version != "1" {
		t.Fatalf("value = %#v", value)
	}
}

// TestDeleteCanceledContextDoesNotMutate verifies that canceled Delete calls do not delete.
func TestDeleteCanceledContextDoesNotMutate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": []byte("value")})
	})

	err := driver.Delete(ctx, "database")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	value, err := driver.Get(context.Background(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	if string(value.Data) != "value" || value.Version != "1" {
		t.Fatalf("value = %#v", value)
	}
}

// TestBuilderSeedsValues verifies that a construction policy seeds version one
// without changing the driver's reference or version semantics.
func TestBuilderSeedsValues(t *testing.T) {
	t.Parallel()

	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"database": []byte("value")})
	})

	value, err := driver.Get(context.Background(), secret.Reference{Name: "database"})

	if err != nil {
		t.Fatal(err)
	}

	if string(value.Data) != "value" || value.Version != "1" {
		t.Fatalf("value = %#v", value)
	}
}

// TestMemorySecretPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestMemorySecretPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *memorysecret.Builder
	calls := 0
	component := memorysecret.New(func(secrets *memorysecret.Builder) {
		calls++
		retained = secrets
	}, func(secrets *memorysecret.Builder) {
		if calls != 1 || retained != secrets {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	memorysecret.New(func(secrets *memorysecret.Builder) {
		if retained == secrets {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestMemorySecretNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestMemorySecretNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*memorysecret.Builder)
	empty := []func(*memorysecret.Builder){}

	if memorysecret.New(absent...) == nil || memorysecret.New(empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestMemorySecretNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestMemorySecretNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		memorysecret.New(nil, func(secrets *memorysecret.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestMemorySecretPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestMemorySecretPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		memorysecret.New(func(secrets *memorysecret.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMemorySecretValuesReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMemorySecretValuesReturnsDraft(t *testing.T) {
	t.Parallel()

	secrets := &memorysecret.Builder{}

	if secrets.Values(nil) != secrets {
		t.Fatal("setting returned a different draft")
	}
}
