package cloudflarequeue

import (
	"testing"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
)

// TestPoliciesPublishDetachedSettings verifies same-draft composition, native
// identity, required queue-map copies, and ignored NewFrom connection settings.
func TestPoliciesPublishDetachedSettings(t *testing.T) {
	t.Parallel()

	client := cloudflare.NewClient()
	routes := map[string]string{"work": "original"}
	var retained *Builder
	calls := 0

	driver, err := NewFrom(client, "account", routes, func(consumer *Builder) {
		calls++
		retained = consumer
		consumer.BatchSize(-1)
		consumer.Endpoint(":invalid endpoint ignored")
	}, func(consumer *Builder) {
		calls++

		if consumer != retained {
			t.Fatal("different drafts")
		}

		consumer.BatchSize(5)
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.BatchSize(9)
	routes["work"] = "later"

	if calls != 2 || driver.batchSize != 5 || driver.queueIDs["work"] != "original" {
		t.Fatal("construction did not publish detached settings")
	}

	if driver.Client() != client {
		t.Fatal("native client replaced")
	}
}

// TestSettingsReturnReceiver checks all applicable settings return one draft.
func TestSettingsReturnReceiver(t *testing.T) {
	t.Parallel()

	queue := &Builder{}

	if queue.Endpoint("") != queue || queue.BatchSize(1) != queue {
		t.Fatal("endpoint or batch replaced draft")
	}

	if queue.VisibilityTimeout(time.Minute) != queue || queue.PollInterval(time.Second) != queue {
		t.Fatal("lease or poll delay replaced draft")
	}

	if queue.MaxPollInterval(time.Minute) != queue || queue.Logger(nil) != queue {
		t.Fatal("backoff or logger replaced draft")
	}
}

// TestCallbackOmissionPreservesDefaults covers zero and expanded callback slices.
func TestCallbackOmissionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)

	for _, callbacks := range [][]func(*Builder){nil, {}, omitted, {func(consumer *Builder) {
		_ = consumer
	}}} {
		driver, err := NewFrom(cloudflare.NewClient(), "account", map[string]string{"work": "queue"}, callbacks...)

		if err != nil {
			t.Fatal(err)
		}

		if driver.batchSize != 1 || driver.visibility != 30*time.Second || driver.pollInterval != time.Second {
			t.Fatal("defaults changed")
		}
	}
}

// TestNilPolicyPanicsBeforeClientValidation distinguishes nil policy misuse
// from normal invalid-client errors.
func TestNilPolicyPanicsBeforeClientValidation(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = NewFrom(nil, "", nil, nil)
}

// TestPolicyPanicPropagates verifies application failures are not translated
// into ordinary invalid-token errors.
func TestPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "policy" {
			t.Fatal("callback panic changed")
		}
	}()

	_, _ = New("", "", nil, func(consumer *Builder) {
		consumer.BatchSize(5)
		panic("policy")
	})
}
