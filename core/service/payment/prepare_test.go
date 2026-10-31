package payment_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/payment"
)

// TestPrepareBuilderPublishesDetachedValues checks both Money ownership
// boundaries and retained draft edits across all optional preparation settings.
func TestPrepareBuilderPublishesDetachedValues(t *testing.T) {
	t.Parallel()

	amount := payment.Money{Amount: 1200, Currency: "USD"}
	var retained *payment.PreparePaymentRequestBuilder
	request := payment.NewPreparePaymentRequest(amount, "prepare-1", func(details *payment.PreparePaymentRequestBuilder) {
		retained = details
		details.Description("Book").Reference("order-1").Channel(payment.ClientChannelIOS)
		details.ReturnURL("com.example.shop:/payment").DeviceReference("device-1")
	})

	amount.Amount = 1
	copy := request.Amount()
	copy.Amount = 2

	if copy.Amount == request.Amount().Amount {
		t.Fatal("Money getter did not detach")
	}

	retained.Description("changed").Reference("changed").Channel(payment.ClientChannelAndroid)
	retained.ReturnURL("https://other.example/paid").DeviceReference("changed")

	if request.Amount().Amount != 1200 || request.Description() != "Book" {
		t.Fatal("owned values changed")
	}

	if request.Reference() != "order-1" || request.Channel() != payment.ClientChannelIOS {
		t.Fatal("draft changed publication")
	}

	if request.ReturnURL() != "com.example.shop:/payment" || request.DeviceReference() != "device-1" {
		t.Fatal("retained draft changed callback or device reference")
	}

	if request.IdempotencyKey() != "prepare-1" || request.Err() != nil {
		t.Fatal("published required values changed")
	}
}

// TestPrepareCallbacksRunOnceInOrderOnInvalidInput checks direct ordered execution
// and required-input error precedence, including after valid optional edits.
func TestPrepareCallbacksRunOnceInOrderOnInvalidInput(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *payment.PreparePaymentRequestBuilder
	request := payment.NewPreparePaymentRequest(payment.Money{}, "", func(details *payment.PreparePaymentRequestBuilder) {
		calls++
		retained = details
		details.Description("first")
	}, func(details *payment.PreparePaymentRequestBuilder) {
		calls++

		if retained != details || calls != 2 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		details.Description("second")
	})

	if calls != 2 || request.Description() != "second" {
		t.Fatal("callback execution changed")
	}

	if !strings.Contains(request.Err().Error(), "positive") {
		t.Fatal("required error lost precedence")
	}
}

// TestPrepareFirstInvalidSettingIsSticky checks that later invalid/valid setters
// never replace the first diagnostic or turn a declaration into valid input.
func TestPrepareFirstInvalidSettingIsSticky(t *testing.T) {
	t.Parallel()

	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1",
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Reference("bad\n").Reference("valid")
			details.Channel("unknown").Channel(payment.ClientChannelWeb)
			details.ReturnURL("javascript:alert(1)").ReturnURL("https://shop.example/paid")
		})

	if !errors.Is(request.Err(), payment.ErrInvalidRequest) {
		t.Fatal("invalid settings repaired")
	}

	if !strings.Contains(request.Err().Error(), "reference") {
		t.Fatal("first invalid setting replaced")
	}
}

// TestPrepareAcceptsOptionalCallbackURIs keeps safe hosted destinations and native
// SDK callback syntax separate from application scheme/host authorization.
func TestPrepareAcceptsOptionalCallbackURIs(t *testing.T) {
	t.Parallel()

	for _, location := range []string{"", "https://shop.example/paid", "http://localhost:8080/paid",
		"http://127.0.0.1/paid", "http://[::1]/paid", "com.example.shop:/payment", "myapp://payment/return", "myapp:payment"} {
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1",
			func(details *payment.PreparePaymentRequestBuilder) {
				details.ReturnURL(location)
			})

		if err := request.Err(); err != nil {
			t.Fatalf("callback %q: %v", location, err)
		}

		if request.ReturnURL() != location {
			t.Fatal("callback normalized")
		}
	}
}

// TestPrepareRejectsUnsafeCallbackURIs rejects executable/content schemes,
// credentials, controls, remote cleartext HTTP, and malformed destinations.
func TestPrepareRejectsUnsafeCallbackURIs(t *testing.T) {
	t.Parallel()

	for _, location := range []string{"/relative", "//host/path", "http://shop.example/paid", "http://127.0.0.1.example/paid",
		"javascript:alert(1)", "JAVASCRIPT:alert(1)", "data:text/plain,test", "file:///private/file", "content://device/1",
		"myapp://user:password@host/paid", "myapp://host/%0a", "myapp://host/%C2%85", "myapp://host/\n",
		"myapp://host/paid#", "myapp://host/paid#fragment",
		"myapp:", "myapp://host/with space", "myapp://host/%zz", "https://user:password@shop.example/paid"} {
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1",
			func(details *payment.PreparePaymentRequestBuilder) {
				details.ReturnURL(location)
			})

		if !errors.Is(request.Err(), payment.ErrInvalidRequest) {
			t.Fatalf("unsafe callback accepted: %q", location)
		}
	}
}

// TestPrepareRejectsUnsafeDeviceReferences applies the opaque 255-byte policy,
// not a rune count or generic provider-specific metadata escape hatch.
func TestPrepareRejectsUnsafeDeviceReferences(t *testing.T) {
	t.Parallel()

	for _, reference := range []string{"two words", "device\n", "device\x00", "device\u2003", "\xff", strings.Repeat("a", 256)} {
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1",
			func(details *payment.PreparePaymentRequestBuilder) {
				details.DeviceReference(reference)
			})

		if !errors.Is(request.Err(), payment.ErrInvalidRequest) {
			t.Fatal("unsafe device reference accepted")
		}
	}
}

