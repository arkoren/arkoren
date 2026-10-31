package postmarkwebhook_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/driver/postmark/postmarkwebhook"
)

// TestSourcesRejectControlCredentials validates both public Basic Auth source
// constructors against embedded NUL and DEL bytes. This is an existing guard,
// not a new restriction on otherwise valid opaque passwords such as spaces.
func TestSourcesRejectControlCredentials(t *testing.T) {
	t.Parallel()

	for _, password := range []string{"fixture\x00secret", "fixture\x7fsecret"} {
		_, err := postmarkwebhook.New("fixture", password)

		if !errors.Is(err, webhook.ErrInvalid) {
			t.Errorf("outbound credential error = %v, want webhook.ErrInvalid", err)
		}

		_, err = postmarkwebhook.NewInbound("fixture", password)

		if !errors.Is(err, webhook.ErrInvalid) {
			t.Errorf("inbound credential error = %v, want webhook.ErrInvalid", err)
		}
	}
}

// outbound builds a real receiver with a typed event and reports construction
// errors at the test boundary, keeping request assertions in each scenario.
func outbound[P any](t *testing.T, event webhook.Event[postmarkwebhook.Envelope, P], accept webhook.AcceptFunc[P]) router.Handler {
	t.Helper()

	source, err := postmarkwebhook.New("hooks", "secret")

	if err != nil {
		t.Fatal(err)
	}

	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
		receiver.Handle(event, accept)
	})

	if err != nil {
		t.Fatal(err)
	}

	return handler
}

// authenticated creates a provider-shaped POST using the test endpoint's
// credentials. Transport security belongs to the public deployment boundary.
func authenticated(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://example.com/hooks", strings.NewReader(body))
	request.SetBasicAuth("hooks", "secret")

	return request
}

func TestDeliveredDecodesProviderFields(t *testing.T) {
	t.Parallel()

	var got webhook.Delivery[postmarkwebhook.Delivery]
	handler := outbound(t, postmarkwebhook.Delivered, func(_ context.Context, delivery webhook.Delivery[postmarkwebhook.Delivery]) error {
		got = delivery

		return nil
	})

	request := authenticated(`{"RecordType":"Delivery","MessageID":"message","Recipient":"person@example.com","DeliveredAt":"2026-10-01T12:00:00.1234567Z","Details":"250 OK","Metadata":{"order":"123"}}`)
	request.Header.Set("X-PM-Webhook-Trace-Id", "notification-1")
	recorder := httptest.NewRecorder()

	if err := handler(recorder, request); err != nil {
		t.Fatal(err)
	}

	if recorder.Code != http.StatusOK || got.ID != "notification-1" || got.Type != "Delivery" {
		t.Fatalf("status=%d delivery=%+v", recorder.Code, got)
	}

	if got.Payload.MessageID != "message" || got.Payload.Metadata["order"] != "123" || got.Payload.DeliveredAt.Nanosecond() != 123456700 {
		t.Fatalf("payload=%+v", got.Payload)
	}
}

func TestMissingAuthenticationRejectsBeforeParsing(t *testing.T) {
	t.Parallel()

	handler := outbound(t, postmarkwebhook.Delivered, func(context.Context, webhook.Delivery[postmarkwebhook.Delivery]) error {
		t.Fatal("unauthenticated handler called")

		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`not JSON`))
	recorder := httptest.NewRecorder()

	err := handler(recorder, request)

	if !errors.Is(err, webhook.ErrRejected) || recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d error=%v", recorder.Code, err)
	}
}

func TestIncorrectCredentialsReject(t *testing.T) {
	t.Parallel()

	handler := outbound(t, postmarkwebhook.Delivered, func(context.Context, webhook.Delivery[postmarkwebhook.Delivery]) error {
		t.Fatal("unauthenticated handler called")

		return nil
	})

	for _, credentials := range [][2]string{{"wrong", "secret"}, {"hooks", "wrong"}, {"", ""}} {
		request := authenticated(`{"RecordType":"Delivery"}`)
		request.SetBasicAuth(credentials[0], credentials[1])
		recorder := httptest.NewRecorder()

		if err := handler(recorder, request); !errors.Is(err, webhook.ErrRejected) {
			t.Fatalf("status=%d error=%v", recorder.Code, err)
		}
	}
}

func TestDuplicateAuthorizationRejects(t *testing.T) {
	t.Parallel()

	handler := outbound(t, postmarkwebhook.Delivered, func(context.Context, webhook.Delivery[postmarkwebhook.Delivery]) error {
		t.Fatal("ambiguous authentication accepted")

		return nil
	})

	request := authenticated(`{"RecordType":"Delivery"}`)
	request.Header.Add("Authorization", request.Header.Get("Authorization"))

	if err := handler(httptest.NewRecorder(), request); !errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("error=%v", err)
	}
}

