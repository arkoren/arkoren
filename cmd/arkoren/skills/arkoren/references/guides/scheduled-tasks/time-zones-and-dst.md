---
title: Time Zones and DST
nav: Time Zones & DST
description: Choose the time zone of a schedule, what happens to a wall-clock time skipped or repeated when clocks change, and which drivers differ.
order: 20
---

# Time Zones and DST

> [!NOTE]
> Hatchet references below are retained development context for a pending,
> unpublished integration, not available driver support. See the
> [scheduler comparison](drivers.md#compatibility-matrix) for available drivers.

## Introduction

A daily digest may need to arrive at the same local time even when clocks
change. A maintenance job usually needs only a predictable schedule. Choose a
time zone for each spec to distinguish those requirements: `schedule.UTC()`
uses UTC, while `schedule.In(location)` uses a zone you load.

In a zone with daylight saving time (DST), some local times disappear or occur
twice during clock changes. This page explains how Arkoren handles those times,
how it warns you, and where the drivers differ.

## Choosing a zone

Start every spec from UTC unless the wall-clock time matters to people. UTC has
no DST, so 03:00 names one calendar instant each day. Delivery still depends on
driver availability and missed-run policies. A job that prunes records or builds a nightly export has no
reason to care what the clock on a wall in Amsterdam says.

Use `schedule.In` when the schedule follows a person's local clock, such as a
digest scheduled for 09:00 where they live. The scheduled time is not an
arrival-time guarantee. Load the location with the standard library and handle
any loading error before constructing the entry:

```go
import (
	"time"

	"arkoren.dev/core/service/schedule"
)

amsterdam, err := time.LoadLocation("Europe/Amsterdam")

digest := schedule.NewEntry("digest", sendDigest, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.In(amsterdam).Hour(9))
})
```

`In(nil)` is an error kept on the spec and reported by `Add`. The `Local` zone is rejected by the Temporal and
Restate drivers with `schedule.ErrUnsupported`, because a process's local zone
differs between machines; load a named zone instead.

Zone data comes from the operating system. A minimal image, such as a scratch
or distroless container without zone files, makes `time.LoadLocation` fail.
Embed the data in the binary with a blank import in your program:

```go
import _ "time/tzdata"
```

## What happens when clocks change

The core calendar, used by memory and Restate, follows the rules below.
[Temporal and Hatchet use their own calendars](#how-the-drivers-differ), so
check your driver before relying on these rules.

In spring, clocks jump forward and a range of wall-clock times does not exist.
In Europe/Amsterdam on 29 March 2026, the clock goes from 02:00 straight to
03:00. An entry at 02:30 has no 02:30 to run at, and Arkoren **skips it for that
day**. In autumn, clocks go back and a range happens twice. On 25 October 2026
the hour from 02:00 to 03:00 occurs twice, and an entry at 02:30 runs **once, at
its first occurrence**. Entries at times outside those hours are unaffected,
which is why most schedules never notice.

To inspect the core calendar, use `Next`, which returns the next matching time
strictly after a given moment, in the spec's zone:

```go
entry := schedule.NewEntry("nightly", nightly, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.In(amsterdam).Hour(2).Minute(30))
})

before := time.Date(2026, 3, 28, 12, 0, 0, 0, amsterdam)
fmt.Println(entry.Next(before)) // 2026-03-30 02:30:00 +0200 CEST
```

The skipped day is the 29th, so the next match is on the 30th. `Next` returns
the zero time when the spec is invalid or nothing matches within five years.
It calculates a calendar match, not a delivery prediction for an engine.

## The startup warning

You do not have to remember these dates. When you add an entry in a non-UTC
zone, `Scheduler.Add` checks the next year for a transition that lands on one
of its times, and logs a warning once for that entry if it finds one. The
record carries the entry name, the zone, the date, the wall-clock time, and the
effect, which is `skipped` or `run once`. The warning never fails `Add`: the
entry is registered as written. The warning describes the core calendar;
Temporal's autumn behavior still differs. If it shows up for an entry that must
run every day, move it to UTC or to a time outside the transition hours.

## How the drivers differ

The rules above are the ones the scheduler's own calendar follows, and the
memory and Restate drivers use it, so they behave exactly as described.
Temporal computes its own calendar. The comparison lists available drivers:

| Driver | Spring gap | Autumn overlap | Non-UTC entries |
| --- | --- | --- | --- |
| Memory | skipped | once | supported |
| Temporal | skipped | runs twice | supported |
| Restate | skipped | once | supported |

Temporal runs an entry inside the autumn overlap twice, once per occurrence, so
an entry in a DST zone on Temporal must tolerate that or sit outside the
overlap. In the pending integration, Hatchet evaluates cron expressions in UTC.
When an entry carries another zone, the Hatchet driver's `Run` returns `schedule.ErrUnsupported`;
the `Builder.AllowCronTZ(true)` construction setting instead prefixes the expression with
`CRON_TZ=<zone>`, which depends on how your Hatchet engine reads it, so test
it against your engine before relying on it. See [Scheduler Drivers](drivers.md)
for the rest of each driver.

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
