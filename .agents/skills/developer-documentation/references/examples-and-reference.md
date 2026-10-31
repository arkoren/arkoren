# Examples and Reference Entries

Arkoren builder examples use descriptive role names different from the imported
package for receivers, callback parameters, and published locals. Prefer natural
nouns (`details`, `statement`, `definition`, `table`, `predicate`, `object`,
`listener`); use single words, not composites. The type already identifies the builder role. Use neither initials
nor generic `builder`. Write `func(details *problem.Builder)` and
`func(object *validate.StructBuilder[User])` with normal imports. Retain aliases for
native SDKs and distinct same-name imports, not solely to accommodate a local
name. Follow the contributor `arkoren-go` naming rule in source comments, shared
documentation, and standalone website examples. Use `workflow.Builder` for the
main declaration builder and keep distinct roles such as `workflow.ClientBuilder`.

## Contents

- [Function and chain formatting](#function-and-chain-formatting)
- [The example unit](#the-example-unit)
- [Example completeness](#example-completeness)
- [Progressive walkthroughs](#progressive-walkthroughs)
- [Method and option entries](#method-and-option-entries)
- [Tables, output, and annotations](#tables-output-and-annotations)
- [Testing examples](#testing-examples)

## Function and Chain Formatting

Put each example function's body on new lines, with the closing brace on its
own line. This applies to named functions and callback literals in skills,
guides, reference notes, README files, package examples, and website snippets.
Collection callbacks follow the same format. Bodyless function signatures and
function types are not function bodies. Omit generic type arguments that Go
can infer from arguments or callbacks; retain them when inference needs them.

Prefer fluent chains on one line, regardless of call count. Keep callback
bodies multiline without moving their method call to a separate line:

```go
failure := problem.Conflict().Derive(func(details *problem.Builder) {
    details.Detail("The record changed before this update.")
})
```

Keep short chains compact:

```go
deadline, bounded := incoming.Context().Deadline()
```

Wrap only when length or complexity makes the example harder to read. Keep
related calls together; in Go, leave the dot at the end of a wrapped line.
Prefer independent, one-action-per-line statements for mutable builder settings.
Preserve readable domain or role names when reformatting callbacks.

Separate operation handlers from constructor configuration when combining them
hides the operation being taught. A named handler or policy can clarify a longer
example; a short inline callback can remain beside its use. Apply the same
judgment to index key functions, captured dependencies, and validation schemas:
keep one clear teaching unit rather than requiring extra blocks or helper names.

Validation examples configure typed drafts, not published schemas. Start with
one small rule or child callback; introduce reusable policies only when reuse
is the lesson. Put advanced conditions, nested paths, and collection predicates
on their dedicated pages instead of expanding the first usable example.

## The Example Unit

A code block is one part of a teaching unit. Use this order:

1. A brief lead-in that establishes the situation and purpose when they are not already clear, then names the action. Follow the [use-case-first prose pattern](prose-style.md#paragraph-rhythm).
2. The smallest code block that can demonstrate that accomplishment.
3. Explain the result, default, return value, or side effect when the surrounding context has not already established it. Several related snippets may share that explanation.
4. A conditional branch only if it is a common next need.

````markdown
To retrieve the report, call `get` with its identifier:

```ts
const report = await reports.get('daily-sales');
```

The method returns the report when it exists. If no matching report exists,
`get` returns `undefined`.
````

Do not introduce a block with “Example:” when an action sentence is available. Do not leave a block without an interpretation unless the syntax is self-evident and the surrounding section has already established its contract.

## Example Completeness

Include everything a reader needs to understand the lesson, but nothing that belongs to a different lesson.

Each snippet teaches one operation or decision. Name pre-existing collaborators
in the lead-in instead of repeating imports, types, constructors, lifecycle
wiring, routing, and test harnesses. Introduce each new concept before its first
use, in the order the reader creates and connects the objects. For a service's
first construction example, show a sample driver and the facade's `New` call.
Prefer an in-process driver when it supports the lesson; a provider-specific
page uses that provider. Include only required arguments and settings needed for
the stated goal. This lets the reader see the package's entry point without
learning application composition at the same time.

Teach optional builder settings in a later block or section, introduced by the
reason to change the default. Builders are construction-only: show the relevant
constructor with its callback, not a setter on the published value or an
unexplained free-standing draft. For example, with the `cache` and `memorycache`
packages imported from `arkoren.dev/core/service/cache` and its `memorycache`
subpackage, baseline construction is:

```go
driver := memorycache.New()
store := cache.New(driver)
```

This creates a process-local cache. If metrics must distinguish it from other
caches, the separate configuration example can reuse the existing `driver`:

```go
store := cache.New(driver, func(store *cache.Builder) {
    store.Name("sessions")
})
```

These are alternative constructions, not consecutive declarations in one scope.
Neither example needs a composer, startup hooks, probes, or explicit telemetry.
Explain relevant lifecycle requirements in adjacent prose and link their
canonical setup; retain lifecycle code when managing that lifecycle is the
lesson. For this driver, `Start` optionally runs an expiry janitor and `Stop`
ends it; expired entries are not returned even without the janitor.

Split a block when the lesson changes, with prose explaining
each step; remove unrelated setup rather than merely distributing it across
blocks. Keep code small, concise, and tailored to the operation; there is no
default line maximum. Remove unrelated setup rather than necessary context or
consequential checks. Use full implementations or cumulative
walkthroughs only when the request or existing page purpose calls for them;
authoring a tutorial title does not create that permission. Link to an existing
runnable package example for full setup. Keep imports and prerequisite types available in prose or a nearby small
snippet; splitting a large program into arbitrary slices does not simplify it.

In focused snippets, omit routine error checks that only return, wrap, log, or
panic on an error. Keep the error result visible in the assignment; examples
assume the ordinary success path unless failure behavior is the lesson. Using a
returned value in the next call does not by itself make its error check part of
the lesson. For a read followed by a conditional update, omit routine propagation
after both calls and retain the branch for a rejected update, such as `!applied`.
Retain checks that demonstrate recovery, error classification, retry or rollback,
security decisions, or assertions under test. Complete runnable programs and
complete function implementations still handle errors before using results.
If a helper wrapper exists only to propagate errors, show the operation as a
fragment instead of preserving the wrapper and its boilerplate.
State this convention once on an introductory page rather than adding an
omission comment to every snippet. Preserve checks that enforce authentication,
authorization, session mutation, or deployment boundaries, and guards needed to
demonstrate cleanup of a returned resource. Ordinary propagation is not a safety
decision merely because the call reads or changes application data.

Distinguish verification in the review report: complete programs are compiled;
fragments are checked against current API names, types, and assumptions. An
API-checked fragment is not an executed program. Generated API declaration
blocks use reference shorthand and are not subject to runnable-example checks.

| Include | Omit unless relevant |
| --- | --- |
| required imports or package names | unrelated application bootstrap |
| filename or configuration location | framework boilerplate that does not affect the behavior |
| type, interface, or schema when it shapes use | every type in a long hierarchy |
| realistic setup values | anonymous magic values with no purpose |
| an assertion, output, or result comment | redundant comments that narrate syntax |
| exact command flags | flags unrelated to the stated goal |

Show the invocation being taught, with prerequisites named in prose:

With an existing reports client named `client`, publish a notification that the
report is ready:

```ts
const result = await client.publish({ type: 'report.ready' });
```

If the lesson depends on application-specific code that would distract from the point, show the seam honestly:

```ts
const report = await loadReport(id);
// Persist the report using your application's storage layer.
```

Do not substitute a fake implementation that readers could mistake for production code.

Use comments for observable outcome, intent, or intentional omission:

```ts
await cache.invalidate('reports');

// The next read loads a fresh report from the source.
```

## Progressive Walkthroughs

Use this pattern only when `page_type` calls for a requested tutorial or an
existing onboarding lesson, not as the default for package documentation.

Reuse identifiers while building one scenario. Each step should add one idea and preserve the prior success path.

````markdown
### Creating the client

First, create a client with the default timeout:

```ts
const client = createClient({ token });
```

### Sending a request

Next, use the client to send the report request:

```ts
const response = await client.post('/reports', { format: 'csv' });
```

The response contains the generated report identifier.

### Allowing more time

If the request may exceed the default timeout, pass a longer timeout for this
request:

```ts
const response = await client.post('/reports', { format: 'csv' }, { timeout: 60_000 });
```
````

Do not mutate the same sample until its state is unclear. Start a fresh snippet when a variant changes the model, setup, or intended result.

For a teaching page, establish the mental model and a focused baseline before
expanding into reference detail. A direct-lookup page can establish shared
notation briefly and answer immediately; do not force a preceding tutorial.
Let reference entries stand independently without repeating baseline setup.

## Method and Option Entries

Use a repeatable micro-structure for methods, CLI commands, configuration options, and endpoints:

````markdown
### `retry`

The `retry` method reruns an operation when it fails with a retryable error.

```ts
const result = await retry(() => {
  return client.fetchReport(id);
}, { attempts: 3 });
```

The callback runs once initially and up to two additional times. If every attempt
fails, `retry` rethrows the last error.

If delays between attempts are required, pass a `backoff` option:

```ts
await retry(() => {
  return client.fetchReport(id);
}, { attempts: 3, backoff: 500 });
```
````

Follow this order:

1. identifier heading in code formatting;
2. one-sentence contract beginning with the identifier;
3. canonical minimal example;
4. result, return value, mutation, or error semantics;
5. common parameter or variation;
6. limitation, warning, or related link.

For aliases, use a short pointer: “The `average` method is an alias for [`avg`](#avg).” Do not maintain two full entries that can drift.

When an API has overloaded forms, demonstrate the common form first. List less common signatures in a compact table or a separate variant subsection.

## Tables, Output, and Annotations

Use tables for values readers compare, not narrative flow.

```markdown
| Option | Default | Effect |
| --- | --- | --- |
| `attempts` | `3` | Maximum number of executions, including the first. |
| `backoff` | `0` | Milliseconds to wait before each retry. |
```

Name units, scope, and inclusivity. “Maximum number of executions, including the first” prevents a common off-by-one interpretation.

When output is short and meaningful, show it:

```text
Report created: rpt_123
```

When a data structure changes, use result comments near the relevant code:

```ts
const names = users.map((user) => user.name);

// ['Ari', 'Sam']
```

Do not show output that depends on randomness, timestamps, private data, or unstable formatting unless you mark the unstable field clearly.

## Testing Examples

Teach tests by isolating the observable contract.

1. Arrange only the state needed for the behavior.
2. Replace external side effects with a fake, spy, or test double when the test is about dispatch rather than the external system.
3. Execute one meaningful action.
4. Assert the externally visible result or interaction.
5. Test the side-effect handler directly in its own test when it has behavior of its own.

```ts
const deliveries = fakeDeliveries();
const service = createReportService({ deliveries });

await service.publish(report);

expect(deliveries).toHaveSent({ type: 'report.ready', reportId: report.id });
```

Explain what the fake proves: it proves the service requested delivery; it does not prove the delivery provider can send a real message. Do not use broad assertions when a precise assertion communicates the contract.
