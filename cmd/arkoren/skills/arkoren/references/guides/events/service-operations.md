---
title: Service Events
description: Interpret local payment, push, search, and secret operation results without inventing durable business outcomes.
order: 35
summary:
  - Observe returned driver outcomes through each service's opt-in event policy.
  - Distinguish API completion from settlement, device delivery, atomic index changes, or credential rollout.
  - Use bounded event classifications while keeping native returned diagnostics separate.
---

# Service Events

A successful API call is not necessarily a completed business action. A payment
can remain pending, an accepted push can remain undelivered, and a secret update
can leave consumers using an older credential. These events report the local
operation result at the facade's driver boundary, not those later outcomes.

Enable the service's construction-time `Events` policy and listen on that same
bus. Omitted or nil buses publish nothing. Publication is synchronous and
best effort: listener failures cannot replace returned results, but delay or
stop later observations. Other processes receive nothing automatically.
Do not drive required fulfillment, auditing, or reconciliation solely from these
notifications; use an [application-owned durable record](auditing.md).

## Payments

`payment.Builder.Events` enables two topics, both carrying
`payment.OperationEvent{Operation, ErrorType}`:

| Topic | Name | Driver Result |
| --- | --- | --- |
| `payment.OperationCompleted` | `payment.operation.completed` | Nil error. |
| `payment.OperationFailed` | `payment.operation.failed` | Nonnil error. |

Each validated facade call that reaches the driver and returns publishes one
observation after its telemetry operation ends. `Operation` is the fixed
snake_case facade operation, such as `get_payment` or `refund_payment`.
Validation failures, missing drivers, pre-canceled calls, and provider panics
publish nothing. Cancellation returned after driver dispatch can still publish.

Completion includes reads, pending results, and idempotent replays. It proves
neither authorization nor settlement and must not be treated as fulfillment
eligibility. The payload omits resource IDs, amounts, keys, references, URLs,
handoff secrets, and native errors.

`ErrorType` is empty on completion. On failure it is a payment sentinel name,
`context.Canceled`, `context.DeadlineExceeded`, or `unknown`.
`payment.ErrOutcomeUnknown` outranks all joined causes: the provider might have
applied a mutation. Other context classifications precede other payment sentinels.
The returned error retains original causes separately.

To observe uncertainty without exporting provider diagnostics, use `context` and
`payment` from `arkoren.dev/core/service/payment`, with your existing bus and logger:

```go
_, err := bus.Listen(payment.OperationFailed,
	func(ctx context.Context, notice payment.OperationEvent) error {
		logger.WarnContext(ctx, "payment call failed",
			"operation", notice.Operation,
			"error_type", notice.ErrorType)

		return nil
	})
```

This records the classification, not whether funds moved. Never infer that retry
with a new key is safe. Follow [payment recovery](../payments/recovery.md) for
uncertain outcomes; operation behavior belongs in
[hosted checkout](../payments/hosted-checkout.md#starting-checkout),
[explicit completion](../payments/hosted-checkout.md#explicit-completion),
[refunds](../payments/refunds.md), and [subscriptions](../payments/subscriptions.md).
See the [`payment` API reference](https://pkg.go.dev/arkoren.dev/core/service/payment).

## Push Notifications

`push.Builder.Events` enables `push.Accepted` (`push.submission.accepted`) and
`push.Failed` (`push.submission.failed`). Both carry
`SubmissionEvent{Duration, ErrorClass}` for the returned driver call from `Send`.
Nil error selects acceptance; nonnil error selects failure. Preflight rejection,
missing drivers, and provider panics publish neither.

`Duration` measures driver work, excluding listener execution and telemetry
completion, not device latency. `ErrorClass` is empty on acceptance; otherwise
it is `outcome_unknown`, `canceled`, `deadline_exceeded`, a push sentinel name,
or `error`, in that precedence. Unknown acceptance outranks cancellation because
the provider might already have accepted the submission.

Payloads omit recipients, endpoints, tokens, contents, receipts, and native
errors. Acceptance is not delivery or display; failure is not proof that delivery
cannot occur. Native returned errors remain available to the caller and require
separate redaction. Retry and registration cleanup stay application-owned; see
[Push Notifications](../push/overview.md) and the
[`push` API reference](https://pkg.go.dev/arkoren.dev/core/service/push).

## Search

`search.Builder.Events` enables administrative outcomes only. All three topics
carry `MutationEvent{Operation, Duration, ErrorClass}`:

| Topic | Name | Observation |
| --- | --- | --- |
| `search.Synced` | `search.index.synced` | `Sync` returned nil error. |
| `search.Dropped` | `search.index.dropped` | `Drop` returned nil error. |
| `search.Failed` | `search.index.failed` | Either administrative call returned an error. |

`Operation` is `sync` or `drop`. `Duration` measures driver work, excluding listener
execution. `ErrorClass` is empty on success; otherwise context classes `canceled`
and `deadline_exceeded` precede known search sentinel names, with `error` as the
fallback. Native errors, index names, documents, queries, and document IDs are absent.

Sync can succeed without changes, and dropping an absent index can succeed.
Failure can follow partial changes, particularly across multiple indexes; neither
atomicity nor rollback is implied. `Check`, queries, document writes/deletes,
invalid registrations, and provider panics publish nothing. Deployment procedures
stay in [Deploying Search](../search/deploying.md); see the
[`search` API reference](https://pkg.go.dev/arkoren.dev/core/service/search).

## Secrets

`secret.Builder.Events` enables `secret.MutationSucceeded`
(`secret.mutation.succeeded`) and `secret.MutationFailed`
(`secret.mutation.failed`). Both carry `MutationEvent{Operation, ErrorType}` after
facade `Put`, `Update`, or `Delete` returns from the driver. Nil error selects
success; nonnil error selects failure, including a missing secret.

`Operation` is `put`, `update`, or `delete`. `ErrorType` is empty on success;
otherwise it is `secret.ErrNotFound`, `context.Canceled`,
`context.DeadlineExceeded`, or `other`. Names, versions, values, sizes, and native
diagnostics are omitted. Context cancellation and deadline classifications take
precedence over `secret.ErrNotFound`. Returned driver errors remain unchanged.

Reads, direct driver/native calls, provider-console updates, and other-process
writes are outside these observations. Success does not prove credential rollout,
global rotation, or permanent erasure; failure does not establish that the
provider made no change. See [Managing Secrets](../secrets/managing.md) and the
[`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
