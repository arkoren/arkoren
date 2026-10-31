package pushtest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/push"
)

// Submission records an accepted immutable message and its resolved policy.
// Message inspection returns independent data; Config is a plain value.
//
// Example:
//
//	for _, submission := range fake.Sent() {
//		if submission.Config.TTL == 0 { _ = submission.Message.Title() }
//	}
type Submission struct {
	// Message is the accepted immutable declaration, never a live draft.
	// Its content is sensitive and is not printed by assertion failures.
	//
	// Example:
	//
	//	for _, submission := range fake.Sent() { _ = submission.Message.Title() }
	Message push.Message

	// Config is the exact raw policy received by Fake.Send, with no defaults.
	// Zero TTL records explicit no-store, not an omitted sender setting.
	//
	// Example:
	//
	//	for _, submission := range fake.Sent() { _ = submission.Config.TTL }
	Config push.SendConfig
}

// Fake is a concurrency-safe push.Driver that records accepted submissions.
// It performs portable preflight before consuming injected failures and returns
// sequential acceptance IDs, not delivery confirmations. Its zero value works.
//
// Example:
//
//	fake := pushtest.New()
//	_, err := push.New(fake).Send(ctx, push.NewMessage(push.Token("device-registration"), "Ready"))
//	if err != nil { return err }
//	fake.AssertSentCount(t, 1)
type Fake struct {
	// mutex guards all recordings and failure settings.
	mutex sync.Mutex

	// sent contains immutable values in acceptance order.
	sent []Submission

	// failWith is the persistent error, cleared by FailWith(nil) or Reset.
	failWith error

	// failNext takes precedence once, then leaves failWith unchanged.
	failNext error
}

// New creates an empty Fake without I/O, delivery work, or retained callbacks.
// Failure injection and recordings are owned by this instance.
//
// Example:
//
//	fake := pushtest.New()
//	fake.AssertNothingSent(t)
func New() *Fake {
	return &Fake{}
}

// Send validates message, context, and raw policy, then records acceptance.
// Invalid input does not consume FailNext. Injected failures return unchanged
// with a zero receipt and are not recorded. No defaults or retries are applied.
//
// Example:
//
//	_, err := fake.Send(ctx, push.NewMessage(push.Token("device-registration"), "Ready"), push.SendConfig{})
//	if err != nil { return err }
//	fake.AssertSent(t)
func (fake *Fake) Send(ctx context.Context, message push.Message, config push.SendConfig) (push.Receipt, error) {
	if err := push.CheckSend(ctx, message, config); err != nil {
		return push.Receipt{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	if err := fake.takeFailure(); err != nil {
		return push.Receipt{}, err
	}

	fake.sent = append(fake.sent, Submission{Message: message, Config: config})

	return push.Receipt{ID: fmt.Sprintf("fake-%d", len(fake.sent))}, nil
}

// takeFailure consumes one nonnil one-shot error before the persistent error.
// The caller must hold mutex; validation must already have succeeded.
func (fake *Fake) takeFailure() error {
	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failWith
}

// FailNext replaces the one-shot failure for the next valid Send.
// Nil clears it. One-shot failures take precedence over FailWith.
//
// Example:
//
//	fake.FailNext(push.ErrUnavailable)
func (fake *Fake) FailNext(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.failNext = err
}

// FailWith replaces the persistent error used by subsequent valid sends.
// Nil clears it; FailNext can temporarily override it without clearing it.
//
// Example:
//
//	fake.FailWith(push.ErrRateLimited)
//	fake.FailWith(nil)
func (fake *Fake) FailWith(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.failWith = err
}

// Reset clears recorded submissions and both injected failures atomically.
// Subsequent acceptance IDs restart at fake-1; no device delivery is simulated.
//
// Example:
//
//	fake.Reset()
//	fake.AssertNothingSent(t)
func (fake *Fake) Reset() {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.sent = nil
	fake.failNext = nil
	fake.failWith = nil
}

// Sent returns an independent slice in acceptance order, or nil when empty.
// Message values are immutable and their metadata getters clone; changing a
// returned record, Config, or inspected map cannot alter future snapshots.
//
// Example:
//
//	for _, submission := range fake.Sent() { _ = submission.Config.TTL }
func (fake *Fake) Sent() []Submission {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	return slices.Clone(fake.sent)
}

// AssertSent reports failure unless one accepted submission matches all
// predicates. No predicates matches any submission; nil predicates panic.
// Predicates run on a snapshot outside the lock and may inspect the fake.
//
// Example:
//
//	fake.AssertSent(t, func(submission pushtest.Submission) bool {
//		return submission.Message.Title() == "Ready"
//	})
func (fake *Fake) AssertSent(t testing.TB, predicates ...func(Submission) bool) {
	t.Helper()

	if !fake.anySent(predicates) {
		t.Error("no sent push submission matches the predicates")
	}
}

// AssertNotSent reports failure if any accepted submission matches all
// predicates. Without predicates it requires no accepted submissions.
// Assertion diagnostics never print sensitive content or recipient values.
//
// Example:
//
//	fake.AssertNotSent(t, func(submission pushtest.Submission) bool {
//		return submission.Message.Title() == "Unexpected"
//	})
func (fake *Fake) AssertNotSent(t testing.TB, predicates ...func(Submission) bool) {
	t.Helper()

	if fake.anySent(predicates) {
		t.Error("a sent push submission matches the predicates")
	}
}

// anySent matches immutable snapshot values outside the lock, avoiding a
// deadlock when a predicate calls another fake inspection method.
func (fake *Fake) anySent(predicates []func(Submission) bool) bool {
	return slices.ContainsFunc(fake.Sent(), func(submission Submission) bool {
		return !slices.ContainsFunc(predicates, func(predicate func(Submission) bool) bool {
			return !predicate(submission)
		})
	})
}

// AssertSentCount reports failure unless exactly count submissions succeeded.
// Rejections from preflight or failure injection do not contribute to the count.
//
// Example:
//
//	fake.AssertSentCount(t, 2)
func (fake *Fake) AssertSentCount(t testing.TB, count int) {
	t.Helper()

	if sent := len(fake.Sent()); sent != count {
		t.Errorf("sent %d push submissions, want %d", sent, count)
	}
}

// AssertNothingSent reports failure if any submission succeeded since creation
// or Reset. It checks acceptance only, not whether device delivery occurred.
//
// Example:
//
//	fake.AssertNothingSent(t)
func (fake *Fake) AssertNothingSent(t testing.TB) {
	t.Helper()

	fake.AssertSentCount(t, 0)
}
