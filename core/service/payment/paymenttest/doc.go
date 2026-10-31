// Package paymenttest provides a scripted outbound fake and driver conformance
// tests. Neither the fake nor the suite contacts a provider automatically.
//
// Run always checks context and request validation. Journey tests additionally
// require deterministic provider fixtures through Harness hooks; WithoutApproval
// explicitly omits journeys instead of silently skipping approval. Standalone
// unsupported subscription and refund-lookup checks still use valid input.
// WithoutSubscriptions keeps one-off journeys without plans or activation hooks.
// Preparation checks run independently of hosted approval and require no plan
// or settlement hooks. WithoutPreparation requires a valid unsupported result,
// not an introspected capability or a skipped validation boundary. Session-only
// and reference-free handoffs are valid; no universal lookup is assumed.
// Harness amount and delayed-identity controls adapt fixtures, not contract
// semantics. Hooks run once and must be bounded. Live hosted approval must not
// be automated by guessing provider URLs.
//
// Fake is deliberately not a ledger. Queue operation-specific replies, inspect
// Attempted and Accepted snapshots, and use FailNext or FailWith for dispatch
// failures. Unscripted valid calls return ErrUnexpectedCall.
// ReplyPreparePayment scripts handoffs independently of hosted completion and
// financial reads. PreparedPayments and AssertPreparedPayment inspect accepted
// initialization, including uncertain acceptance, never successful settlement.
//
// Example:
//
//	fake := paymenttest.New()
//	fake.ReplyGetPayment(payment.Payment{ID: "pi_test", Status: payment.StatusPending}, nil)
//	result, err := fake.GetPayment(ctx, "pi_test")
//	if err != nil { return err }
//	_ = result.Status
package paymenttest
