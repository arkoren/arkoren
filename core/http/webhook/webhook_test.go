package webhook_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// errBadSignature and errBadPayload stand for provider verification failures.
var (
	errBadSignature = errors.New("bad signature secret-cause")
	errBadPayload   = errors.New("bad payload secret-cause")
)

// echo is a static event that decodes the string envelope into itself.
var echo = webhook.NewEvent("echo", func(envelope string) (string, error) {
	return envelope, nil
})

// recorder collects handled payloads.
type recorder struct {
	mu     sync.Mutex
	events []string
}

// accept records the delivery payload.
func (r *recorder) accept(_ context.Context, delivery webhook.Delivery[string]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, delivery.Payload)

	return nil
}

// count returns the number of handled deliveries.
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.events)
}

// headerSource treats the body as the envelope, X-Id as the ID and X-Type as
// the event type, defaulting to "echo".
func headerSource() webhook.Source[string] {
	return webhook.NewSource(func(r *http.Request, body []byte) (webhook.Delivery[string], error) {
		kind := r.Header.Get("X-Type")

		if kind == "" {
			kind = "echo"
		}

		return webhook.Delivery[string]{ID: r.Header.Get("X-Id"), Type: kind, Payload: string(body)}, nil
	})
}

// newHandler builds a handler or fails the test.
func newHandler(t *testing.T, source webhook.Source[string], configure ...webhook.BuilderFunc[string]) router.Handler {
	t.Helper()

	handler, err := webhook.Handler(source, configure...)

	if err != nil {
		t.Fatal(err)
	}

	return handler
}

// bindEcho returns a configuration handling echo with accepted.
func bindEcho(accepted *recorder, more ...webhook.BuilderFunc[string]) webhook.BuilderFunc[string] {
	return func(receiver *webhook.Builder[string]) {
		receiver.Handle(echo, accepted.accept)

		for _, configure := range more {
			configure(receiver)
		}
	}
}

// post sends body with event ID id to handler.
func post(handler router.Handler, body, id string) *http.Response {
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	req.Header.Set("X-Id", id)

	return handler.Record(req)
}

// readBody returns the response body.
func readBody(t *testing.T, response *http.Response) string {
	t.Helper()

	data, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// brokenDriver fails the cache operations the receiver uses.
type brokenDriver struct {
	cache.Driver
	failHas bool
	failAdd bool
	onAdd   func()
}

// Has fails when failHas is set.
func (driver brokenDriver) Has(ctx context.Context, key string) (bool, error) {
	if driver.failHas {
		return false, errors.New("cache down")
	}

	return driver.Driver.Has(ctx, key)
}

// Add fails when failAdd is set and reports the call to onAdd.
func (driver brokenDriver) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if driver.onAdd != nil {
		driver.onAdd()
	}

	if driver.failAdd {
		return false, errors.New("cache down")
	}

	return driver.Driver.Add(ctx, key, value, ttl)
}

// TestHandlerSuccessAnswers200EmptyBody checks the happy path.
func TestHandlerSuccessAnswers200EmptyBody(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), bindEcho(accepted))
	response := post(handler, "hello", "evt-1")

	if response.StatusCode != http.StatusOK || readBody(t, response) != "" || accepted.count() != 1 {
		t.Fatalf("status = %d accepted = %d, want empty 200 and 1", response.StatusCode, accepted.count())
	}
}

// TestHandlerPassesDeliveryMetadataToHandler checks ID, type and payload.
func TestHandlerPassesDeliveryMetadataToHandler(t *testing.T) {
	t.Parallel()

	var got webhook.Delivery[string]
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(echo, func(_ context.Context, delivery webhook.Delivery[string]) error {
			got = delivery

			return nil
		})
	})
	post(handler, "hello", "evt-1")

	if got != (webhook.Delivery[string]{ID: "evt-1", Type: "echo", Payload: "hello"}) {
		t.Fatalf("delivery = %+v", got)
	}
}

