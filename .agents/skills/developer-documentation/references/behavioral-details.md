# Behavioral Details That Readers Need

## Contents

- [Absence, defaults, and explicit values](#absence-defaults-and-explicit-values)
- [Cardinality, mutation, and evaluation](#cardinality-mutation-and-evaluation)
- [Ownership, cancellation, and completion](#ownership-cancellation-and-completion)
- [Ordering, limits, and continuation tokens](#ordering-limits-and-continuation-tokens)
- [Precedence and scope](#precedence-and-scope)
- [Portable contracts and integrations](#portable-contracts-and-integrations)

## Absence, Defaults, and Explicit Values

Do not write “uses the default when no value is supplied” until you know whether an empty string, `null`, `false`, zero, or an empty collection counts as “no value”. These distinctions affect real callers.

Document the exact state that selects the default:

```markdown
When `timeout` is omitted, the client uses 30 seconds. Passing `0` disables the
per-request timeout; it does not select the default.
```

When normalization changes input, show the boundary and result:

```markdown
The page number is clamped to at least `1`. Values larger than `100` are rejected
instead of being silently reduced.
```

For fallback accessors, distinguish missing from present-but-empty:

```markdown
`valueOr('region', 'us-east')` returns the fallback only when `region` is absent.
An explicitly configured empty string is returned unchanged.
```

State whether empty output is `[]`, `{}`, `null`, `undefined`, no body, or an error. Consumers often need this to decide how to serialize or branch.

## Cardinality, Mutation, and Evaluation

A method name does not communicate whether it returns one item, many items, an iterator, or a promise. Put that information in the first contract sentence.

In a guide, establish the practical need before that contract sentence using
the [use-case-first pattern](prose-style.md#paragraph-rhythm). A direct reference
entry can begin with the contract when the reader is already looking up the
operation.

```markdown
`find` returns one record or `undefined`. `select` returns an array of matching
records. `cursor` returns an async iterator and does not load all records at once.
```

For collection and query APIs, state all applicable properties:

- whether the operation mutates the input or returns a new value;
- whether a new value copies, aliases, or retains its input storage;
- whether it evaluates immediately, lazily, or when materialized;
- whether repeated iteration repeats work or observes new source state;
- ordering guarantee or absence of one;
- memory implication for large inputs;
- early-stop, partial-result, and error policy.

```markdown
`map` returns a new collection and leaves the original collection unchanged.
`lazyMap` runs the callback while the result is consumed. Iterating it twice calls
the callback twice unless the source has been materialized.
```

Never describe a wrapper as immutable, lazy, replayable, or memory-efficient without describing the condition that makes the claim true.

## Ownership, Cancellation, and Completion

Document who owns a resource after a successful call and the event that ends work.

```markdown
`open` returns a readable body. The caller must close the body after reading it.
```

For streams, subscriptions, transactions, locks, and background jobs, state:

- who starts the work;
- when it becomes visible or durable;
- who must close, acknowledge, commit, unsubscribe, or release;
- cancellation and timeout behavior;
- whether cleanup is automatic on error;
- what a successful acknowledgement proves and does not prove.

```markdown
A successful `publish` result confirms that the transport accepted the message. It
does not confirm that a downstream consumer processed the message.
```

Separate dispatch from execution. “The job is queued” must not imply that it has run, succeeded, or been persisted.

## Ordering, Limits, and Continuation Tokens

Document ordering before pagination or “first” operations. A result cannot be reliably resumed unless its order is stable.

```markdown
Results are ordered by `createdAt` and then `id`. The cursor is valid only for this
ordering and the same filter set.
```

For a cursor or continuation token, state:

- whether callers must treat it as opaque;
- the source/filter/order/context that scopes its validity;
- how next and previous tokens are derived;
- when a token is absent, including empty results;
- invalid, expired, malformed, or incompatible-token behavior;
- whether records added or removed between pages can change traversal.

For limits, state whether `0`, a negative value, and an omitted value differ. Name whether a limit is inclusive, whether it applies before or after filtering, and whether the response indicates that more data exists.

## Precedence and Scope

When multiple sources can provide a value, show the order explicitly. Do not say “the last value wins” without identifying what “last” means.

```markdown
Values are resolved in this order: explicit options, environment variables, then
configuration file. A later provider replaces the complete value for the same key.
```

Document scope for configuration, caches, context, and credentials:

- process, request, session, transaction, worker, connection, or global;
- inheritance or replacement behavior;
- whether a change affects existing long-lived objects;
- cache invalidation or restart required for a change to take effect.

State negative guarantees plainly:

```markdown
The core client does not read credentials from disk. Provide credentials through
the configured credential provider.
```

A clear non-guarantee prevents more implementation errors than a vague caveat.

## Portable Contracts and Integrations

Separate the stable contract from integration-specific capabilities. Tell readers what all implementations promise, then label extras by integration.

```markdown
All storage adapters support read, write, list, and delete. Version identifiers and
public URLs are available only when the selected adapter supports them.
```

For an integration, list prerequisites immediately before use: credentials, paired fields, endpoint/region, permissions, plugin, driver, or local service. State unsupported combinations and their supported alternative.

```markdown
The local development transport accepts plaintext connections only on a trusted
local network. Use TLS for any remote or production connection.
```

Do not document an integration's behavior as if it were portable. Do not document a portable abstraction without identifying the integration-bound operations readers cannot rely on.
