package delivery

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"arkoren.dev/core/service/queue"
)

// newDelivery retains the pre-extraction fixture shape for the ownership test.
// State capture uses only the envelope; native receipt routing stays in awsqueue.
func newDelivery(_ any, _ string, _ types.Message, message queue.Message) *State {
	return New(message, nil)
}

// TestDeliveryMessageOwnsMetadata verifies that mutating a published snapshot
// cannot alter the delivery or a later snapshot of the same message.
func TestDeliveryMessageOwnsMetadata(t *testing.T) {
	t.Parallel()

	delivery := newDelivery(nil, "queue-url", types.Message{ReceiptHandle: aws.String("receipt")}, queue.Message{
		Metadata: map[string]string{"traceparent": "original"},
	})

	first := delivery.Message()
	first.Metadata["traceparent"] = "changed"

	if got := delivery.Message().Metadata["traceparent"]; got != "original" {
		t.Fatalf("later snapshot metadata = %q, want original", got)
	}

	if got := delivery.message.Metadata["traceparent"]; got != "original" {
		t.Fatalf("delivery metadata = %q, want original", got)
	}
}
