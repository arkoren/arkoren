package gcpsecret

import "testing"

// TestConstructionPoliciesPublishIndependentSettings checks NewFrom publication
// and that each policy receives the same draft exactly once in order.
func TestConstructionPoliciesPublishIndependentSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	driver := NewFrom(nil, func(secrets *Builder) {
		calls++
		retained = secrets

		if secrets.Project("old") != secrets {
			t.Fatal("Project returned another draft")
		}
	}, func(secrets *Builder) {
		calls++

		if secrets != retained {
			t.Fatal("callbacks received different drafts")
		}

		secrets.Project("new")
	})

	retained.Project("later")

	if calls != 2 || driver.project != "new" {
		t.Fatalf("calls = %d, project = %q", calls, driver.project)
	}
}

// TestConstructionNilPolicyPanics distinguishes supplied nil from omission.
func TestConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	NewFrom(nil, nil)
}

// TestCallbackOmissionPreservesDefaults checks zero, empty, and expanded nil
// callbacks without creating a gRPC client or obtaining credentials.
func TestCallbackOmissionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)

	for _, callbacks := range [][]func(*Builder){nil, {}, omitted, {func(secrets *Builder) {
		_ = secrets
	}}} {
		if NewFrom(nil, callbacks...).project != "" {
			t.Fatal("default project changed")
		}
	}
}

// TestNewPolicyPanicPropagates proves callbacks precede native client creation
// and no recovery translates application failures into credential errors.
func TestNewPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "policy" {
			t.Fatal("callback panic changed")
		}
	}()

	_, _ = New(t.Context(), func(secrets *Builder) {
		secrets.Project("example")
		panic("policy")
	})
}
