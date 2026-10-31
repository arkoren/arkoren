package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
)

// noopSource authenticates every request as an "event" delivery.
func noopSource() Source[string] {
	return NewSource(func(*http.Request, []byte) (Delivery[string], error) {
		return Delivery[string]{Type: "event", Payload: "payload"}, nil
	})
}

// noopEvent decodes the envelope unchanged.
var noopEvent = NewEvent("event", func(envelope string) (string, error) {
	return envelope, nil
})

// bindNoop binds noopEvent to a handler that does nothing.
func bindNoop(receiver *Builder[string]) {
	receiver.Handle(noopEvent, func(context.Context, Delivery[string]) error { return nil })
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor,
// identical draft receivers, and ordering without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder[string]
	order := 0

	_, err := Handler(noopSource(), func(receiver *Builder[string]) {
		retained = receiver
		order++
		bindNoop(receiver)

		if receiver.MaxBodySize(5) != receiver {
			t.Fatal("setting returned another draft")
		}
	}, func(receiver *Builder[string]) {
		if order != 1 || receiver != retained {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		order++
	})

	if err != nil || order != 2 {
		t.Fatalf("callback count = %d, error = %v, want 2 and nil", order, err)
	}
}

// TestConstructionOmissionAndEmptyExpansions supply zero callbacks. A supplied
// nil is misuse and must panic before a later callback can execute.
func TestConstructionOmissionAndEmptyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []BuilderFunc[string]

	if _, err := Handler(noopSource(), omitted...); !errors.Is(err, ErrInvalid) {
		t.Fatalf("omitted callbacks error = %v, want ErrInvalid (nothing bound)", err)
	}

	if _, err := Handler(noopSource(), []BuilderFunc[string]{}...); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty callbacks error = %v, want ErrInvalid (nothing bound)", err)
	}

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	_, _ = Handler(noopSource(), nil, func(receiver *Builder[string]) {
		reached = true
	})
}

// TestConstructionApplicationPanicPropagates keeps callback misuse separate
// from ordinary validation and prevents later callbacks from executing.
func TestConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "construction sentinel" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	_, _ = Handler(noopSource(), func(receiver *Builder[string]) {
		panic("construction sentinel")
	}, func(receiver *Builder[string]) {
		reached = true
	})
}

// TestBuilderPublicationDetachesBodyLimit checks a retained draft cannot raise
// the published body limit after construction has finished.
func TestBuilderPublicationDetachesBodyLimit(t *testing.T) {
	t.Parallel()

	var retained *Builder[string]

	handler, err := Handler(noopSource(), func(receiver *Builder[string]) {
		retained = receiver
		bindNoop(receiver)
		receiver.MaxBodySize(4)
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.MaxBodySize(1 << 20)

	response := handler.Record(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345")))

	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.StatusCode)
	}
}

// TestBuilderPublicationDetachesBindings checks adding a binding on a retained
// draft leaves the authenticated event unbound in the published handler.
func TestBuilderPublicationDetachesBindings(t *testing.T) {
	t.Parallel()

	source := NewSource(func(*http.Request, []byte) (Delivery[string], error) {
		return Delivery[string]{Type: "later", Payload: "payload"}, nil
	})

	var retained *Builder[string]

	handler, err := Handler(source, func(receiver *Builder[string]) {
		retained = receiver
		bindNoop(receiver)
	})

	if err != nil {
		t.Fatal(err)
	}

	accepted := false
	retained.Handle(NewEvent("later", func(envelope string) (string, error) {
		return envelope, nil
	}), func(context.Context, Delivery[string]) error {
		accepted = true

		return nil
	})

	response := handler.Record(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))

	if response.StatusCode != http.StatusOK || accepted {
		t.Fatalf("status = %d accepted = %v, want acknowledged unbound event", response.StatusCode, accepted)
	}
}

// invalid builds a handler and returns its construction error.
func invalid(source Source[string], configure ...BuilderFunc[string]) error {
	_, err := Handler(source, configure...)

	return err
}

