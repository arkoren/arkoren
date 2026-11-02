---
title: Troubleshooting
description: Diagnose migration failures and follow targeted recovery procedures, including MySQL ledger repair.
summary:
  - Diagnose acquisition, definition, and history failures before retrying.
  - Reconcile partial or uncertain changes with schema and ledger evidence.
  - Review MySQL binary-ledger compatibility and authorize repairs without losing history.
order: 40
---

# Troubleshooting

When a migration fails, stop the deployment and identify where it stopped:
acquisition, ledger preparation, a user step, bookkeeping, commit, or cleanup.
The safe next action depends on that boundary. Keep the original error and
deployment identity, but do not publish credentials or sensitive native error text.

This page assumes a configured migrator with the same definitions, table, and
lock name as the failed run. [Deployment](deployment.md) explains the transaction
and session boundaries that determine whether work can already be final.

## Acquisition Failures

An error matching `migrate.ErrLockTimeout` means the configured budget expired
while acquiring a session or waiting for the lock, before protected work began.
It also matches `context.DeadlineExceeded`. A caller cancellation or deadline
that ended first is returned without the lock-timeout marker.

Check whether another approved runner is active and whether unfinished cursors,
transactions, sessions, or root named locks occupy the pool. Let the legitimate
holder finish or resolve the resource leak before increasing timeouts. Do not
acquire the migration lock manually before calling the migrator: it is not
reentrant. After correcting acquisition, verify that the next run reaches
protected preparation; a larger timeout does not fix a scope bypass or deadlock.

If PostgreSQL lock behavior is inconsistent behind PgBouncer, check pooling mode.
Use direct or session pooling for session advisory locks. For SQLite, verify that
the database and host profile support cross-process file locks; memory databases
cannot coordinate independent migration processes.

## Definition Failures

`ErrInvalidMigration` can indicate missing Up steps, invalid IDs, a dialect
restriction, invalid configuration, or MySQL ledger incompatibility. Read the
wrapped context rather than treating every instance as a malformed definition.
`ErrDuplicateID` means registration reused an ID. Fix construction before execution;
do not rename a previously applied migration to make registration pass.

For `ErrOutOfOrder`, compare pending and recorded definitions within the affected
group. Restore the intended append-only order and investigate why the earlier
definition is pending. Deliberately allowing a late change requires reviewing its
dependencies and effects on already-applied changes, not merely switching off
the error. The default policy warns and runs late work.

## History Mismatches

`Status` marks recorded IDs absent from registration as `Missing`. Restore the
original definitions and their relative order. Rollback rejects unknown targets
with `ErrUnknownMigration` before executing Down steps; do not delete ledger rows
or guess the ordering from timestamps to force it through.

`ErrIrreversible` means a rollback target has no Down steps. Restore from an
appropriate backup or deploy a reviewed forward correction. Inventing a Down
step after data has been discarded does not make that data recoverable.

With `arkoren.dev/core/service/database/migrate` imported, inspect history after
resolving connectivity and acquisition problems:

```go
statuses, err := migrator.Status(ctx)
```

Handle the error before using `statuses`. Status is protected by the migration
lock and can create the ledger on a fresh database; it is not a read-only probe.
Preparation, metadata, session-loss, or cleanup failure returns nil. On success,
compare IDs, batches, and `Ran` values with the deployed definitions and actual
schema/data, not only the last log line.

## Partial Changes

On MySQL or with `WithoutTransaction`, a failed step or ledger insert can leave
changes applied but unrecorded. `Up` sees that ID as pending and will run it again.
A later failure also leaves earlier successfully committed migrations in place.
Do not treat the whole batch as rolled back.

Before another execution:

1. Quiesce competing migration runners and relevant application writers.
2. Preserve a backup and capture the error, registered order, history, and actual
   objects/data touched by the failed step.