// TestHandlerVerifySkipAnswers200WithoutAccept checks an authenticated skip.
func TestHandlerVerifySkipAnswers200WithoutAccept(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	source := webhook.NewSource(func(*http.Request, []byte) (webhook.Delivery[string], error) {
		return webhook.Delivery[string]{}, fmt.Errorf("ping: %w", webhook.ErrSkip)
	})

	response := post(newHandler(t, source, bindEcho(accepted)), "x", "")

	if response.StatusCode != http.StatusOK || accepted.count() != 0 {
		t.Fatalf("status = %d accepted = %d, want 200 and 0", response.StatusCode, accepted.count())
	}
}

// failingSource fails verification with err.
func failingSource(err error) webhook.Source[string] {
	return webhook.NewSource(func(*http.Request, []byte) (webhook.Delivery[string], error) {
		return webhook.Delivery[string]{}, err
	})
}

// TestHandlerRejectMapsStatusesWithoutCause checks mapped 4xx answers.
func TestHandlerRejectMapsStatusesWithoutCause(t *testing.T) {
	t.Parallel()

	configure := bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Reject(http.StatusUnauthorized, errBadSignature)
		receiver.Reject(http.StatusUnprocessableEntity, errBadPayload)
	})

	signature := post(newHandler(t, failingSource(errBadSignature), configure), "x", "")
	payload := post(newHandler(t, failingSource(errBadPayload), configure), "x", "")

	if signature.StatusCode != http.StatusUnauthorized || payload.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("statuses = %d %d, want 401 422", signature.StatusCode, payload.StatusCode)
	}

	if strings.Contains(readBody(t, signature), "secret-cause") {
		t.Fatal("response leaked the cause")
	}
}

// TestHandlerRejectFirstMatchWins checks mapping order.
func TestHandlerRejectFirstMatchWins(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, failingSource(errBadSignature), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Reject(http.StatusUnauthorized, errBadSignature)
		receiver.Reject(http.StatusBadRequest, errBadSignature)
	}))

	if status := post(handler, "x", "").StatusCode; status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestHandlerErrRejectedAnswers400 checks the unmapped rejection class.
func TestHandlerErrRejectedAnswers400(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, failingSource(fmt.Errorf("%w: %w", webhook.ErrRejected, errBadPayload)), bindEcho(&recorder{}))

	if status := post(handler, "x", "").StatusCode; status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

// TestHandlerUnclassifiedVerifyErrorAnswers503 checks the retryable default.
func TestHandlerUnclassifiedVerifyErrorAnswers503(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, failingSource(errors.New("verifier down secret-cause")), bindEcho(&recorder{}))
	response := post(handler, "x", "")

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}

	if strings.Contains(readBody(t, response), "secret-cause") {
		t.Fatal("response leaked the cause")
	}
}

// TestHandlerUnboundEventAnswers200AndPublishesIgnored checks an authenticated
// event without a handler is acknowledged and reported without its type.
func TestHandlerUnboundEventAnswers200AndPublishesIgnored(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	bus := event.New()
	var ignored []webhook.IgnoredEvent

	if _, err := bus.Listen(webhook.Ignored, func(_ context.Context, payload webhook.IgnoredEvent) error {
		ignored = append(ignored, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Events(bus).Label("orders")
	}))

	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader("x"))
	req.Header.Set("X-Type", "attacker-supplied-type")

	response := handler.Record(req)

	if response.StatusCode != http.StatusOK || accepted.count() != 0 {
		t.Fatalf("status = %d accepted = %d, want 200 and 0", response.StatusCode, accepted.count())
	}

	if len(ignored) != 1 || ignored[0] != (webhook.IgnoredEvent{Label: "orders", Reason: webhook.IgnoreReasonUnbound}) {
		t.Fatalf("ignored = %+v", ignored)
	}

	assertEventJSON(t, ignored, `[{"Label":"orders","Reason":"unbound"}]`)
}