// TestHandlerRejectsInvalidSource checks a zero Source and nil verify.
func TestHandlerRejectsInvalidSource(t *testing.T) {
	t.Parallel()

	if err := invalid(Source[string]{}, bindNoop); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero source error = %v, want ErrInvalid", err)
	}

	if err := NewSource[string](nil).Err(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil verify error = %v, want ErrInvalid", err)
	}
}

// TestHandlerRejectsNoBindings checks a receiver that handles nothing.
func TestHandlerRejectsNoBindings(t *testing.T) {
	t.Parallel()

	if err := invalid(noopSource()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// TestHandlerRejectsDuplicateBinding checks one owner per event key.
func TestHandlerRejectsDuplicateBinding(t *testing.T) {
	t.Parallel()

	err := invalid(noopSource(), bindNoop, bindNoop)

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// TestHandlerRejectsInvalidEvents checks the zero Event and bad keys.
func TestHandlerRejectsInvalidEvents(t *testing.T) {
	t.Parallel()

	decode := func(envelope string) (string, error) { return envelope, nil }

	bad := []Event[string, string]{
		{},
		NewEvent("", decode),
		NewEvent("line\nbreak", decode),
		NewEvent[string, string]("event", nil),
	}

	for _, event := range bad {
		if event.Err() == nil || !errors.Is(event.Err(), ErrInvalid) {
			t.Fatalf("event %q error = %v, want ErrInvalid", event.Key(), event.Err())
		}

		err := invalid(noopSource(), func(receiver *Builder[string]) {
			receiver.Handle(event, func(context.Context, Delivery[string]) error { return nil })
		})

		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Handle(%q) error = %v, want ErrInvalid", event.Key(), err)
		}
	}
}

// TestEventKeysAreOpaque checks provider keys with punctuation and length are valid.
func TestEventKeysAreOpaque(t *testing.T) {
	t.Parallel()

	decode := func(envelope string) (string, error) { return envelope, nil }

	for _, key := range []string{"payment_intent.succeeded", "pull_request", "Org/Repo:Event", strings.Repeat("k", 300)} {
		if err := NewEvent(key, decode).Err(); err != nil {
			t.Fatalf("key %q error = %v", key, err)
		}
	}
}

// TestHandlerRejectsNilAccept checks a nil handler.
func TestHandlerRejectsNilAccept(t *testing.T) {
	t.Parallel()

	err := invalid(noopSource(), func(receiver *Builder[string]) {
		receiver.Handle(noopEvent, nil)
	})

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// TestDeduplicateRejectsNilCache checks diagnostics identify the missing cache
// rather than listing unrelated requirements.
func TestDeduplicateRejectsNilCache(t *testing.T) {
	t.Parallel()

	err := invalid(noopSource(), bindNoop, func(receiver *Builder[string]) {
		receiver.Deduplicate(nil, cache.NewNamespace[bool]("billing"))
	})

	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cache must not be nil") {
		t.Fatalf("error = %v, want invalid nil cache diagnostic", err)
	}
}

// TestDeduplicateRejectsZeroNamespace checks an omitted identity is invalid,
// rather than silently using a shared namespace.
func TestDeduplicateRejectsZeroNamespace(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())

	err := invalid(noopSource(), bindNoop, func(receiver *Builder[string]) {
		receiver.Deduplicate(store, cache.Namespace[bool]{})
	})

	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "invalid namespace") {
		t.Fatalf("error = %v, want invalid namespace diagnostic", err)
	}
}

// TestDeduplicateRejectsNegativeTTL checks the duration's invalid sign is
// reported separately from the required cache and namespace.
func TestDeduplicateRejectsNegativeTTL(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())

	err := invalid(noopSource(), bindNoop, func(receiver *Builder[string]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("billing", func(space *cache.NamespaceBuilder) {
			space.TTL(-time.Second)
		}))
	})

	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "invalid ttl") {
		t.Fatalf("error = %v, want invalid negative ttl diagnostic", err)
	}
}

