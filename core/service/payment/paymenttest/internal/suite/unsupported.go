package suite

import (
	"testing"

	"arkoren.dev/core/service/payment"
)

// checkUnsupportedSubscriptions exercises valid subscription requests without
// relying on plan setup or approval. Repeated calls must remain unsupported and
// must not fabricate resources. There is no subscription state to hydrate.
func checkUnsupportedSubscriptions(t *testing.T, driver payment.Driver) {
	t.Helper()

	ctx := t.Context()
	request := payment.NewSubscriptionCheckoutRequest("plan-test", "https://shop.example/account", "unsupported-signup")

	for range 2 {
		checkout, err := driver.CreateSubscriptionCheckout(ctx, request)

		requireError(t, err, payment.ErrUnsupported)

		if checkout.ID != "" || checkout.SubscriptionID != "" {
			t.Fatal("unsupported subscription creation returned a resource")
		}

		subscription, err := driver.GetSubscription(ctx, "subscription-test")

		requireError(t, err, payment.ErrUnsupported)

		if subscription.ID != "" {
			t.Fatal("unsupported subscription lookup returned a resource")
		}

		err = driver.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest("subscription-test"))

		requireError(t, err, payment.ErrUnsupported)
	}
}
