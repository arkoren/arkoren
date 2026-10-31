# Restate scheduling

`arkoren.dev/driver/restate/restateschedule` keeps recurring ticks and deferred
tasks in Restate. Install it with `go get arkoren.dev/driver/restate` and configure
an authenticated [shared host](../restateworkflow/README.md) before binding the
scheduler. The server must reach the endpoint and know its deployment.

Given that host, create the driver before `host.Start`:

```go
import (
	"arkoren.dev/core/service/schedule"
	"arkoren.dev/driver/restate/restateschedule"
)

driver, err := restateschedule.New(host, func(scheduler *restateschedule.Builder) {
	scheduler.Prefix("billing")
})
scheduler := schedule.New(driver)
```

Register entries and task handlers on `scheduler`, then register `scheduler.Run`
alongside `host.Serve` in your application. The host owns endpoint cleanup;
scheduler Run cancellation, including cancellation during publication, is clean.
Ingress publication failures retry until canceled. The host's readiness probe
checks the admin API, not deployment registration or scheduler dispatch readiness.

Deferred payloads retain valid JSON values, including explicit `null`. Recurring
entry dispatch receives nil payload bytes, including messages from older workers.
A job accepted before dispatch is installed retries when it executes; application
failures are terminal, while missing dispatch and unknown names remain retryable.
At-least-once delivery still requires idempotent handlers.

## Removing and reintroducing entries

The index removes entries absent from the next declaration set. Removal clears
the active specification but retains its generation number. Reintroducing the
same name advances that generation, so old queued ticks cannot revive it. Service
names, state keys, tick payloads, and idempotency-key formats do not change.

The Remove handler now journals individual clears instead of ClearAll. Keep the
old deployment available until its in-flight Remove invocations finish. Do not
force a replacement onto incompatible recorded commands. If an older deployment
already removed an entry, its generation history is gone: drain its pending ticks
before reintroducing that name. The new tombstone cannot reconstruct deleted
history. Keep tombstones when managing Restate state manually too.

Upgrade serving workers before relying on preservation of null task payloads;
older workers discard those payload bytes. Prefixes are normalized for service
names but remain exact object keys; transport escaping does not change persisted
identities. Choose distinct normalized prefixes on a shared server.

For operation details and native timing limits, see
[the package API](https://pkg.go.dev/arkoren.dev/driver/restate/restateschedule).