// TestHandlerDecodeErrorAnswers503 checks a bound decode failure is retryable.
func TestHandlerDecodeErrorAnswers503(t *testing.T) {
	t.Parallel()

	failing := webhook.NewEvent("echo", func(string) (string, error) {
		return "", errors.New("schema mismatch secret-cause")
	})

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(failing, accepted.accept)
	})

	response := post(handler, "x", "")

	if response.StatusCode != http.StatusServiceUnavailable || accepted.count() != 0 {
		t.Fatalf("status = %d accepted = %d, want 503 and 0", response.StatusCode, accepted.count())
	}

	if strings.Contains(readBody(t, response), "secret-cause") {
		t.Fatal("response leaked the cause")
	}
}

// TestHandlerDecodeSkipAnswers200 checks a deliberate ignore before accept.
func TestHandlerDecodeSkipAnswers200(t *testing.T) {
	t.Parallel()

	skipping := webhook.NewEvent("echo", func(string) (string, error) {
		return "", fmt.Errorf("old format: %w", webhook.ErrSkip)
	})

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(skipping, accepted.accept)
	})

	if status := post(handler, "x", "").StatusCode; status != http.StatusOK || accepted.count() != 0 {
		t.Fatalf("status = %d accepted = %d, want 200 and 0", status, accepted.count())
	}
}

// TestHandlerAcceptErrorsAnswer503AndAreNotMarkedSeen checks every accept
// error, including ErrSkip, is retryable and does not suppress the retry.
func TestHandlerAcceptErrorsAnswer503AndAreNotMarkedSeen(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())

	for _, failure := range []error{errors.New("queue down"), webhook.ErrSkip, errors.New("x")} {
		calls := 0
		handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
			receiver.Handle(echo, func(context.Context, webhook.Delivery[string]) error {
				calls++

				if calls == 1 {
					return failure
				}

				return nil
			})
			receiver.Deduplicate(store, cache.NewNamespace[bool]("scope-"+failure.Error()))
		})

		first := post(handler, "x", "evt-1").StatusCode
		second := post(handler, "x", "evt-1").StatusCode

		if first != http.StatusServiceUnavailable || second != http.StatusOK || calls != 2 {
			t.Fatalf("%v: statuses = %d %d calls = %d, want 503 200 2", failure, first, second, calls)
		}
	}
}

// TestHandlerDuplicateAnswers200WithoutSecondAccept checks deduplication.
func TestHandlerDuplicateAnswers200WithoutSecondAccept(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())
	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders"))
	}))
	post(handler, "x", "evt-1")

	if status := post(handler, "x", "evt-1").StatusCode; status != http.StatusOK || accepted.count() != 1 {
		t.Fatalf("status = %d accepted = %d, want 200 and 1", status, accepted.count())
	}
}

// TestHandlerDeduplicateExpiresAfterTTL checks the remembered ID expires.
func TestHandlerDeduplicateExpiresAfterTTL(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		accepted := &recorder{}
		store := cache.New(memorycache.New())
		handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
			receiver.Deduplicate(store, cache.NewNamespace[bool]("orders", func(space *cache.NamespaceBuilder) {
				space.TTL(time.Hour)
			}))
		}))
		post(handler, "x", "evt-1")
		time.Sleep(2 * time.Hour)
		post(handler, "x", "evt-1")

		if accepted.count() != 2 {
			t.Fatalf("accepted = %d, want 2", accepted.count())
		}
	})
}

// TestHandlerDeduplicateUsesNamespaceRetention checks the declared retention.
func TestHandlerDeduplicateUsesNamespaceRetention(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		accepted := &recorder{}
		store := cache.New(memorycache.New())
		handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
			receiver.Deduplicate(store, cache.NewNamespace[bool]("orders", func(space *cache.NamespaceBuilder) {
				space.TTL(72 * time.Hour)
			}))
		}))
		post(handler, "x", "evt-1")
		time.Sleep(71 * time.Hour)
		post(handler, "x", "evt-1")
		time.Sleep(2 * time.Hour)
		post(handler, "x", "evt-1")

		if accepted.count() != 2 {
			t.Fatalf("accepted = %d, want 2", accepted.count())
		}
	})
}

