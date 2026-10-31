// Package restateschedule implements [schedule.Driver] on Restate virtual objects.
//
// Three services are bound to a [restate.Host]: a virtual object keyed by
// entry name that chains one delayed message per tick, an index object that
// removes entries that disappeared from the code, and a service that runs
// deferred jobs. Restate stores the chain, so ticks survive restarts and run
// once per tick across replicas.
//
// Construction callbacks edit one fresh [Builder], once in order; nil entries
// panic, while expanded nil or empty slices omit edits. Published settings detach
// from retained drafts; the host and logger remain shared dependencies.
//
//	driver, err := restateschedule.New(restate.New(), func(scheduler *restateschedule.Builder) {
//		scheduler.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = schedule.New(driver)
//
// Caveats:
//   - It needs a running restate-server. Replicas send their entries through
//     the ingress, so the ingress must be reachable.
//   - The endpoint must be reachable by the server and registered, with
//     [restate.Builder.Register] or by the operator.
//   - Each rollout is a new deployment. Registered handlers must stay
//     compatible with in-flight invocations of older deployments.
//   - Runs are at least once. A handler may run again after a crash.
//   - Daylight saving time follows the core parser: a time in the spring gap
//     is skipped and a time in the autumn overlap runs once.
//   - The Local time zone is rejected with [schedule.ErrUnsupported]: replicas
//     could disagree about it.
//   - Entries marked without overlapping run inside the Tick invocation, so
//     the next run of the same entry waits for the current one.
//   - Deferred task payloads retain JSON null. Recurring dispatch receives nil,
//     including legacy recurring messages whose payload member was null.
//   - Removing an entry retains its generation tombstone. Re-adding the same
//     name advances the generation so already queued ticks stay obsolete.
//   - Removal now journals individual clears rather than ClearAll. Keep an old
//     deployment available for its in-flight Remove invocations until they end;
//     do not force replacement onto an incompatible handler journal. Names,
//     state keys, tick format, and idempotency-key format are unchanged. Entries
//     removed by older workers have no tombstone; drain their pending ticks
//     before reintroducing the name, because that history cannot be recovered.
package restateschedule
