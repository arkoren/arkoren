# Mollie payments

Use `molliepayment` to send a buyer to Mollie's hosted card checkout and read
the resulting payment. The driver also creates explicit partial or full refunds
and reads each refund separately. It uses the standard HTTP client, not an SDK.

## Contents

- [Create a backend](#create-a-backend)
- [Start and reconcile checkout](#start-and-reconcile-checkout)
- [Refund an explicit amount](#refund-an-explicit-amount)
- [Recover an uncertain outcome](#recover-an-uncertain-outcome)
- [Handle classic callbacks safely](#handle-classic-callbacks-safely)
- [Scope and verification](#scope-and-verification)

## Create a backend

Enable `creditcard` on your Mollie profile and obtain a test-mode profile API
key. Install the module:

```sh
go get arkoren.dev/driver/mollie
```

This constructor example performs no network access. Replace `test_example`
with a securely loaded profile key before making requests:

```go
package main

import (
    "fmt"

    "arkoren.dev/core/service/payment"
    "arkoren.dev/driver/mollie/molliepayment"
)

func main() {
    backend, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
        merchant.WebhookURL("https://shop.example/mollie")
    })
    if err != nil {
        fmt.Println(err)
        return
    }
    client := payment.New(backend)
    fmt.Println(client != nil, backend.HTTPClient().Timeout)
}
```

The program prints `true 30s`. `WebhookURL` is optional; without it, your
application must arrange status reconciliation through reads. Construction
callbacks run once in order. Retaining a builder cannot change a published
driver. `HTTPClient` copies supplied client policy, borrows its transport and
jar, disables redirects and defaults nonpositive timeouts to thirty seconds.
The driver never closes borrowed resources. Do not change the returned client
concurrently or weaken redirect safety. Custom transports must not retry POSTs
or log bearer credentials.

Formatting the driver with fmt or slog redacts its complete owned configuration,
including the API key and callback destination. This does not sanitize values
you extract or native HTTP objects you log separately.

## Start and reconcile checkout

With `client` from the previous example and your request context `ctx`, create
one checkout. Persist the request and its stable idempotency key before calling:

```go
checkout, err := client.CreateCheckout(ctx, payment.NewCheckoutRequest(
    payment.Money{Amount: 1200, Currency: "EUR"}, "https://shop.example/paid", "order-123",
    func(details *payment.CheckoutRequestBuilder) {
        details.Description("Book order")
        details.Reference("order-123")
    }))
// Persist checkout.ID and checkout.PaymentID, then redirect to checkout.ApprovalURL.
```

Amounts are integer currency minor units; the wire request sends an exact
decimal and currency. Mollie enforces your profile's currency and amount limits.
The fixed method is `creditcard`, sequence is `oneoff`, and capture is
`automatic`. Empty descriptions become `Payment`; optional references are stored
in metadata. `CancelURL` is forwarded when present; otherwise Mollie uses
`ReturnURL`. Checkout and payment IDs are both Mollie's `tr_` identity.

An approval URL or customer return is not proof of settlement. Read the payment
and act only on `payment.StatusSucceeded`, which maps to Mollie's `paid` state.
`open` requires buyer action; `pending` and `authorized` remain pending. Unknown
native states stay unknown. `NeedsCompletion` is always false. Snapshots use only
the provider response and return caller-owned optional pointers.

## Refund an explicit amount

With the saved payment ID, create a refund using a different persisted key:

```go
refund, err := client.RefundPayment(ctx, payment.NewRefundRequest(checkout.PaymentID,
    payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))
// Persist refund.ID verbatim; use client.GetRefund(ctx, refund.ID) to reconcile.
```

Mollie checks currency agreement and remaining refundable balance. The driver
does not perform a hidden lookup before or after the mutation. Refund IDs are
opaque compound values containing both the native parent `tr_` and refund `re_`
tokens; bare `re_` IDs cannot be read through this driver. No database or
process-local identity cache is required. `queued`, `pending` and `processing`
are accepted pending results; only `refunded` maps to successful returned funds.

## Recover an uncertain outcome

No mutation is retried automatically. Mollie's
[idempotency cache](https://docs.mollie.com/reference/api-idempotency) lasts one
hour and is scoped to the API credential. Application-controlled recovery must
reuse the same request, credential and key within that window. Rotating a key
or retrying after expiry can produce a second effect, especially for partial
refunds. Do not replace an uncertain refund's key and blindly retry.

Cancellation before dispatch returns the context error without
`payment.ErrOutcomeUnknown`. After dispatch, transport loss, cancellation,
malformed or incomplete success responses, oversized bodies, redirects, 5xx,
408 and 409 responses wrap `ErrOutcomeUnknown`. A 409 can mean the first
idempotent request is still processing. Confirmed other 4xx rejections keep
their ordinary category. Known pending results are not unknown outcomes.
Errors expose only safe HTTP status diagnostics; explicitly unwrapped transport
or decoder causes can contain secrets and must not be logged without redaction.

Malformed successful GET JSON and unusable GET snapshots wrap
`payment.ErrUnavailable`, never `ErrOutcomeUnknown`. Existing transport and HTTP
rejection classifications are unchanged. `Error.Status` retains a non-2xx HTTP
status when available; zero also occurs after successful-response body failures,
so it does not prove that no reply arrived.

## Handle classic callbacks safely

[Classic callbacks](https://docs.mollie.com/docs/webhooks) are unsigned form
POSTs containing an `id` hint. They are not authenticated webhook events, so
this package supplies neither a `webhook.Source` nor automatic routes.

Your application must bound the body, require exactly one ID, match it to a
persisted payment and merchant, then call `GetPayment` using that merchant's
credential. Act on the authenticated GET result, not callback fields. Commit
deduplicated processing before acknowledging with HTTP 200. Rate-limit unknown
hints and acknowledge unknown IDs without disclosing order information. Refund
callbacks carry the payment ID: reconcile your persisted refund IDs separately
with `GetRefund`. These reads observe current state, not event chronology.

## Scope and verification

Manual capture, other payment methods, addresses/cart lines, Mollie Components,
customer and mandate setup, recurring subscriptions, and next-gen webhooks are
outside this driver. Valid preparation, completion and subscription operations return
`payment.ErrUnsupported` after context and request validation. No unsupported
operation sends HTTP.

Protocol tests run the portable payment conformance suite, including approval,
settlement and partial refunds, with `WithoutPreparation`, `WithoutCompletion`
and `WithoutSubscriptions`. These deterministic fixtures are not live provider
verification. For contributors, run from this module:

```sh
GOWORK=off go vet ./...
GOWORK=off go test -short -race ./...
golangci-lint run ./...
```

The `integration` build tag additionally enables read-only authentication and
an optional existing-test-payment lookup. Supply `MOLLIE_INTEGRATION=1`, a
test-mode `MOLLIE_API_KEY`, and optionally `MOLLIE_TEST_PAYMENT_ID`, then run
`GOWORK=off go test -tags integration -race ./...`. Short mode skips these
checks. They reject live keys and never create, charge or refund anything.
Hosted approval and real refund settlement still require independent sandbox
verification with a buyer; this suite does not claim to automate that flow.

Protocol references: [create payment](https://docs.mollie.com/reference/create-payment),
[get payment](https://docs.mollie.com/reference/get-payment),
[create refund](https://docs.mollie.com/reference/create-refund),
[get refund](https://docs.mollie.com/reference/get-refund), and
[method-specific parameters](https://docs.mollie.com/reference/extra-payment-parameters).
