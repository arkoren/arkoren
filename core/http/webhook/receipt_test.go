package webhook_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/middleware/errormap"
	"arkoren.dev/core/http/middleware/requestlog"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
	"arkoren.dev/foundation/problem"
)

// errAppCause is an application error an app-wide mapper may target.
var errAppCause = errors.New("not found secret-cause")

// TestReceiptRejectionMatchesClassAndCause checks a rejection error matches
// ErrRejected and the verifier's cause, but not the unavailable class.
func TestReceiptRejectionMatchesClassAndCause(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, failingSource(errBadSignature), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Reject(http.StatusUnauthorized, errBadSignature)
	}))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))

	if !errors.Is(err, webhook.ErrRejected) || !errors.Is(err, errBadSignature) || errors.Is(err, webhook.ErrUnavailable) {
		t.Fatalf("rejection error = %v", err)
	}
}

// TestReceiptUnavailableMatchesClassAndCause checks an unavailable error
// matches ErrUnavailable and the verifier's cause, but not the rejected class.
func TestReceiptUnavailableMatchesClassAndCause(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, failingSource(errAppCause), bindEcho(&recorder{}))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))

	if !errors.Is(err, webhook.ErrUnavailable) || !errors.Is(err, errAppCause) || errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("unavailable error = %v", err)
	}
}

// TestReceiptResponseSurvivesAppWideErrorMap checks that mappers matching the
// app cause or the class sentinel cannot change the committed response.
func TestReceiptResponseSurvivesAppWideErrorMap(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, failingSource(errAppCause), bindEcho(&recorder{}))
	mapped := errormap.Middleware(
		errormap.Map(errAppCause, problem.NotFound()),
		errormap.Map(webhook.ErrUnavailable, problem.Conflict()),
	)(handler)

	response := mapped.Record(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
	body := readBody(t, response)

	if response.StatusCode != http.StatusServiceUnavailable || strings.Contains(body, "secret-cause") {
		t.Fatalf("status = %d body = %q, want unchanged 503 without the cause", response.StatusCode, body)
	}
}

// TestReceiptRejectionSurvivesErrorMapOfClass checks a mapped rejection class.
func TestReceiptRejectionSurvivesErrorMapOfClass(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, failingSource(errBadSignature), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Reject(http.StatusUnauthorized, errBadSignature)
	}))

	mapped := errormap.Middleware(errormap.Map(webhook.ErrRejected, problem.Conflict()))(handler)

	response := mapped.Record(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))

	if response.StatusCode != http.StatusUnauthorized || strings.Contains(readBody(t, response), "secret-cause") {
		t.Fatalf("status = %d, want unchanged 401 without the cause", response.StatusCode)
	}
}

// TestOversizedReceiptSurvivesNativeErrorMap checks the original 413 problem
// is committed before a mapper matching request.ErrBodyTooLarge can replace it.
func TestOversizedReceiptSurvivesNativeErrorMap(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.MaxBodySize(4)
	}))

	mapped := errormap.Middleware(errormap.Map(request.ErrBodyTooLarge, problem.NotFound()))(handler)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("oversized secret-payload"))

	response := mapped.Record(req)
	expected := httptest.NewRecorder()

	request.ErrBodyTooLarge.ServeHTTP(expected, req)

	body := readBody(t, response)

	if response.StatusCode != http.StatusRequestEntityTooLarge || body != expected.Body.String() {
		t.Fatalf("status = %d body = %q, want original 413 problem", response.StatusCode, body)
	}

	if strings.Contains(body, "secret-payload") {
		t.Fatal("response leaked the payload")
	}
}

// TestOversizedReceiptSurvivesClassErrorMap checks an application mapping the
// rejection class cannot change the native 413 problem representation either.
func TestOversizedReceiptSurvivesClassErrorMap(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.MaxBodySize(4)
	}))

	mapped := errormap.Middleware(errormap.Map(webhook.ErrRejected, problem.Conflict()))(handler)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("oversized"))

	response := mapped.Record(req)
	expected := httptest.NewRecorder()

	request.ErrBodyTooLarge.ServeHTTP(expected, req)

	if response.StatusCode != http.StatusRequestEntityTooLarge || readBody(t, response) != expected.Body.String() {
		t.Fatalf("status = %d, want original 413 problem", response.StatusCode)
	}
}

