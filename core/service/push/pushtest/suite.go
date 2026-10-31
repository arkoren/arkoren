package pushtest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/push"
)

// suite owns the factory shared by independently provisioned conformance
// scenarios. A factory must supply an accepted driver-specific recipient.
type suite struct {
	// newDriver returns a ready backend and a canonical value recipient.
	newDriver func(t *testing.T) (push.Driver, push.Recipient)
}

// Run verifies portable direct-driver submission requirements. Each subtest
// gets a fresh ready driver and a valid recipient from newDriver; arrange any
// infrastructure cleanup with t.Cleanup. Receipts may legitimately have no ID.
// The suite checks validation, context errors, ownership, raw retention, and
// concurrency, not actual device delivery or provider-specific wire encoding.
//
// Example:
//
//	func TestDriver(t *testing.T) {
//		pushtest.Run(t, func(t *testing.T) (push.Driver, push.Recipient) {
//			return pushtest.New(), push.Token("device-registration")
//		})
//	}
func Run(t *testing.T, newDriver func(t *testing.T) (push.Driver, push.Recipient)) {
	t.Helper()

	checks := suite{newDriver: newDriver}

	t.Run("AcceptsMessage", checks.acceptsMessage)
	t.Run("RejectsZeroMessage", checks.rejectsZeroMessage)
	t.Run("RejectsInvalidDeclaration", checks.rejectsInvalidDeclaration)
	t.Run("RejectsNilContext", checks.rejectsNilContext)
	t.Run("RejectsCanceledContext", checks.rejectsCanceledContext)
	t.Run("RejectsExpiredDeadline", checks.rejectsExpiredDeadline)
	t.Run("PreservesOwnership", checks.preservesOwnership)
	t.Run("AcceptsRawNoStore", checks.acceptsRawNoStore)
	t.Run("RejectsNegativeRawTTL", checks.rejectsNegativeRawTTL)
	t.Run("ConcurrentSend", checks.concurrentSend)
}

// acceptsMessage exercises required and optional content with explicit
// retention. It deliberately does not require a provider receipt ID.
func (checks suite) acceptsMessage(t *testing.T) {
	driver, recipient := checks.newDriver(t)
	message := push.NewMessage(recipient, "Order ready", func(notification *push.MessageBuilder) {
		notification.Body("Collect your order.").Data(map[string]string{"order": "42"})
	})

	if _, err := driver.Send(t.Context(), message, push.SendConfig{TTL: time.Hour}); err != nil {
		t.Fatalf("Send valid message: %v", err)
	}
}

// rejectsZeroMessage requires portable validation and an empty error receipt
// before provider I/O, including for a completely uninitialized Message.
func (checks suite) rejectsZeroMessage(t *testing.T) {
	driver, _ := checks.newDriver(t)

	receipt, err := driver.Send(t.Context(), push.Message{}, push.SendConfig{})

	if !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("Send zero message error = %v", err)
	}

	if receipt != (push.Receipt{}) {
		t.Fatal("failed Send returned a receipt")
	}
}

// rejectsInvalidDeclaration checks sticky declaration errors, not just the
// all-zero sentinel case, before a driver reaches its provider backend.
func (checks suite) rejectsInvalidDeclaration(t *testing.T) {
	driver, recipient := checks.newDriver(t)
	message := push.NewMessage(recipient, "Ready", func(notification *push.MessageBuilder) {
		notification.Body("\xff").Body("valid replacement")
	})

	receipt, err := driver.Send(t.Context(), message, push.SendConfig{})

	if !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("Send invalid declaration error = %v", err)
	}

	if receipt != (push.Receipt{}) {
		t.Fatal("failed Send returned a receipt")
	}
}

