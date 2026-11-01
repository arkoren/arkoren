# Schedule

> [!NOTE]
> Hatchet references below describe a pending, unpublished integration, not
> available support. See the [scheduler comparison](../../../cmd/arkoren/skills/arkoren/references/guides/scheduled-tasks/drivers.md#compatibility-matrix)
> for available drivers.

`arkoren.dev/core/service/schedule` runs functions on a calendar and defers one-off typed
tasks. The scheduler owns what runs; a driver owns when it runs and how
processes coordinate.

Install with `go get arkoren.dev/core` and import
`"arkoren.dev/core/service/schedule"`. For driver setup and lifecycle examples,
see the [scheduled tasks guide](../../../cmd/arkoren/skills/arkoren/references/guides/scheduled-tasks/overview.md).

To prune old records every five minutes, register your `prune` function
(`func(context.Context) error`) with a scheduler backed by your selected driver:

```go
scheduler := schedule.New(driver)

err := scheduler.Add(
	schedule.NewEntry("prune", prune, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Every(5 * time.Minute))
	}),
)
```

Choose a different specification in the entry callback when the timing changes:

- `schedule.UTC().Hour(3)` runs daily at 03:00 UTC.
- `schedule.In(amsterdam).Weekdays().Hour(9)` runs at 09:00 on weekdays in a
  location loaded with `time.LoadLocation("Europe/Amsterdam")`.
- `schedule.UTC().Cron("0 */6 * * *")` runs every six hours.

Set `entry.Timeout(time.Hour)` to bound a run, or `entry.WithoutOverlapping()`
to prevent a new run while the previous one is still active.

Overlap coordination belongs to the driver. A manual `RunNow` bypasses it;
use application-level locking when manual and scheduled runs must not overlap.

To send a reminder later, declare a typed task and register your `sendReminder`
handler (`func(context.Context, Reminder) error`). `Reminder` is your payload:

```go
var Remind = schedule.NewTask[Reminder]("remind")
err := scheduler.Handle(Remind, sendReminder)
id, err := scheduler.After(ctx, Remind, Reminder{UserID: 42}, 10*time.Minute)
err = scheduler.Cancel(ctx, id)
```

`After` returns a job ID; `Cancel` asks the driver to remove it. Unknown or
finished IDs also succeed, so success does not prove that a pending job was
removed. Hatchet cannot stop a job that has already started. After registering
entries and handlers, register the scheduler inside your application composer:

```go
compose.Run(scheduler.Run)
```

Here `compose` is the `*application.Composer` passed to your bootstrap function.
The runner launches after composition and startup both succeed.

`NewEntry(name, fn, configure...)` publishes an immutable entry. Callbacks run
directly once, synchronously in order on one fresh `*schedule.EntryBuilder`;
its settings return that same draft. Retaining it cannot change the published
entry. `When` is required, and a nil execution function, invalid name or spec,
or negative timeout remains a normal error from `Err` and `Add`. Callbacks
still run for invalid declarations; their panics, including supplied nil
callbacks, propagate. Expanded nil or empty slices supply zero callbacks.
`Spec` remains immutable and fluent. Overlap is allowed and timeout is unlimited
by default; `Timeout(0)` explicitly removes a limit.

Timeouts are cooperative, not forced termination. In particular, memory driver
shutdown stops timers but keeps admitted execution contexts detached from runner
cancellation while draining. A handler waiting only for that detached context
with no positive timeout can prevent the scheduler's `Run` from returning.
Bound task I/O and set entry timeouts when work must finish within a known time.
The application's shutdown budget bounds its runner wait, not the lifetime of
that work: cleanup can proceed while a scheduler execution remains active.

Manual `RunNow` retains the caller's cancellation and entry timeout. Its MCP
tool reports `not_found` only when entry lookup fails; a registered entry's
nested `ErrUnknownName` remains an execution error for adapter redaction.

- Start every `Spec` with `UTC()`. Use `In(location)` only when the wall-clock
  time matters to people.
- In a zone with daylight saving time, a time inside the spring gap is skipped
  and a time inside the autumn overlap runs once with memory or Restate.
  Temporal runs the repeated time twice; Hatchet supports UTC by default. `Add` logs a warning at
  startup for entries affected during the next year.
- Entries and task handlers are closures registered by name in every process
  that runs the scheduler. Entries and tasks share one name namespace.
- Add `import _ "time/tzdata"` on minimal images without zone files.
- Return `schedule.ErrSkipped` from a function to log a skipped run instead of
  a failure.

The package uses only the standard library. Drivers implement `schedule.Driver`.
