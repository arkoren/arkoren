// Package workflow defines durable, typed workflows built from retried steps, signals, and sleeps, run by a
// swappable engine driver. A [Workflow] is a function
// that orchestrates [Step] values, waits for [Signal] values, sleeps, and
// starts other workflows; a [Driver] runs it and decides how it survives
// restarts. A [Client] starts runs and talks to them. Workflow signals are
// unrelated to the payload-less signals of package arkoren.dev/core/event.
//
//	var SendWelcome = workflow.NewStep("send-welcome", sendWelcome, func(step *workflow.StepBuilder) {
//		step.Retry(5)
//		step.Backoff(time.Second, 2, time.Minute)
//	})
//	var EmailConfirmed = workflow.NewSignal[string]("email-confirmed")
//
//	var Onboard = workflow.New("onboard", func(c *workflow.Context, user User) (string, error) {
//		if _, err := c.Step(SendWelcome, user); err != nil {
//			return "", err
//		}
//
//		token, err := c.Wait(EmailConfirmed, workflow.Timeout(72*time.Hour))
//		if errors.Is(err, workflow.ErrTimeout) {
//			return "expired", nil
//		}
//
//		return token, err
//	}, func(definition *workflow.Builder) {
//		definition.MaxDuration(96 * time.Hour)
//	})
//
// A worker process registers the definitions with the driver:
//
//	registry, err := workflow.Handlers(Onboard, SendWelcome)
//
// [Registry.Workflows] and [Registry.Steps] return eager collection snapshots
// sorted by name. Their records are detached from registration maps, not live
// queries; functions and their captures remain shared, not cloned by wrappers.
//
// A server process only needs a client:
//
//	client := workflow.NewClient(driver)
//	run, err := client.Start(ctx, Onboard, "user-42", user)
//	err = client.Signal(ctx, Onboard, "user-42", EmailConfirmed, "abc")
//	result, err := run.Result(ctx)
//
// # Determinism
//
// A driver may replay a workflow function from the start to rebuild its
// state. The function must therefore not read the clock, random numbers, the
// network, or global state directly. Use [Context.Now], [Context.UUID], and
// steps, which a driver records. Inputs and outputs must round-trip through
// the [Codec], which is encoding/json/v2 by default.
//
// # Signals
//
// [Context.Wait] returns the first value of a signal and remembers it: a
// later Wait or [Context.Select] on the same signal in the same run sees the
// same value. A signal sent before the workflow waits for it is not lost.
//
// # Child ownership
//
// [Context.Child] waits for a newly created or existing run. Only the caller
// that creates the child owns its cancellation: parent cancellation or timeout
// stops newly created children. Joining an existing run ignores the new input
// and acquires no ownership; cancellation or timeout ends only the joining wait.
// Drivers must preserve that atomic decision across replay or reject Child with
// [ErrUnsupported] before submitting work. Use [Context.Spawn] for intentionally
// detached work, not as a substitute for unsupported Child semantics.
//
// # Errors
//
// A failure of a run reaches [Run.Result] as an error. [ErrCanceled],
// [ErrTimeout], and any [*Error] keep their kind across the wire, so
// [errors.Is] works in the process that started the run. A step that returns
// [Permanent] is not retried.
//
// # Failure messages
//
// A failing run stores the text of its error in the engine's history, and
// [Run.Result] returns it to every caller, in any process. Error text is
// persisted and exported, so keep secrets and personal data out of the
// messages of errors a workflow function or step returns; wrap them with
// [NewError] and a generic message when the underlying error may carry
// values.
//
// # Registration
//
// Definitions are closures registered by name in every worker process. [New]
// and [NewStep] publish immutable definitions from [Builder] and
// [StepBuilder] callbacks. Settings return the same draft, and the first error (an invalid
// name, a nil function, a bad duration) is kept and reported by [Workflow.Err]
// and [Step.Err] and returned by [Handlers] as [ErrInvalidName], [ErrNilFunc],
// or [ErrInvalidOptions]. Callbacks run directly once, synchronously in order on
// one fresh draft; supplied nil entries and application panics propagate.
// Expanded nil or empty callback slices omit edits. Publication detaches the
// settings, so retained drafts cannot change definitions. Registration captures
// the name, function, and settings independently of the published pointer.
// Names are 1-64 lowercase letters,
// digits, or hyphens with no hyphen at either end, and share one namespace for
// workflows and steps. Run ids are free-form but not empty and free of ASCII
// control characters, else [ErrInvalidID]. A workflow must set
// [Builder.MaxDuration]; omission is rejected only at registration.
// Zero workflows and steps are invalid and report ErrInvalidName. Client.Start,
// Context.Step, Context.Child, and Context.Spawn reject stored declaration errors
// before encoding or dispatch, even when a worker registered a valid definition
// under that same name. Deriving a zero step preserves its declaration error.
// To change the settings of one step call, use [Step.Derive]:
//
//	singleAttempt := SendWelcome.Derive(func(step *workflow.StepBuilder) {
//		step.Retry(1)
//	})
//	_, err := workflowContext.Step(singleAttempt, user)
//
// Derive preserves the name, function, inherited policy, and first error.
// Backoff initial zero requests immediate step retries without a deliberate
// positive delay. An engine unable to express the completed multi-attempt policy
// returns ErrUnsupported before scheduling or executing the affected step. It
// must not substitute a positive default or retry the whole workflow instead.
// Single attempts and positive backoff retain their normal support. Provider
// duration limits remain provider constraints; this defines no new core maximum.
// Names, JSON payloads, and an explicit serialized trace carrier cross the driver;
// context.Context itself never crosses processes.
//
// # Events
//
// NewClient uses ClientBuilder callbacks for Codec, Telemetry, and Events,
// separate from definition and per-start options. Callbacks run once in order on
// one fresh draft; nil entries panic and nil/empty slice expansion omits edits.
// Publication detaches retained roots, not shared codecs or event buses. A final
// nil codec selects JSON and must match the worker registry's encoding.
//
// With [ClientBuilder.Events], a [Client] publishes [RunStarted] ("workflow.run.started")
// after [Client.Start] starts a run, [RunSignaled] ("workflow.run.signaled")
// after [Client.Signal] delivers a signal, and [RunCanceled]
// ("workflow.run.canceled") after [Run.Cancel] on a run from that client.
// Payloads carry the workflow name, the run ID, and for signals the signal
// name; never inputs, outputs, metadata, or signal values. Failed calls
// publish nothing.
// These are local successful-action notifications, not a distributed engine
// completion feed. Polling Result or Status does not publish terminal events.
//
// # Telemetry
//
// ClientBuilder.Telemetry observes start, signal, cancel, status, and result
// calls. Result duration is wait latency, not execution time. Configure worker
// observations separately through NewRegistry and RegistryBuilder.Telemetry:
//
//	registry, err := workflow.NewRegistry([]workflow.Definition{Onboard, SendWelcome},
//		func(worker *workflow.RegistryBuilder) {
//			worker.Telemetry(observations)
//		})
//	if err != nil {
//		return err
//	}
//	_ = registry.Steps()
//
// Nil telemetry follows the current process default per call/attempt. Actual
// StepHandler.Handle invocations produce workflow step_attempt spans, a count,
// and a seconds histogram. Each retry is a new attempt; replayed orchestration,
// suspension, sleeps, and recorded step results produce no attempt observations.
// The step context supports dependency child spans. No payload, signal value,
// output, metadata, run ID, or raw error text enters automatic telemetry; metric
// labels contain only operation, outcome, and classified error type.
//
// StartOptions.Trace carries propagation independently of application metadata.
// Drivers forward it as deterministic data to children/spawns where supported,
// then Registry.NewTraceContext extracts it onto each native attempt context.
// Signals do not replace this start parent. Configure an exporter in every pod
// and use a common backend for distributed traces; local event buses and memory
// capture do not aggregate remote pods. Only trusted submitters should be able
// to supply carriers. No terminal engine subscription exists in this contract.
package workflow
