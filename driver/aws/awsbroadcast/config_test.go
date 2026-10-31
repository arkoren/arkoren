package awsbroadcast

import (
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// TestSQSPoliciesCopyRoutingAtCaptureAndPublication checks both ownership
// boundaries and ordered once-only composition on a shared draft.
func TestSQSPoliciesCopyRoutingAtCaptureAndPublication(t *testing.T) {
	t.Parallel()

	routes := map[string]string{"users.created": "original"}
	var retained *SQSBuilder
	calls := 0
	client := sqs.NewFromConfig(aws.Config{
		Region: "eu-west-1",
	})

	driver, err := NewSQSFrom(client, func(consumer *SQSBuilder) {
		calls++
		retained = consumer
		consumer.Queues(routes)
		routes["users.created"] = "changed during construction"
		consumer.MaxMessages(-1)
	}, func(consumer *SQSBuilder) {
		calls++

		if consumer != retained {
			t.Fatal("different drafts")
		}

		consumer.MaxMessages(5)
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Queues(map[string]string{"users.created": "later"})
	retained.MaxMessages(9)

	if calls != 2 || driver.queues["users.created"] != "original" || driver.maxMessages != 5 {
		t.Fatalf("calls=%d routes=%v batch=%d", calls, driver.queues, driver.maxMessages)
	}

	if driver.Client() != client {
		t.Fatal("native client was replaced")
	}
}

// TestSQSSettingsReturnReceiver checks the entire restricted method surface.
func TestSQSSettingsReturnReceiver(t *testing.T) {
	t.Parallel()

	consumer := &SQSBuilder{}

	if consumer.Queues(nil) != consumer || consumer.QueueURL("queue") != consumer {
		t.Fatal("routing method replaced draft")
	}

	if consumer.WaitTimeSeconds(1) != consumer || consumer.MaxMessages(2) != consumer {
		t.Fatal("polling method replaced draft")
	}

	if consumer.Logger(nil) != consumer {
		t.Fatal("Logger replaced draft")
	}
}

// TestSQSCallbackOmissionPreservesDefaults covers zero, empty, and expanded nil
// callbacks without invoking AWS credential loading or making requests.
func TestSQSCallbackOmissionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*SQSBuilder)

	for _, callbacks := range [][]func(*SQSBuilder){nil, {}, omitted, {func(consumer *SQSBuilder) {
		_ = consumer
	}}} {
		config := configuredSQS(callbacks...)
		config.QueueURL = "queue"

		driver, err := newSQSFrom(new(fakeSQSClient), config)

		if err != nil {
			t.Fatal(err)
		}

		if driver.waitTime != 20 || driver.maxMessages != 1 || driver.logger != slog.Default() {
			t.Fatal("defaults changed")
		}
	}
}

// TestSQSNilCallbackPanicsBeforeValidation distinguishes nil entry misuse from
// an invalid required native client.
func TestSQSNilCallbackPanicsBeforeValidation(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = NewSQSFrom(nil, nil)
}

// TestSQSPolicyPanicPropagates proves policies run before credential loading.
func TestSQSPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "policy" {
			t.Fatal("callback panic changed")
		}
	}()

	_, _ = NewSQS(t.Context(), func(consumer *SQSBuilder) {
		consumer.QueueURL("queue")
		panic("policy")
	})
}
