// Package hatchet holds the [Host] that drivers in this module share. A host
// owns one Hatchet client and one worker. Drivers such as
// arkoren.dev/driver/hatchet/hatchetschedule register their workflows with
// [Host.Register] before [Host.Start], which connects, registers every
// workflow, and starts the worker.
//
// Construction callbacks edit one fresh [Builder], once in order; a supplied
// nil callback panics. Expanded nil or empty slices leave defaults unchanged.
// Publication detaches owned native-option slices, not SDK hooks or captures.
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		host := hatchet.New(func(host *hatchet.Builder) {
//			host.Token(token)
//		}, func(host *hatchet.Builder) {
//			host.HostPort("localhost:7077")
//		})
//		compose.Cleanup(host.Stop)
//		driver := compose.Must(hatchetschedule.New(host))
//		scheduler := schedule.New(driver)
//		// Add entries to scheduler before startup.
//		compose.Startup(host.Start)
//		compose.Run(scheduler.Run)
//		compose.Check("hatchet", host.Ping)
//	})
//	if err != nil {
//		return err
//	}
//
// A host with no registrations, or created with [Builder.NoWorker], connects the
// client only and starts no worker. When a registered task is durable, Start
// checks that the engine supports durable eviction (v0.80.0 or newer) and
// returns [ErrEngineUnsupported] otherwise. [Builder.DurableSlots] sets the
// number of durable tasks the worker keeps at once.
// NoWorker defaults to false; a later false restores permission without forcing
// an empty worker. HasWorker reports permission, not running-worker evidence.
//
// # Lifecycle limits
//
// New performs no I/O and retains the existing pointer-only API. Start validates
// explicit adapter settings before acquiring the SDK client. Stop is terminal;
// it waits for concurrent startup, cancels and joins admitted application actions,
// and invokes native release once, even with an expired cleanup context. Task
// bodies must honor cancellation. Do not call Stop from a task it must join.
//
// The pinned SDK performs some startup RPCs with background contexts and starts
// an internal goroutine that can panic on worker failure. RegisterTimeout only
// budgets registration-log observation, not all startup. Canceled observation
// waits for the same log/timeout handoff and cleans up synchronously. Neither
// Start nor Stop guarantees caller-bounded termination or joins opaque SDK
// intake/reporting goroutines. Native Client.Close releases embedded mode but is
// a no-op for ordinary gRPC connections. Ping checks API reachability, not worker
// liveness or schedule Run readiness. Deploy under a process supervisor; a host
// is not an in-process supervisor for SDK panics.
// StartBlocking(ctx) still calls the same native Start path before waiting for
// ctx. The SDK's task WithPanicHandler does not catch startup-loop panics; no
// per-client dialer/interceptor or owned-connection injection is exposed by this
// SDK version. See https://github.com/hatchet-dev/hatchet/issues/3283 for the
// upstream startup-context defect. Do not equate a closed issue with a fix in
// the pinned release.
// Native shutdown also has a confirmed race between the eviction manager's
// unlocked wait-metadata read and a returning durable wait. The host cannot
// synchronize that private SDK cache; durable shutdown is not race-clean in the
// pinned release, even though admitted application callbacks are joined.
package hatchet
