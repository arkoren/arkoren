package awsqueue

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// TestQueuePoliciesPublishDetachedSettings checks once-only callbacks on one
// draft, replacement order, native-client identity, and required-map ownership.
func TestQueuePoliciesPublishDetachedSettings(t *testing.T) {
	t.Parallel()

	routes := map[string]string{"work": "https://sqs.example/work"}
	client := sqs.NewFromConfig(aws.Config{
		Region: "eu-west-1",
	})
	var retained *Builder
	calls := 0

	driver, err := NewFrom(client, routes, func(consumer *Builder) {
		calls++
		retained = consumer
		consumer.MaxMessages(-1)
	}, func(consumer *Builder) {
		calls++

		if consumer != retained {
			t.Fatal("different drafts")
		}

		consumer.MaxMessages(5)
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.MaxMessages(9)
	routes["work"] = "changed"

	if calls != 2 || driver.maxMessages != 5 || driver.queues["work"] != "https://sqs.example/work" {
		t.Fatal("construction did not publish an independent snapshot")
	}

	if driver.Client() != client {
		t.Fatal("native client was replaced")
	}
}

// TestQueueSettingsReturnReceiver checks every setting without depending on
// optional chained usage in caller code.
func TestQueueSettingsReturnReceiver(t *testing.T) {
	t.Parallel()

	queue := &Builder{}

	if queue.FailureQueueURL("") != queue || queue.WaitTimeSeconds(1) != queue {
		t.Fatal("routing or polling replaced draft")
	}

	if queue.MaxMessages(2) != queue || queue.Logger(nil) != queue {
		t.Fatal("batch or logger replaced draft")
	}

	if queue.VisibilityTimeout(time.Minute) != queue {
		t.Fatal("visibility replaced draft")
	}
}

// TestQueueCallbackOmissionPreservesDefaults compares all zero-policy forms.
func TestQueueCallbackOmissionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)

	for _, callbacks := range [][]func(*Builder){nil, {}, omitted, {func(consumer *Builder) {
		_ = consumer
	}}} {
		config := configured(callbacks...)

		if config.WaitTimeSeconds != 20 || config.MaxMessages != 1 {
			t.Fatalf("defaults changed: %+v", config)
		}
	}
}

// TestQueueNilCallbackPanicsBeforeValidation distinguishes nil policy misuse
// from ordinary missing-client validation.
func TestQueueNilCallbackPanicsBeforeValidation(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = NewFrom(nil, nil, nil)
}

// TestQueuePolicyPanicPropagates verifies that construction does not recover
// application failures or begin credential loading ahead of callbacks.
func TestQueuePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "policy" {
			t.Fatal("callback panic changed")
		}
	}()

	_, _ = New(t.Context(), nil, func(consumer *Builder) {
		consumer.MaxMessages(5)
		panic("policy")
	})
}

// TestQueueNewFromRejectsUnsupportedVisibilityTimeouts covers ticker underflow,
// negative leases, the SQS upper limit, and duration/narrowing overflow inputs.
// No requests are made through the supplied native client.
func TestQueueNewFromRejectsUnsupportedVisibilityTimeouts(t *testing.T) {
	t.Parallel()

	client := sqs.NewFromConfig(aws.Config{Region: "eu-west-1"})

	for _, timeout := range []time.Duration{
		math.MinInt64, -time.Second, -time.Nanosecond, time.Nanosecond,
		time.Second - time.Nanosecond, 12*time.Hour + time.Nanosecond,
		13 * time.Hour, time.Duration(math.MaxInt32) * time.Second,
		(time.Duration(math.MaxInt32) + 1) * time.Second, math.MaxInt64,
	} {
		driver, err := NewFrom(client, map[string]string{"work": "https://sqs.example/work"}, func(consumer *Builder) {
			consumer.VisibilityTimeout(timeout)
		})

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("NewFrom visibility %s: driver = %v, error = %v; want nil, ErrInvalidConfig", timeout, driver, err)
		}
	}
}

// TestQueueNewRejectsUnsupportedVisibilityBeforeAWSConfiguration poisons the
// SDK's environment parsing to prove ordinary validation precedes AWS loading.
// The test is serial because it temporarily changes the process environment.
func TestQueueNewRejectsUnsupportedVisibilityBeforeAWSConfiguration(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "not-an-integer")

	for _, timeout := range []time.Duration{
		math.MinInt64, -time.Second, -time.Nanosecond, time.Nanosecond,
		time.Second - time.Nanosecond, 12*time.Hour + time.Nanosecond,
		13 * time.Hour, time.Duration(math.MaxInt32) * time.Second,
		(time.Duration(math.MaxInt32) + 1) * time.Second, math.MaxInt64,
	} {
		driver, err := New(t.Context(), map[string]string{"work": "https://sqs.example/work"}, func(consumer *Builder) {
			consumer.VisibilityTimeout(timeout)
		})

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New visibility %s: driver = %v, error = %v; want nil, ErrInvalidConfig", timeout, driver, err)
		}
	}
}

