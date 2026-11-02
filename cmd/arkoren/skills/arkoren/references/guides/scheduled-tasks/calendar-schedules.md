---
title: Calendar Schedules
description: Define recurring entries with calendar fields or cron and choose overlap and cooperative timeout policies.
order: 30
summary:
  - Define immutable entries using UTC calendar fields, aligned cadences, or cron.
  - Choose overlap and cooperative timeout policies without assuming exclusive effects.
  - Register complete batches before the scheduler starts and inspect future matches.
---

# Calendar Schedules

A stock refresh that runs every weekday belongs on a calendar, rather than
being deferred again by each request. A calendar **entry** combines a stable
name, your execution function, and an immutable `schedule.Spec` describing
matching times. It does not start a timer until the scheduler runs.

Use an existing [configured scheduler](drivers.md) for the examples below.
For work triggered once by a user action, use [Deferred Tasks](deferred-tasks.md).

## Defining Entries

With your application's `refreshStock` function of type
`func(context.Context) error`, create a weekday refresh at 06:15 UTC. Import
`time` and `arkoren.dev/core/service/schedule`:

```go
refresh := schedule.NewEntry("refresh-stock", refreshStock, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Weekdays().Hour(6).Minute(15))
})
```

The callback edits a fresh draft; publication copies its settings and retains
the execution function without calling it. Configure entries only during
construction. Retaining the draft cannot change the published definition, but
the function's captured services remain shared and must outlive executions.

`When` is required. An omitted schedule, nil execution function, or invalid spec
is a declaration error reported by `Err` and `scheduler.Add`, not an inferred
default. Names contain 1–64 lowercase letters, digits, or hyphens, with no edge
hyphen. They share a namespace with deferred tasks.

## Calendar Fields

Start with `schedule.UTC()` unless the job must follow a person's local clock;
[Time Zones](time-zones-and-dst.md) covers named zones and daylight saving time.
Unset time fields finer than the coarsest selected field become zero, while
coarser fields match every value. For example:

```go
daily := schedule.UTC().Hour(6)
hourly := schedule.UTC().Minute(15)
monthly := schedule.UTC().Day(1)
```

These mean 06:00 daily, minute :15 of every hour, and midnight on the first day
of each month. Date-only specs use midnight; a zone with no fields is invalid.
`Day(31)` skips months without that date rather than rolling into the next month.

If refreshes are needed on selected weekdays, pass several values to `On`:

```go
twice := schedule.UTC().On(time.Monday, time.Thursday).Hour(6)
```

This matches 06:00 on Monday and Thursday. `Day` cannot be combined with `On`,
`Weekdays`, or `Weekends`. Field methods return independent specs, sort and
deduplicate values, and keep the first error. Setting the same field twice is
an error rather than an override.

## Aligned Cadences

For repeated maintenance within each hour, choose an aligned calendar cadence:

```go
cadence := schedule.UTC().Every(15 * time.Minute)
```

This matches :00, :15, :30, and :45, not fifteen minutes after startup or the
previous completion. `Every` accepts positive whole seconds, minutes, or hours
under 24 hours. The step must divide its unit's cycle evenly: 15 minutes works,
7 minutes does not. Finer fields become zero; coarser fields remain unrestricted
unless you select them, such as with `Weekdays()`.

Calendar matches are intended run times, not guaranteed delivery times. Memory
does not recover missed ticks; engine recovery differs. Check
[the driver comparison](drivers.md#compatibility-matrix) before using ticks as
a record of business work. Persist business progress separately if every period
must eventually be processed.

## Cron Expressions

If an existing operational schedule is already expressed as cron, use `Cron`
instead of the field methods:

```go
overnight := schedule.UTC().Cron("0 2 * * 1-5")
```

This matches 02:00 on weekdays. Expressions have five fields, or six with
seconds first. Fields accept numeric values, lists, ranges, wildcards, and
steps; weekday numbers are 0–6, starting with Sunday. Named months, weekday
names, and cron macros are not part of this parser.

`Cron` cannot be combined with field methods, including `Every`. If both
day-of-month and day-of-week fields are restricted in raw cron, either can
match; that differs from combining restrictions as an AND condition.

## Overlap Policy

By default, another scheduled run can start while the previous one remains
active. If overlapping refreshes would compete for the same resource, construct
the entry with `WithoutOverlapping` and a positive timeout:

```go
refresh := schedule.NewEntry("refresh-stock", refreshStock, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Every(5 * time.Minute))
	entry.WithoutOverlapping()
	entry.Timeout(4 * time.Minute)
})
```

This is an alternative to the earlier definition. Memory skips a tick while
that entry is busy in the same process; it does not coordinate other replicas.
Temporal uses its native skip policy. Restate executes inline within its
serialized tick invocation, so a pending tick waits rather than overlapping
that invocation. These are driver policies, not a cross-replica lock around
all application effects.

`RunNow` bypasses those guards. A retried or duplicated delivery can repeat
effects even without concurrent runs. Make the function idempotent, and use
application-level locking or fencing when correctness requires it.

`Timeout` defaults to zero, meaning no facade execution limit; negative values
are invalid. A positive value cancels the supplied context after that duration.
The function must observe the context or pass it to I/O: no scheduler timeout
forcibly stops code. Engine drivers also have native execution policies on
[Drivers](drivers.md). A function returning nil after cancellation still counts
as successful; see [Error Handling](error-handling.md#timeout-boundaries).

## Registering Entries

After constructing the definitions, register them before `Run`:

```go
err := scheduler.Add(refresh)
```

`Add` accepts multiple entries atomically. An invalid definition or duplicate
name rejects the whole batch, leaving existing registrations unchanged. Starting
`Run` closes registration permanently for that facade.

Construction callbacks run synchronously once in order, and later callbacks can
replace valid timing or timeout settings. The first invalid setting cannot be
repaired by a later callback. A supplied nil callback or callback panic propagates;
runtime handler panic recovery does not apply during construction.

To inspect a future match without starting work, use `Next` with an existing
time named `after`:

```go
next := refresh.Next(after)
```

The result is strictly after `after`, or zero if the entry is invalid or no
match exists within five years. This uses the core calendar, not an engine
delivery prediction. Start work with [the scheduler runner](overview.md#running-schedulers).

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
