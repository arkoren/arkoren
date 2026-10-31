# Memory Schedule

`arkoren.dev/core/service/schedule/memoryschedule` is an in-process implementation of the Arkoren
`schedule.Driver` contract, built on `time.AfterFunc`. It runs recurring
entries and deferred jobs. It is single-process only: every replica runs every
entry. Deferred timers are discarded when `Run` stops, and all work is lost on
process exit. Submissions accepted while stopped or draining wait for a later
raw-driver `Run`.

It is included in `go get arkoren.dev/core`. Import
`"arkoren.dev/core/service/schedule/memoryschedule"`, pass
`memoryschedule.New()` to `schedule.New`, and register entries before running
the scheduler. See the [memory scheduler setup](../../../../cmd/arkoren/skills/arkoren/references/guides/scheduled-tasks/drivers.md#memory).

To test code that defers jobs, use `scheduletest.Fake`. To test timing, run the
driver inside `testing/synctest`.

## Cancellation and ownership

`Defer` copies accepted payload bytes, including jobs submitted before `Run`.
An already-canceled submission returns the context error and no ID without
scheduling. An already-canceled `Cancel` returns the context error without
removing work. Cancellation after acceptance does not revoke a job; request
removal with a live-context `Cancel` instead. Cancel cannot interrupt a dispatch
that has already started.

Canceling `Run` stops timer admission and discards queued timers, then waits for
admitted executions. Those execution contexts keep values but not the runner's
deadline or cancellation. The driver has no drain timeout: a handler waiting
only for its detached context can keep `Run` blocked indefinitely. Entry timeout
defaults to unlimited, so set a positive cooperative timeout and bound I/O when
work needs an execution deadline. Deferred task handlers own their own I/O bounds.

The application defaults to a 30-second runner-wait budget before continuing with
cleanup. That budget does not kill executions, and is not a hard total shutdown
deadline: cleanup and telemetry receive separate minimum budgets. Do not assume
application cleanup means scheduler work has finished.

Another `Run` is rejected with the existing `ErrAlreadyRunning` identity until
the first has finished draining. Once it returns, the raw driver can run again;
a `schedule.Scheduler` facade remains single-use. New deferred submissions during
draining remain pending for a later raw-driver Run.
