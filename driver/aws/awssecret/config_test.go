package awssecret

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// TestConstructionPoliciesPublishIndependentSettings checks ordered direct
// callbacks, receiver identity, and detachment from a retained draft.
func TestConstructionPoliciesPublishIndependentSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	config := configured(func(secrets *Builder) {
		calls++
		retained = secrets

		if secrets.Region("old") != secrets {
			t.Fatal("Region returned another draft")
		}
	}, func(secrets *Builder) {
		calls++

		if secrets != retained {
			t.Fatal("callbacks received different drafts")
		}

		secrets.Region("new")
	})

	retained.Region("later")

	if calls != 2 || config.Region != "new" {
		t.Fatalf("calls = %d, region = %q", calls, config.Region)
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

	_, _ = New(t.Context(), nil)
}

// TestSecretSettingsReturnReceiverAndRetainNativeProvider checks the complete
// setting surface while preserving native provider ownership.
func TestSecretSettingsReturnReceiverAndRetainNativeProvider(t *testing.T) {
	t.Parallel()

	secret := &Builder{}
	provider := aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", ""))

	if secret.Region("") != secret || secret.Endpoint("") != secret {
		t.Fatal("connection settings replaced draft")
	}

	if secret.Name("application") != secret || secret.Credentials(provider) != secret {
		t.Fatal("secret settings replaced draft")
	}

	if secret.config.Credentials != provider {
		t.Fatal("credential provider replaced")
	}
}

// TestSecretCallbackOmissionPreservesDefaults checks all zero-policy forms.
func TestSecretCallbackOmissionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)

	for _, callbacks := range [][]func(*Builder){nil, {}, omitted, {func(secrets *Builder) {
		_ = secrets
	}}} {
		config := configured(callbacks...)

		if config.Name != "" || config.Region != "" || config.Credentials != nil {
			t.Fatal("defaults changed")
		}
	}
}

// TestSecretPolicyPanicPropagates verifies that application panics occur before
// credential loading and are not converted into AWS configuration errors.
func TestSecretPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "policy" {
			t.Fatal("callback panic changed")
		}
	}()

	_, _ = New(t.Context(), func(secrets *Builder) {
		secrets.Region("eu-west-1")
		panic("policy")
	})
}