// TestPrepareDeviceReferenceAndChannelLimits accepts omission and exact byte
// limits while constraining client platform values to the portable vocabulary.
func TestPrepareDeviceReferenceAndChannelLimits(t *testing.T) {
	t.Parallel()

	for _, channel := range []payment.ClientChannel{"", payment.ClientChannelWeb, payment.ClientChannelIOS, payment.ClientChannelAndroid} {
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1",
			func(details *payment.PreparePaymentRequestBuilder) {
				details.Channel(channel).DeviceReference(strings.Repeat("a", 255))
			})

		if err := request.Err(); err != nil {
			t.Fatal(err)
		}
	}

	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1",
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Channel("windows")
		})

	if !errors.Is(request.Err(), payment.ErrInvalidRequest) {
		t.Fatal("unknown platform accepted")
	}
}

// TestPreparationContextPrecedesInvalidRequest checks nil, canceled, and expired
// contexts before sticky domain errors, without any driver dispatch.
func TestPreparationContextPrecedesInvalidRequest(t *testing.T) {
	t.Parallel()

	request := payment.PreparePaymentRequest{}
	var emptyContext context.Context

	if !errors.Is(request.Validate(emptyContext), payment.ErrInvalidRequest) {
		t.Fatal("nil context accepted")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if !errors.Is(request.Validate(ctx), context.Canceled) {
		t.Fatal("domain error displaced cancellation")
	}

	if !errors.Is(payment.SessionID("").Validate(ctx), context.Canceled) {
		t.Fatal("session error displaced cancellation")
	}

	expired, release := context.WithDeadline(t.Context(), time.Time{})
	t.Cleanup(release)

	if !errors.Is(request.Validate(expired), context.DeadlineExceeded) {
		t.Fatal("expired context displaced")
	}
}

// TestRequestConstructorsPropagateNilCallbacks documents misuse consistently for
// each request draft family; nil slices still mean no optional edits.
func TestRequestConstructorsPropagateNilCallbacks(t *testing.T) {
	t.Parallel()

	for _, construct := range []func(){
		func() {
			payment.NewCheckoutRequest(payment.Money{}, "", "", nil)
		},
		func() {
			payment.NewSubscriptionCheckoutRequest("", "", "", nil)
		},
		func() {
			payment.NewCancelSubscriptionRequest("", nil)
		},
		func() {
			payment.NewPreparePaymentRequest(payment.Money{}, "", nil)
		},
	} {
		requireConstructionPanic(t, construct)
	}
}

// requireConstructionPanic observes direct application/misuse panic propagation.
// It does not recover panics in production constructors or turn them into errors.
func requireConstructionPanic(t *testing.T, construct func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Error("constructor swallowed callback panic")
		}
	}()
	construct()
}

// TestRequestConstructorsAcceptExpandedNilCallbacks treats an expanded nil slice
// as omission, unlike a supplied nil function entry, for all draft families.
func TestRequestConstructorsAcceptExpandedNilCallbacks(t *testing.T) {
	t.Parallel()

	amount := payment.Money{Amount: 100, Currency: "USD"}
	var checkout []payment.CheckoutRequestBuilderFunc
	var subscription []payment.SubscriptionCheckoutRequestBuilderFunc
	var cancellation []payment.CancelSubscriptionRequestBuilderFunc
	var preparation []payment.PreparePaymentRequestBuilderFunc
	requests := []interface{ Err() error }{
		payment.NewCheckoutRequest(amount, "https://shop.example/paid", "order-1", checkout...),
		payment.NewSubscriptionCheckoutRequest("plan-1", "https://shop.example/account", "signup-1", subscription...),
		payment.NewCancelSubscriptionRequest("sub_1", cancellation...),
		payment.NewPreparePaymentRequest(amount, "prepare-1", preparation...),
	}

	for _, request := range requests {
		if err := request.Err(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRequestConstructorsPropagateApplicationPanic checks that user callback
// failures are never recovered, translated, or hidden as domain validation.
func TestRequestConstructorsPropagateApplicationPanic(t *testing.T) {
	t.Parallel()

	for _, construct := range []func(){
		func() {
			payment.NewCheckoutRequest(payment.Money{}, "", "", func(details *payment.CheckoutRequestBuilder) {
				panic("application failure")
			})
		},
		func() {
			payment.NewSubscriptionCheckoutRequest("", "", "", func(details *payment.SubscriptionCheckoutRequestBuilder) {
				panic("application failure")
			})
		},
		func() {
			payment.NewCancelSubscriptionRequest("", func(details *payment.CancelSubscriptionRequestBuilder) {
				panic("application failure")
			})
		},
		func() {
			payment.NewPreparePaymentRequest(payment.Money{}, "", func(details *payment.PreparePaymentRequestBuilder) {
				panic("application failure")
			})
		},
	} {
		requireConstructionPanic(t, construct)
	}
}

// TestPreparePaymentRequestCodec checks every portable handoff field. Optional
// native callbacks belong here, not in the stricter hosted checkout payload.
func TestPreparePaymentRequestCodec(t *testing.T) {
	t.Parallel()

	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "prepare-1",
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Description("Book").Reference("order-1").Channel(payment.ClientChannelIOS)
			details.ReturnURL("com.example.shop:/payment").DeviceReference("device-1")
		})

	checkRequestCodec(t, request, `{"amount":{"amount":1200,"currency":"USD"},"idempotency_key":"prepare-1","description":"Book","reference":"order-1","channel":"ios","return_url":"com.example.shop:/payment","device_reference":"device-1"}`)
}
