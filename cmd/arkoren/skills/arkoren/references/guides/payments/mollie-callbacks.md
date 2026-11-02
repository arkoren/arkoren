---
title: Mollie Callbacks
description: Bound unsigned callback hints, verify saved merchant ownership, and reconcile authoritative provider reads durably.
order: 80
summary:
  - Parse bounded unsigned hints without treating their IDs as payment evidence.
  - Check persisted merchant ownership before reading and durably reconciling provider state.
  - Reconcile saved refund handles and test duplicate, malformed, and failed callback paths.
---

# Mollie Callbacks

Mollie's classic callback is an unsigned form POST containing a payment ID.
Anyone can send one. Treat it as a hint to read provider state, never as evidence
that an order was paid. Use a plain application handler, not `webhook.Handler`:
there is no shipped `molliewebhook` or authenticated Mollie Source.

Configure the reachable HTTPS callback URL on the
[Mollie driver](drivers.md#connecting-mollie) before creating checkout. The
following fragments assume `payments *payment.Client` for one fixed merchant
and environment, `mux *http.ServeMux`, and application-owned durable storage.
Apply route-level rate limits before mounting the handler; a body limit does
not limit request frequency.

## Binding Ownership

Save each returned payment ID with its order, application account, merchant,
and test/live environment. For multiple merchants, select credentials from this
persisted association, never from callback input. A valid-looking ID alone must
not cause an authenticated lookup under arbitrary credentials.

The handler uses two application functions, not Arkoren APIs:

- `ownsPayment(context.Context, payment.ID) (bool, error)` checks that the ID is
  saved under the fixed client's merchant/environment.
- `reconcile(context.Context, payment.Payment) error` checks the returned identity,
  order association, expected amount/currency, and permitted state transition;
  it commits processing idempotently and durably. Only a matching succeeded
  payment can satisfy its collection condition.

An unknown ID is acknowledged without revealing order data. A known ID still
requires an authoritative provider read before reconciliation.

## Parsing Hints

Use a small parser to require exactly one body form field with one valid ID.
Import `context`, `errors`, `net/http`, and `arkoren.dev/core/service/payment`. The caller
applies a 1024-byte limit before invoking this helper:

```go
func readMollieHint(r *http.Request) (payment.ID, error) {
    if err := r.ParseForm(); err != nil {
        return "", err
    }
    ids := r.PostForm["id"]
    if len(r.PostForm) != 1 || len(ids) != 1 {
        return "", errors.New("expected one payment id")
    }
    id := payment.ID(ids[0])
    if err := id.Validate(r.Context()); err != nil {
        return "", err
    }
    return id, nil
}
```

This validates the hint's shape, not its authenticity. Query parameters do not
choose the payment: only the parsed POST body is used.

## Reconciling State

With the application functions above supplied, mount the handler and reject
unusable input before any provider request:

```go
mux.HandleFunc("POST /payments/mollie", func(w http.ResponseWriter, r *http.Request) {
    r.Body = http.MaxBytesReader(w, r.Body, 1024)
    id, err := readMollieHint(r)
    if err != nil {
        w.WriteHeader(http.StatusBadRequest)
        return
    }
    known, err := ownsPayment(r.Context(), id)
    if err != nil {
        w.WriteHeader(http.StatusServiceUnavailable)
        return
    }
    if !known {
        w.WriteHeader(http.StatusOK)
        return
    }
    reconcileMollieHint(w, r, id, payments, reconcile)
})
```

Only a saved association reaches the authenticated lookup. The next helper
uses the same fixed merchant's `payments` client and application `reconcile`:

```go
func reconcileMollieHint(
    w http.ResponseWriter,
    r *http.Request,
    id payment.ID,
    payments *payment.Client,
    reconcile func(context.Context, payment.Payment) error,
) {
    observed, err := payments.GetPayment(r.Context(), id)
    if err != nil {
        w.WriteHeader(http.StatusServiceUnavailable)
        return
    }
    if err := reconcile(r.Context(), observed); err != nil {
        w.WriteHeader(http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusOK)
}
```

These helpers show application collaborators; bind the client and reconciliation
function in your handler's own dependency wiring. A 200 acknowledges durable
handling or an intentionally ignored unknown ID, not successful collection.
Provider read/reconciliation failures return 503 to permit retries. A malformed
or unusable successful GET response wraps `payment.ErrUnavailable`; it cannot
supply payment evidence or resolve an earlier uncertain mutation.

Duplicate and delayed hints must not repeat fulfillment. Commit the order
transition and fulfillment handoff atomically, or use a durable outbox. A
callback can fail after a partial application effect; acknowledgement alone is
not a transaction boundary.

## Following Refunds

Refund callbacks contain the payment ID, not a compound refund handle. Read
the refund IDs you already saved with `GetRefund` to reconcile returned funds;
do not reconstruct them from the hint. [Payment Refunds](refunds.md) distinguishes
pending progress from confirmed refund success. Next-gen Mollie webhooks are
not implemented.

Test malformed/repeated IDs, unknown IDs, wrong merchant/environment, outages,
malformed successful GET bodies, unusable snapshots, duplicates, and recording
failures. None may grant fulfillment without a matching succeeded payment.
Use [Testing](testing.md) for provider outcomes and `net/http/httptest` for parsing
and HTTP responses; no callback signature should be expected.

For callback configuration and snapshot behavior, see the
[Mollie payment API](https://pkg.go.dev/arkoren.dev/driver/mollie/molliepayment).
