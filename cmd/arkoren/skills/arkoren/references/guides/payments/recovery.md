---
title: Payment Recovery
description: Preserve uncertain mutation outcomes and reconcile saved operations within provider-specific replay limits.
order: 70
summary:
  - Record requests, keys, merchant scope, and references before recovering interrupted mutations.
  - Preserve earlier uncertainty even when a later same-key call fails differently.
  - Reconcile known resources or investigate within provider-specific replay limits.
---

# Payment Recovery

A provider can accept a payment operation even when your application receives
no usable reply. Recovery means finding what happened to that saved operation,
not assuming the error permits a new attempt. This applies to checkout creation,
preparation, enrollment, completion, refunds, and cancellation.

## Recording Attempts

Before dispatch, save the authorized application account/order, immutable request,
logical operation identity, provider, merchant, and test/live environment. Save
the operation key where the request supports one. Cancellation has no portable
caller-key field, but still needs an application-owned operation record.

Coordinate concurrent callers so browser retries, workers, return paths, and
notifications do not start independent mutations for one intended effect.
Persist every observed resource ID and result, including references returned
with an error. A timeout, expired application claim, or failed local save is not
evidence that the provider did nothing.

Keep the exact credential identity when it affects replay scope, without placing
the credential itself in ordinary logs. Protect saved approval URLs and client
handoff credentials as secrets. Arkoren supplies no durable ledger, transaction
coordinator, local billing projection, or background reconciliation worker.

## Preserving Uncertainty

`errors.Is(err, payment.ErrOutcomeUnknown)` means a dispatched mutation may have
taken effect. Cancellation after dispatch can retain both outcome uncertainty
and the caller's context error. Pre-dispatch cancellation alone has no effect
from that call; it says nothing about an earlier call.

> [!WARNING]
> A later same-key rejection or pre-dispatch failure does not resolve an earlier
> uncertain attempt. Preserve the original request/key and unresolved state.
> Neither the latest error nor an unavailable read authorizes a fresh-key charge,
> refund, or fulfillment.

Model that uncertainty separately from the latest error. For example, an
application-owned saved checkout record can include these fields:

```go
import "arkoren.dev/core/service/payment"

type CheckoutAttempt struct {
    Request   payment.CheckoutRequest
    Checkout  payment.CheckoutID
    Uncertain bool
}
```

This is a storage-model fragment, not an Arkoren persistence API. Store its
account/merchant/environment association alongside it. With `attempt` loaded
under that association and `errors` imported, preserve uncertainty after a call:

```go
if errors.Is(err, payment.ErrOutcomeUnknown) {
    attempt.Uncertain = true
}
```

Persist that update with the observed references and outcome. Do not assign
`attempt.Uncertain = errors.Is(err, payment.ErrOutcomeUnknown)`: a later known
failure would erase an earlier unknown effect. Only reconciliation evidence
can resolve the saved uncertainty. Preserve context error identity with
`errors.Is` as well; do not infer dispatch state from error text.

## Reading Evidence

If a resource ID is known, read that resource using its persisted provider,
merchant, and environment. `GetCheckout`, `GetPayment`, `GetRefund`, and
`GetSubscription` are reads, not hidden capture or settlement operations.
Match observations against the saved target, amount/currency where applicable,
and account association before changing application state.

A succeeded payment can satisfy a matching order's collection condition;
pending/unknown cannot. Refund progress is independent: pending means accepted
work is not finalized, not that the mutation outcome is unknown. Subscription
lifecycle is not evidence of paid access. Preserve previously observed
relationships if a later snapshot omits them.

Choose bounded current-state reads through your application's recovery policy,
not an unbounded polling loop. A failed or not-found read does not by itself
settle an earlier uncertain mutation. A successful HTTP status with an unusable
body is not a valid financial observation either.

If creation returned no ID, the contract has no list/search operation, universal
preparation/session lookup, or lookup by operation key or Reference. Correlation
text does not make a missing resource discoverable. Use supported provider
replay or provider-specific investigation rather than casting another ID.

## Replaying Requests

Replay only the identical saved request/key within that operation's documented
provider scope and retention. Keep merchant/environment fixed. Do not treat a
provider idempotency mechanism as permanent exactly-once processing or assume
all operations share its guarantees.

Stripe and PayPal replay behavior is provider/operation-specific; verify the
relevant [Stripe idempotency policy](https://docs.stripe.com/api/idempotent_requests)
or [PayPal idempotency policy](https://developer.paypal.com/api/rest/reference/idempotency/)
before choosing a recovery window. The portable contract provides no common
retention duration or guarantee that every endpoint supports a replay key.

Mollie's [POST response cache](https://docs.mollie.com/reference/api-idempotency)
lasts one hour per exact API credential. Rotating credentials, even within one
account, loses that replay scope. Reusing a key after expiry can create a new
effect, particularly another partial refund. Retain the credential association
and original dispatch timing before considering replay.

Outside a supported replay window, investigate through the provider's dashboard
or a provider-specific process before authorizing a new attempt. Do not turn
missing evidence into permission to retry with a new key.

[Driver-owned clients](drivers.md) do not automatically replay mutations.
Stripe `NewFrom` preserves the supplied SDK/HTTP policies instead; configure
those deliberately. Custom transports must not add mutation replay independently.
No driver performs post-success hydration to fill missing fields, although a
documented preflight read can guard currency or lifecycle.

## Classifying Errors

Use `errors.Is`/`errors.As`, not message matching. Classify the current call
without erasing the operation's saved history:

| Error | Response |
| --- | --- |
| `ErrInvalidRequest`, `ErrUnsupported` | Correct input or choose a supported operation; waiting does not fix it. |
| `ErrUnauthorized`, `ErrForbidden` | Fix provider credentials/permissions; buyer approval does not fix provider authorization. |
| `ErrDeclined` | Use an application-owned buyer recovery path for this known rejection. |
| `ErrInvalidState` | Reconcile lifecycle before considering another mutation. |
| `ErrRateLimited`, `ErrUnavailable` | Apply a bounded provider-aware policy; do not automatically repeat a charge. |
| `ErrNotFound` | Recheck the persisted identity/scope; it does not resolve an earlier ambiguous effect. |
| `ErrOutcomeUnknown` | Preserve the original operation and reconcile its possible effect. |

For PayPal, malformed successful GET JSON and unusable OAuth replies wrap
`ErrUnavailable` without adding `ErrOutcomeUnknown`. Semantic GET snapshot
failures keep their specific validation/capability errors. For Mollie, malformed
or unusable successful GET snapshots wrap `ErrUnavailable`. Successful mutation
decoding failures retain outcome uncertainty in both drivers. A pre-dispatch
OAuth failure can leave an earlier same-key attempt unresolved.

Mollie's retained native error status can be zero after a successful response
that could not be read or decoded. Zero does not prove that no response arrived.
Safe driver error messages do not sanitize explicitly unwrapped causes; redact
those and borrowed HTTP diagnostics before logging.

Use [Testing](testing.md#testing-uncertainty) to exercise a lost reply followed by
a later rejection without moving money. For error identities and operation
contracts, see the [payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