// TestHandlerKeepsFirstSettingError checks later invalid and valid settings
// cannot replace the initial diagnostic, while later callbacks still execute.
func TestHandlerKeepsFirstSettingError(t *testing.T) {
	t.Parallel()

	var first error
	reached := false

	err := invalid(noopSource(), bindNoop, func(receiver *Builder[string]) {
		receiver.Deduplicate(nil, cache.NewNamespace[bool]("billing"))

		first = receiver.settings.err
	}, func(receiver *Builder[string]) {
		reached = true
		receiver.MaxBodySize(-1)
		receiver.MaxBodySize(1024)
		receiver.Deduplicate(cache.New(memorycache.New()), cache.NewNamespace[bool]("billing"))
	})

	if !reached || !errors.Is(err, first) {
		t.Fatalf("reached = %v error = %v, want first error %v", reached, err, first)
	}
}

// TestBuilderFailDoesNotFormatLaterErrors checks a discarded diagnostic cannot
// run its argument's formatting method after the first invalid setting.
func TestBuilderFailDoesNotFormatLaterErrors(t *testing.T) {
	t.Parallel()

	formatted := false
	receiver := &Builder[string]{}
	receiver.MaxBodySize(-1)
	receiver.fail("later %s", formatProbe{formatted: &formatted})

	if formatted {
		t.Fatal("later invalid setting formatted a discarded error")
	}
}

// formatProbe records formatting so the sticky-error test can distinguish
// preserving the first error from eagerly constructing a discarded one.
type formatProbe struct {
	// formatted is set only when String is invoked by error formatting.
	formatted *bool
}

// String records formatting without an error or side effect outside the test.
func (probe formatProbe) String() string {
	*probe.formatted = true

	return "later"
}

// TestHandlerRejectsNegativeBodyLimit keeps body-limit validation covered
// independently of the deduplication settings and their diagnostics.
func TestHandlerRejectsNegativeBodyLimit(t *testing.T) {
	t.Parallel()

	err := invalid(noopSource(), bindNoop, func(receiver *Builder[string]) {
		receiver.MaxBodySize(-1)
	})

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// TestHandlerRejectRefusesInvalidStatuses checks the allowed Reject statuses.
func TestHandlerRejectRefusesInvalidStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []int{200, 302, 500, http.StatusNotAcceptable, http.StatusGone, http.StatusRequestTimeout, http.StatusTooManyRequests} {
		err := invalid(noopSource(), bindNoop, func(receiver *Builder[string]) {
			receiver.Reject(status, errors.New("x"))
		})

		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("status %d error = %v, want ErrInvalid", status, err)
		}
	}
}

// TestHandlerRejectRefusesEmptyErrors checks Reject needs real errors.
func TestHandlerRejectRefusesEmptyErrors(t *testing.T) {
	t.Parallel()

	for _, errs := range [][]error{nil, {nil}} {
		err := invalid(noopSource(), bindNoop, func(receiver *Builder[string]) {
			receiver.Reject(http.StatusUnauthorized, errs...)
		})

		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("errors %v: error = %v, want ErrInvalid", errs, err)
		}
	}
}

// TestDispatchChecksLocallyVisibleConfiguration checks nil dispatcher, zero job.
func TestDispatchChecksLocallyVisibleConfiguration(t *testing.T) {
	t.Parallel()

	job := queue.NewJob[Delivery[string]]("event-job")
	dispatcher := queue.NewDispatcher(queuetest.New())
	nilDispatcher := invalid(noopSource(), func(receiver *Builder[string]) {
		receiver.Dispatch(noopEvent, nil, job)
	})

	zeroJob := invalid(noopSource(), func(receiver *Builder[string]) {
		receiver.Dispatch(noopEvent, dispatcher, queue.Job[Delivery[string]]{})
	})

	if !errors.Is(nilDispatcher, ErrInvalid) || !errors.Is(zeroJob, ErrInvalid) {
		t.Fatalf("errors = %v, %v, want ErrInvalid", nilDispatcher, zeroJob)
	}
}