func TestUnknownAuthenticatedEventIsAcknowledged(t *testing.T) {
	t.Parallel()

	handler := outbound(t, postmarkwebhook.Delivered, func(context.Context, webhook.Delivery[postmarkwebhook.Delivery]) error {
		t.Fatal("unknown event handled")

		return nil
	})

	recorder := httptest.NewRecorder()

	if err := handler(recorder, authenticated(`{"RecordType":"FutureEvent","ChangedAt":"future-format"}`)); err != nil {
		t.Fatal(err)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
}

func TestMalformedAuthenticatedEnvelopeIsRetryable(t *testing.T) {
	t.Parallel()

	handler := outbound(t, postmarkwebhook.Delivered, func(context.Context, webhook.Delivery[postmarkwebhook.Delivery]) error {
		t.Fatal("malformed event handled")

		return nil
	})

	for _, body := range []string{`{`, `null`, `[]`, `{}`, `{"RecordType":12}`, `{"RecordType":"Delivery","RecordType":"Open"}`} {
		recorder := httptest.NewRecorder()

		err := handler(recorder, authenticated(body))

		if err == nil || recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("body=%s status=%d error=%v", body, recorder.Code, err)
		}
	}
}

func TestBoundInvalidTimestampIsRetryable(t *testing.T) {
	t.Parallel()

	handler := outbound(t, postmarkwebhook.Delivered, func(context.Context, webhook.Delivery[postmarkwebhook.Delivery]) error {
		t.Fatal("malformed payload handled")

		return nil
	})

	recorder := httptest.NewRecorder()

	err := handler(recorder, authenticated(`{"RecordType":"Delivery","DeliveredAt":"invalid"}`))

	if err == nil || recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d error=%v", recorder.Code, err)
	}
}

