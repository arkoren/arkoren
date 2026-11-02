---
title: Testing
description: Assert application submissions with the push fake and verify browser delivery through separate acceptance checkpoints.
order: 50
summary:
  - Assert recipient, message content, and resolved retention without external requests.
  - Inject uncertain acceptance and preserve cancellation causes without treating failures as delivery evidence.
  - Verify browser registration, display, clicks, and revocation through separate manual checkpoints.
---
# Testing

Test notification submission without asking for browser permission or contacting
a provider. `pushtest.Fake` records accepted messages and resolved retention so
you can inspect the application's intent. It does not prove encryption,
provider acceptance, delivery, or service-worker display; those need a separate
browser check.

## Asserting Submissions

The fake is included in `arkoren.dev/core` and requires no credentials or network
access. In your application's `push_test.go`, import these packages and define
the small input used by the test:

```go
package notification_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/push"
	"arkoren.dev/core/service/push/pushtest"
)

type OrderNotice struct {
	OrderID string
	Path    string
}
```

Construct a fresh fake per test and inject its sender into your application.
This complete test demonstrates the submission boundary directly; replace its
`Send` call and message construction with your order service invocation when
testing that service's behavior:

```go
func TestOrderNotice(t *testing.T) {
	t.Parallel()
	fake := pushtest.New()
	sender := push.New(fake)
	recipient := push.Token("test-device")
	notice := OrderNotice{OrderID: "123", Path: "/orders/123"}
	message := push.NewMessage(recipient, "Order shipped", func(notification *push.MessageBuilder) {
		notification.Body("Your order is on its way.")
		notification.Data(map[string]string{"order_id": notice.OrderID, "url": notice.Path})
	})
	if _, err := sender.Send(t.Context(), message, push.TTL(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fake.AssertSentCount(t, 1)
	fake.AssertSent(t, func(submission pushtest.Submission) bool {
		return submission.Message.To() == recipient &&
			submission.Message.Title() == "Order shipped" &&
			submission.Message.Body() == "Your order is on its way." &&
			submission.Message.Data()["order_id"] == notice.OrderID &&
			submission.Message.Data()["url"] == notice.Path &&
			submission.Config.TTL == 5*time.Minute
	})
}
```

The count excludes unexpected additional submissions; the predicate checks the
recipient, content, and raw retention received by the driver. No start, stop, or
cleanup hook is needed. Synthetic `push.Token` input is convenient here but does
not imply that Web Push supports native tokens.

`AssertSent` requires one recorded submission to match every supplied predicate.
`AssertNotSent` fails if any submission matches them all; with no predicates it
requires no recordings. `AssertNothingSent` checks the same empty state.
`Sent()` returns an independent slice in acceptance order, and message metadata
accessors return copies. Assertion failures do not print sensitive content.

## Testing Uncertainty

Your application must not mistake cancellation for proof of non-acceptance. Add
this second complete test to the same file to preserve both causes and verify
that failure returns no receipt or accepted recording:

```go
func TestUnknownAcceptance(t *testing.T) {
	t.Parallel()
	fake := pushtest.New()
	fake.FailNext(errors.Join(context.Canceled, push.ErrOutcomeUnknown))
	sender := push.New(fake)
	message := push.NewMessage(push.Token("test-device"), "Order shipped")
	receipt, err := sender.Send(t.Context(), message)
	if !errors.Is(err, push.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatal("expected uncertain acceptance with preserved cancellation")
	}
	if receipt != (push.Receipt{}) {
		t.Fatal("failed submission returned a receipt")
	}
	fake.AssertNothingSent(t)
}
```

The fake does not claim that an unknown submission failed to reach a real
provider; it records only accepted test submissions. Adapt this injection to
your application's policy test and assert that it does not blindly replay an
uncertain notification or remove a refreshed registration.

`FailNext` replaces the next valid send's failure and takes precedence over
`FailWith`, which persists until cleared. Invalid input does not consume the
one-shot failure. To exercise persistent throttling with an existing `fake`:

```go
fake.FailWith(push.ErrRateLimited)
```

Subsequent valid sends return that failure until `fake.FailWith(nil)` clears it.
`FailNext(nil)` clears only the one-shot failure. `Reset()` clears both failure
settings and recordings, and acceptance IDs restart at `fake-1`; it does not
simulate device state or delivery.

Run `go test ./...` from your application module. These tests make no external
requests. If you implement a custom driver, the
[conformance suite](../testing/conformance-suites.md) and `pushtest.Run` check
portable validation, ownership, raw TTL, context errors, and concurrency—not
provider wire compatibility or actual device delivery.

## Verifying Browsers

Use the [browser walkthrough](browser-integration.md) on an HTTPS deployment with
a protected test account. Do not paste real endpoints or subscription keys into
logs or issue reports. Test supported browsers independently; on iOS and iPadOS
use the installed Home Screen app in the supported version context.

1. Click the enable button. Confirm permission and one stored registration owned
   by the signed-in account, with the correct VAPID identity and a version. A
   failed upload must not show the enabled status.
2. Submit a small notification using that registration and its matching signer.
   Check nil error separately from a visible notification; operating-system
   settings can suppress display after acceptance.
3. Click the notification. Confirm same-origin navigation and authorization on
   the order page. Send a cross-origin `data.url` and confirm fallback to the
   home page, not navigation to the other origin.
4. Restart the server with the same persisted signing key. Check that the existing
   registration still accepts sends. Exercise offline expiry and no-store without
   assuming that accepted notifications must eventually appear.
5. Revoke permission or unsubscribe and exercise application cleanup. Confirm
   that a definitive not-found response invalidates only the submitted version,
   and authentication errors do not delete live registrations.
6. Sign out and switch accounts on the same browser. Confirm the chosen ownership
   policy prevents implicit transfer or disclosure of the first account's updates.

This is a manual acceptance checklist, not a report of executed browser tests.
Neither passing fake assertions nor seeing one notification establishes
exactly-once delivery, an inbox, or guaranteed future display.

For recording, injection, and assertion operations, see the
[pushtest API reference](https://pkg.go.dev/arkoren.dev/core/service/push/pushtest).
