---
title: Reconciling Mollie Callbacks
description: Treat unsigned Mollie callbacks as hints and verify persisted payment ownership and provider state before fulfillment.
order: 35
---

# Reconciling Mollie Callbacks

## Introduction

Mollie's classic callback is an unsigned form POST carrying a payment ID hint,
not authenticated evidence of a payment. Use it to trigger an authenticated
provider read with the [Mollie driver](drivers.md#mollie), then reconcile the
result with your order. The module has no `molliewebhook` companion or
authenticated `webhook.Source`; mount an application handler rather than the
generic webhook receiver.

## Establishing ownership before lookup

Persist each payment ID with its order, merchant account, and environment when
creating checkout. Select the corresponding driver from that persisted
association, never from callback input. Anyone can forge the callback; apply
route-level rate limits, bound the body, and accept exactly one ID before a
provider call. Acknowledge unknown IDs without revealing order information.

The following fragment assumes an existing `mux *http.ServeMux`, a configured
`payments *payment.Client` for one merchant/environment, and two application
functions:

- `ownsPayment(context.Context, payment.ID) (bool, error)` checks the persisted
  association with that client.
- `reconcile(context.Context, payment.Payment) error` checks the expected order
  amount, currency, and association, permits only eligible state transitions,
  and commits processing idempotently and durably. Only a succeeded payment
  can satisfy its payment-success condition.

Import `context`, `net/http`, and `arkoren.dev/core/service/payment` in your
handler package. Configure rate limits before registering the route. The body
limit below is 1024 bytes; it does not limit request frequency.

## Reading and reconciling the hint

Register the handler after supplying those application functions:

```go
mux.HandleFunc("POST /payments/mollie", func(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ids := r.PostForm["id"]
	if len(ids) != 1 || len(r.PostForm) != 1 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id := payment.ID(ids[0])
	if err := id.Validate(r.Context()); err != nil {
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
})
```

The authenticated GET supplies payment evidence; the incoming POST remains
untrusted. A 200 acknowledges durable handling or an intentionally ignored
unknown ID, not automatically successful payment. Failures allow Mollie to
retry, but duplicates and delayed hints must never repeat fulfillment. Keep
the application's order transition and fulfillment handoff atomic or use a
durable outbox; a callback can fail after part of an effect occurred.

A successful HTTP GET with malformed JSON or an unusable payment snapshot returns
an error wrapping `payment.ErrUnavailable`. The handler above answers 503 without
calling `reconcile`. It must not infer payment success from the HTTP status or from
the unsigned incoming hint. The read failure does not settle an earlier uncertain
checkout or refund mutation.

## Reconciling refunds and testing failure paths

Refund callbacks carry a payment ID. Use your persisted compound refund handles
with `GetRefund` to observe returned funds; do not reconstruct a refund ID from
the callback. Next-gen Mollie webhooks are not implemented.

Test malformed and repeated IDs, unknown IDs, wrong merchant/environment,
provider outages, malformed successful GET bodies, unusable snapshots, duplicate
callbacks, and a durable reconciliation failure.
Assert that none grants fulfillment without the expected succeeded payment
and matching order amount/currency. Use [Testing Payments](testing.md) for
scripted provider outcomes; exercise HTTP parsing separately with
`httptest`.

For the selected driver's exact API, see the
[`molliepayment` reference](https://pkg.go.dev/arkoren.dev/driver/mollie/molliepayment).
