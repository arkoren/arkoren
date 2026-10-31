# Contracts, Caveats, and Safety

## Contents

- [Document the complete contract](#document-the-complete-contract)
- [Defaults and conventions](#defaults-and-conventions)
- [Failure paths](#failure-paths)
- [Notes and warnings](#notes-and-warnings)
- [Security and production guidance](#security-and-production-guidance)
- [Troubleshooting](#troubleshooting)
- [Precision checklist](#precision-checklist)

## Document the Complete Contract

Document the behavior a developer must account for, not only the happy-path syntax. For each operation, determine and state:

- required inputs, accepted formats, and validation rules;
- return value, output, state change, or generated resource;
- default values and their scope;
- automatic behavior performed by the product;
- error, status, exception, or fallback behavior;
- ordering, timing, transaction, concurrency, lifecycle, and compatibility constraints;
- file, endpoint, command, environment, or runtime boundary.

Explain the ordinary result immediately after the relevant example. Introduce
less-common failure or ownership rules in focused paragraphs where they become
relevant; a guide's first example does not need the entire reference contract
packed into its next paragraph. Keep essential safety conditions beside the
action they qualify:

```markdown
The method returns a parsed response for successful requests. For responses with a
non-success status, it throws `RequestError` and includes the status and body.
```

State whether an API mutates its receiver, returns a copy, queues work, starts work immediately, persists state, or merely prepares an object. These distinctions are often the reason a reader consulted the page.

## Defaults and Conventions

Expose conventions before customizations. Tell readers:

- the default name, location, backend, encoding, timeout, or retry count;
- when an inferred value is derived from a class, file, environment, or identifier;
- what happens when a required resource does not exist;
- whether a command creates missing directories or tables;
- whether a generated resource is present in a fresh project or only after a generator runs.

Use precise scopes:

```markdown
By default, the worker reads the `default` queue on the default connection.
```

is better than:

```markdown
By default, the worker reads the default queue.
```

For a configuration setting, name the configuration location, runtime effect, and deployment implication:

```markdown
Set `cache.enabled` in `config/cache.ts`. When configuration caching is enabled,
changes to environment variables do not take effect until the cache is rebuilt.
```

## Failure Paths

Make conditional behavior explicit. Readers must not have to infer whether a failure throws, returns a value, redirects, retries, leaves partial state, or is ignored.

```markdown
If the upload is larger than the configured limit, the server returns `413` before
storing the file. Otherwise, it stores the file and returns its identifier.
```

For retries, document all of these when applicable:

- what error classes or status codes are retryable;
- initial attempt versus additional attempts;
- delay, backoff, and timeout interaction;
- final error or fallback after retries end;
- idempotency requirement or duplicate-work risk.

For asynchronous work, document dispatch, execution, failure storage, timeout, cancellation, and worker lifecycle separately. “Queued” does not tell the reader when or whether work runs.

## Notes and Warnings

Use notes to preserve flow while surfacing a useful fact. Use warnings to prevent meaningful harm.

Shared guides and API pages use GitHub alerts:

> [!NOTE]
> Explain an automatic default, an adjacent capability, a portability detail, or a small prerequisite that does not change the primary path.

> [!WARNING]
> State the dangerous action or configuration, the concrete consequence, and the safe alternative or required mitigation.

A useful warning has all three parts:

```markdown
> [!WARNING]
> Do not set `timeout` higher than the worker lease. If the lease
> expires first, another worker may begin the same job. Keep the lease longer than
> the maximum job timeout.
```

Place the essential constraint before or beside the first command, option, or
example it qualifies. Put it before the action when acting first could cause
harm. A longer explanation may follow later. Ordinary prose may carry a clear,
prominent constraint; not every caveat needs an alert. Do not use a warning to
decorate common advice, and do not put requirements in a note.

## Security and Production Guidance

Treat security and production guidance as behavior, not policy boilerplate.

- Identify the exact value, command, or setting that creates exposure.
- Name what can be disclosed, overwritten, trusted incorrectly, or left unavailable.
- Explain the condition that makes the risk real.
- Give the exact safe configuration or operational action.

```markdown
> **Warning:** Do not enable detailed error output in production. It can include
> request data, stack traces, and configuration values. Enable it only in a local
> environment that does not handle real user data.
```

Distinguish environment-specific behavior. Document cache invalidation, long-lived process reloads, writable paths, external dependency availability, and destructive commands before readers run them.

For destructive commands, state scope and recovery:

```markdown
This command deletes every record in the selected database before recreating the
schema. Do not run it against production data. Use a migration when existing data
must be preserved.
```

## Troubleshooting

Write causal remedies, not a list of guesses:

```text
symptom → cause → correction → verification or prevention
```

````markdown
If new code is not running after deployment, restart the worker. Workers remain
in memory and do not load changed files automatically:

```shell
tool worker:restart
```

After the restart, submit a new job and confirm that its log entry includes the
new version.
````

Include relevant boundaries: operating system, dependency version, database driver, protocol, browser, deployment mode, or runtime setting. A limitation without its boundary looks like a product bug or a universal rule.

## Precision Checklist

Before publishing, ask:

- Does every required value have a name, type or format, scope, and default where relevant?
- Does each example explain success, failure, and automatic behavior when those affect implementation?
- Does every safety claim name the specific risk and mitigation?
- Are timing, ordering, and durability constraints close to the relevant operation?
- Are limitations tied to the affected driver, platform, mode, or version?
- Does the text distinguish optional configuration from required configuration?
- Does a reader know what state should exist before and after each command?
