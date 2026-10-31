package openaiwebhook

import (
	"encoding/json/v2"
	"errors"

	"arkoren.dev/core/http/webhook"
)

// Response is a background response lifecycle notification, not response output.
// The event key in Delivery.Type distinguishes completion, failure, cancellation
// and interruption. Fetching output or failure details requires application I/O.
//
// Example:
//
//	accept := func(_ context.Context, delivery webhook.Delivery[openaiwebhook.Response]) error {
//		_ = delivery.Payload.ID // Pass this reference to the Responses API in a worker.
//		return nil
//	}
//	_ = accept
type Response struct {
	// ID references the model response, never the event or webhook delivery.
	// Example: _ = openaiwebhook.Response{ID: "resp_example"}
	ID string `json:"id"`
	// CreatedAt is the lifecycle event time in Unix seconds, not receipt time.
	// Example: _ = openaiwebhook.Response{CreatedAt: 1719168000}
	CreatedAt int64 `json:"created_at"`
}

// ResponseCompleted identifies a completed background response. Its decoder
// requires data.id and a positive created_at but performs no response retrieval.
//
// Example:
//
//	receiver.Handle(openaiwebhook.ResponseCompleted, accept)
var ResponseCompleted = webhook.NewEvent("response.completed", decodeResponse)

// ResponseFailed identifies a failed background response. The payload references
// the response; it does not contain a failure reason or generated contents.
//
// Example:
//
//	receiver.Handle(openaiwebhook.ResponseFailed, accept)
var ResponseFailed = webhook.NewEvent("response.failed", decodeResponse)

// ResponseCanceled identifies a canceled background response. The native key
// retains OpenAI's spelling, response.cancelled; decoding performs no I/O.
//
// Example:
//
//	receiver.Handle(openaiwebhook.ResponseCanceled, accept)
var ResponseCanceled = webhook.NewEvent("response.cancelled", decodeResponse)

// ResponseIncomplete identifies an interrupted background response. Inspect the
// response separately for incomplete details; this notification only references it.
//
// Example:
//
//	receiver.Handle(openaiwebhook.ResponseIncomplete, accept)
var ResponseIncomplete = webhook.NewEvent("response.incomplete", decodeResponse)

// errResponse marks an invalid bound response reference without exposing JSON.
var errResponse = errors.New("openai webhook: invalid response notification")

// decodeResponse validates the common thin response data shape. Metadata is
// copied from the authenticated envelope; no SDK calls or content hydration occur.
func decodeResponse(envelope Envelope) (Response, error) {
	// reference mirrors only data.id; lifecycle time belongs to the envelope,
	// never to similarly named future fields inside event-specific data.
	var reference struct {
		// ID is the response reference supplied in the thin notification.
		ID string `json:"id"`
	}

	if err := json.Unmarshal(envelope.Data, &reference); err != nil {
		return Response{}, errResponse
	}

	if reference.ID == "" || envelope.CreatedAt <= 0 {
		return Response{}, errResponse
	}

	return Response{ID: reference.ID, CreatedAt: envelope.CreatedAt}, nil
}
