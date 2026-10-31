package schedule

import (
	"context"
	"fmt"
	"time"

	"arkoren.dev/core/internal/name"
)

// Entry is an immutable named function that runs on a [Spec]. Construct it
// with [NewEntry]; configuration belongs to [EntryBuilder], not the published
// value. Its first declaration error is reported by [Entry.Err].
//
// Example:
//
//	entry := NewEntry("report", func(context.Context) error {
//		return nil
//	}, func(entry *EntryBuilder) {
//		entry.When(UTC().Hour(3))
//	})
//	if err := entry.Err(); err != nil {
//		panic(err)
//	}
type Entry struct {
	// name identifies the entry.
	name string

	// fn is the function to run.
	fn func(context.Context) error

	// spec says when the entry runs.
	spec Spec

	// withoutOverlapping asks the driver not to start a run while the previous
	// run is still going.
	withoutOverlapping bool

	// timeout bounds one run. Zero means no limit.
	timeout time.Duration

	// description is free text for humans.
	description string

	// err is the first error found while building the entry.
	err error
}

// EntryBuilder configures one entry during [NewEntry]. Settings return the
// same draft and preserve its first error. Retaining a draft does not permit
// changes to an already published entry. It is not safe for concurrent use.
//
// Example:
//
//	entry := NewEntry("report", func(context.Context) error {
//		return nil
//	}, func(entry *EntryBuilder) {
//		entry.When(UTC().Hour(3))
//		entry.WithoutOverlapping()
//	})
//	if err := entry.Err(); err != nil {
//		panic(err)
//	}
type EntryBuilder struct {
	// entry owns the draft settings; publication copies this value. Spec
	// contains only immutable text, an error, and a shared time.Location.
	entry Entry
}

// NewEntry creates an entry called entryName that runs fn. The name has 1-64
// lowercase letters, digits, or hyphens, and does not start or end with a
// hyphen. A bad name is kept on the entry and reported by [Entry.Err] and
// [Scheduler.Add] as [ErrInvalidName]; a nil fn as [ErrInvalidEntry]. Set when
// it runs with [EntryBuilder.When]; omission never infers a schedule.
// Callbacks run directly once, synchronously in order on one fresh draft,
// even for invalid names or nil fn. Supplied nil callbacks and callback panics
// propagate; expanded nil or empty slices supply no callbacks. Final validation
// follows all callbacks. Publication copies settings, not fn or its captures.
//
// Example:
//
//	entry := NewEntry("report", func(context.Context) error {
//		return nil
//	}, func(entry *EntryBuilder) {
//		entry.When(UTC().Hour(3))
//		entry.Timeout(time.Hour)
//	})
//	if err := entry.Err(); err != nil {
//		panic(err)
//	}
func NewEntry(entryName string, fn func(context.Context) error, configure ...EntryBuilderFunc) Entry {
	entry := EntryBuilder{
		entry: Entry{
			name: entryName,
			fn:   fn,
		},
	}

	if err := name.Identifier(entryName); err != nil {
		entry.entry.err = fmt.Errorf("%w: entry name: %w", ErrInvalidName, err)
	} else if fn == nil {
		entry.entry.err = fmt.Errorf("%w: %s: nil function", ErrInvalidEntry, entryName)
	}

	for _, configure := range configure {
		configure(&entry)
	}

	entry.entry.err = entry.entry.Err()

	return entry.entry
}

// When replaces the run schedule and returns the same draft. Invalid specs
// record the first error immediately; a later valid spec cannot clear it.
// Spec is immutable, so later Spec transformations cannot change this draft.
//
// Example:
//
//	entry := NewEntry("report", func(context.Context) error {
//		return nil
//	}, func(entry *EntryBuilder) {
//		entry.When(UTC().Hour(3))
//	})
//	if err := entry.Err(); err != nil {
//		panic(err)
//	}
func (entry *EntryBuilder) When(spec Spec) *EntryBuilder {
	if entry.entry.err != nil {
		return entry
	}

	entry.entry.spec = spec

	if err := spec.Err(); err != nil {
		entry.entry.err = fmt.Errorf("%s: %w", entry.entry.name, err)
	}

	return entry
}

// WithoutOverlapping asks the driver not to start a run while the previous
// run of the entry is still going. Overlap is allowed by default. It returns
// the same draft without changing any earlier error.
//
// Example:
//
//	entry := NewEntry("report", func(context.Context) error {
//		return nil
//	}, func(entry *EntryBuilder) {
//		entry.When(UTC().Hour(3))
//		entry.WithoutOverlapping()
//	})
//	if err := entry.Err(); err != nil {
//		panic(err)
//	}
func (entry *EntryBuilder) WithoutOverlapping() *EntryBuilder {
	entry.entry.withoutOverlapping = true

	return entry
}