// TestHandlerDeduplicateUsesLiteralNamespace checks only the delivery ID is
// encoded, not an explicit namespace that may already include encoded parts.
func TestHandlerDeduplicateUsesLiteralNamespace(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())
	space := cache.NewNamespace[bool]("webhook:v1:YmlsbGluZw")
	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, space)
	}))
	post(handler, "x", "a:b")

	seen, err := store.Get(context.Background(), cache.NewKey[bool]("webhook:v1:YmlsbGluZw:YTpi"))

	if err != nil || !seen {
		t.Fatalf("literal namespace marker = %v, %v", seen, err)
	}
}

// TestHandlerDeduplicatePreservesExplicitDotMarkers checks a receiver can keep
// existing deduplication state by explicitly selecting the former separator.
func TestHandlerDeduplicatePreservesExplicitDotMarkers(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())

	if err := store.Put(t.Context(), cache.NewKey[bool]("orders.YTpi"), true); err != nil {
		t.Fatal(err)
	}

	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders", func(space *cache.NamespaceBuilder) {
			space.Separator(".")
		}))
	}))
	post(handler, "x", "a:b")

	if accepted.count() != 0 {
		t.Fatal("explicit dot namespace did not reuse the existing marker")
	}
}

// TestHandlerDeduplicateZeroTTLDoesNotExpire checks retention comes solely
// from the namespace, without the former implicit three-day fallback.
func TestHandlerDeduplicateZeroTTLDoesNotExpire(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		accepted := &recorder{}
		store := cache.New(memorycache.New())
		handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
			receiver.Deduplicate(store, cache.NewNamespace[bool]("orders"))
		}))
		post(handler, "x", "evt-1")
		time.Sleep(100 * time.Hour)
		post(handler, "x", "evt-1")

		if accepted.count() != 1 {
			t.Fatalf("accepted = %d, want 1", accepted.count())
		}
	})
}

// TestHandlerDeduplicateHonorsCustomSeparators checks base64url characters
// are literal separators too, and another receiver can reuse the exact key.
func TestHandlerDeduplicateHonorsCustomSeparators(t *testing.T) {
	t.Parallel()

	for _, separator := range []string{"-", "_"} {
		accepted := &recorder{}
		store := cache.New(memorycache.New())
		space := cache.NewNamespace[bool]("orders", func(space *cache.NamespaceBuilder) {
			space.Separator(separator)
		})

		first := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
			receiver.Deduplicate(store, space)
		}))

		second := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
			receiver.Deduplicate(store, space)
		}))
		post(first, "x", "a:b")

		seen, err := store.Get(context.Background(), cache.NewKey[bool]("orders"+separator+"YTpi"))

		if err != nil || !seen {
			t.Fatalf("separator %q: marker = %v, %v", separator, seen, err)
		}

		post(second, "x", "a:b")

		if accepted.count() != 1 {
			t.Fatalf("separator %q: accepted = %d, want 1", separator, accepted.count())
		}
	}
}

// TestHandlerDeduplicateHonorsEmptySeparator checks empty joins concatenate
// literally, including reusing an identity produced by a different namespace.
func TestHandlerDeduplicateHonorsEmptySeparator(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())
	first := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders-", func(space *cache.NamespaceBuilder) {
			space.Separator("")
		}))
	}))

	second := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders", func(space *cache.NamespaceBuilder) {
			space.Separator("-")
		}))
	}))
	post(first, "x", "a:b")

	seen, err := store.Get(context.Background(), cache.NewKey[bool]("orders-YTpi"))

	if err != nil || !seen {
		t.Fatalf("concatenated marker = %v, %v", seen, err)
	}

	post(second, "x", "a:b")

	if accepted.count() != 1 {
		t.Fatalf("accepted = %d, want 1 for the shared literal identity", accepted.count())
	}
}