func TestTrackingIdentityDoesNotCollapseDistinctNotifications(t *testing.T) {
	t.Parallel()

	source, err := postmarkwebhook.New("hooks", "secret")

	if err != nil {
		t.Fatal(err)
	}

	var identities []string
	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
		receiver.Deduplicate(cache.New(memorycache.New()), cache.NewNamespace[bool]("postmark-tracking"))
		receiver.Handle(postmarkwebhook.Clicked, func(_ context.Context, delivery webhook.Delivery[postmarkwebhook.Tracking]) error {
			identities = append(identities, delivery.ID)

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, identity := range []string{"trace-1", "trace-1", "trace-2", "", ""} {
		request := authenticated(`{"RecordType":"Click","MessageID":"same-message","OriginalLink":"https://example.com"}`)
		request.Header.Set("X-PM-Webhook-Trace-Id", identity)

		if err := handler(httptest.NewRecorder(), request); err != nil {
			t.Fatal(err)
		}
	}

	if strings.Join(identities, ",") != "trace-1,trace-2,," {
		t.Fatalf("handled identities=%q", identities)
	}
}

func TestBounceDecodesComplaintFields(t *testing.T) {
	t.Parallel()

	var got postmarkwebhook.Bounce
	handler := outbound(t, postmarkwebhook.SpamComplaint, func(_ context.Context, delivery webhook.Delivery[postmarkwebhook.Bounce]) error {
		got = delivery.Payload

		return nil
	})

	if err := handler(httptest.NewRecorder(), authenticated(`{"RecordType":"SpamComplaint","ID":692560174,"Type":"SpamComplaint","Email":"person@example.com","Inactive":true,"BouncedAt":"2026-10-01T12:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	if got.ID != 692560174 || !got.Inactive || got.BouncedAt.IsZero() {
		t.Fatalf("payload=%+v", got)
	}
}

func TestSubscriptionCanOmitMessageID(t *testing.T) {
	t.Parallel()

	var got postmarkwebhook.Subscription
	handler := outbound(t, postmarkwebhook.SubscriptionChanged, func(_ context.Context, delivery webhook.Delivery[postmarkwebhook.Subscription]) error {
		got = delivery.Payload

		return nil
	})

	if err := handler(httptest.NewRecorder(), authenticated(`{"RecordType":"SubscriptionChange","MessageID":null,"Tag":null,"SuppressionReason":null,"Recipient":"person@example.com","Origin":"Recipient","SuppressSending":true,"ChangedAt":"2026-10-01T12:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	if got.Recipient != "person@example.com" || !got.SuppressSending || got.ChangedAt.IsZero() {
		t.Fatalf("payload=%+v", got)
	}
}

func TestInboundHasNoRecordTypeAndPreservesSenderDate(t *testing.T) {
	t.Parallel()

	source, err := postmarkwebhook.NewInbound("hooks", "secret")

	if err != nil {
		t.Fatal(err)
	}

	var got webhook.Delivery[postmarkwebhook.Email]
	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
		receiver.Handle(postmarkwebhook.Inbound, func(_ context.Context, delivery webhook.Delivery[postmarkwebhook.Email]) error {
			got = delivery

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	request := authenticated(`{"MessageID":"inbound-message","Subject":"Help","Date":"Thu, 5 Nov 2026 16:33:54 -0500","HtmlBody":"<p>Hello</p>"}`)
	request.Header.Set("X-PM-Webhook-Trace-Id", "ignored-for-inbound")

	if err := handler(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}

	if got.ID != "inbound-message" || got.Type != "Inbound" || got.Payload.Date != "Thu, 5 Nov 2026 16:33:54 -0500" {
		t.Fatalf("delivery=%+v", got)
	}

	if got.Payload.HTMLBody != "<p>Hello</p>" {
		t.Fatalf("payload=%+v", got.Payload)
	}
}

func TestInvalidCredentialsFailConstruction(t *testing.T) {
	t.Parallel()

	for _, credentials := range [][2]string{{"", "secret"}, {"hooks", ""}, {"user:name", "secret"}, {"hooks", "secret\n"}} {
		_, err := postmarkwebhook.New(credentials[0], credentials[1])

		if !errors.Is(err, webhook.ErrInvalid) {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestOpenedDecodesFirstOpenAndObservationTime(t *testing.T) {
	t.Parallel()

	var got postmarkwebhook.Tracking
	handler := outbound(t, postmarkwebhook.Opened, func(_ context.Context, delivery webhook.Delivery[postmarkwebhook.Tracking]) error {
		got = delivery.Payload

		return nil
	})

	if err := handler(httptest.NewRecorder(), authenticated(`{"RecordType":"Open","FirstOpen":true,"ReceivedAt":"2026-10-01T12:00:00Z","MessageID":"message","Recipient":"person@example.com"}`)); err != nil {
		t.Fatal(err)
	}

	if !got.FirstOpen || got.ReceivedAt.IsZero() || got.MessageID != "message" {
		t.Fatalf("payload=%+v", got)
	}
}

func TestBouncedDecodesProviderCategory(t *testing.T) {
	t.Parallel()

	var got postmarkwebhook.Bounce
	handler := outbound(t, postmarkwebhook.Bounced, func(_ context.Context, delivery webhook.Delivery[postmarkwebhook.Bounce]) error {
		got = delivery.Payload

		return nil
	})

	if err := handler(httptest.NewRecorder(), authenticated(`{"RecordType":"Bounce","ID":692560173,"Type":"HardBounce","Email":"person@example.com","Inactive":true}`)); err != nil {
		t.Fatal(err)
	}

	if got.Type != "HardBounce" || got.Email != "person@example.com" || got.ID != 692560173 {
		t.Fatalf("payload=%+v", got)
	}
}

func TestAcceptFailureIsRetryable(t *testing.T) {
	t.Parallel()

	failure := errors.New("storage unavailable")
	handler := outbound(t, postmarkwebhook.Delivered, func(context.Context, webhook.Delivery[postmarkwebhook.Delivery]) error {
		return failure
	})
	recorder := httptest.NewRecorder()

	err := handler(recorder, authenticated(`{"RecordType":"Delivery"}`))

	if !errors.Is(err, failure) || recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d error=%v", recorder.Code, err)
	}
}

func TestInboundRejectsMissingIdentityAsRetryable(t *testing.T) {
	t.Parallel()

	source, err := postmarkwebhook.NewInbound("hooks", "secret")

	if err != nil {
		t.Fatal(err)
	}

	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
		receiver.Handle(postmarkwebhook.Inbound, func(context.Context, webhook.Delivery[postmarkwebhook.Email]) error {
			t.Fatal("malformed inbound email handled")

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{`{`, `null`, `{"MessageID":""}`, `{"MessageID":1}`} {
		recorder := httptest.NewRecorder()

		err := handler(recorder, authenticated(body))

		if err == nil || recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("body=%s status=%d error=%v", body, recorder.Code, err)
		}
	}
}

func TestMappedEventRetainsProviderKeyAndDecoder(t *testing.T) {
	t.Parallel()

	mapped := postmarkwebhook.Delivered.Map(func(notification postmarkwebhook.Delivery) (string, error) {
		return notification.MessageID, nil
	})

	var got string
	handler := outbound(t, mapped, func(_ context.Context, delivery webhook.Delivery[string]) error {
		got = delivery.Payload

		return nil
	})

	if err := handler(httptest.NewRecorder(), authenticated(`{"RecordType":"Delivery","MessageID":"message"}`)); err != nil {
		t.Fatal(err)
	}

	if got != "message" || mapped.Key() != "Delivery" {
		t.Fatalf("payload=%s key=%s", got, mapped.Key())
	}
}