// TestOversizedReceiptKeepsClassCauseAndDiagnostics checks a body-limit
// rejection retains both error identities and publishes its safe category.
func TestOversizedReceiptKeepsClassCauseAndDiagnostics(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var rejected webhook.RejectedEvent

	if _, err := bus.Listen(webhook.Rejected, func(_ context.Context, payload webhook.RejectedEvent) error {
		rejected = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.MaxBodySize(4)
		receiver.Events(bus)
		receiver.Label("orders")
	}))

	writer := httptest.NewRecorder()

	err := handler(writer, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("oversized")))

	if !errors.Is(err, webhook.ErrRejected) || !errors.Is(err, request.ErrBodyTooLarge) {
		t.Fatalf("error = %v, want ErrRejected and request.ErrBodyTooLarge", err)
	}

	if writer.Code != http.StatusRequestEntityTooLarge || errors.Is(err, webhook.ErrUnavailable) {
		t.Fatalf("status = %d error = %v, want committed rejection", writer.Code, err)
	}

	if rejected != (webhook.RejectedEvent{Label: "orders", Reason: webhook.RejectionReasonBodyTooLarge, Status: 413}) {
		t.Fatalf("rejected = %+v, want safe body-limit diagnostics", rejected)
	}
}

// TestReceiptCauseIsLoggedByRequestLog checks logs keep the cause while the
// response does not carry it.
func TestReceiptCauseIsLoggedByRequestLog(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := newHandler(t, failingSource(errAppCause), bindEcho(&recorder{}))
	logged := requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(slog.New(slog.NewTextHandler(&logs, nil)))
	})(errormap.Middleware(errormap.Map(errAppCause, problem.NotFound()))(handler))

	response := logged.Record(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))

	if !strings.Contains(logs.String(), "secret-cause") || !strings.Contains(logs.String(), "status=503") {
		t.Fatalf("log = %q, want the cause and status 503", logs.String())
	}

	if strings.Contains(readBody(t, response), "secret-cause") {
		t.Fatal("response leaked the cause")
	}
}

// invoice is the payload type of the Dispatch tests.
type invoice struct {
	Number string `json:"number"`
}

// invoiceEvent decodes the string envelope into an invoice.
var invoiceEvent = webhook.NewEvent("echo", func(envelope string) (invoice, error) {
	return invoice{Number: envelope}, nil
})

// TestDispatchKeepsDeliveryMetadataInQueue checks the queued payload.
func TestDispatchKeepsDeliveryMetadataInQueue(t *testing.T) {
	t.Parallel()

	job := queue.NewJob[webhook.Delivery[invoice]]("invoice-paid")
	fake := queuetest.New()
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Dispatch(invoiceEvent, queue.NewDispatcher(fake), job)
	})

	if status := post(handler, "INV-1", "evt-1").StatusCode; status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	fake.AssertDispatchedCount(t, job, 1)
	fake.AssertDispatched(t, job, func(got webhook.Delivery[invoice]) bool {
		return got == webhook.Delivery[invoice]{ID: "evt-1", Type: "echo", Payload: invoice{Number: "INV-1"}}
	})
}

// TestDeliveryJSONTagsAreStable checks the wire shape of a queued delivery.
func TestDeliveryJSONTagsAreStable(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(webhook.Delivery[invoice]{ID: "evt-1", Type: "echo", Payload: invoice{Number: "INV-1"}})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `{"id":"evt-1","type":"echo","payload":{"number":"INV-1"}}` {
		t.Fatalf("json = %s", data)
	}
}

// TestDispatchFailureAnswers503 checks a queue failure is retryable.
func TestDispatchFailureAnswers503(t *testing.T) {
	t.Parallel()

	job := queue.NewJob[webhook.Delivery[invoice]]("invoice-paid")
	fake := queuetest.New()
	fake.FailWith(errors.New("queue down"))

	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Dispatch(invoiceEvent, queue.NewDispatcher(fake), job)
	})

	if status := post(handler, "x", "").StatusCode; status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

// TestAcceptPermanentErrorAnswers503 checks queue.Permanent is not special.
func TestAcceptPermanentErrorAnswers503(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(echo, func(context.Context, webhook.Delivery[string]) error {
			return queue.Permanent(errors.New("never"))
		})
	})

	if status := post(handler, "x", "").StatusCode; status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}
