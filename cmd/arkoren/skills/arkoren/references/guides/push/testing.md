---
title: Testing Push
nav: Testing
description: Assert submitted messages with pushtest.Fake and verify browser display separately from provider acceptance.
order: 40
---
# Testing Push

## Introduction

An application test should prove that an order update submits the right
notification without contacting a push service. `pushtest.Fake` records accepted
messages and their resolved TTL. A separate browser check verifies permissions,
encryption interoperability, service-worker display, and click behavior; a fake
cannot establish those outcomes.

## Asserting a submission

The fake is included in `arkoren.dev/core`; no vendor credentials or network
access are required. Add the following complete test as `push_test.go` in your
application. Replace the direct `Send` with your order service's call when
testing its integration:

```go
package notification_test

import (
	"context"
	"testing"
	"time"

	"arkoren.dev/core/service/push"
	"arkoren.dev/core/service/push/pushtest"
)

// TestOrderNotification checks content and resolved retention, not delivery.
// A token is convenient synthetic test data; Web Push itself requires a browser subscription.
func TestOrderNotification(t *testing.T) {
	t.Parallel()
	fake := pushtest.New()
	sender := push.New(fake)
	message := push.NewMessage(push.Token("test-device"), "Order shipped", func(notification *push.MessageBuilder) {
		notification.Body("Your order is on its way.")
		notification.Data(map[string]string{"order_id": "123", "url": "/orders/123"})
	})
	if _, err := sender.Send(context.Background(), message, push.TTL(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fake.AssertSentCount(t, 1)
	fake.AssertSent(t, func(submission pushtest.Submission) bool {
		return submission.Message.Title() == "Order shipped" &&
			submission.Message.Data()["order_id"] == "123" &&
			submission.Config.TTL == 5*time.Minute
	})
}
```

Run `go test ./...` in your application module. The test passes only if one
submission has the expected title, order identifier, and retention. Inspect
`submission.Message.To()` and `Body()` in additional predicates when recipient
selection and display text are part of your application's contract. Assertions
do not print message contents or subscription secrets on failure.

## Exercising failure policy

With the `fake` and `sender` from the preceding test, import `errors` and inject
an ambiguous outcome before the next valid send:

```go
fake.Reset()
fake.FailNext(push.ErrOutcomeUnknown)
_, err := sender.Send(context.Background(), message)
if !errors.Is(err, push.ErrOutcomeUnknown) {
	t.Fatal("expected unknown acceptance")
}
fake.AssertNothingSent(t)
```

`FailNext` affects one valid send. Invalid input does not consume it. `FailWith`
sets a persistent failure until cleared with nil; `Reset` clears both failure
settings and recordings. Failed sends are not recorded as accepted submissions.
These controls let you test that your application does not automatically replay
an ambiguous send or delete a refreshed registration after an older send fails.

The fake accepts both canonical recipient value types so application tests can
use synthetic `push.Token` values. That does not imply that the Web Push driver
supports tokens or that a native mobile push driver is installed.

## Verifying a browser

Use the [browser integration](browser-integration.md) with an HTTPS deployment
and a protected test account. Do not paste real subscription endpoints into issue
reports or application logs. Test Chrome, Firefox, and Safari independently;
on iOS and iPadOS, launch the installed Home Screen web app on version 16.4 or
later rather than an ordinary browser tab.

1. Click the enable button. Confirm that permission is granted and the application
   stores one registration for the current account, with a VAPID key identifier
   and version. A failed registration response must not produce an “enabled” UI.
2. Send a small notification using that stored subscription and matching VAPID
   private key. Confirm nil error separately from the visible notification.
3. Click the notification. Confirm that it opens your origin and that the order
   page checks authorization. Test a cross-origin `data.url`; it must fall back
   to your home page rather than navigate there.
4. Restart the server with the same persisted key. Confirm that the existing
   subscription still accepts sends. Test offline expiry and zero TTL without
   assuming that accepted notifications must appear later.
5. Revoke permission or unsubscribe, then test your registration cleanup flow.
   Confirm that a definitive 404/410 invalidates only the version used for that
   send, and an authentication error does not delete the subscription.
6. Sign out on a shared device. Confirm that the application applies its chosen
   registration ownership policy rather than sending one account's notifications
   to another account using the same browser.

This is a manual acceptance checklist, not a report of executed browser tests.
Operating-system notification settings can suppress display even after provider
acceptance. Diagnose each checkpoint rather than treating a receipt as display
confirmation.

For fake operations, see the [push API reference](testing.md).
