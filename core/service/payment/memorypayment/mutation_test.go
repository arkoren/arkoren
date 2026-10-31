package memorypayment_test

import (
	"encoding/json/v2"
	"errors"
	"sync"
	"testing"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/memorypayment"
)

// TestPreparationCreatesOnlyUnconfirmedPendingPayment keeps preparation separate
// from approval, hosted completion, and settlement in the simulator.
func TestPreparationCreatesOnlyUnconfirmedPendingPayment(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "prepare-1")

	prepared, err := driver.PreparePayment(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	if prepared.CheckoutID != "" || prepared.SessionID != "" {
		t.Fatal("invented hosted or SDK session")
	}

	authorization, ok := prepared.Action.Authorization()

	if !ok || authorization.Protocol != "memory.authorization" {
		t.Fatal("not an inert memory handoff")
	}

	observed, err := driver.GetPayment(t.Context(), prepared.PaymentID)

	if err != nil {
		t.Fatal(err)
	}

	if observed.Status != payment.StatusPending || observed.NativeStatus != "unconfirmed" {
		t.Fatal("preparation implied confirmation or settlement")
	}

	if observed.Amount != request.Amount() {
		t.Fatal("prepared amount changed")
	}
}

// TestPreparationResolutionIsExplicit verifies that only the existing simulator
// control changes the pending payment, while key replay retains initialization.
func TestPreparationResolutionIsExplicit(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "prepare-1")

	prepared, err := driver.PreparePayment(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.ResolvePayment(t.Context(), prepared.PaymentID, payment.StatusSucceeded); err != nil {
		t.Fatal(err)
	}

	observed, err := driver.GetPayment(t.Context(), prepared.PaymentID)

	if err != nil || observed.Status != payment.StatusSucceeded {
		t.Fatal("explicit resolution failed")
	}

	replayed, err := driver.PreparePayment(t.Context(), request)

	if err != nil || replayed != prepared {
		t.Fatal("replay changed original handoff")
	}
}

// TestPreparationReplaysSemanticDecodedPayload compares values after publication
// and JSON decoding, never internal pointers or construction error identities.
func TestPreparationReplaysSemanticDecodedPayload(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "prepare-1",
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Description("Book").Reference("order-1").Channel(payment.ClientChannelIOS)
			details.ReturnURL("com.example.shop:/payment").DeviceReference("device-1")
		})

	original, err := driver.PreparePayment(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	wire, err := json.Marshal(request)

	if err != nil {
		t.Fatal(err)
	}

	var decoded payment.PreparePaymentRequest

	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}

	replayed, err := driver.PreparePayment(t.Context(), decoded)

	if err != nil || replayed != original {
		t.Fatal("semantic request did not replay")
	}
}

// TestPreparationChangedOptionalPayloadRejectsKeyReuse ensures risk correlation
// and client options are part of deduplication, not ignored provider hints.
func TestPreparationChangedOptionalPayloadRejectsKeyReuse(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	amount := payment.Money{Amount: 1200, Currency: "USD"}

	_, err := driver.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(amount, "prepare-1"))

	if err != nil {
		t.Fatal(err)
	}

	changed := payment.NewPreparePaymentRequest(amount, "prepare-1", func(details *payment.PreparePaymentRequestBuilder) {
		details.DeviceReference("different-device")
	})

	if _, err := driver.PreparePayment(t.Context(), changed); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("changed preparation payload replayed")
	}
}

// TestConcurrentPreparationReplaysOnePayment checks atomic deduplication without
// background work, sleeps, provider confirmation, or a second financial resource.
func TestConcurrentPreparationReplaysOnePayment(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "prepare-1")
	results := make(chan payment.Preparation, 16)
	var workers sync.WaitGroup

	for range cap(results) {
		workers.Go(func() {
			prepared, err := driver.PreparePayment(t.Context(), request)

			if err != nil {
				t.Error(err)

				return
			}

			results <- prepared
		})
	}

	workers.Wait()
	close(results)
	var original payment.Preparation

	for prepared := range results {
		if original.Action.Kind() == "" {
			original = prepared
		}

		if prepared != original {
			t.Fatal("concurrent requests created multiple handoffs")
		}
	}
}

// TestPreparationDoesNotCompleteHostedCheckout keeps independent hosted and SDK
// idempotency scopes from approving or capturing one another's resources.
func TestPreparationDoesNotCompleteHostedCheckout(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	amount := payment.Money{Amount: 1200, Currency: "USD"}

	checkout, err := driver.CreateCheckout(t.Context(), payment.NewCheckoutRequest(amount, "https://shop.example/paid", "shared-key"))

	if err != nil {
		t.Fatal(err)
	}

	prepared, err := driver.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(amount, "shared-key"))

	if err != nil {
		t.Fatal(err)
	}

	if prepared.PaymentID == "" || prepared.CheckoutID != "" {
		t.Fatal("preparation fabricated hosted linkage")
	}

	observed, err := driver.GetCheckout(t.Context(), checkout.ID)

	if err != nil || observed.Status != payment.CheckoutStatusOpen {
		t.Fatal("SDK preparation changed hosted approval")
	}

	_, err = driver.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest(checkout.ID, "capture-1"))

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatal("SDK preparation authorized hosted completion")
	}
}

// TestResetForgetsPreparationKeys retains the simulator's identity sequence but
// removes preparation replays and the pending payments they originally created.
func TestResetForgetsPreparationKeys(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "prepare-1")

	original, err := driver.PreparePayment(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	driver.Reset()

	_, err = driver.GetPayment(t.Context(), original.PaymentID)

	if !errors.Is(err, payment.ErrNotFound) {
		t.Fatal("reset retained prepared payment")
	}

	next, err := driver.PreparePayment(t.Context(), request)

	if err != nil || next.PaymentID == original.PaymentID {
		t.Fatal("reset retained preparation replay")
	}
}