// TestQueueConstructorsRejectUnsupportedPollingWaits checks both construction
// paths at the native polling boundaries. Invalid SDK environment parsing must
// not mask the driver's configuration error from New.
func TestQueueConstructorsRejectUnsupportedPollingWaits(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "not-an-integer")
	client := sqs.NewFromConfig(aws.Config{Region: "eu-west-1"})

	for _, seconds := range []int32{math.MinInt32, -1, 21, math.MaxInt32} {
		configure := func(consumer *Builder) {
			consumer.WaitTimeSeconds(seconds)
		}

		driver, err := NewFrom(client, map[string]string{"work": "https://sqs.example/work"}, configure)

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("NewFrom wait %d: driver = %v, error = %v; want nil, ErrInvalidConfig", seconds, driver, err)
		}

		driver, err = New(t.Context(), map[string]string{"work": "https://sqs.example/work"}, configure)

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New wait %d: driver = %v, error = %v; want nil, ErrInvalidConfig", seconds, driver, err)
		}
	}
}

// TestQueueConstructorsRejectUnsupportedBatchSizes verifies one shared native
// batch-size limit for New and NewFrom, before any AWS configuration loading.
func TestQueueConstructorsRejectUnsupportedBatchSizes(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "not-an-integer")
	client := sqs.NewFromConfig(aws.Config{Region: "eu-west-1"})

	for _, count := range []int32{math.MinInt32, -1, 11, math.MaxInt32} {
		configure := func(consumer *Builder) {
			consumer.MaxMessages(count)
		}

		driver, err := NewFrom(client, map[string]string{"work": "https://sqs.example/work"}, configure)

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("NewFrom batch %d: driver = %v, error = %v; want nil, ErrInvalidConfig", count, driver, err)
		}

		driver, err = New(t.Context(), map[string]string{"work": "https://sqs.example/work"}, configure)

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New batch %d: driver = %v, error = %v; want nil, ErrInvalidConfig", count, driver, err)
		}
	}
}

// TestQueueConstructorsRejectIncompleteRoutes keeps required queue-map
// validation ahead of AWS loading for absent, empty, and blank routing entries.
func TestQueueConstructorsRejectIncompleteRoutes(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "not-an-integer")
	client := sqs.NewFromConfig(aws.Config{Region: "eu-west-1"})

	for _, routes := range []map[string]string{
		nil, {}, {"": "https://sqs.example/work"}, {" ": "https://sqs.example/work"},
		{"work": ""}, {"work": " \t"},
	} {
		driver, err := NewFrom(client, routes)

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("NewFrom routes %v: driver = %v, error = %v; want nil, ErrInvalidConfig", routes, driver, err)
		}

		driver, err = New(t.Context(), routes)

		if driver != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New routes %v: driver = %v, error = %v; want nil, ErrInvalidConfig", routes, driver, err)
		}
	}
}

// TestQueueNewFromRejectsNilNativeClient covers the public conversion guard;
// ordinary misconfiguration returns ErrInvalidConfig without a usable driver.
func TestQueueNewFromRejectsNilNativeClient(t *testing.T) {
	t.Parallel()

	driver, err := NewFrom(nil, map[string]string{"work": "https://sqs.example/work"})

	if driver != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewFrom nil client: driver = %v, error = %v; want nil, ErrInvalidConfig", driver, err)
	}
}

// TestQueueNewAcceptsSupportedVisibilityTimeouts exercises successful default,
// whole-second, and fractional settings through AWS configuration loading.
// Missing shared files and disabled instance metadata keep the test offline.
func TestQueueNewAcceptsSupportedVisibilityTimeouts(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/missing")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/missing")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	for _, timeout := range []time.Duration{
		0, time.Second, time.Second + time.Nanosecond, 1500 * time.Millisecond,
		12*time.Hour - time.Nanosecond, 12 * time.Hour,
	} {
		driver, err := New(t.Context(), map[string]string{"work": "https://sqs.example/work"}, func(consumer *Builder) {
			consumer.VisibilityTimeout(timeout)
		})

		if err != nil {
			t.Fatalf("New visibility %s: %v", timeout, err)
		}

		if driver == nil {
			t.Fatalf("New visibility %s returned no driver", timeout)
		}
	}
}