// rejectsNilContext requires safe preflight rather than dereferencing nil or
// silently substituting a background context and sending a notification.
func (checks suite) rejectsNilContext(t *testing.T) {
	driver, recipient := checks.newDriver(t)
	var ctx context.Context

	receipt, err := driver.Send(ctx, push.NewMessage(recipient, "Ready"), push.SendConfig{})

	if !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("Send nil context error = %v", err)
	}

	if receipt != (push.Receipt{}) {
		t.Fatal("failed Send returned a receipt")
	}
}

// rejectsCanceledContext checks that the original cancellation identity is
// preserved and that an already canceled request cannot be accepted.
func (checks suite) rejectsCanceledContext(t *testing.T) {
	driver, recipient := checks.newDriver(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	receipt, err := driver.Send(ctx, push.NewMessage(recipient, "Ready"), push.SendConfig{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send canceled context error = %v", err)
	}

	if receipt != (push.Receipt{}) {
		t.Fatal("failed Send returned a receipt")
	}
}

// rejectsExpiredDeadline uses an already expired fixed deadline without real
// waits, requiring preservation of context.DeadlineExceeded.
func (checks suite) rejectsExpiredDeadline(t *testing.T) {
	driver, recipient := checks.newDriver(t)
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	t.Cleanup(cancel)

	receipt, err := driver.Send(ctx, push.NewMessage(recipient, "Ready"), push.SendConfig{})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send expired deadline error = %v", err)
	}

	if receipt != (push.Receipt{}) {
		t.Fatal("failed Send returned a receipt")
	}
}

// preservesOwnership checks message and config values across a submission,
// including mutations of original and inspected metadata containers.
func (checks suite) preservesOwnership(t *testing.T) {
	driver, recipient := checks.newDriver(t)
	data := map[string]string{"order": "42"}
	message := push.NewMessage(recipient, "Ready", func(notification *push.MessageBuilder) {
		notification.Data(data)
	})

	data["order"] = "caller mutation"
	config := push.SendConfig{TTL: time.Hour}

	if _, err := driver.Send(t.Context(), message, config); err != nil {
		t.Fatalf("Send: %v", err)
	}

	inspected := message.Data()
	inspected["order"] = "getter mutation"

	if !reflect.DeepEqual(message.Data(), map[string]string{"order": "42"}) {
		t.Fatal("message data changed after Send or external mutation")
	}

	if message.To() != recipient || message.Title() != "Ready" || config.TTL != time.Hour {
		t.Fatal("Send changed message or config")
	}
}

// acceptsRawNoStore checks that the zero raw config is a supported request,
// not negative retention or a reason to apply a facade default. Actual native
// encoding of no-store requires focused tests in each driver package.
func (checks suite) acceptsRawNoStore(t *testing.T) {
	driver, recipient := checks.newDriver(t)

	if _, err := driver.Send(t.Context(), push.NewMessage(recipient, "Ready"), push.SendConfig{}); err != nil {
		t.Fatalf("Send raw no-store: %v", err)
	}
}

// rejectsNegativeRawTTL distinguishes raw configuration from facade options:
// negative raw retention is invalid rather than another no-store spelling.
func (checks suite) rejectsNegativeRawTTL(t *testing.T) {
	driver, recipient := checks.newDriver(t)

	receipt, err := driver.Send(t.Context(), push.NewMessage(recipient, "Ready"), push.SendConfig{TTL: -1})

	if !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("Send negative raw TTL error = %v", err)
	}

	if receipt != (push.Receipt{}) {
		t.Fatal("failed Send returned a receipt")
	}
}

// concurrentSend shares one immutable declaration and raw driver across
// workers. Completion is joined before reporting errors or factory cleanup.
func (checks suite) concurrentSend(t *testing.T) {
	driver, recipient := checks.newDriver(t)
	message := push.NewMessage(recipient, "Ready")
	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			if _, err := driver.Send(t.Context(), message, push.SendConfig{TTL: time.Hour}); err != nil {
				t.Errorf("concurrent Send: %v", err)
			}
		})
	}

	workers.Wait()
}
