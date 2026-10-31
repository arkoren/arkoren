---
title: Built-in Events
description: Every event Arkoren's own services publish, grouped by service, with the option that enables it, when it fires, and its payload.
order: 30
---

# Built-in Events

## Introduction

To observe a failed email or blocked request, find its topic below and enable
the component's [event bus](overview.md). Entries give the publishing condition
and payload; topics are package-level variables. A payload marked `none` is a
signal whose listener takes `event.Void`. For typed payloads, listen with the
topic and its type, as in [Writing Listeners](listeners.md):

Mail failure events expose a bounded class rather than provider diagnostics,
so this listener logs only that class. Some other failure events still carry
raw errors, which can quote sensitive data; review their payloads before
forwarding them to external logs or alerts.

```go
import (
	"context"
	"log/slog"

	"arkoren.dev/core/service/mail"
)

_, err := app.Events().Listen(mail.Failed, func(ctx context.Context, observed mail.FailedEvent) error {
	slog.WarnContext(ctx, "mail submission failed", "error_class", observed.ErrorClass)

	return nil
})
```

## How services publish

Except for the application's own events, components publish nothing without an explicit bus.
HTTP builders and queue consumers expose `Events`; router recovery uses
`router.RecoverBuilder.Events`; OAuth uses `ClientBuilder.Events`.
For example, configure a mailer's event bus with a construction policy:

```go
mailer := mail.New(driver, func(mailer *mail.Builder) {
	mailer.Events(app.Events())
})
```

Services publish synchronously with `Emit`, so a
listener that fails or panics is logged and the operation carries on, and a
slow listener delays the operation but cannot fail it. Events report what
happened; they do not let you veto it. A failure stops delivery to the remaining
listeners for that event.

The bus is process-local and best effort: listeners in another pod do not
receive these notifications, and a crash can lose an observation. Use them for
local monitoring, not as proof that a business transition was durably recorded.
Queueing from a listener does not close the crash window before enqueueing;
required records need an application-owned durable handoff.

Many notification schemas omit dedicated fields for secrets, tokens, session IDs,
request bodies, cache or storage contents, job or task payloads, and full URLs.
This is not a blanket redaction guarantee: some events retain identifiers or
raw errors, and recovered-panic events intentionally retain sensitive diagnostics.
Event contexts can also retain the caller's request values. Select safe
attributes rather than dumping payloads, contexts, or error chains to external logs.

Identity login/logout methods and free functions such as `gate.Authorize` and
`gate.Require` publish nothing, because they have no bus; their caller reports
its own events, or uses a configured value such as a `gate.Gate` built with
`gate.Builder.Events`.

Some events fire once for every request a client sends that fails a check:
`http.contenttype.rejected`, `http.auth.rejected`, and `http.gate.denied`. They are not deduplicated, so the client controls their
rate. Keep their listeners cheap, such as a counter or a log line, and put the
[rate limiting middleware](../abuse-prevention/rate-limiting.md) in front of the
routes so a flood of bad requests is cut off before it reaches them.

## Application

The application always owns a bus and always publishes these, with no option
needed. Their timing, hooks, and readiness semantics are explained in
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md)
and [Health and Readiness](../application/health-and-readiness.md).

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `application.Starting` | `app.starting` | none | composition and telemetry startup succeed, before user startup callbacks |
| `application.Started` | `app.started` | none | every runner has been launched |
| `application.Stopping` | `app.stopping` | none | shutdown begins |
| `application.Stopped` | `app.stopped` | none | shutdown has finished |
| `application.Failed` | `app.failed` | `FailedEvent{Err}` | startup fails or a runner fails |
| `application.Ready` | `app.ready` | none | the first readiness pass succeeds, and when the verdict turns from not ready to ready |
| `application.Unready` | `app.unready` | `UnreadyEvent{Failing}` | the first readiness pass fails, and when the verdict turns from ready to not ready |
| `application.CheckFailed` | `app.check.failed` | `CheckEvent{Name, Err}` | a health check fails after passing, or on its first pass |
| `application.CheckRecovered` | `app.check.recovered` | `CheckEvent{Name}` | a health check passes after failing |