// TestHandlerDeduplicateDefaultNamespacesAreSeparate checks these namespaces
// produce different identities when joined with the default colon separator.
func TestHandlerDeduplicateDefaultNamespacesAreSeparate(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())
	first := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("a"))
	}))

	second := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("ab"))
	}))
	post(first, "x", "bc")
	post(second, "x", "c")

	if accepted.count() != 2 {
		t.Fatalf("accepted = %d, want 2", accepted.count())
	}
}

// TestHandlerDeduplicateEncodesOnlyID checks dots in delivery IDs are encoded,
// so these identities remain distinct even with an explicit dot separator.
func TestHandlerDeduplicateEncodesOnlyID(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())
	first := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("a.b", func(space *cache.NamespaceBuilder) {
			space.Separator(".")
		}))
	}))

	second := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("a", func(space *cache.NamespaceBuilder) {
			space.Separator(".")
		}))
	}))
	post(first, "x", "c")
	post(second, "x", "b.c")

	if accepted.count() != 2 {
		t.Fatalf("accepted = %d, want 2 distinct cache identities", accepted.count())
	}
}

// TestHandlerDeduplicateCheckFailureStillAccepts checks the check fails open.
func TestHandlerDeduplicateCheckFailureStillAccepts(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(brokenDriver{Driver: memorycache.New(), failHas: true})
	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders"))
	}))

	if status := post(handler, "x", "evt-1").StatusCode; status != http.StatusOK || accepted.count() != 1 {
		t.Fatalf("status = %d accepted = %d, want 200 and 1", status, accepted.count())
	}
}

// TestHandlerDeduplicateMarkFailureStillAnswers200 checks a failed mark does not
// fail a handled event.
func TestHandlerDeduplicateMarkFailureStillAnswers200(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(brokenDriver{Driver: memorycache.New(), failAdd: true})
	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders"))
	}))

	if status := post(handler, "x", "evt-1").StatusCode; status != http.StatusOK || accepted.count() != 1 {
		t.Fatalf("status = %d accepted = %d, want 200 and 1", status, accepted.count())
	}
}

// orderWriter records whether the cache was marked before the status was written.
type orderWriter struct {
	http.ResponseWriter
	marked        *bool
	markedAtWrite bool
}

// WriteHeader captures the mark state at the moment the response commits.
func (writer *orderWriter) WriteHeader(status int) {
	writer.markedAtWrite = *writer.marked
	writer.ResponseWriter.WriteHeader(status)
}

// TestHandlerMarksSeenBeforeWriting200 checks the order of mark and response.
func TestHandlerMarksSeenBeforeWriting200(t *testing.T) {
	t.Parallel()

	marked := false
	store := cache.New(brokenDriver{Driver: memorycache.New(), onAdd: func() { marked = true }})

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders"))
	}))

	writer := &orderWriter{ResponseWriter: httptest.NewRecorder(), marked: &marked}
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader("x"))
	req.Header.Set("X-Id", "evt-1")

	if err := handler(writer, req); err != nil {
		t.Fatal(err)
	}

	if !writer.markedAtWrite {
		t.Fatal("200 was written before the event was marked seen")
	}
}

// TestHandlerEmptyIDSkipsDeduplication checks every delivery is accepted.
func TestHandlerEmptyIDSkipsDeduplication(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())
	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders"))
	}))
	post(handler, "x", "")
	post(handler, "x", "")

	if accepted.count() != 2 {
		t.Fatalf("accepted = %d, want 2", accepted.count())
	}
}

// TestHandlerConcurrentDuplicatesMayBothBeAccepted documents the race: the
// check and the mark are not one atomic step. Run sequentially it dedups.
func TestHandlerConcurrentDuplicatesAreBestEffort(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	store := cache.New(memorycache.New())
	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("orders"))
	}))

	var wait sync.WaitGroup

	for range 8 {
		wait.Go(func() { post(handler, "x", "evt-1") })
	}

	wait.Wait()

	if accepted.count() < 1 || accepted.count() > 8 {
		t.Fatalf("accepted = %d, want between 1 and 8", accepted.count())
	}
}

