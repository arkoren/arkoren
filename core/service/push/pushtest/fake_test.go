package pushtest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/service/push"
	"arkoren.dev/core/service/push/pushtest"
)

func ExampleFake() {
	fake := pushtest.New()
	sender := push.New(fake)

	_, err := sender.Send(context.Background(), push.NewMessage(push.Token("native"), "Ready"), push.TTL(time.Hour))

	if err != nil {
		fmt.Println("submission failed")

		return
	}

	for _, submission := range fake.Sent() {
		fmt.Println(submission.Message.Title(), submission.Config.TTL)
	}
	// Output: Ready 1h0m0s
}

func TestFakeConforms(t *testing.T) {
	t.Parallel()

	pushtest.Run(t, func(t *testing.T) (push.Driver, push.Recipient) {
		return pushtest.New(), push.Token("native-registration")
	})
}

func TestFakeRecordsExactRawPolicyAndIndependentSnapshots(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	data := map[string]string{"order": "42"}
	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		notification.Data(data)
	})

	config := push.SendConfig{}

	receipt, err := fake.Send(t.Context(), message, config)

	if err != nil {
		t.Fatal(err)
	}

	if receipt.ID != "fake-1" {
		t.Fatal("unexpected acceptance ID")
	}

	data["order"] = "source mutation"
	sent := fake.Sent()
	sent[0].Message.Data()["order"] = "getter mutation"
	sent[0].Config.TTL = time.Hour
	sent[0].Message = push.Message{}
	config.TTL = time.Minute

	if fake.Sent()[0].Config.TTL != 0 || fake.Sent()[0].Message.Data()["order"] != "42" {
		t.Fatal("fake recording was mutated through external state")
	}

	fake.AssertSentCount(t, 1)
}

func TestFakeFailurePrecedenceAndClearing(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	message := push.NewMessage(push.Token("native"), "Ready")
	fake.FailWith(push.ErrRateLimited)
	fake.FailNext(push.ErrUnavailable)

	receipt, err := fake.Send(t.Context(), message, push.SendConfig{})

	if !errors.Is(err, push.ErrUnavailable) || receipt != (push.Receipt{}) {
		t.Fatalf("one-shot failure = %v, %v", receipt, err)
	}

	receipt, err = fake.Send(t.Context(), message, push.SendConfig{})

	if !errors.Is(err, push.ErrRateLimited) || receipt != (push.Receipt{}) {
		t.Fatalf("persistent failure = %v, %v", receipt, err)
	}

	fake.AssertNothingSent(t)
	fake.FailWith(nil)
	fake.FailNext(push.ErrUnavailable)
	fake.FailNext(nil)

	if _, err := fake.Send(t.Context(), message, push.SendConfig{}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSentCount(t, 1)
}

func TestFakeInvalidPreflightDoesNotConsumeFailure(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	fake.FailNext(push.ErrUnavailable)

	if _, err := fake.Send(t.Context(), push.Message{}, push.SendConfig{}); !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("invalid message error = %v", err)
	}

	message := push.NewMessage(push.Token("native"), "Ready")

	if _, err := fake.Send(t.Context(), message, push.SendConfig{}); !errors.Is(err, push.ErrUnavailable) {
		t.Fatalf("one-shot failure was consumed by preflight: %v", err)
	}

	if _, err := fake.Send(t.Context(), message, push.SendConfig{}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSentCount(t, 1)
}

func TestFakeAssertionsMatchAllPredicatesOutsideLock(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	message := push.NewMessage(push.Token("native"), "Ready")

	if _, err := fake.Send(t.Context(), message, push.SendConfig{TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSent(t,
		func(submission pushtest.Submission) bool {
			return submission.Message.Title() == "Ready" && len(fake.Sent()) == 1
		},
		func(submission pushtest.Submission) bool {
			return submission.Config.TTL == time.Hour
		})
	fake.AssertNotSent(t,
		func(submission pushtest.Submission) bool {
			return submission.Message.Title() == "Ready"
		},
		func(submission pushtest.Submission) bool {
			return submission.Config.TTL == 0
		})
	fake.AssertSent(t)
	fake.AssertSentCount(t, 1)
}

func TestFakeResetClearsRecordingsAndFailures(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	message := push.NewMessage(push.Token("native"), "Ready")

	if _, err := fake.Send(t.Context(), message, push.SendConfig{}); err != nil {
		t.Fatal(err)
	}

	fake.FailNext(push.ErrUnavailable)
	fake.FailWith(push.ErrRateLimited)
	fake.Reset()

	fake.AssertNothingSent(t)
	fake.AssertNotSent(t)

	if fake.Sent() != nil {
		t.Fatal("reset retained recording slice")
	}

	receipt, err := fake.Send(t.Context(), message, push.SendConfig{})

	if err != nil || receipt.ID != "fake-1" {
		t.Fatalf("reset acceptance = %v, %v", receipt, err)
	}
}

func TestFakeZeroValueWorks(t *testing.T) {
	t.Parallel()

	var fake pushtest.Fake

	_, err := fake.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"), push.SendConfig{})

	if err != nil {
		t.Fatal(err)
	}

	fake.AssertSentCount(t, 1)
}
