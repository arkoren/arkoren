// Package temporal holds the code shared by the Temporal drivers: the [Host],
// which owns one Temporal client and one worker for a task queue.
//
// Construction callbacks edit one fresh [Builder], once in order; a supplied
// nil callback panics. Expanded nil or empty slices leave defaults unchanged.
// Published SDK records are value copies, not recursive native snapshots.
//
//	host := temporal.New(func(host *temporal.Builder) {
//		host.TaskQueue("orders")
//		host.NoWorker(true)
//	})
//	_ = host.Client() // Nil until Start.
//
// A module root never holds a driver. Each driver, such as
// arkoren.dev/driver/temporal/temporalschedule, registers its workflows and
// activities with a Host through [Host.Register] before [Host.Start] runs, and
// reads the client through [Host.Client].
//
// [New] builds a Host that dials Temporal in [Host.Start] and closes the client
// in [Host.Stop]. [NewFrom] wraps a caller-owned client, which [Host.Stop]
// leaves open.
// Its applicable worker settings include NoWorker and WorkerStopTimeout, in
// addition to TaskQueue and WorkerOptions. Logger and connection settings are
// ignored by NewFrom: workers inherit the caller-owned client's logger. A nonzero
// native worker stop timeout still wins over the host timeout. A dialing host
// adapts slog.Default when neither host nor native client supplies a logger.
//
// With no registrations, or with [Builder.NoWorker], [Host.Start] creates no
// worker and the Host is only a client; a process that just starts runs uses
// this. A nil NewFrom client fails in Start. A canceled startup does not publish
// a started host. SDK worker startup cannot itself be interrupted: Start checks
// cancellation again after it returns and releases acquired resources on failure.
// Registration callbacks and SDK hooks must return for startup to finish.
//
// [Host.Stop] initiates cleanup even with an expired context. It waits for the
// worker only until that context ends; owned client release follows completion
// of native worker cleanup, possibly later. Call Stop after Start returns.
// [Builder.WorkerStopTimeout] (ten seconds by default) is the native grace wait,
// potentially applied separately to internal workers, not a total shutdown
// budget. Expiry cancels activity contexts, not arbitrary application code.
// SDK shutdown hooks must return; callbacks that ignore cancellation can outlive
// cleanup. Use Stop's context to bound the caller's wait after startup.
// NoWorker defaults to false; a later false restores permission, but never
// forces a worker without registrations. WorkerDisabled reports the stored flag.
package temporal
