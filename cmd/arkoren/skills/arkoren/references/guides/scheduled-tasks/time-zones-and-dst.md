---
title: Time Zones
description: Choose UTC or named local time and account for skipped and repeated wall-clock times during daylight saving changes.
order: 50
summary:
  - Choose UTC for machine schedules or a named location for local-clock work.
  - Predict core calendar behavior across daylight saving gaps and overlaps.
  - Interpret registration warnings and Temporal's different autumn behavior.
---

# Time Zones

A daily account digest should follow its recipients' local clock, while
background cleanup usually needs only a predictable machine schedule. Choose
the zone as part of each calendar spec: UTC avoids seasonal clock changes;
a named location follows that region's time rules.

This page assumes you can [define a calendar entry](calendar-schedules.md).
Time zones describe matching calendar instants, not guaranteed execution or
arrival times.

## Choosing Zones

Use `schedule.UTC()` unless the wall-clock time matters to people. For example,
`schedule.UTC().Hour(6)` matches 06:00 UTC each day without a daylight saving
transition. Driver outages and missed-tick policies still affect delivery.

For a local digest at 09:00, use `schedule.In(location)`. Avoid `time.Local`
for shared scheduling: its meaning depends on the machine, and Temporal and
Restate reject the `Local` zone with `schedule.ErrUnsupported`. Load an explicit
named zone instead.

## Loading Locations

Load the desired location using the standard library before constructing a spec.
Import `time` and `arkoren.dev/core/service/schedule`:

```go
location, err := time.LoadLocation("Europe/Amsterdam")
```

Handle the load error before using `location`. `schedule.In(nil)` retains an
invalid-spec error rather than selecting a default zone.

With your application's `sendDigest` function of type
`func(context.Context) error`, construct the entry:

```go
digest := schedule.NewEntry("account-digest", sendDigest, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.In(location).Hour(9))
})
```

The match is 09:00 Amsterdam time, with minutes and seconds defaulting to zero.
Register it with `scheduler.Add(digest)` before starting the scheduler.

Zone loading needs available timezone data. If a minimal container lacks zone
files, embed the standard library's database with a blank import in your program:

```go
import _ "time/tzdata"
```

Deploy timezone data consistently across workers and engines when local-clock
behavior matters; `Next` cannot predict a remote engine using different rules.

## Clock Changes

Daylight saving time creates a spring **gap**, when a range of local times does
not exist, and an autumn **overlap**, when a range occurs twice. The core
calendar used by memory and Restate skips nonexistent times and chooses only
the first occurrence of repeated times.

In Amsterdam on March 29, 2026, the clock jumps from 02:00 to 03:00. A 02:30
entry has no matching instant that day and is skipped, not moved to 03:30.
On October 25, 2026, the hour beginning at 02:00 repeats. The core calendar
matches 02:30 only on its first occurrence.

If a business process must run for every date, do not depend on a tick inside
those transition hours. Choose UTC or a local time outside the transition,
and persist business progress so missed delivery can be reconciled separately.

## Inspecting Matches

Before choosing a local time, inspect the core calendar without running any
handler. Reuse the loaded `location` and import `fmt` for this inspection:

```go
spec := schedule.In(location).Hour(2).Minute(30)
before := time.Date(2026, time.March, 28, 12, 0, 0, 0, location)
fmt.Println(spec.Next(before))
```

The result skips March 29:

```text
2026-03-30 02:30:00 +0200 CEST
```

`Spec.Next` and `Entry.Next` return the first matching instant strictly after
the supplied time. Invalid specs and searches with no match within five years
return zero time. These methods calculate core calendar matches, not remote
engine delivery times.

## Registration Warnings

`scheduler.Add` examines the next year for a transition intersecting a non-UTC
entry's matching times. If it finds one, it logs one warning for that entry
with its name, zone, date, wall time, and `skipped` or `run once` effect.

The warning does not reject registration or change the spec. It describes the
core calendar, including when Temporal's own overlap behavior differs. If it
identifies a risky business time, choose another time or zone; do not treat
successful registration as proof that every date will have an execution.

## Driver Calendars

Memory and Restate use the core calendar. Temporal uses its engine calendar:

| Driver | Spring Gap | Autumn Overlap |
| --- | --- | --- |
| Memory | Skips the nonexistent time. | Matches the first occurrence only. |
| Temporal | Skips the nonexistent time. | Matches both occurrences. |
| Restate | Skips the nonexistent time. | Matches the first occurrence only. |

A Temporal entry in a repeated hour must tolerate two calendar firings, in
addition to ordinary duplicate-delivery risks. Move it outside the overlap if
two intended firings are not appropriate. See [Drivers](drivers.md) for
missed-tick recovery, overlap policy, and engine configuration. Hatchet remains
pending and is not an available alternative.

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
