// Package hatchetworkflow provides a Hatchet-backed [workflow.Driver]. Every facade
// workflow becomes a Hatchet standalone durable task and every step a
// standalone task, so retries, timers, waits, and replay after a restart live
// in Hatchet and any worker that registered the same definitions can run
// them.
//
// Construction uses [DriverBuilder] or [RunnerBuilder], separately from workflow
// declarations and host construction. Policies run once in order on one fresh
// draft; nil entries panic and expanded nil or empty slices omit edits. Runners
// publish completed settings to their drivers without replaying user callbacks.
// Retained drafts cannot change published settings; hosts and loggers stay shared.
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		host := hatchet.New(func(host *hatchet.Builder) {
//			host.Token(token)
//		})
//		compose.Cleanup(host.Stop)
//		registry := compose.Must(workflow.Handlers(Onboard, SendWelcome))
//		runner := compose.Must(hatchetworkflow.NewRunner(host, registry))
//		client := workflow.NewClient(runner.Driver())
//		// Pass client to your application services here.
//		_ = client
//		compose.Startup(host.Start)
//	})
//	if err != nil {
//		return err
//	}
//
// A process that only starts and queries runs needs no definitions and no
// worker:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		host := hatchet.New(func(host *hatchet.Builder) {
//			host.Token(token)
//		}, func(host *hatchet.Builder) {
//			host.NoWorker(true)
//		})
//		compose.Cleanup(host.Stop)
//		driver := compose.Must(hatchetworkflow.NewDriver(host))
//		// Pass driver to your workflow client here.
//		_ = driver
//		compose.Startup(host.Start)
//	})
//	if err != nil {
//		return err
//	}
//
// # Mapping
//
// A workflow "<name>" runs as "<prefix>-wf-<name>" with an execution timeout
// of MaxDuration plus [DriverBuilder.TimeoutMargin] (the runtime enforces
// MaxDuration itself), an eviction policy so waits free the worker slot, and
// a versioned idempotency key kept for MaxDuration plus Retention. The key is
// "arkoren.workflow.v2.<namespace>.<prefix>.<name>.<id>", with each component
// independently encoded as unpadded base64url. Namespace is the effective SDK
// prefix, including its trailing underscore, or empty. The separators cannot
// occur in encoded components. Hatchet's idempotency keys are tenant-wide;
// including every component lets namespaces start independently. A second
// Start of the same identity returns [workflow.ErrAlreadyStarted], also after
// the run ended, until that window passes. A step "<name>" runs as
// "<prefix>-step-<name>" with retries Attempts-1. Retryable failures request
// registered StepOptions.Backoff(RetryCount+1) through native RetryAfterError,
// preserving Initial, Factor, and Max on engines supporting explicit delays.
//
// Inputs and outputs are JSON objects {"data": "<facade envelope>"}: the
// payload travels as a string because the SDK decodes numbers into float64.
// Signals are Hatchet events "<prefix>.sig.<name>" in a physical-execution
// scope: "arkoren.workflow.signal.v1.<key>.<execution>", with the entire logical
// key and engine workflow-run ID separately encoded as unpadded base64url.
// Marker keys append ".<signal>" with the signal name encoded the same way.
// The sender uses its verified physical ID; the runtime uses the identical
// DurableContext.WorkflowRunId, never a task ID or invocation/retry counter.
// Reusing a logical ID after TTL expiry creates a different scope and marker.
// The SDK prefixes workflow and event names with the namespace.
// Historical waits consider all retained events in that exact physical scope
// from a fixed Unix epoch boundary, including signals accepted while queued
// before any worker ran. No worker clock or fixed lookback skew excludes them.
//
// The runtime memoizes Now and UUID in the durable event log; Sleep, Wait,
// and Select are durable waits. Step passes the native Hatchet durable context
// to the SDK's durable-child machinery. Child and Spawn return
// [workflow.ErrUnsupported] before submission; external client Start is unchanged.
// MaxDuration still begins at the memoized first worker invocation, not
// submission: the deadline is that time plus Delay plus MaxDuration. Queueing
// does not consume this runtime deadline; ScheduleTimeout bounds queueing.
// The concrete SDK durable context is bound to that deadline for native joins
// and RPCs, then restored before terminal reporting. This is cooperative
// cancellation, not forced termination of arbitrary Go code. Late workflow or
// step success is rejected after its deadline. Durable memo hooks are checked
// before invocation: an SDK capability-probe failure must not silently select
// its process-local memo fallback.
//
// # Execution identity and upgrades
//
// Clients and workers must use the same host namespace and workflow prefix.
// Discovery resolves the exact namespaced workflow to its UUID, filters by
// that UUID and the new key, and checks the returned workflow name and stored
// wire key/ID before caching a physical execution. Start also verifies a
// successful trigger or collision response before retaining its ID. Missing
// identity fails closed with [workflow.ErrNotFound]; verification can fail
// after a trigger already started work. Namespaces are routing identities,
// not Hatchet tenant authorization boundaries.
//
// Workflow inputs declare signal_version=2. Signal marker inputs and events
// also carry execution_id. Both new marker responses and collisions are checked
// for exact marker workflow, namespaced physical key, version, and owner before
// publishing an event; readers verify the event owner and marker again before
// returning its value. Concurrent senders share one marker for a physical
// execution and signal. Repeated Wait/Select and eviction/restart replay retain
// that scope and first value while the required engine history is retained.
//
// There is no fallback to legacy "<prefix>:<name>:<id>" keys, even without a
// namespace. Upgraded clients cannot query, cancel, or signal old runs, and
// upgraded workers reject legacy inputs before invoking application code.
// Pause producers and signal senders, drain old runs with the old deployment,
// then replace workers and clients together before resuming. Inspect any
// remaining old execution by its verified physical ID through Hatchet; do not
// resubmit its logical ID until its effects are reconciled. Old idempotency
// records do not deduplicate new keys. The previous v2 logical-key deployment
// also used logical-only signal scopes. The subsequent signal_version=1 used
// physical scopes but serialized a worker-relative historical cutoff in waits.
// Neither may be signaled or replayed by this version: changing recorded wait
// conditions is not a compatible replay. Only signal_version=2 is accepted,
// even if an older input's v2 logical key still permits Result, Status, and
// Cancel. Drain those runs with their old workers and clients
// too; changing only the signal sender or worker is not a compatible rollout.
// The physical scope encoding remains v1; the wire version also governs wait
// semantics and is separate from that encoding and the v2 logical key.
//
// Child and Spawn are disabled as a fail-closed containment, not a native
// durable-child implementation. Workflows that call either operation fail unless
// they handle ErrUnsupported. Drain those workflows with their old deployment
// before replacement, including runs already using signal_version=2. Reconcile
// existing child effects by verified physical ID before retrying or replacing
// runs; containment does not undo earlier work. Do not mix old and new workers.
// Native/raw submission or ordinary Start inside a parent is not a safe
// substitute for the missing durable trigger-and-join semantics.
//
// # Caveats
//
//   - Needs Hatchet engine v0.80.0 or newer (durable eviction); Host.Start
//     fails with hatchet.ErrEngineUnsupported otherwise. A version that is
//     not semver only logs a warning.
//   - Delay is a durable sleep at the start of the run, so it counts toward
//     the Hatchet execution timeout; a Delay above [DriverBuilder.TimeoutMargin]
//     returns [workflow.ErrUnsupported].
//   - Per-call options can only shorten registered attempts and timeout (a longer
//     timeout is clamped). A completed Initial=0 overrides positive registration
//     backoff; other per-call backoff changes are ignored. Hatchet counts timeouts
//     in whole seconds; the step derives its own context at Go duration precision.
//     Unlimited registered step timeouts return workflow.ErrUnsupported at
//     NewRunner rather than silently acquiring an arbitrary native limit.
//   - Registered Initial/Factor/Max are sent as explicit millisecond retry
//     delays. Engine v0.111.0+ is required to honor them. Older engines fall back
//     to native factor/max backoff and cannot preserve arbitrary Initial. Server
//     configuration can clamp explicit delays; set its maximum at least as high
//     as the required policy (including uncapped growth). Sub-millisecond delays
//     are truncated by the SDK. These are native fidelity limits, not local sleeps.
//     Initial=0 sends a present retry_after_ms=0, not an absent override: on
//     supported engines it adds no deliberately positive delay, while queueing
//     and delivery can still take time. Finite Attempts remain enforced. Older
//     engines cannot preserve this zero-delay policy through native backoff.
//     Derived zero-delay calls add retry_without_delay to native step inputs;
//     absent fields retain registration behavior. Upgrade all workers before
//     producing that field, and drain older affected workflows before replacement
//     because replay can change their recorded child input. No durable node,
//     wait predicate, logical key, or signal protocol version changes.
//   - Hatchet delivers the newest of several events that arrived before a
//     wait. To keep the first value of a signal, the first Signal stores its
//     value in a marker run (workflow "<prefix>-signal", registered by the
//     Runner, idempotency key per physical execution and signal) and every
//     event carries its ID. Marker idempotency lasts [DriverBuilder.Lookback], measured
//     from marker trigger, independently of the workflow's TTL. Keep Lookback
//     longer than the interval from the first signal through the last possible
//     signal or replay, including queueing, execution, and recovery. Engine
//     event and marker/input retention and the driver's Lookback search window
//     must also cover those intervals. All retained events are eligible, but
//     this does not recover deleted data or extend engine retention. Expiring
//     history or marker idempotency can lose the first-value guarantee within
//     the same physical execution; it cannot expose a different one. Workflow
//     logical idempotency still expires MaxDuration plus Retention after trigger;
//     configure that window to cover queueing/execution and the required period
//     of duplicate-start protection, independently of signal retention.
//   - Run listing lags behind Start. A small LRU enables LookupGrace retries for
//     recently verified runs, never indefinite physical-ID reuse. Each logical
//     lookup rediscovers and verifies the current physical execution; an unknown
//     ID gets one discovery pass, including paginated workflow-name resolution.
//     Result pins the execution it resolved for the duration of that call.
//   - Result caps every status timer, including initial delay and jitter, at
//     PollInterval. Network request time is additional. Cancellation observed
//     in native calls retains the caller's context error identity.
//   - Child and Spawn always return [workflow.ErrUnsupported] without accessing
//     the backend or durable history. The former ordinary submission path could
//     repeat completed work when a parent replayed after the child's TTL. Native
//     durable submission cannot preserve joining an existing logical execution,
//     so there is no fallback or opt-out. Step and external client Start remain
//     supported; facade ID validation still precedes runtime calls.
//   - Set a Hatchet namespace with hatchet.Builder.Namespace (or
//     HATCHET_CLIENT_NAMESPACE), not through ClientOptions: the driver
//     applies it to the event keys of durable waits.
//   - A failed memo of the clock or of an identifier stops the run instead of
//     falling back to local values, so a replay stays deterministic.
//   - Cancel cancels the Hatchet run. A running step stops only when it honors its
//     context.
//   - Host startup and shutdown have pinned-SDK limits: some startup RPCs ignore
//     caller cancellation, worker failures can panic in an internal goroutine,
//     and opaque native intake/reporting is not joinable. Stop joins admitted
//     cooperative application actions, not all SDK goroutines. Native shutdown
//     eviction also races returning waits; see package hatchet. Functional replay
//     does not establish race-free cleanup.
//
// # Telemetry
//
// Configure workflow.RegistryBuilder.Telemetry for worker attempts separately
// from workflow.ClientBuilder.Telemetry for client calls. StartOptions.Trace
// travels in a dedicated wire field, never reserved identity metadata. Durable
// coordination forwards it into step inputs; only actual task attempts extract
// it onto native timeout/cancellation contexts. Replay and eviction do not count
// as attempts. Signals keep the start parent; Child/Spawn remain unsupported.
// Upgrade old workers before expecting continuity, and export each pod to a
// common backend. Client Result observations measure wait latency, not durable
// execution time. Local event buses are not a distributed completion feed.
// StepHandler observes each actual invocation and its returned error, not the
// terminal engine outcome. A late nil return records one successful body attempt
// while the adapter rejects the native result with workflow.ErrTimeout. Client
// Result observes that timeout separately; the adapter emits no duplicate attempt
// or terminal engine event. These observations describe different boundaries.
package hatchetworkflow
