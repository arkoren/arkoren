---
title: Recovering Hatchet Runs
description: Pending, unpublished Hatchet integration notes on execution identity, signal history, and safe worker replacement.
order: 45
---

# Recovering Hatchet Runs

> [!NOTE]
> Hatchet is pending and unpublished. This page preserves development recovery
> and safety context; it does not describe an installable Arkoren module or
> independently verified public behavior. See the available
> [workflow drivers](drivers.md#compatibility-matrix) for new applications.

## Introduction

Recovering a worker must resume the intended physical execution without
repeating another run's effects or consuming its signals. Use these identity
and retention rules with the [Hatchet setup](drivers.md#hatchet). They apply to
worker and client-only processes in the same Hatchet tenant; namespaces route
work but are not tenant authorization boundaries.

## Keeping execution identity stable

Workers and clients must share the effective host namespace, driver prefix,
workflow name, and logical ID. The execution key is
`arkoren.workflow.v2.<namespace>.<prefix>.<name>.<id>`, with each component
separately encoded as unpadded base64url. The namespace is `host.Namespace()`,
including its trailing underscore, or empty. Encoded components contain no
dots, so punctuation and empty components cannot create delimiter collisions.

The SDK adds the effective namespace to workflow names such as
`<prefix>-wf-<name>` and `<prefix>-step-<name>`. Discovery resolves the exact
namespaced workflow to its engine UUID and lists by that UUID and execution
key. Before caching a physical run, the driver verifies its returned workflow
name and stored wire key/ID. Successful trigger responses and duplicate-key
collision responses receive the same ownership checks: a collision alone does
not prove ownership.

Missing or mismatched identity returns `workflow.ErrNotFound`, without caching
or mutating that execution. Verification can fail after a trigger already
started work; reconcile trusted engine data before submitting replacement work.
Listings can lag. The driver keeps a small LRU of verified executions, never
unverified submission receipts. Each logical lookup rediscovers the current
physical execution; the cache only enables consistency retries for recently
verified runs. An unknown ID gets one discovery pass. Retry waits use the
remaining `LookupGrace`, including its final fraction, rather than returning
early when a full backoff would not fit. Native request time is additional;
`LookupGrace` neither guarantees visibility nor bounds the whole operation.

## Retaining signals through recovery

Signals use event `<prefix>.sig.<signal>` in physical scope
`arkoren.workflow.signal.v1.<key>.<execution>`. The logical execution key and
Hatchet workflow-run ID are separately encoded as unpadded base64url; marker
idempotency keys append the similarly encoded signal name. The sender uses a
physical ID verified through Hatchet. The runtime uses
`DurableContext.WorkflowRunId()`, not a task ID or retry counter.

The first signal stores its value in a `<prefix>-signal` marker workflow.
Workflow inputs declare `signal_version: 2`; signal events and marker inputs
also carry `execution_id`. Before publishing, the driver verifies the exact
namespaced marker workflow, physical marker key, version, and owner, including
on marker idempotency collisions. Waiters verify event version/owner and marker
ownership before reading the value. Missing or mismatched ownership fails
closed with `workflow.ErrNotFound`.

Concurrent senders share one first-value marker per physical execution and
signal. Repeated waits and replay after eviction or worker replacement retain
that execution's scope and first value while its required records remain.
Reusing a logical ID after workflow TTL expiry produces a different physical
scope and marker, even when old events remain.

Size two independent retention windows before deploying long-running work:

- Marker idempotency lasts `Lookback` (default 30 days) from the first marker
  trigger. Keep it longer than the interval from the first signal through the
  last possible signal or replay, including queueing, execution, and recovery.
  The run/marker search window and engine event and marker/input retention must
  cover the corresponding history too.
- Logical duplicate-start protection expires `MaxDuration + Retention` after
  trigger; omitted `Retention` uses `Lookback`. Include queueing, execution,
  and the period during which duplicate starts must remain blocked.

Historical matching considers all retained events in the exact physical scope
from a fixed Unix epoch boundary. Signals accepted while queued remain
eligible regardless of first worker admission. This does not recover deleted
data or extend engine retention. Expired history or marker idempotency can lose
delivery or first-value guarantees within one physical execution, but cannot
make a different execution reuse its value.

`ScheduleTimeout` separately bounds admission. `MaxDuration` begins at the
memoized first worker invocation, with `Delay` added, not at submission time.
Hatchet's workflow execution timeout includes the configured margin. Steps use
`Attempts - 1` retries. Registered retry-delay overrides require engine v0.111.0+,
are truncated to milliseconds by the SDK, and remain subject to the native cap;
older engines cannot preserve arbitrary initial delays. The local profile pins
v0.111.0, which supports those overrides. The SDK lifecycle limits remain despite
a passing full native suite; see [the driver limits](drivers.md#hatchet).

Unlimited registered step timeouts return `workflow.ErrUnsupported` from
`NewRunner`. Finite step deadlines are cooperative: after a handler returns, late
success becomes `workflow.ErrTimeout`. The whole-second native timeout adds
reporting grace but cannot kill Go code. A failed clock/UUID memo or unavailable
durable memo transport stops the run rather than using process-local values.

## Replacing an older deployment

> [!WARNING]
> Pause producers and signal senders and drain old runs with the old deployment
> before replacing workers and clients together. Mixed key or wait protocols
> are not rolling-compatible. Reconcile existing effects before resubmitting
> a logical ID: old idempotency records do not block a new-format start.

Derived zero-delay step overrides now carry `retry_without_delay` in the private
step input. Old workers ignore the field and retain their registered positive
backoff. Upgrade all workers before relying on it. Drain workflows containing
affected derived calls on their original deployment before replacement: replay
can otherwise change recorded child-task input. Unaffected calls keep their
earlier input shape; the field adds no durable history node or automatic migration.

The legacy key was `<prefix>:<name>:<id>`, without a namespace. There is no
legacy fallback, even for an empty namespace. New clients cannot find, read,
cancel, or signal those runs through the facade. New workers reject old wire
keys before application code, including during replay. If legacy runs remain,
verify their exact namespaced workflow and physical ID through trusted Hatchet
data and manage them explicitly in Hatchet.

Earlier v2 logical-key deployments also need draining. Inputs without a signal
version used logical-only signals. Version 1 isolated physical executions but
recorded worker-relative historical wait conditions. Current clients and workers
accept only `signal_version: 2`: older v2 keys still permit `Result`, `Status`,
and `Cancel`, but signaling and worker replay are rejected before application
code. Upgrading only senders or only workers is unsupported. The physical scope
encoding remains `arkoren.workflow.signal.v1`; its encoding version is separate
from the logical-key and wire/wait protocol versions.

`Child` and `Spawn` are disabled as fail-closed containment, including during
replay of version 2 workflows. Drain affected workflows using the old deployment
before replacement. Reconcile existing child effects by verified physical ID
before retrying or replacing runs. Containment neither undoes earlier work nor
restores child support. `Step` and external client `Start` remain supported;
native submission is not a safe workaround for portable child joins.

Keep namespace and prefix stable for the lifetime of runs you need to access.
After replacement, verify expected status and results by the same logical tuple,
and test queued signals, replay after eviction, and retention boundaries against
your engine. [Memory tests](testing.md) cannot establish engine replay or
retention behavior. The [`hatchetworkflow` API reference URL](https://pkg.go.dev/arkoren.dev/driver/hatchet/hatchetworkflow)
is pending publication; it does not establish package availability.
