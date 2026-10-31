package awsmail

import "testing"

// TestMailPoliciesPublishIndependentSettings checks ordered direct policies and
// receiver identity without touching credentials or the network.
func TestMailPoliciesPublishIndependentSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	config := configured(func(mailer *Builder) {
		calls++
		retained = mailer

		if mailer.Region("old") != mailer {
			t.Fatal("Region returned another draft")
		}
	}, func(mailer *Builder) {
		calls++

		if mailer != retained {
			t.Fatal("different drafts")
		}

		mailer.Region("new")
	})

	retained.Region("later")

	if calls != 2 || config.Region != "new" {
		t.Fatalf("calls=%d region=%q", calls, config.Region)
	}
}

// TestMailCallbackOmissionPreservesDefaults covers zero and expanded slices.
func TestMailCallbackOmissionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)

	for _, callbacks := range [][]func(*Builder){nil, {}, omitted, {func(mailer *Builder) {
		_ = mailer
	}}} {
		if configured(callbacks...).Region != "" {
			t.Fatal("default region changed")
		}
	}
}

// TestMailNilPolicyPanics verifies callbacks run before credential loading.
func TestMailNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New(t.Context(), nil)
}

// TestMailPolicyPanicPropagates keeps application panics distinct from AWS
// configuration errors without loading credentials.
func TestMailPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "policy" {
			t.Fatal("callback panic changed")
		}
	}()

	_, _ = New(t.Context(), func(mailer *Builder) {
		mailer.Region("eu-west-1")
		panic("policy")
	})
}
