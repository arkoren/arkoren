package mailtest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/mail"
)

// Fake is a [mail.Driver] that records messages instead of sending them.
//
// Fake validates messages like real drivers and returns receipts with
// sequential IDs. It is safe for concurrent use.
type Fake struct {
	// mutex guards every field below.
	mutex sync.Mutex

	// sent holds immutable accepted messages in send order.
	sent []mail.Message

	// failWith fails every Send until cleared.
	failWith error

	// failNext fails the next Send only.
	failNext error
}

var _ mail.Driver = (*Fake)(nil)

// New creates an empty [Fake].
func New() *Fake {
	return &Fake{}
}

// Send validates message, records its immutable value, and returns a receipt.
// Message owns its private storage and getters detach their results, so recorded
// history cannot be changed through the caller's value. An active [Fake.FailNext]
// or [Fake.FailWith] error prevents recording and is returned unchanged.
//
// Example:
//
//	fake := mailtest.New()
//	if _, err := fake.Send(ctx, message); err != nil {
//		return err
//	}
//	fake.AssertSentCount(t, 1)
func (fake *Fake) Send(ctx context.Context, message mail.Message) (mail.Receipt, error) {
	if err := mail.CheckSend(ctx, message); err != nil {
		return mail.Receipt{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	if err := fake.takeFailure(); err != nil {
		return mail.Receipt{}, err
	}

	fake.sent = append(fake.sent, message)

	return mail.Receipt{ID: fmt.Sprintf("fake-%d", len(fake.sent))}, nil
}

// takeFailure returns the injected error to use for this call and consumes
// a pending FailNext error. The caller holds the mutex.
func (fake *Fake) takeFailure() error {
	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failWith
}

// FailWith makes every following Send return err until FailWith(nil).
func (fake *Fake) FailWith(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.failWith = err
}

// FailNext makes the next Send return err.
func (fake *Fake) FailNext(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.failNext = err
}

// Reset clears recorded messages and injected failures.
func (fake *Fake) Reset() {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.sent = nil
	fake.failWith = nil
	fake.failNext = nil
}

// Sent returns accepted immutable messages in send order in a detached slice.
// Replacing entries, editing getter results or modifying attachment getter bytes
// cannot change history. Empty history returns an empty slice.
//
// Example:
//
//	for _, message := range fake.Sent() {
//		fmt.Println(message.Subject())
//	}
func (fake *Fake) Sent() []mail.Message {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	messages := make([]mail.Message, len(fake.sent))
	copy(messages, fake.sent)

	return messages
}

// AssertSent fails t unless at least one recorded message satisfies every
// predicate. Without predicates any message matches.
func (fake *Fake) AssertSent(t testing.TB, preds ...func(mail.Message) bool) {
	t.Helper()

	if !fake.anySent(preds) {
		t.Errorf("no sent message matches the predicate (%d sent)", len(fake.Sent()))
	}
}

// AssertNotSent fails t if any recorded message satisfies every predicate.
// Without predicates any message matches.
func (fake *Fake) AssertNotSent(t testing.TB, preds ...func(mail.Message) bool) {
	t.Helper()

	if fake.anySent(preds) {
		t.Errorf("a sent message matches the predicate")
	}
}

// anySent reports whether a recorded message satisfies every predicate.
func (fake *Fake) anySent(preds []func(mail.Message) bool) bool {
	return slices.ContainsFunc(fake.Sent(), func(message mail.Message) bool {
		return !slices.ContainsFunc(preds, func(pred func(mail.Message) bool) bool {
			return !pred(message)
		})
	})
}

// AssertSentCount fails t unless exactly n messages were recorded.
func (fake *Fake) AssertSentCount(t testing.TB, n int) {
	t.Helper()

	if count := len(fake.Sent()); count != n {
		t.Errorf("sent %d messages, want %d", count, n)
	}
}

// AssertNothingSent fails t if any message was recorded.
func (fake *Fake) AssertNothingSent(t testing.TB) {
	t.Helper()

	fake.AssertSentCount(t, 0)
}