3. Determine which statements completed and whether the final commit or session
   outcome is known. On connection loss, consult server evidence and reconcile
   state rather than assuming the client error proves rollback.
4. Choose an owner-approved recovery: safely complete the pending idempotent
   change, restore an appropriate backup, or prepare a reviewed corrective path.
   Do not casually edit ledger rows to claim work ran.
5. Verify schema, data invariants, and ledger together before allowing deployment
   or replay. Ensure old and new application versions remain compatible.

An error after confirmed commit can leave both schema and ledger final.
`database.ErrAfterCommit` reports that boundary when produced by transaction
cleanup or hooks, but a step can itself return an error carrying the same marker.
Classify the origin and inspect state; a marker in a step's error is not proof
that its enclosing migration committed. The migrator does not reconnect or
replay after losing its retained session.

## MySQL Ledger

MySQL operations verify actual ID-column and index metadata under the retained
session and migration lock before user steps or history-row mutations. New ledgers
use `id VARBINARY(191) NOT NULL` with a full, single-column primary key on `id`.
ID matching is byte-exact, consistent with Go, rather than collation-based.

An existing character column is unsupported even with a `_bin` or NO PAD
collation. Fixed BINARY, nullable or generated ID columns, composite/prefix/
expression primary keys, and additional unique indexes are also rejected.
Nonunique secondary indexes are permitted. The library never automatically
ALTERs an existing ledger. Preparation can already issue `CREATE TABLE IF NOT EXISTS`
before rejecting a layout; rejection does not mean no SQL ran.

Column and index inspection failures preserve their cause. Missing or hidden
metadata is an inability to establish compatibility, not proof that storage
needs conversion. First verify the selected schema, metadata visibility, and
permissions. `SHOW FULL COLUMNS` and `SHOW INDEX` inspect the addressed target;
an unqualified configured table is resolved to the retained session's current
database before steps. Later `USE` statements cannot redirect bookkeeping.
ID insert/delete bindings use the original UTF-8 bytes.

### Authorized Repair

> [!WARNING]
> Changing a legacy ledger can corrupt migration identity or conceal applied
> work. Do not run a generic ALTER or delete history to bypass validation.
> Require explicit owner authorization and a reviewed, database-specific plan
> that preserves ID bytes and every history tuple.

Before an approved conversion, quiesce all migrators, ledger writers, and
conflicting administrator DDL. The migration lock coordinates cooperating
runners; it does not exclude arbitrary schema administration. Back up the full
schema and every `(HEX(id), batch, ran_at)` tuple.

Prove that stored ID bytes match the intended UTF-8 application identities,
fit within 191 bytes without truncation, and remain uniquely represented. Use
`OCTET_LENGTH`, not a character count, to check that byte limit.
Non-UTF-8 legacy storage can require an owner decision: physical bytes and
application identity cannot both be preserved by blindly changing a charset.
Never normalize, trim, rename, or discard IDs as an incidental conversion step.

Inspect constraints, triggers, generated values, indexes, and column attributes
before selecting the repair. Use strict SQL mode during the approved change.
There is no universally safe ALTER recipe; attributes and dependencies can
change what must be preserved or restated. The library performs no conversion,
and the ordinary migration role does not need new ALTER privileges for this check.

After repair, compare the row count and every saved history tuple byte-for-byte,
including full timestamp precision. Inspect canonical
column and primary-key metadata, and confirm there are no extra unique indexes.
Run `Status` with the original definitions and target to verify the protected
inspection succeeds. Reconcile any earlier failed nontransactional user steps
before authorizing `Up`: repairing the ledger does not undo or complete them.
Reconcile deployment records too: conversion cannot recover history already
lost or conflated under the old equality rules. Stop on conversion warnings or
changed tuples rather than accepting coercion or reconstructing timestamps.

For error identities and ledger behavior, see the
[`migrate` API reference](https://pkg.go.dev/arkoren.dev/core/service/database/migrate).
