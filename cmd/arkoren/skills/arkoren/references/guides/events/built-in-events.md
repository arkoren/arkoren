---
title: Built-in Events
description: Find component notifications, their enabling policies, and the boundary each observation describes.
order: 30
summary:
  - Find built-in topics by component and enable their publishing policy.
  - Interpret application, transaction, worker, and model observations without assuming durable business completion.
  - Locate detailed HTTP payloads and bounded service-result classifications.
---

# Built-in Events

Use this catalog to find the notification for an operation you want to observe.
Topic variables belong to the named package; payload names below are relative
to that package. A signal has no data and its listener takes `event.Void`.
[Writing Listeners](listeners.md) covers registration and ownership.

## Publishing Policies

Apart from application lifecycle and health events, components publish nothing
without an explicitly configured bus. Configure `Events` when constructing the
component. With `mail` imported from `arkoren.dev/core/service/mail`, an existing
mail driver, and the shared `bus`:

```go
mailer := mail.New(driver, func(mailer *mail.Builder) {
	mailer.Events(bus)
})
```

The setting enables notifications, not subscriptions. Components use best-effort
`Emit`: callbacks run synchronously, and their errors or panics are logged without
replacing the operation result. Failure stops delivery to later listeners. Slow
listeners delay the operation. Events stay in the publishing process, are not
retried, and can be lost on a crash—even if a listener would enqueue work.
Required records belong at an [application-owned durable boundary](auditing.md).

Payload omissions are not universal redaction. Some events retain raw errors,
identifiers, or diagnostic stacks; contexts retain caller values. Select safe
attributes instead of dumping a payload, context, or returned error chain.

## Application

The application always publishes on `app.Events()`. Register before `Start` or
during synchronous composition; a startup callback is too late for `Starting`.

| Topic | Name | Observation |
| --- | --- | --- |
| `application.Starting` | `app.starting` | Signal after composition and telemetry startup, before user startup callbacks. |
| `application.Started` | `app.started` | Signal after all runners launch; readiness can still be pending. |
| `application.Stopping` | `app.stopping` | Signal before runner cancellation and cleanup. |
| `application.Stopped` | `app.stopped` | Signal after runner waiting and cleanup, before telemetry finalization. |
| `application.Failed` | `app.failed` | `FailedEvent{Err}` for the original startup or runner failure. |
| `application.Ready` | `app.ready` | Signal for a successful first readiness pass or transition to ready. |
| `application.Unready` | `app.unready` | `UnreadyEvent{Failing}` for a failed first pass or transition to unready. |
| `application.CheckFailed` | `app.check.failed` | `CheckEvent{Name, Err}` for an initially failing check or transition from passing. |
| `application.CheckRecovered` | `app.check.recovered` | `CheckEvent{Name, Err: nil}` for a check recovering from failure. |

`Stopped` does not mean cleanup succeeded: inspect the lifecycle method's returned
error. The application is still stopping while that callback runs. `Failed`
excludes later cleanup and telemetry-finalization errors. Composition or telemetry
startup failure can publish `Failed` without `Starting`. `Failed` and `Stopped`
use contexts detached from cancellation.

`Failing` lists check names in registration order; do not mutate the shared slice.
Check errors and startup errors are raw diagnostics, not safe external text.
See [Lifecycle and Ownership](../application/lifecycle-and-ownership.md) and
[Health and Readiness](../application/health-and-readiness.md) for operating policy.

## HTTP

[HTTP Events](http-events.md) owns the exact publication conditions, payloads,
and privacy limits for server lifecycle, recovered panics, outgoing requests,
CSRF/rate-limit/content-type rejections, sessions, signed links, and webhook
receipts. Enable each component separately; setting a server's bus does not
implicitly enable every middleware's events.

## Authentication and Authorization

These notifications observe refusal, not successful identity establishment:

| Topic | Name | Enabling Policy | Observation |
| --- | --- | --- | --- |
| `auth.Rejected` | `http.auth.rejected` | `auth.MiddlewareBuilder.Events` on `Require` | `RejectedEvent{Method}` when an anonymous request is rejected. |
| `gate.Denied` | `http.gate.denied` | `gate.Builder.Events` on `gate.New` | `DeniedEvent{Method, Rule}` when a rule returns false. |

`Rule` is the zero-based rule index in registration order. `Method` is empty
for `Gate.Authorize`, which has no request. Neither payload contains a path,
user, or cause. Rule errors, missing users, allowed calls, and the free
`gate.Authorize`/`gate.Require` functions publish no gate event. Identity
login/logout methods also have no bus and publish nothing themselves.