// Timeout bounds one run and returns the same draft. Zero means no limit
// (also the default); a negative duration records an error. An earlier error
// prevents this setting from changing the draft.
// Cancellation is cooperative: a function must observe its context to stop.
// Drivers can detach scheduled execution from runner cancellation while
// draining. A positive timeout then remains an independent execution deadline;
// zero does not impose a shutdown drain bound. RunNow retains caller cancellation.
//
// Example:
//
//	entry := NewEntry("report", func(context.Context) error {
//		return nil
//	}, func(entry *EntryBuilder) {
//		entry.When(UTC().Hour(3))
//		entry.Timeout(time.Hour)
//	})
//	if err := entry.Err(); err != nil {
//		panic(err)
//	}
func (entry *EntryBuilder) Timeout(duration time.Duration) *EntryBuilder {
	if entry.entry.err != nil {
		return entry
	}

	if duration < 0 {
		entry.entry.err = fmt.Errorf("%w: %s: negative timeout %s", ErrInvalidEntry, entry.entry.name, duration)

		return entry
	}

	entry.entry.timeout = duration

	return entry
}

// Description replaces the human-readable description and returns the same
// draft. Empty text clears it; this setting does not change any earlier error.
//
// Example:
//
//	entry := NewEntry("report", func(context.Context) error {
//		return nil
//	}, func(entry *EntryBuilder) {
//		entry.When(UTC().Hour(3))
//		entry.Description("Nightly report")
//	})
//	if err := entry.Err(); err != nil {
//		panic(err)
//	}
func (entry *EntryBuilder) Description(text string) *EntryBuilder {
	entry.entry.description = text

	return entry
}

// Name returns the entry name.
// The identity remains unchanged after registration or execution.
//
//	for _, entry := range scheduler.Entries() {
//		fmt.Println(entry.Name())
//	}
func (entry Entry) Name() string {
	return entry.name
}

// Spec returns the spec that says when the entry runs.
// Transformations of the returned immutable value do not edit the entry.
//
//	for _, entry := range scheduler.Entries() {
//		fmt.Println(entry.Spec().Expression())
//	}
func (entry Entry) Spec() Spec {
	return entry.spec
}

// Overlapping reports whether a run may start while the previous run is
// still going. It is true unless [EntryBuilder.WithoutOverlapping] was used.
// Coordination belongs to the driver; manual RunNow execution bypasses it.
//
//	for _, entry := range scheduler.Entries() {
//		fmt.Println(entry.Name(), entry.Overlapping())
//	}
func (entry Entry) Overlapping() bool {
	return !entry.withoutOverlapping
}

// TimeoutValue returns the bound on one run, or zero for no limit.
// It reports the configured cooperative deadline, not a forced termination.
//
//	for _, entry := range scheduler.Entries() {
//		fmt.Println(entry.Name(), entry.TimeoutValue())
//	}
func (entry Entry) TimeoutValue() time.Duration {
	return entry.timeout
}

// DescriptionValue returns the description of the entry.
// Empty text is valid and has no execution semantics.
//
//	for _, entry := range scheduler.Entries() {
//		fmt.Println(entry.DescriptionValue())
//	}
func (entry Entry) DescriptionValue() string {
	return entry.description
}

// Next returns the first run time strictly after the given time, or the zero
// time when the entry is invalid or nothing matches within five years.
//
//	after := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
//	for _, entry := range scheduler.Entries() {
//		fmt.Println(entry.Next(after))
//	}
func (entry Entry) Next(after time.Time) time.Time {
	if entry.Err() != nil {
		return time.Time{}
	}

	return entry.spec.Next(after)
}

// Err returns the first error found while building the entry, or nil. A bad
// name wraps [ErrInvalidName]; a nil function, a bad spec or timeout, and a
// missing spec wrap [ErrInvalidEntry]. An entry without a spec is invalid.
//
//	entry := schedule.NewEntry("report", nil)
//	if err := entry.Err(); err != nil {
//		fmt.Println(errors.Is(err, schedule.ErrInvalidEntry))
//	}
func (entry Entry) Err() error {
	if entry.err != nil {
		return entry.err
	}

	if entry.spec.Location() == nil {
		return fmt.Errorf("%w: %s: spec is required", ErrInvalidEntry, entry.name)
	}

	return entry.spec.Err()
}

// EntryBuilderFunc edits the fresh declaration draft used by [NewEntry].
// Callbacks run synchronously once in order, including invalid declarations;
// supplied nil callbacks panic. [Entry.Err] reports declaration errors.
//
// Example:
//
//	policy := schedule.EntryBuilderFunc(func(entry *schedule.EntryBuilder) {
//		entry.When(schedule.UTC().Hour(3))
//	})
//	entry := schedule.NewEntry("cleanup", cleanup, policy)
//	if err := entry.Err(); err != nil {
//		return err
//	}
type EntryBuilderFunc = func(*EntryBuilder)