`Failing` lists the names of the checks that failed the pass, in registration
order. The no-payload topics are signals, so a listener takes an `event.Void`.
`Starting` listeners registered during composition run after telemetry starts,
so logging through `app.Logger()` reaches configured telemetry or local capture.
Composition or telemetry-initialization failures publish `Failed` without
`Starting`; the event does not announce the beginning of composition.
`app.failed` and `app.stopped` are published with a context that is not
canceled, so a listener can still do its work during a shutdown caused by a
canceled context. Because a runner's exit is reported as `app.failed`, you do
not need a separate event for `Server.Serve` or a consumer stopping.

## HTTP

See [HTTP Events](http-events.md#topics) for server listening and shutdown,
outgoing requests, request rejections, sessions, signed links, and webhook
receipts. The catalog lists each enabling builder setting, payload, and
publishing condition. Authentication and authorization topics follow below.

### Recovered panics

`router.PanicRecoveredEvent` intentionally retains `Err` and `Stack` as sensitive
server-side diagnostics. The returned error's `Stack()` getter returns an owned
copy, and the event's stack bytes are detached from that error's storage. This
protects ownership, not privacy: listeners still share the event's `Stack` slice
and must copy it before mutation. The diagnostic error and its causes are not
redacted by that copy.

See [recovered panic events](http-events.md#recovered-panics) for the request
metadata, stack, and `http.ErrAbortHandler` exception.

### Outgoing requests

See [outgoing request events](http-events.md#outgoing-requests) for final
transport or retry failures and their payload.

### Request rejections

See [request rejection events](http-events.md#request-rejections) for CSRF,
rate-limit, and content-type failures, including notification rate and
untrusted payload fields.

### Session changes

`Path` on `session.DestroyedEvent` and `session.PersistFailedEvent` is now the
registered route pattern with its method prefix removed, or empty if no pattern
is available. It never falls back to the expanded request path; parameters and
queries are excluded, but a host-specific pattern retains its host prefix.
Keep secrets out of registered route literals. The persistence event excludes
the storage error; `session.Builder.ErrorHandler` still receives the original
diagnostic error, which can contain backend keys or other sensitive data.

See [session change events](http-events.md#session-changes) for expiry,
destruction, and persistence failures, including tombstone and context timing.

### Signed links

`link.RejectedEvent` and `link.ConsumedEvent` now retain only `Method`: `Path`
has been removed from both, and `RejectedEvent.Err` has been removed. A binding
callback failure remains in the middleware's returned problem for `errors.Is`
and `errors.As`, not in the event payload. Direct `Signer.Consume` and
`Signer.ConsumeURL` calls leave `Method` empty. Returned diagnostics and request
context values still need a privacy review before logging.

See [signed-link events](http-events.md#signed-links) for verification rejection
and successful one-time consumption, and the operations that publish nothing.

### Webhook receipts

`RejectedEvent.Reason`, `FailedEvent.Stage`, and `IgnoredEvent.Reason` now use
`webhook.RejectionReason`, `webhook.FailureStage`, and `webhook.IgnoreReason`.
Compare with their constants and convert explicitly for string-only APIs, such
as `string(failure.Stage)`. The existing string/JSON category values and member
names are unchanged, and empty zero values stay `""` (unspecified). Provider
event keys, receipt identities, and application labels do not change.

See [webhook receipt events](http-events.md#webhook-receipts) for rejected,
failed, and ignored deliveries, the constants, and their deliberately limited
payloads. Only authenticated unbound types publish `Ignored`; skips and
duplicates publish nothing. Receiver failures keep their locally committed
responses, including the native 413 problem for oversized bodies.

## Authentication and authorization

`auth.Require` and a `gate.Gate` report the requests they turn away:

| Topic | Name | Option | Payload | Fires |
| --- | --- | --- | --- | --- |
| `auth.Rejected` | `http.auth.rejected` | `auth.Builder.Events` on `Require` | `RejectedEvent{Method}` | `Require` rejected an anonymous request; the default response is 401 |
| `gate.Denied` | `http.gate.denied` | `gate.Builder.Events` on `gate.New` | `DeniedEvent{Method, Rule}` | a rule of the gate returned false |

`Rule` is the zero-based index of the rule that denied, in the order you passed
the rules, so the payload identifies the rule without saying who was denied.
`Method` is empty when the denial came from `Gate.Authorize`, which has no
request. `Path` has been removed from both payload types; neither carries the
resource path, user identity, or cause. HTTP methods are client-controlled;
do not put secrets in custom method names or assume event contexts are redacted.
A rule that returns an error, a missing user, and the free
functions `gate.Authorize` and `gate.Require` publish nothing. Both events fire
for every rejection. See [Authentication Overview](../authentication/overview.md)
and [Authorization](../authorization/denials.md#observing-denials).

With `oauth.ClientBuilder.Events`, the OAuth client in `arkoren.dev/driver/oauth/oauthauth`
publishes four events, described in
[OAuth and OpenID Connect](../authentication/oauth.md#events):

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `oauthauth.LoginSucceeded` | `oauth.login.succeeded` | `LoginSucceededEvent{Subject, Issuer}` | the callback stored the user in the session |
| `oauthauth.LoginFailed` | `oauth.login.failed` | `LoginFailedEvent{ProviderError, Err}` | the callback was rejected |
| `oauthauth.LoggedOut` | `oauth.logged-out` | none | logout removed the user from the session |
| `oauthauth.TokenRejected` | `oauth.token.rejected` | `TokenRejectedEvent{Err}` | `Verify` rejected a bearer token |

`LoginFailed` covers an invalid or replayed login cookie, a provider error or a
missing code, a failed code exchange or token check, and an error from your own
login function. Its `Err` is now only a bounded classification:
`oauthauth.ErrInvalidCallback`, `oauthauth.ErrProviderUnavailable`, or an opaque
local-login failure. Use `errors.Is` only to match those exported classifications;
there is no native or application cause to inspect in this payload. The opaque
local-login classification has no exported sentinel; do not classify it by text.

`ProviderError` admits only the RFC 6749 callback hints `invalid_request`,
`unauthorized_client`, `access_denied`, `unsupported_response_type`, `invalid_scope`,
`server_error`, and `temporarily_unavailable`. Unknown or absent values become
empty. This is an untrusted callback hint, not an authenticated provider diagnosis
or a free-text diagnostic.

`TokenRejectedEvent.Err` is a fixed opaque rejection, with no exported sentinel
or cause to unwrap. Count `oauth.token.rejected` outcomes rather than trying to
recover native verifier errors from that payload. Event names and fields are
unchanged; their errors no longer carry native/application diagnostics.

`Callback` and `Verify` still return errors preserving their original causes for
application error handling. Those causes can contain credentials or
provider-controlled claims; review them before logging. The event payloads have
no token, claim, or email fields, but `LoginSucceededEvent.Subject` and `Issuer`
can identify a person, and event contexts retain caller values. Select safe
attributes explicitly.

Invalid identity/mapper configuration and missing selected sessions return
before callback or logout outcome events. Login/logout events describe
request-local mutation, not durable session persistence.

## Database

With `database.Builder.Events`, the database facade reports the end of every
outermost transaction; nested transactions that join an outer one report
nothing of their own. Both topics carry a `database.TransactionEvent` with the
`System` (`postgresql`, `mysql`, or `sqlite`), the `Namespace` you configured,
and the `Err` that ended the transaction, which is nil for a commit.

| Topic | Name | Fires |
| --- | --- | --- |
| `database.TransactionCommitted` | `database.transaction.committed` | the commit succeeded |
| `database.TransactionRolledBack` | `database.transaction.rolled-back` | the body returned an error, the context ended, or the commit failed |

A panic rolls the transaction back but continues to the caller instead of
becoming an event. A confirmed commit still publishes the committed event with
nil `Err` if later cleanup or after-commit work fails with `database.ErrAfterCommit`.
Listeners of the committed event run after the commit, outside
the finished transaction. See [Query Monitoring](../database/query-monitoring.md)
and [Transactions](../database/transactions.md).

With `migrate.Builder.Events`, the migrator reports each migration it handles, all
with a `migrate.MigrationEvent` carrying the migration `ID`, its `Group` (empty
for your application's own), the `Batch`, whether it was a `Rollback`, the
`Duration`, and the `Err`:

| Topic | Name | Fires |
| --- | --- | --- |
| `migrate.Applied` | `database.migration.applied` | `Up` applied and recorded one migration |
| `migrate.RolledBack` | `database.migration.rolled-back` | `Rollback` reverted one migration and deleted its record |
| `migrate.Failed` | `database.migration.failed` | applying or reverting a migration failed; the run stops there |

See [Migrations](../database/migrations.md).

## Feature flags

With `feature.Builder.Events`, the flags facade reports driver failures that made a
caller fall back to its default value:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `feature.EvaluationFailed` | `feature.evaluation.failed` | `EvaluationFailedEvent{Key, Err}` | the driver failed to evaluate a flag |

`Err` wraps the driver's error with the key, such as `feature.ErrNotFound` or
`feature.ErrNotReady`. The payload omits the subject, attributes, and values
that can identify people. Off flags, successful evaluations, and facade-detected
failures (invalid flag definitions, invalid subjects, or type conversion errors)
publish nothing. Every driver failure publishes, even on hot paths. See
[Feature Flags Overview](../feature-flags/overview.md#events).

## Search

See [search operation events](service-operations.md#search) for opt-in `Sync`/`Drop`
outcomes; queries and document writes publish nothing.

## File storage

With `storage.Builder.Events`, the storage facade publishes after a successful
change:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `storage.ObjectWritten` | `storage.object.written` | `ObjectWrittenEvent{Store, Object}` | `Put`, or a helper built on it, stored an object |
| `storage.ObjectDeleted` | `storage.object.deleted` | `ObjectDeletedEvent{Store, Key}` | `Delete` succeeded |

`Store` is the name given with `storage.Builder.Name`, or `default`. Reads, listings,
failed writes, and writes made straight to the backend with a signed URL publish
nothing. See [File Storage Events and Telemetry](../file-storage/events-and-telemetry.md#events).

## Secrets

See [secret operation events](service-operations.md#secrets) for opt-in facade
mutation outcomes; reads and external writes publish nothing.

## Queues

With `ConsumerBuilder.Events` on the queue consumer, failures, retries, rejections,
and timeouts publish the events below. All payloads carry the message `ID`, the
job `Name`, the `Queue`, and the driver's delivery count as `Attempts`:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `queue.JobFailed` | `queue.job.failed` | `JobFailedEvent{ID, Name, Queue, Attempts, Err}` | the driver accepted terminal settlement of a failed delivery |
| `queue.JobRetried` | `queue.job.retried` | `JobRetriedEvent{ID, Name, Queue, Attempts, Delay, Err}` | the driver accepted a failed delivery back for another attempt |
| `queue.JobRejected` | `queue.job.rejected` | `JobRejectedEvent{ID, Name, Queue, Attempts, Err}` | the job returned `queue.Reject` and the driver accepted the rejection |
| `queue.JobTimedOut` | `queue.job.timed-out` | `JobTimedOutEvent{ID, Name, Queue, Attempts, Timeout}` | an attempt returned an error after the job's timeout elapsed |

`queue.job.failed` fires only after the driver accepts terminal settlement for a
final failure: the job returned a permanent error or used up its retries, its
payload did not decode, or no handler took its name within the attempt limit.
If `Delivery.Fail` returns an error, no failure event is published. The facade
returns the settlement error to the consumer driver, whose policy determines
whether consumption continues or ends. If `Consume` returns an error,
`Consumer.Run` cancels its other queue consumers and returns the first error
after they stop. The broker outcome remains unconfirmed.

Accepted settlement alone does not prove that a failed-job or dead-letter record
was durably stored. `queue.job.retried`
carries the `Delay` the driver was asked to wait, from `queue.RetryAfter`, the
job's backoff, or zero; it does not fire when retries are exhausted, when the
consumer stops mid-job and releases the delivery, or when the driver redelivers
an unknown name on its own. A rejection is a deliberate outcome, so
`queue.job.rejected` replaces `queue.job.failed` rather than adding to it.

`queue.job.timed-out` describes the attempt and fires before it is settled, so
successful settlement is followed by `queue.job.retried`, `queue.job.failed`, or
`queue.job.rejected` for the same delivery. A retry, rejection, or terminal-failure
settlement error can prevent that follow-up event. An attempt that succeeds
after its deadline publishes nothing. See
[Consuming Jobs](../queues/consuming-jobs.md#when-a-job-fails-for-good).

## Broadcasting

With `broadcast.ConsumerBuilder.Events` on the consumer, each listener that fails a delivery
publishes one event:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `broadcast.ListenerFailed` | `broadcast.listener.failed` | `ListenerFailedEvent{ID, Name, Err}` | a listener returned an error, could not decode the payload, or panicked |

`ID` is the message identifier, empty when the driver sets none, and `Name` is
the broadcast name. `Err` is the listener's error, a `broadcast.Permanent`
error, the decode error, or the recovered panic. A delivery that several
listeners fail publishes one event per failing listener, in listener order. The
payload leaves out the message payload and metadata. See
[Broadcasting Overview](../broadcasting/overview.md#delivery).

## Mail

| Topic | Name | Option | Payload | Fires |
| --- | --- | --- | --- | --- |
| `mail.Sent` | `mail.sent` | `mail.Builder.Events` | `SentEvent{Receipt, Recipients, Attachments}` | a driver returned acceptance, not recipient delivery |
| `mail.Failed` | `mail.failed` | `mail.Builder.Events` | `FailedEvent{ErrorClass, Recipients, Attachments}` | validation or the driver returned an error |

Failure events carry only counts and a bounded `ErrorClass`: `context.Canceled`,
`context.DeadlineExceeded`, `mail.ErrInvalidMessage`, or `error`, in that priority
order when wrapped or joined causes match. They contain no diagnostic text,
arbitrary provider type name, or error cause. **Breaking change:** `ErrorClass`
replaces `FailedEvent.Err`; compare the string instead of inspecting a payload
error. `Mailer.Send` still returns the original error with its causes intact.

With no bus, events are disabled. A nil context fails before instrumentation,
and a driver panic propagates without either `Sent` or `Failed`. Neither event
proves delivery, and a failure does not prove that remote acceptance was
impossible. See
[Mail Events and Telemetry](../mail/events-and-telemetry.md#events).

## Push notifications

See [push operation events](service-operations.md#push-notifications) for opt-in
submission outcomes, never delivery or display notifications.

## Payments

See [payment operation events](service-operations.md#payments) for opt-in API-call
outcomes, never settlement or fulfillment evidence.

## Scheduled tasks

With `schedule.Builder.Events`, the scheduler reports run outcomes and deferred
tasks:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `schedule.RunFailed` | `schedule.run.failed` | `RunFailedEvent{Name, Task, Duration, Err}` | an entry or task run returned an error |
| `schedule.RunSkipped` | `schedule.run.skipped` | `RunSkippedEvent{Name, Task, Duration}` | an entry or task run returned `schedule.ErrSkipped` |
| `schedule.TaskDeferred` | `schedule.task.deferred` | `TaskDeferredEvent{Name, ID, At}` | `At` or `After` handed a task to the driver |
| `schedule.TaskCanceled` | `schedule.task.canceled` | `TaskCanceledEvent{ID}` | `Cancel` returned without an error |

The run events fire for runs started by a driver and by `RunNow` alike. `Task`
is true for a deferred task rather than a scheduled entry, and `Err` includes a
recovered panic or a returned timeout error. A function that returns nil after
its deadline still reports success. A skip is not a failure, so a run that
returns `ErrSkipped` publishes `schedule.run.skipped` and never
`schedule.run.failed`.

`TaskDeferredEvent` carries the task `Name`, the job `ID` that `Cancel` accepts,
and the time `At` which it is due, never the task payload; a failed call
publishes nothing. The driver contract treats an unknown or already finished ID
as a successful cancellation without saying which case occurred, so
`schedule.task.canceled` means the cancellation was accepted, not that a
pending job was removed. See
[reacting to failed runs](../scheduled-tasks/error-handling.md#reacting-to-failures)
and [Scheduled Tasks Overview](../scheduled-tasks/overview.md#events-and-telemetry).

## Workflows

With `workflow.ClientBuilder.Events` on the client, the operations you call on a run
report their success:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `workflow.RunStarted` | `workflow.run.started` | `RunEvent{Workflow, RunID}` | `Client.Start` started a run |
| `workflow.RunSignaled` | `workflow.run.signaled` | `RunSignaledEvent{Workflow, RunID, Signal}` | `Client.Signal` delivered a signal |
| `workflow.RunCanceled` | `workflow.run.canceled` | `RunEvent{Workflow, RunID}` | `Run.Cancel` asked the driver to cancel a run |

Payloads carry the workflow name, run ID, and signal name, never input, output,
signal values, or metadata. Failed calls publish nothing, including a start of
an already-started run. Canceling a finished run succeeds and also publishes
`workflow.run.canceled`. These client events describe what your process asked
for, not execution inside the driver's run. See
[Workflows Overview](../workflows/overview.md#events-and-telemetry).

## Language models

With `llm.ModelBuilder.Events` or `llm.EmbedderBuilder.Events`, calls report their outcome:

| Topic | Name | Fires |
| --- | --- | --- |
| `llm.ResponseGenerated`, `llm.ResponseFailed` | `llm.response.generated`, `llm.response.failed` | `Generate`, or a `Stream` that finished |
| `llm.EmbeddingsGenerated`, `llm.EmbeddingsFailed` | `llm.embeddings.generated`, `llm.embeddings.failed` | `Embed` |
| `llm.AgentCompleted`, `llm.AgentFailed` | `llm.agent.completed`, `llm.agent.failed` | `Run` or `RunStream` ended |
| `llm.ToolInvoked`, `llm.ToolFailed` | `llm.tool.invoked`, `llm.tool.failed` | a tool call of an agent ended |

The payloads carry the model or agent, token usage, counts, and the duration,
never prompts, replies, tool arguments, or raw errors. The turns inside a run do
not publish `llm.response.*` events; the agent events cover the run. A stream the
consumer abandons publishes nothing.

`ResponseFailedEvent`, `EmbeddingsFailedEvent`, and `AgentFailedEvent` carry a
bounded `ErrorType` string. Values are known `llm` sentinel names (such as
`llm.ErrRateLimited`), `context.Canceled`, `context.DeadlineExceeded`, or `unknown`.
No error message, cause, or arbitrary native type name enters those payloads.
Context errors take precedence; execution policy failures otherwise use
`llm.ErrToolAvailability` rather than their `ErrPermanent` wrapper.

> [!NOTE]
> This is a breaking payload API change: `ErrorType` replaces `Err` on those
> three types. Compare the string in listeners instead of calling `errors.Is`
> on a payload error. Operations still return or yield errors that preserve the
> original causes for `errors.Is` and `errors.As`; those errors can contain
> sensitive diagnostics, so review them before forwarding them to logs or clients.

See [Language Models Overview](../llm/overview.md#usage-events-and-telemetry)
for usage, events, and telemetry.

## Listening to everything

A pattern listener sees every event whose name matches, which is the quickest way
to log what your services do while you are developing. Use only synthetic data
with this callback: it forwards whole payloads, including raw database errors
and storage identifiers, without redaction. With `regexp` imported:

```go
_, err := app.Events().ListenRegex(regexp.MustCompile(`^(storage|database)\.`),
	func(ctx context.Context, name string, payload any) error {
		slog.DebugContext(ctx, "event", "name", name, "payload", payload)

		return nil
	})
```

Names start with the area (`app`, `http`, `session`, `link`, `oauth`,
`database`, `feature`, `search`, `storage`, `secret`, `queue`, `broadcast`,
`mail`, `push`, `payment`, `schedule`,
`workflow`), so a prefix pattern selects a service. Remember that the payloads
of `http.panic.recovered` include a stack. For a durable record of who did what,
see [Auditing with Events](auditing.md).

For every function and option, see [the `event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