Each refusal publishes, even when a custom error response changes the default
status. Clients control the frequency: keep callbacks cheap and place
[rate limiting](../abuse-prevention/rate-limiting.md) before these checks. Methods
are client-controlled too; keep secrets out of custom method names. See
[Authentication](../authentication/overview.md) and
[denial monitoring](../authorization/denials.md#denial-events).

### OAuth Outcomes

The client in `arkoren.dev/driver/oauth/oauthauth` uses `ClientBuilder.Events`:

| Topic | Name | Observation |
| --- | --- | --- |
| `oauthauth.LoginSucceeded` | `oauth.login.succeeded` | `LoginSucceededEvent{Subject, Issuer}` after local session login. |
| `oauthauth.LoginFailed` | `oauth.login.failed` | `LoginFailedEvent{ProviderError, Err}` for a rejected callback. |
| `oauthauth.LoggedOut` | `oauth.logged-out` | Signal after removing the user from the local session. |
| `oauthauth.TokenRejected` | `oauth.token.rejected` | `TokenRejectedEvent{Err}` for bearer rejection. |

Callback rejection covers invalid/replayed login cookies, provider errors, missing
codes, exchange/token-check failures, and local login failures. Configuration and
missing-session exits before cookie consumption publish nothing. Login/logout
events describe request-local mutation, not durable session persistence or provider logout.

`LoginFailedEvent.Err` is only `ErrInvalidCallback`, `ErrProviderUnavailable`, or
an opaque local-login classification with no exported sentinel or cause. Match
the two exported classes with `errors.Is`; do not classify opaque errors by text.
`TokenRejectedEvent.Err` is a fixed opaque error with no exported sentinel or
native cause. Count that outcome rather than trying to unwrap diagnostics.

`ProviderError` admits only `invalid_request`, `unauthorized_client`, `access_denied`,
`unsupported_response_type`, `invalid_scope`, `server_error`, or
`temporarily_unavailable`; absent or unknown hints become empty. It remains an
untrusted callback hint, not an authenticated diagnosis. `Callback` and `Verify`
return errors with original causes separately; those causes can expose credentials
or claims. `Subject` and `Issuer` can identify a person even without email or tokens.
See [OAuth Login](../authentication/oauth.md#events) for configuration.

## Database

With `database.Builder.Events`, outermost transactions publish
`database.TransactionCommitted` (`database.transaction.committed`) or
`database.TransactionRolledBack` (`database.transaction.rolled-back`). Both carry
`TransactionEvent{System, Namespace, Err}`. Nested joined transactions publish
nothing independently.

A confirmed commit publishes nil `Err` before after-commit hooks; later cleanup
or hook errors can still return `database.ErrAfterCommit`. Before confirmed
commit, body/context/commit failures publish rollback. A body error bearing
`ErrAfterCommit` still requests rollback. Panics propagate without a transaction
event. Committed-event listeners run outside the finished transaction.
See [Transactions](../database/transactions.md) and
[Query Monitoring](../database/query-monitoring.md).

With `migrate.Builder.Events`, each handled migration carries
`MigrationEvent{ID, Group, Batch, Rollback, Duration, Err}`:

| Topic | Name | Observation |
| --- | --- | --- |
| `migrate.Applied` | `database.migration.applied` | Applied and recorded one migration. |
| `migrate.RolledBack` | `database.migration.rolled-back` | Reverted one migration and removed its record. |
| `migrate.Failed` | `database.migration.failed` | Work did not complete; the run stops. |

`Group` is empty for application migrations. A confirmed commit followed by
cleanup failure still publishes the completed outcome with nil payload `Err`;
the returned error stops the run. Raw database errors can contain sensitive
data. Migration procedures stay in [Migrations](../migrations/overview.md).

## Feature Flags

`feature.Builder.Events` enables `feature.EvaluationFailed`
(`feature.evaluation.failed`), carrying `EvaluationFailedEvent{Key, Err}` when
driver evaluation fails and the caller receives its default plus the error.
The error retains the driver's cause, wrapped with the flag key. Subject,
attributes, and values are omitted.

Successful/off evaluations, invalid definitions or subjects, and facade type
conversion failures publish nothing. Every driver failure publishes, including
hot-path misses. See [Feature Flags](../feature-flags/overview.md#events).

## Search

`search.Builder.Events` enables administrative `Sync`/`Drop` observations:
`search.Synced`, `search.Dropped`, and `search.Failed`. Queries and document
writes publish nothing. [Search outcomes](service-operations.md#search) give
their canonical names, bounded payload, and partial-effect limits.

## File Storage

With `storage.Builder.Events`, successful facade changes publish:

| Topic | Name | Observation |
| --- | --- | --- |
| `storage.ObjectWritten` | `storage.object.written` | `ObjectWrittenEvent{Store, Object}` after `Put` or a helper built on it. |
| `storage.ObjectDeleted` | `storage.object.deleted` | `ObjectDeletedEvent{Store, Key}` after successful idempotent deletion. |

`Store` is the configured name or `default`. Deleting an absent object also
succeeds, so the notification does not prove prior existence. Reads, listings,
failed writes, and direct signed-URL uploads publish nothing. Object metadata
and keys can identify resources. See
[storage monitoring](../file-storage/events-and-telemetry.md#object-events).

## Secrets

`secret.Builder.Events` enables `secret.MutationSucceeded` and
`secret.MutationFailed` for facade `Put`, `Update`, and `Delete` results.
[Secret outcomes](service-operations.md#secrets) define their payload and names.
Reads and external/native writes publish nothing; these are not rotation or rollout records.

## Queues

Enable `queue.ConsumerBuilder.Events` on the consumer. Payloads share
`ID`, `Name`, `Queue`, and the driver's delivery count `Attempts` (zero if unavailable):

| Topic | Name | Additional Fields / Observation |
| --- | --- | --- |
| `queue.JobFailed` | `queue.job.failed` | `JobFailedEvent.Err`; accepted terminal failure settlement. |
| `queue.JobRetried` | `queue.job.retried` | `JobRetriedEvent{Delay, Err}`; accepted handoff for another attempt. |
| `queue.JobRejected` | `queue.job.rejected` | `JobRejectedEvent.Err`; accepted deliberate rejection, instead of `JobFailed`. |
| `queue.JobTimedOut` | `queue.job.timed-out` | `JobTimedOutEvent.Timeout`; attempt returned an error after its deadline, before settlement. |

Terminal failure covers permanent/exhausted errors, undecodable payloads, and
unknown names after their attempt limit. If settlement fails, no settlement
notification fires; the error returns through consumption and the broker outcome
remains unconfirmed. If the driver's `Consume` returns an error, `Consumer.Run`
cancels its other queue consumers, waits for them to stop, and returns the first
error. Accepted settlement does not prove durable failed-job storage.

`Delay` is the requested retry delay, not an exact execution time. Exhausted
retries, shutdown release, and driver-controlled unknown-name redelivery do not
publish `JobRetried`. A timeout can be followed by retry, failure, or rejection
for the same delivery; settlement failure can prevent that follow-up. Success
after the deadline publishes no timeout event. Errors retain diagnostics;
payload bytes are omitted. See [Consuming Jobs](../queues/consuming-jobs.md).

## Broadcasting

`broadcast.ConsumerBuilder.Events` enables `broadcast.ListenerFailed`
(`broadcast.listener.failed`), carrying `ListenerFailedEvent{ID, Name, Err}`
once per listener that returns an error, fails decoding, or panics. Several
listeners can fail one delivery and publish in listener order. `ID` can be empty.
Success publishes nothing; transport settlement is not the observation. Payload
and metadata are omitted, but errors retain causes. See
[broadcast delivery](../broadcasting/message-delivery.md#observing-failures).

## Mail

With `mail.Builder.Events`, `mail.Sent` (`mail.sent`) carries
`SentEvent{Receipt, Recipients, Attachments}` after driver acceptance;
`mail.Failed` (`mail.failed`) carries `FailedEvent{ErrorClass, Recipients, Attachments}`
after returned validation or driver failure. Counts omit addresses and contents;
the receipt is a provider submission identifier, not a delivery receipt.

Failure classes are `context.Canceled`, `context.DeadlineExceeded`,
`mail.ErrInvalidMessage`, then `error`, in that priority order. No native cause
or diagnostic text enters `FailedEvent`; `Send` returns the original error
separately. Nil-context rejection and driver panics publish neither outcome.
Acceptance is not recipient delivery, and failure does not rule out remote
acceptance. See [mail monitoring](../mail/events-and-telemetry.md#events).

## Push Notifications

`push.Builder.Events` enables `push.Accepted` and `push.Failed` for returned
submission results. [Push outcomes](service-operations.md#push-notifications)
give their names and payload; neither confirms device delivery or display.

## Payments

`payment.Builder.Events` enables `payment.OperationCompleted` and
`payment.OperationFailed` for driver-returned facade calls.
[Payment outcomes](service-operations.md#payments) give their names and bounded
payload; neither is a settlement or fulfillment event.

## Scheduled Tasks

Enable `schedule.Builder.Events` for run outcomes and deferred-task requests:

| Topic | Name | Observation |
| --- | --- | --- |
| `schedule.RunFailed` | `schedule.run.failed` | `RunFailedEvent{Name, Task, Duration, Err}` for a returned run error other than `ErrSkipped`. |
| `schedule.RunSkipped` | `schedule.run.skipped` | `RunSkippedEvent{Name, Task, Duration}` when execution returns `ErrSkipped`. |
| `schedule.TaskDeferred` | `schedule.task.deferred` | `TaskDeferredEvent{Name, ID, At}` after successful `At`/`After` submission. |
| `schedule.TaskCanceled` | `schedule.task.canceled` | `TaskCanceledEvent{ID}` after successful cancellation request. |

Driver-started runs and `RunNow` both publish run outcomes. `Task` distinguishes
deferred tasks from entries; task payloads are omitted. A skip is not a failure,
and a driver's overlap guard preventing invocation is not an execution skip.
Failure errors can include recovered panics and timeout errors; returning nil
after a deadline still succeeds. Driver retry policy is separate.

`TaskDeferred` reports acceptance, not task completion. Canceling an unknown or
finished ID can succeed and publish too; it does not prove removal or that a
running handler stopped. See [failure events](../scheduled-tasks/error-handling.md#failure-events)
and [deferred tasks](../scheduled-tasks/deferred-tasks.md).

## Workflows

Enable `workflow.ClientBuilder.Events` on the client:

| Topic | Name | Observation |
| --- | --- | --- |
| `workflow.RunStarted` | `workflow.run.started` | `RunEvent{Workflow, RunID}` after accepted start. |
| `workflow.RunSignaled` | `workflow.run.signaled` | `RunSignaledEvent{Workflow, RunID, Signal}` after accepted signal delivery. |
| `workflow.RunCanceled` | `workflow.run.canceled` | `RunEvent{Workflow, RunID}` after accepted cancellation. |

Failures, including already-started runs, publish nothing. Canceling a finished
run succeeds and publishes. These are local client actions, not subscriptions to
engine completion or proof a signal was consumed. Input, output, signal values,
and metadata are omitted; run IDs remain potentially identifying data. See
[workflow observations](../workflows/running-workflows.md#events-and-telemetry).

## Language Models

Use `llm.ModelBuilder.Events` for model/agent/tool events and
`llm.EmbedderBuilder.Events` for embeddings:

| Topics | Names | Observation |
| --- | --- | --- |
| `llm.ResponseGenerated`, `llm.ResponseFailed` | `llm.response.generated`, `llm.response.failed` | Standalone `Generate` or exhausted `Stream`. |
| `llm.EmbeddingsGenerated`, `llm.EmbeddingsFailed` | `llm.embeddings.generated`, `llm.embeddings.failed` | Returned `Embed` outcome. |
| `llm.AgentCompleted`, `llm.AgentFailed` | `llm.agent.completed`, `llm.agent.failed` | Finished `Run` or `RunStream`. |
| `llm.ToolInvoked`, `llm.ToolFailed` | `llm.tool.invoked`, `llm.tool.failed` | Tool outcome, including argument or availability rejection before a handler runs. |

Each topic has its correspondingly named `…Event` type. Payloads describe
model/agent/tool names, usage or counts where relevant, and duration—not prompts,
replies, arguments, vectors, or raw errors. `Steps` counts model turns, not tool
calls. Agent turns do not also publish standalone response events. Abandoning
a stream, even by breaking at standalone `Done`, suppresses its final outcome.

Response, embeddings, and agent failure payloads carry bounded `ErrorType`:
context cancellation/deadline names first, then known `llm` sentinel names,
otherwise `unknown`. Policy failures use `llm.ErrToolAvailability` ahead of
`ErrPermanent` unless a context error matches. Tool failure carries no error
field. Original returned/yielded errors retain diagnostic causes and need a
separate privacy review. See [observing calls](../llm/overview.md#observing-calls).

For registration and dispatch, see the [`event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
