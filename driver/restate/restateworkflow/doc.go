// Package restateworkflow implements [workflow.Driver] on Restate workflow services.
//
// Two types split the work. [NewRunner] runs in the worker process: it binds
// one Restate workflow service per workflow of a [workflow.Registry] to a
// [restate.Host]. [NewDriver] runs in any process that starts and controls
// runs: it talks to the Restate ingress and admin API through the host and
// needs no endpoint, so its host can be client-only with
// [restate.Builder.ClientOnly].
//
// Construction uses [DriverBuilder] or [RunnerBuilder], separately from workflow
// declarations and host construction. Policies run once in order on one fresh
// draft; nil entries panic and expanded nil or empty slices omit edits. Published
// settings are independent of retained drafts; hosts and loggers stay shared.
//
//	driver, err := restateworkflow.NewDriver(restate.New(), func(driver *restateworkflow.DriverBuilder) {
//		driver.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = workflow.NewClient(driver)
//
// # Services
//
// The workflow "onboard" with the default prefix becomes the service
// ArkorenWf_onboard. The facade allows only [a-z0-9-] in names; the "Wf_"
// marker guarantees a leading letter and keeps the mapping injective. The
// service has three handlers:
//
//   - Run executes the workflow. Its input and output are the JSON payloads of
//     the facade, so plain application/json callers work. A failure ends the
//     invocation with a terminal error whose message is the
//     [workflow.EncodeError] envelope; the code is 409 for a canceled run and
//     500 otherwise.
//   - Signal is a shared handler. It resolves the durable promise "sig:<name>".
//     Only the first value of a promise counts, so a second signal is dropped
//     without error. It answers 404 unless Run is running.
//   - Cancel is a shared handler that stops the local attempt of a run.
//
// Steps run inline in Run as durable Run actions (restate.Run). A step has no
// service of its own. Attempts, Initial, Factor, and Max become the retry
// policy of the action; a positive Timeout supplies a context deadline and
// expiry yields [workflow.ErrTimeout] after the handler returns. A late success after
// expiry is rejected. Handlers must cooperate with context cancellation; this
// driver cannot forcibly terminate arbitrary handler code and does not start
// untracked goroutines to escape it. Zero Timeout adds no attempt deadline.
// Initial zero means immediate retry and Max zero removes the backoff cap up to
// the largest Go duration; explicit fields prevent native defaults from taking
// over. Restate retry delays have millisecond resolution and its factor is
// represented as float32. A [workflow.Permanent] failure is a terminal
// error and is not retried. Restate records the result of an action, so a
// replay does not run a finished step again.
//
// Child returns [workflow.ErrUnsupported] before submitting work. Restate's
// durable call/send invocation-ID notification does not say whether this call
// created the workflow or found another owner's run. A request/response conflict
// is not a creation acknowledgement available before waiting. Sending then
// attaching cannot safely decide whether parent cancellation owns that ID, and
// a separate status lookup is not atomic with creation. No control store is used.
// Spawn still sends and returns after a short durable timer, which makes the run
// visible to Status. Context.Now and Context.UUID are journaled, so replays see the
// same values. Each Now is one journal entry.
//
// # Semantics
//
//   - MaxDuration: Run starts a durable timer when the handler first runs, so
//     the clock starts at the start of execution, not at Start (a delayed run
//     does not spend it while it waits). Every blocking call of the runtime
//     (Sleep, Wait, Select, Step, Spawn) also waits for this timer. When it
//     fires the call returns an error that matches [workflow.ErrTimeout], Done
//     is closed, and later runtime work fails at once. Child rejects without
//     waiting or creating a child. The limit is enforced at the next blocking
//     call: code that computes without calling the runtime is not interrupted,
//     and a step that is running when the timer fires is abandoned, not
//     stopped. The inactivity and abort timeouts of the Run handler are a
//     fixed 24 hours, not MaxDuration, so a long step is not aborted by the
//     server defaults.
//   - Cancel: [Driver.Cancel] finds the invocation in sys_invocation, calls the
//     Cancel handler, and calls the admin cancel. The handler closes Done at
//     once when the attempt runs in the process that serves the call, so a
//     workflow blocked on Done unblocks; otherwise, and for a running step or
//     blocking call, Restate delivers the cancellation at the next durable
//     operation. Result is [workflow.ErrCanceled] and Status is
//     [workflow.Canceled]. In the legacy server profile, cancellation before
//     execution discards the record: Result and Status return
//     [workflow.ErrNotFound], and the key is immediately reusable. Portable
//     retained cancellation needs the persisted server feature
//     preflight_invocation_termination_retention and positive effective workflow
//     retention. This feature is opt-in in the tested 1.7.12 server, introduced
//     in 1.7.8; enabling it establishes a persistent server-version floor. The
//     driver does not enable or verify it. Admin health and requested feature
//     flags do not prove that all partitions applied their feature barriers.
//   - Signal: the Signal handler returns 404 unless Run is running (a state
//     key set by Run and cleared at its end), which is [workflow.ErrNotFound]
//     for an unknown or finished run. For an accepted run that has not started,
//     for example one that waits for its delay, the driver repeats the call
//     with force, and the promise keeps the value. A signal sent before the
//     workflow waits for it is kept by the promise.
//   - Start with a delay is a delayed send. Metadata becomes request headers
//     prefixed "arkoren-meta-". A second Start with the same id is
//     [workflow.ErrAlreadyStarted].
//   - Status: a run without an output is [workflow.Running], which includes a
//     run that waits for its delay or retries. A failure with code 409 is
//     [workflow.Canceled], any other failure is [workflow.Failed].
//   - Retention: [workflow.Builder.Retention] sets the workflow retention of the
//     service, which is how long Restate keeps the run, its output, and its
//     promises after the end. Without it the Restate default applies (one
//     day). After that, Result and Status return [workflow.ErrNotFound].
//
// # Telemetry
//
// Configure workflow.RegistryBuilder.Telemetry in serving processes separately
// from workflow.ClientBuilder.Telemetry in client pods. StartOptions.Trace is
// encoded into explicit arkoren-trace-* invocation headers, never user metadata
// headers. Spawn forwards only that carrier as deterministic data.
// Actual Run closures extract it onto native attempt contexts before invoking
// steps; orchestration replay and suspension produce no telemetry side effects.
// SDK attempt headers and signals do not replace the start parent. Upgrade old
// workers before expecting continuity and export all pods to a common backend.
// Client Result observations measure waiting, not durable execution time.
//
// # Caveats
//
//   - It needs a running restate-server (tested with 1.7.12) that can reach
//     the endpoint of the host, and a registered deployment, either through
//     [restate.Builder.Register] or by the operator. Cancel needs the SQL
//     introspection API of the admin service.
//   - The Restate SDK ends and suspends attempts with panics. The runtime
//     implements Repanic, so the facade raises them again instead of turning
//     them into workflow failures.
//   - The workflow function replays after every suspension, so it must be
//     deterministic. Do not use goroutines that call the context.
//   - The runner and the drivers that talk to it must use the same prefix and
//     the same codec in [workflow.HandlersWith] and [workflow.ClientBuilder.Codec].
//   - Each rollout is a new deployment. Keep workflows compatible with runs in
//     flight. Child rejection removes the previous send/attach journal sequence;
//     it is not replay-compatible with in-flight histories that used Child.
//     Drain those runs on their original deployment before upgrading; do not
//     force-migrate them to the rejecting worker. Existing completed outputs
//     remain retained. No version marker or automatic history migration is added.
//   - A failing step attempt makes the SDK log an error line and retry through
//     the server; this is the SDK's way to back off and not a failure.
package restateworkflow