// TestHandlerOversizedBodyAnswers413 checks the body limit.
func TestHandlerOversizedBodyAnswers413(t *testing.T) {
	t.Parallel()

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), bindEcho(accepted, func(receiver *webhook.Builder[string]) {
		receiver.MaxBodySize(4)
	}))

	if status := post(handler, "12345", "").StatusCode; status != http.StatusRequestEntityTooLarge || accepted.count() != 0 {
		t.Fatalf("status = %d accepted = %d, want 413 and 0", status, accepted.count())
	}
}

// TestHandlerOversizedBodyReturnsBodyTooLarge checks the returned error.
func TestHandlerOversizedBodyReturnsBodyTooLarge(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.MaxBodySize(4)
	}))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345")))

	if !errors.Is(err, request.ErrBodyTooLarge) {
		t.Fatalf("error = %v, want request.ErrBodyTooLarge", err)
	}
}

// failingReader fails its first read with err after running before.
type failingReader struct {
	before func()
	err    error
}

// Read runs the hook and fails.
func (reader failingReader) Read([]byte) (int, error) {
	if reader.before != nil {
		reader.before()
	}

	return 0, reader.err
}

// timeoutError is a network timeout.
type timeoutError struct{}

// Error describes the timeout.
func (timeoutError) Error() string { return "i/o timeout" }

// Timeout reports true.
func (timeoutError) Timeout() bool { return true }

// Temporary reports false.
func (timeoutError) Temporary() bool { return false }

// TestHandlerMalformedBodyAnswers400 checks a body that cannot be read is
// rejected as malformed and published without its cause.
func TestHandlerMalformedBodyAnswers400(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var rejected []webhook.RejectedEvent

	if _, err := bus.Listen(webhook.Rejected, func(_ context.Context, payload webhook.RejectedEvent) error {
		rejected = append(rejected, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Events(bus)
	}))

	req := httptest.NewRequest(http.MethodPost, "/hook", failingReader{err: io.ErrUnexpectedEOF})

	if status := handler.Record(req).StatusCode; status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}

	want := webhook.RejectedEvent{Reason: webhook.RejectionReasonUnreadableBody, Status: http.StatusBadRequest}

	if len(rejected) != 1 || rejected[0] != want {
		t.Fatalf("rejected = %+v", rejected)
	}

	assertEventJSON(t, rejected, `[{"Label":"","Reason":"unreadable_body","Status":400}]`)
}

// TestHandlerCanceledBodyReadAnswers503 checks cancellation is not a malformed
// request.
func TestHandlerCanceledBodyReadAnswers503(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	handler := newHandler(t, headerSource(), bindEcho(&recorder{}))
	body := failingReader{before: cancel, err: context.Canceled}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/hook", body)

	err := handler(httptest.NewRecorder(), req)

	if !errors.Is(err, webhook.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want ErrUnavailable wrapping context.Canceled", err)
	}
}

// TestHandlerTimeoutBodyReadAnswers503 checks a read timeout is retryable.
func TestHandlerTimeoutBodyReadAnswers503(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}))
	req := httptest.NewRequest(http.MethodPost, "/hook", failingReader{err: timeoutError{}})

	if status := handler.Record(req).StatusCode; status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

