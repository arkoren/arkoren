# Temporal schedules

`arkoren.dev/driver/temporal/temporalschedule` implements the core schedule
driver using Temporal Schedules for recurring entries and delayed workflows for
deferred jobs. Install it with `go get arkoren.dev/driver/temporal`.

Construct `temporalschedule.New(host)` before starting your existing Temporal
host, then pass the driver to `schedule.New(driver)`. Start the host before
running the scheduler or issuing deferred-job commands. A client supplied by
`temporal.NewFrom` is already available; a dialing host has no client until
Start succeeds. Driver operations without a client return `ErrNotStarted`.

You can defer a job before the scheduler's Run begins, including from a separate
process using the same prefix and task queue. If the native activity arrives
before the local dispatcher is installed, it waits within its activity timeout
instead of failing the single-attempt delivery. Start Run promptly: waiting
consumes the activity's execution budget. Only one Run can be active locally.

On cancellation, Run returns nil, removes its dispatcher, and cancels active
dispatch contexts. Later activities wait for the next local Run. Run does not
wait for callbacks to finish or forcibly stop callbacks that ignore context.
Host shutdown also cancels waiting and active activities; its native grace wait
is not a total shutdown deadline. Stop the host after its Start has returned.

Each firing makes one activity attempt. Activity execution is not an
exactly-once side-effect guarantee: make application effects idempotent when
Temporal redelivers work after failures. The driver reconciles and deletes
undeclared schedules under its prefix; give independently managed schedulers
different prefixes. Deferred workflow IDs are separate from recurring schedules
and are not pruned.

See the [package API](https://pkg.go.dev/arkoren.dev/driver/temporal/temporalschedule)
for construction examples and cron/time-zone restrictions.
