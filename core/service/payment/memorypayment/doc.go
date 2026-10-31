// Package memorypayment simulates outbound payment operations in process.
// It performs no network requests, renewals, or webhook processing. State and
// idempotency keys last until Reset; this is not durable provider storage.
//
// Approval, completion, settlement, and subscription lifecycle are independent:
// create an open checkout, call ApproveCheckout, complete a payment checkout,
// then call ResolvePayment with a terminal outcome. Subscription creation
// records a pending subscription; approval completes enrollment but does not
// activate it. Completing an already complete checkout only observes its state,
// even with a new key. Tests use ResolveSubscription to change lifecycle without
// inventing paid invoices.
// PreparePayment instead creates only an unconfirmed pending payment and inert
// memory.authorization token. It never creates a hosted checkout; explicitly
// call ResolvePayment to simulate a subsequent provider outcome.
//
// Example:
//
//	driver := memorypayment.New()
//	request := payment.NewCheckoutRequest(payment.Money{Amount: 1200, Currency: "USD"},
//		"https://shop.example/paid", "order-1")
//	checkout, err := driver.CreateCheckout(ctx, request)
//	if err != nil { return err }
//	if err := driver.ApproveCheckout(ctx, checkout.ID); err != nil { return err }
package memorypayment