// TestHandlerPublishesRejectedEventsWithoutSensitiveData checks the rejected
// event payloads carry only the label, a reason and a status.
func TestHandlerPublishesRejectedEventsWithoutSensitiveData(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var rejected []webhook.RejectedEvent

	if _, err := bus.Listen(webhook.Rejected, func(_ context.Context, payload webhook.RejectedEvent) error {
		rejected = append(rejected, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := newHandler(t, failingSource(errBadSignature), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Reject(http.StatusUnauthorized, errBadSignature).Events(bus).MaxBodySize(8).Label("orders")
	}))
	post(handler, "bad", "")
	post(handler, "0123456789", "")

	want := []webhook.RejectedEvent{
		{Label: "orders", Reason: webhook.RejectionReasonVerify, Status: 401},
		{Label: "orders", Reason: webhook.RejectionReasonBodyTooLarge, Status: 413},
	}

	if !slices.Equal(rejected, want) {
		t.Fatalf("rejected = %+v, want %+v", rejected, want)
	}

	assertEventJSON(t, rejected, `[{"Label":"orders","Reason":"verify","Status":401},`+
		`{"Label":"orders","Reason":"body_too_large","Status":413}]`)
}

// TestHandlerPublishesFailedEventsWithoutSensitiveData checks the failed event
// payloads carry only the label and a stage.
func TestHandlerPublishesFailedEventsWithoutSensitiveData(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var failed []webhook.FailedEvent

	if _, err := bus.Listen(webhook.Failed, func(_ context.Context, payload webhook.FailedEvent) error {
		failed = append(failed, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	source := webhook.NewSource(func(_ *http.Request, body []byte) (webhook.Delivery[string], error) {
		if string(body) == "down" {
			return webhook.Delivery[string]{}, errors.New("down")
		}

		return webhook.Delivery[string]{ID: "evt-secret", Type: "echo", Payload: string(body)}, nil
	})

	handler := newHandler(t, source, func(receiver *webhook.Builder[string]) {
		receiver.Handle(echo, func(context.Context, webhook.Delivery[string]) error {
			return errors.New("queue down")
		})
		receiver.Events(bus).Label("orders")
	})
	post(handler, "down", "")
	post(handler, "ok", "")

	want := []webhook.FailedEvent{
		{Label: "orders", Stage: webhook.FailureStageVerify},
		{Label: "orders", Stage: webhook.FailureStageAccept},
	}

	if !slices.Equal(failed, want) {
		t.Fatalf("failed = %+v, want %+v", failed, want)
	}

	assertEventJSON(t, failed, `[{"Label":"orders","Stage":"verify"},{"Label":"orders","Stage":"accept"}]`)
}

// TestHandlerPublishesDecodeFailureStage checks the decode stage.
func TestHandlerPublishesDecodeFailureStage(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var failed []webhook.FailedEvent

	if _, err := bus.Listen(webhook.Failed, func(_ context.Context, payload webhook.FailedEvent) error {
		failed = append(failed, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	broken := webhook.NewEvent("echo", func(string) (string, error) { return "", errors.New("mismatch") })

	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(broken, (&recorder{}).accept)
		receiver.Events(bus)
	})
	post(handler, "x", "")

	if len(failed) != 1 || failed[0].Stage != webhook.FailureStageDecode {
		t.Fatalf("failed = %+v, want one decode failure", failed)
	}

	assertEventJSON(t, failed, `[{"Label":"","Stage":"decode"}]`)
}

// TestHandlerSharedEventWorksWithTwoSources checks one static event serves two
// configured sources with different credentials.
func TestHandlerSharedEventWorksWithTwoSources(t *testing.T) {
	t.Parallel()

	secretSource := func(secret string) webhook.Source[string] {
		return webhook.NewSource(func(r *http.Request, body []byte) (webhook.Delivery[string], error) {
			if r.Header.Get("X-Secret") != secret {
				return webhook.Delivery[string]{}, webhook.ErrRejected
			}

			return webhook.Delivery[string]{Type: "echo", Payload: string(body)}, nil
		})
	}

	accepted := &recorder{}
	first := newHandler(t, secretSource("one"), bindEcho(accepted))
	second := newHandler(t, secretSource("two"), bindEcho(accepted))
	send := func(handler router.Handler, secret string) int {
		req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader("x"))
		req.Header.Set("X-Secret", secret)

		return handler.Record(req).StatusCode
	}

	if send(first, "one") != 200 || send(second, "two") != 200 || send(first, "two") != 400 || accepted.count() != 2 {
		t.Fatalf("shared event did not serve both sources; accepted = %d", accepted.count())
	}
}
