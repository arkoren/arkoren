package schedule

import (
	"errors"
)

// ErrSkipped is returned by a function to report that it chose not to run,
// for example because another instance holds a lock. The scheduler logs the
// run as skipped and does not treat it as a failure.
//
//	if anotherWorkerOwnsRun {
//		return schedule.ErrSkipped
//	}
var ErrSkipped = errors.New("skipped")

// ErrInvalidEntry reports an invalid [Spec], [Entry], or task registration:
// a bad spec or timeout, a missing spec, a nil function, or a zero time.
//
//	err := schedule.UTC().Hour(25).Err()
//	fmt.Println(errors.Is(err, schedule.ErrInvalidEntry)) // true
var ErrInvalidEntry = errors.New("invalid schedule entry")

// ErrInvalidName reports an entry or task name that is not 1-64 lowercase
// letters, digits, or hyphens, or that starts or ends with a hyphen.
//
//	entry := schedule.NewEntry("Bad Name", report)
//	fmt.Println(errors.Is(entry.Err(), schedule.ErrInvalidName)) // true
var ErrInvalidName = errors.New("invalid schedule name")

// ErrDuplicateName reports a second entry or task registered under one name.
// Registration remains unchanged when an atomic Add call finds a duplicate.
//
//	if err := scheduler.Add(entry, entry); errors.Is(err, schedule.ErrDuplicateName) {
//		return err
//	}
var ErrDuplicateName = errors.New("duplicate schedule name")

// ErrUnknownName reports a name that no registered entry or task uses.
// An entry can also return this identity from a nested scheduling operation.
//
//	if err := scheduler.RunNow(ctx, "missing"); errors.Is(err, schedule.ErrUnknownName) {
//		return err
//	}
var ErrUnknownName = errors.New("unknown schedule name")

// ErrRunning reports a registration attempted after [Scheduler.Run] started.
// It also rejects repeated facade Run calls, including after the first returns.
//
//	if err := scheduler.Add(entry); errors.Is(err, schedule.ErrRunning) {
//		return err
//	}
var ErrRunning = errors.New("scheduler is running")

// ErrUnsupported reports an operation the driver cannot perform, such as
// deferred jobs on a driver without durable storage.
// Inspect the selected driver's documented capabilities rather than assuming
// that all Driver implementations support deferred or zoned execution.
//
//	_, err := scheduler.At(ctx, Remind, reminder, due)
//	if errors.Is(err, schedule.ErrUnsupported) {
//		return err
//	}
var ErrUnsupported = errors.New("unsupported by schedule driver")

// errNoZone reports a spec built without a location.
var errNoZone = errors.New("no time zone: use UTC or In")
