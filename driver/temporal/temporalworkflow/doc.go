// Package temporalworkflow implements [workflow.Driver] on Temporal workflows and
// activities.
//
// Two types split the work. [NewRunner] runs in the worker process: it
// registers the workflows and steps of a [workflow.Registry] on a
// [temporal.Host]. [NewDriver] runs in any process that starts and controls
// runs: it uses only the client of the host, so the host can be client-only
// with [temporal.Builder.NoWorker].
//
// Construction uses [DriverBuilder] or [RunnerBuilder], separately from workflow
// declarations and host construction. Policies run once in order on one fresh
// draft; nil entries panic and expanded nil or empty slices omit edits. Only
// DriverBuilder exposes Registry; NewRunner's required registry supplies its
// driver registry. Published settings detach from retained drafts, not services.
//
//	driver, err := temporalworkflow.NewDriver(temporal.New(), func(driver *temporalworkflow.DriverBuilder) {
//		driver.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = workflow.NewClient(driver)
//
// # Names
//
// The workflow "onboard" with the default prefix is the Temporal workflow type
// "arkoren-wf-onboard", the step "send-welcome" is the activity type
// "arkoren-step-send-welcome", and the signal "email-confirmed" is the signal
// "arkoren.sig.email-confirmed". The workflow ID is the run id of the facade.
// Payloads are the JSON of the facade as raw JSON values, so the Temporal UI
// shows them as they are.
//
// # Semantics
//
//   - Start: conflict policy FAIL and reuse policy REJECT_DUPLICATE, so a
//     second Start with the same id is [workflow.ErrAlreadyStarted], also after
//     the run ended. Delay is the Temporal start delay and Metadata is the
//     memo. With [DriverBuilder.Registry] (the driver of a [Runner] has it) the
//     execution timeout is MaxDuration plus Delay and an unregistered name is
//     [workflow.ErrNotFound].
//   - MaxDuration: the runner starts a timer when the workflow starts; when it
//     fires, the context of the run is canceled, blocking calls return an
//     error that matches [workflow.ErrTimeout], and the run fails with that
//     error. The server execution timeout (MaxDuration plus Delay, set when
//     the driver knows the registry) is only a backstop for a stuck run; it
//     ends the run as timed out, which is also [workflow.ErrTimeout] and
//     [workflow.Failed].
//   - Cancel: CancelWorkflow. The context of the run is canceled, so Sleep,
//     Wait, Select, Step, and Child return [workflow.ErrCanceled] and the
//     run ends as Canceled; Result is [workflow.ErrCanceled] and Status is
//     [workflow.Canceled]. The driver describes the named execution before
//     observations and commands; a wrong workflow name is [workflow.ErrNotFound]
//     and never issues a cancel or signal. Finished known runs cancel without
//     error. Native clients need description permission in addition to command
//     permissions. These are typed-handle checks, not application authorization.
//   - Signal: SignalWorkflow, which buffers the value until the workflow
//     reads it. An unknown or finished run is [workflow.ErrNotFound]. The
//     facade keeps the first value per signal; later values stay unread.
//   - Status: Running, and ContinuedAsNew, are [workflow.Running]; Completed
//     is [workflow.Completed]; Failed, TimedOut, and Terminated are
//     [workflow.Failed]; Canceled is [workflow.Canceled]. Result of a
//     terminated run is an error of kind "temporal.terminated".
//   - Failures: a failing workflow or step ends with an application error of
//     type "arkoren.error" whose detail is the [workflow.EncodeError]
//     envelope, so error kinds survive. A [workflow.Permanent] failure is a
//     non-retryable application error.
//   - Steps run as activities with a start-to-close timeout of Timeout and a
//     Temporal retry policy built from Attempts, Initial, Factor, and Max.
//     Per-call options override them. Timeout zero removes the attempt limit;
//     the workflow MaxDuration supplies the required native schedule-to-close
//     bound instead. A timeout is [workflow.ErrTimeout]. A positive Max caps
//     even the first retry interval; zero Max requests Go's largest duration
//     rather than Temporal's implicit 100-times-initial cap. Backend duration
//     limits can reduce that request: full Go-duration portability is not promised.
//     Multiple attempts with Initial zero return [workflow.ErrUnsupported] before
//     activity encoding or scheduling; no approximate positive delay is inserted.
//     A single attempt remains supported because backoff is unused. The completed
//     local Step.Derive policy is authoritative, not the registered default.
//   - Child runs a child workflow with the execution timeout of its MaxDuration
//     and waits. Parent cancellation requests cancellation of an owned native
//     child, but ends the runtime wait without requiring child callbacks to stop. The
//     child id is joined when a run with it exists: a workflow cannot join a
//     run, so the runner waits for it in an activity ("<prefix>-join") that
//     reads the result through the client; the run must use the same task
//     queue and the activity is retried at most three times. Spawn starts the child with
//     the parent close policy ABANDON on a context that is disconnected from
//     the parent, and waits only until the child started. Canceling observation
//     of an already existing run does not cancel that run. Spawn's acknowledgement
//     wait still ends on parent cancellation or MaxDuration; the detached child
//     might have started even when that wait returns an error.
//     Creation versus joining is decided by Temporal's atomic child-start
//     acknowledgement, not a status lookup. Only newly created children have
//     parent-close policy TERMINATE; an existing run retains its original owner,
//     including after parent MaxDuration. This clarification adds no history
//     command or marker and does not change the existing child/join sequence.
//   - Wait and Select use a Temporal selector over the signal channels, the
//     cancellation of the run, and timers. Timers that lose are canceled and
//     signals that no case consumed stay buffered.
//   - Now is the workflow time, UUID is derived from the run ID of the execution and a counter (no history
//     event), and Logger
//     writes to the Temporal workflow logger, which is silent while the
//     workflow replays. LogValuers, including nested groups and bound attributes,
//     are resolved before crossing into native logging so slog redaction applies.
//     Raw returns the [go.temporal.io/sdk/workflow.Context]
//     of the run.
//   - Done and Err: Err reads the Temporal context, so it is deterministic and
//     accurate as soon as the cancellation was delivered. Done is a native
//     channel that is closed when a runtime call or Err sees the context done,
//     or when a coroutine of the runtime that waits on the Temporal channel is
//     scheduled. Poll it with a non-blocking select; do not block on it with
//     "<-ctx.Done()": Temporal cannot schedule a native block and fails the
//     workflow task as a deadlock. Use Sleep, Wait, or Select to block.
//   - Retention is a setting of the Temporal namespace and is ignored, like
//     Description.
//
// # Retry-policy compatibility and rollout
//
// Core step defaults remain supported: one attempt, or a positive one-second
// initial delay when Retry enables more attempts. Whole-workflow execution retry
// policy remains absent; finite application step retries are native activity
// retries. Omitting an activity retry policy would enable native server defaults,
// including potentially unlimited attempts, so this driver never uses omission
// to disable application retries.
//
// The previous driver silently replaced zero Initial with a positive native
// interval. Rejecting that policy changes command selection and is not safe as
// an unqualified in-place worker upgrade for existing zero-policy histories.
// Keep those executions on legacy workers using application-owned worker routing
// until they finish, or drain them before upgrading. Explicitly choosing the old
// positive interval can preserve equivalent histories; replay your actual
// histories before deployment. The driver adds no version marker and does not
// use IsReplaying to suppress policy rejection.
//
// Max zero avoids the native implicit 100-times-initial cap, not every backend
// duration bound. Native server versions can cap requested durations. Inspect
// actual server version and effective policy when engine limits matter; do not
// infer the server version from a CLI image tag or assume a universal maximum.
// The integration-tested two-attempt policy on server 1.29.1 retained 876000 hours
// (100 times 365 days), rather than the requested largest Go duration. This is
// evidence for that server and policy, not a duration guarantee for every backend.
//
// # Telemetry
//
// Configure workflow.RegistryBuilder.Telemetry in the worker registry, separate
// from workflow.ClientBuilder.Telemetry in each client process. StartOptions.Trace
// travels in a dedicated argument envelope, never memo. Replay forwards it as
// deterministic data through activities and child/spawn runs; real activities
// extract it before invoking the registered step. Signals keep the start parent.
// No host propagator hook is needed. Deploy upgraded workers before enabling
// traced producers; old workers cannot decode traced envelopes. Legacy untraced
// histories retain their argument shape. Export every pod to a common backend;
// client Result observations measure waiting, not durable run duration.
//
// # Caveats
//
//   - [NewRunner] fails on a host built with [temporal.Builder.NoWorker]. A
//     process that sets Builder.NoWorker is client-only and uses [NewDriver]; a
//     runner on such a host would never run a workflow.
//   - Temporal ends a blocked workflow coroutine with runtime.Goexit, which
//     recover does not catch, so the panic recovery of the facade does not
//     swallow it. The runner needs no special handling. A panic in workflow
//     code becomes a workflow failure, like in the other drivers, and not a
//     retried workflow task.
//   - The workflow function replays, so it must be deterministic. Do not start
//     goroutines that call the context.
//   - The runner and the drivers that talk to it must use the same prefix and
//     the same codec in [workflow.HandlersWith] and [workflow.ClientBuilder.Codec].
//   - Workflow IDs are unique per namespace, so the same run id of two
//     different workflows collides.
//   - A step that fails makes the Temporal worker log an error line for every
//     attempt.
package temporalworkflow
