package awsstorage

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestConstructionPoliciesPublishIndependentSettings checks ordered callbacks
// and copying of completed storage settings without cloning credentials.
func TestConstructionPoliciesPublishIndependentSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	config := configured(func(store *Builder) {
		calls++
		retained = store

		if store.Region("old") != store {
			t.Fatal("Region returned another draft")
		}
	}, func(store *Builder) {
		calls++

		if store != retained {
			t.Fatal("callbacks received different drafts")
		}

		store.Region("new")
	})

	retained.Region("later")

	if calls != 2 || config.Region != "new" {
		t.Fatalf("calls = %d, region = %q", calls, config.Region)
	}
}

// TestConstructionNilPolicyPanics verifies direct invocation before validation.
func TestConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = NewFrom(nil, "", nil)
}

// TestNewDoesNotReplayPolicies checks New's private completed-settings path
// using explicit local-only credentials without sending any request.
func TestNewDoesNotReplayPolicies(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *Builder
	provider := credentials.NewStaticCredentialsProvider("test", "test", "")

	driver, err := New(t.Context(), "bucket", func(store *Builder) {
		calls++
		retained = store
		store.Region("eu-west-1")
		store.Credentials(provider)
		store.Prefix("tenant-a")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Prefix("later")

	if calls != 1 || driver.prefix != "tenant-a/" || driver.Client() == nil {
		t.Fatalf("calls=%d prefix=%q native=%v", calls, driver.prefix, driver.Client() != nil)
	}
}

// TestStorageSettingsReturnReceiverAndRetainNativeProvider checks the complete
// setting surface without recursively cloning credential providers.
func TestStorageSettingsReturnReceiverAndRetainNativeProvider(t *testing.T) {
	t.Parallel()

	storage := &Builder{}
	provider := aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", ""))

	if storage.Region("") != storage || storage.Endpoint("") != storage {
		t.Fatal("connection settings replaced draft")
	}

	if storage.Prefix("") != storage || storage.Credentials(provider) != storage {
		t.Fatal("storage settings replaced draft")
	}

	if storage.config.Credentials != provider {
		t.Fatal("credential provider replaced")
	}
}

// TestStorageCallbackOmissionPreservesDefaults checks all zero-policy forms.
func TestStorageCallbackOmissionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)

	for _, callbacks := range [][]func(*Builder){nil, {}, omitted, {func(store *Builder) {
		_ = store
	}}} {
		config := configured(callbacks...)

		if config.Prefix != "" || config.Region != "" || config.Credentials != nil {
			t.Fatal("defaults changed")
		}
	}
}

// TestStoragePolicyPanicPropagates runs before bucket validation or credentials.
func TestStoragePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "policy" {
			t.Fatal("callback panic changed")
		}
	}()

	_, _ = New(t.Context(), "", func(store *Builder) {
		store.Prefix("tenant")
		panic("policy")
	})
}

// TestNewFromAppliesPrefixOnly verifies native-client identity and that connection
// settings are not transferred to or validated against a supplied S3 client.
func TestNewFromAppliesPrefixOnly(t *testing.T) {
	t.Parallel()

	client := s3.NewFromConfig(aws.Config{
		Region: "original",
	})
	var retained *Builder
	calls := 0

	driver, err := NewFrom(client, "bucket", func(store *Builder) {
		calls++
		retained = store
		store.Region("ignored")
		store.Endpoint(":invalid endpoint ignored")
		store.Credentials(nil)
		store.Prefix("tenant-a")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Prefix("later")

	if calls != 1 || driver.prefix != "tenant-a/" || driver.Client() != client {
		t.Fatal("NewFrom did not publish detached settings around the original client")
	}

	if client.Options().Region != "original" || client.Options().BaseEndpoint != nil {
		t.Fatal("native client connection settings changed")
	}
}
