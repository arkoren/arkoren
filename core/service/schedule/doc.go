// Package schedule runs functions on a calendar and defers one-off typed
// tasks. The [Scheduler] owns what runs; a [Driver] owns when it runs.
//
//	scheduler := schedule.New(driver)
//	err := scheduler.Add(
//		schedule.NewEntry("prune", prune, func(entry *schedule.EntryBuilder) {
//			entry.When(schedule.UTC().Every(5 * time.Minute))
//		}),
//		schedule.NewEntry("report", report, func(entry *schedule.EntryBuilder) {
//			entry.When(schedule.UTC().Hour(3))
//			entry.Timeout(time.Hour)
//		}),
//	)
//	if err != nil {
//		return err
//	}
//
//	var Remind = schedule.NewTask[Reminder]("remind")
//	if err := scheduler.Handle(Remind, remind); err != nil {
//		return err
//	}
//	id, err := scheduler.After(ctx, Remind, Reminder{
//		UserID: 42,
//	}, 10*time.Minute)
//	if err != nil {
//		return err
//	}
//	fmt.Println(id)
//
// Register the scheduler during application composition:
//
//	compose.Run(scheduler.Run)
//
// Entries are immutable snapshots. [NewEntry] invokes its construction
// callbacks directly once, in order on one fresh [EntryBuilder], then checks
// the completed declaration. Supply [EntryBuilder.When]; there is no default
// schedule. Names, nil execution functions, invalid specs, and negative timeouts
// are ordinary errors reported by [Entry.Err] and [Scheduler.Add]. The first
// error is kept. Supplied nil construction callbacks and callback panics
// propagate; expanded nil or empty callback slices supply no edits. Retained
// drafts cannot change published entries. [Spec] keeps its immutable fluent API.
// [Scheduler.Entries] returns an eager collection snapshot in registration
// order. Filtering it does not change registrations or observe later Add calls;
// immutable entries retain shared execution functions and their captures.
//
// # Time zones
//
// Start every [Spec] with [UTC], which has no daylight saving time and runs
// the same way everywhere. Use [In] only when the wall-clock time matters to
// people, such as a digest at 09:00 in Amsterdam.
//
// # Daylight saving time
//
// In a zone with daylight saving time, a wall-clock time that does not exist
// because clocks jump forward is skipped for that day. A wall-clock time that
// occurs twice because clocks go back runs once, at its first occurrence.
// [Scheduler.Add] logs a warning at startup when an entry in a non-UTC zone
// has a time inside such a transition during the next year. Prefer UTC for
// entries that must run every day.
//
// Zone data comes from the operating system. On minimal images without zone
// files, add import _ "time/tzdata" to the program so [time.LoadLocation]
// works.
//
// # Registration
//
// Entries and task handlers are closures registered by name in every process
// that runs the scheduler, so each process must register the same names
// before [Scheduler.Run]. Entries and tasks share one namespace. Only the
// name and a JSON payload travel through the driver.
//
// # Telemetry
//
// With [Builder.Telemetry], or [telemetry.Default] when it is not set, every run
// emits a root span "schedule run", labeled with the entry or task name,
// whose outcome is ok, skipped or failed, plus the arkoren.schedule.run.count
// and arkoren.schedule.run.duration metrics. Spans that cache, database and
// storage calls start from the entry's context become its children. Task
// payloads, error text and job identifiers are never recorded. Disabled
// telemetry costs nothing.
//
//	sched := schedule.New(driver, func(scheduler *schedule.Builder) {
//		scheduler.Telemetry(app.Telemetry())
//	})
//
// Scheduler construction uses Builder callbacks, separate from EntryBuilder
// definitions. Callbacks run directly once in order on one fresh draft; nil
// entries panic and nil/empty slice expansion omits edits. Publication detaches
// retained roots while keeping logger, telemetry and event services shared.
//
// # Events
//
// With [Builder.Events], every failed run publishes [RunFailed]
// ("schedule.run.failed") with the name, duration, and error, and every run
// that returns [ErrSkipped] publishes [RunSkipped] ("schedule.run.skipped")
// with the name and duration, for every driver. Successful runs publish
// nothing. [Scheduler.At] and [Scheduler.After] publish [TaskDeferred]
// ("schedule.task.deferred") with the task name, job identifier, and run
// time, never the payload. [Scheduler.Cancel] publishes [TaskCanceled]
// ("schedule.task.canceled") with the job identifier whenever it succeeds,
// including for an unknown or finished identifier, because the driver cannot
// tell those apart.
//
//	sched := schedule.New(driver, func(scheduler *schedule.Builder) {
//		scheduler.Events(app.Events())
//	})
package schedule
