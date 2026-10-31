// Package hatchetschedule provides a Hatchet-backed [schedule.Driver]. Recurring
// entries become Hatchet cron triggers and deferred jobs become scheduled
// runs, so timing and persistence live in Hatchet and any process with a
// started worker can run the work.
//
// Construction callbacks edit one fresh [Builder], once in order; nil entries
// panic, while expanded nil or empty slices omit edits. Published settings detach
// from retained drafts; the host and logger remain shared dependencies.
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		host := hatchet.New(func(host *hatchet.Builder) {
//			host.Token(token)
//		})
//		compose.Cleanup(host.Stop)
//		driver := compose.Must(hatchetschedule.New(host))
//		scheduler := schedule.New(driver)
//		// Add entries to scheduler before startup.
//		compose.Startup(host.Start)
//		compose.Run(scheduler.Run)
//	})
//	if err != nil {
//		return err
//	}
//
// # Registration
//
// [New] registers three standalone tasks on the host: "<prefix>-run",
// "<prefix>-run-exclusive" (one run per entry name at a time, new runs are
// canceled), and "<prefix>-deferred". Create the driver before Host.Start.
// Cron REST operations apply the host namespace explicitly; ownership metadata
// and hashes retain their logical workflow names. A failed cron inventory request
// fails reconciliation instead of being treated as an empty inventory.
//
// Host.Start can receive native assignments before schedule Run begins. Those
// actions wait for dispatch readiness or native cancellation. Run installs its
// callback once; on every return it removes that callback, cancels admitted call
// contexts, and joins their bodies. A noncooperative body can delay return.
// Later assignments cannot invoke an ended Run's callback. Host.Stop separately
// owns worker release and has the lifecycle limits documented by package hatchet.
//
// # Deferred payloads and rollout
//
// New deferred inputs carry name and payload_json_v1. The latter is a JSON
// string containing the complete business JSON, not a nested business value.
// Hatchet's server and worker decode generic maps using float64 numbers; this
// opaque field preserves int64 and uint64 limits, nested values, null, and
// string payloads. The driver restores bytes only at scheduler dispatch.
// Recurring inputs remain name-only and dispatch a nil payload.
//
// Workers also accept legacy payload fields as raw business JSON. A legacy
// JSON string is never reinterpreted as the new envelope. Inputs containing
// both fields, a non-string payload_json_v1, or invalid enclosed JSON fail
// before dispatch. Empty deferred payloads remain absent.
//
// Upgrade every worker before allowing upgraded producers to defer tasks;
// old workers ignore the new field and cannot dispatch its payload correctly.
// If workers also produce jobs, pause deferrals for the worker rollout, then
// resume after every old worker has stopped. Legacy pending jobs remain
// readable, but already-rounded numbers cannot be recovered. Audit critical
// pending numeric arguments against authoritative records and cancel/recreate
// affected jobs from those records before execution; decoding is not repair.
//
// # Caveats
//
//   - UTC only. Hatchet evaluates cron expressions in UTC. An entry in
//     another time zone makes Run return [schedule.ErrUnsupported] unless
//     [Builder.AllowCronTZ] is set.
//   - Last pod wins. Run reconciles the crons of this prefix with its own
//     entries and deletes the rest. During a rolling deploy the last process
//     to start decides the final set.
//   - No retries, at-least-once delivery. The driver sets no retry count,
//     so a run that fails or times out is marked failed, not retried. Hatchet
//     can still deliver the same run twice, such as after a worker loses its
//     connection. Entries must be idempotent.
//   - Cancel is best effort. A job that already started is not stopped.
//   - MaxRunTime defaults to one hour when zero; a negative value is invalid.
//     This is the native task backstop, not a guarantee of forced Go termination.
package hatchetschedule
