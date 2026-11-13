---
name: arkoren-go
description: >
  The Arkoren repository's API and Go rules: constructors and options,
  immutable definitions, lifecycle, naming, errors, async verbs, telemetry,
  and tests. Use when writing, changing, or reviewing Go code in this
  repository.
---

# Arkoren Go rules

These are the project's choices on top of idiomatic Go. Formatting, imports,
and most lint rules are enforced by tools (the goimports hook and
`golangci-lint`), so they are not repeated here. Review builder aliases, stable
problem identities, and module boundaries explicitly; no repository AST gate
enforces these conventions.

## Package organization

`cmd/arkoren-ci` is an independent development-only Go 1.27 module. Keep it
stdlib-only and independent of core, foundation, drivers and `cmd/arkoren`.
Use standard flags and focused internal packages for implemented maintenance
operations, not plugin registries or empty future-port packages. It is not a
consumer command or release namespace; verify it with the shared Make checks
and `GOWORK=off` like every other CI module.

Maintenance subprocesses use explicit working directories, bounded contexts and
the narrow environment in `internal/process`. Metadata-reading gh receives only
the read token. App gh writes and atomic Git pushes receive an explicit scoped
token; only signing Git/SSH processes receive the temporary dedicated key path.
Public Go builds and git-cliff receive no credentials or ambient Git overrides.
Candidate trees supply data, not executable configuration. Keep native
cliff configuration and its single version pin embedded in the trusted command.
Ordinary module tests stay tool-independent; `make maintenance-check` explicitly
enables real Git/git-cliff/SSH fixtures. Preserve canonical-v1 signature bytes and
the full original plan during recovery. Keep administrative policy mutations out
of ordinary publication. Use native gh for auto-merge and prerelease creation;
leave release Markdown parsing to the existing website TypeScript implementation.

When reorganizing a package, use single-word Go filenames and pair each test
file with its actual source: `foo.go` and `foo_test.go`. Put examples in the
matching test file too. Split cross-cutting tests by the behavior they verify;
do not add empty source files to justify test filenames. Keep public declarations
in the owning package and cohesive private implementations in package-local
`internal/` packages. Prefer shared `core/internal/` only for actual shared use.

## Generated application documentation

In `cmd/arkoren/templates/`, document functions and methods at their
declarations. Keep comments specific to their behavior, inputs, ownership,
and failure handling. Put project setup and architecture guidance in the
generated README and AGENTS.md rather than repeating it above `package`.
Generated Go files have no package-level `Package ...` or `Command ...`
documentation and no generated `doc.go`. This exception applies to consumer
application templates; Arkoren library packages retain their package docs.

Generated function and method comments describe behavior without embedded usage
examples, including exported symbols. Keep usage examples in the generated README
or consumer guides: starter source already demonstrates the wiring. Template
symbols are exempt from the library's public-symbol example requirements.

Export generated bootstrap construction and configuration entry points, such as
`Application`, `Logger`, `Router`, and `Server`. Capitalized names keep them
distinct from lowercase imported package names; use the declared package name
rather than an import alias introduced only to avoid a helper-name collision.

## API shapes

Builder method receivers and construction, declaration, and Derive callback
parameters use descriptive role names different from the imported package.
Prefer a natural noun (`details`, `statement`, `definition`, `job`, `consumer`,
`table`, `predicate`, `object`). Use single words, not composites: a server
draft is `listener`, a database draft is `connection`, and middleware settings
are `policy`. The type already identifies a builder. Never use initials or the
generic `builder`.
For example, `func(details *problem.Builder)`, `func(statement *query.Builder)`,
`func(migration *migrate.MigrationBuilder)`, `func(action *mcp.ToolBuilder[In, Out])`,
`func(definition *workflow.Builder)`, `func(step *workflow.StepBuilder)`, and
`func(agent *llm.AgentBuilder[Out])`.
Keep normal imports and name the value for its role, even when the body does not
yet use the package. Use `object *validate.StructBuilder[User]`, `mailer *mail.Builder`,
`encrypter *crypto.Builder`, `access *gate.Builder`, and
`cookies *session.Builder`. Use `connection *database.Builder`,
`policy *cors.Builder`, and `language *locale.Builder`. Broadcast facades
use `publisher` and `consumer`; a driver serving both roles uses `broker`.
Published locals and ordinary parameters must
also leave imported package names available. If cache and storage share a scope,
use `store` and `files`. Keep legitimate native SDK aliases and aliases
that distinguish two imported packages with the same declared name; remove an
alias used only to work around a local name. Same-package receivers do not shadow
an import of their own package, but builder receivers keep the callback's role.
Apply this rule to source, tests, comments, skills, guides, and standalone website
examples. Do not add forwarding APIs or compatibility aliases for naming.

The main concrete builder is `Builder` when a prefix merely repeats its declared
package name (`workflow.Builder`, not `workflow.WorkflowBuilder`). Keep distinct
roles such as `workflow.ClientBuilder`, `search.QueryBuilder`,
`application.MCPToolsBuilder`, and
`migrate.MigrationBuilder`; related domain words are not exact package stutter.

Examples put every named function and function-literal body on a new line and
its closing brace on its own line, including collection-operation callbacks.
Omit generic type arguments when Go can infer them from the arguments or
callbacks; keep explicit types when inference cannot determine them.
Prefer fluent chains on one line, regardless of call count.
Wrap only when length or complexity makes the example harder to read; keep
related calls together rather than putting every call on its own line.
A multiline callback body does not require a break before its method call.
Ordinary mutable builders prefer one setting action per line.

Every public API takes one of three shapes.

Every exported construction draft has a matching function alias:
`type BuilderFunc = func(*Builder)`. Distinct roles append `Func` to the
draft name (`MCPToolsBuilderFunc`, `StepBuilderFunc`); generic drafts preserve
their type parameters (`AgentBuilderFunc[Out any] = func(*AgentBuilder[Out])`).
Use these aliases in callback parameter, slice, and return types. Function
literals still name their concrete draft parameter. Aliases preserve identity
with ordinary function types, including slices; they are not defined types.
Settings-free APIs, execution handlers, native SDK hooks, and per-call options
retain their separate types.

Go 1.27.1 cannot import cycles through generic builder callback aliases. At an
affected method boundary, spell the identical function type directly, such as
`When(condition ConditionBuilderFunc, configure ...func(*StringBuilder[S]))`.
Keep the matching exported alias and constructor alias parameters; ordinary
callback functions, alias slices, and method values retain the same types.
Use this exception only to break a demonstrated compiler alias cycle.

**Components** (drivers, facades, anything with `Start`, `Stop`, or `Run`) are
configured once, at construction, and have no setters afterwards:

- Configurable core, in-process and vendor construction takes role-specific
  callbacks: `New(<values you have>..., <required settings>..., configure ...BuilderFunc)`.
  Constructors do no lifecycle I/O and validate ordinary configuration;
  supplied construction-callback panics and nil-entry misuse propagate.
- `NewFrom(client, <required settings the client lacks>..., configure...)` wraps
  a native client. Its doc states which settings apply and who closes the client;
  some conversions take ownership. Settings-free conversions have no callback.
  There is no `NewWith` or runtime struct.
- Values the caller already has (driver, handler, client) come first. A
  required setting is one with no safe default and no environment or
  default-chain fallback (secret, key, DSN, host, bucket, queue map, API key),
  ordered as the vendor SDK does (`sql.Open(driverName, dsn)`); the
  constructor still validates its content and names it in the error.
  Everything else is an optional builder setting, even when essential (a Redis address, a token
  from the environment).
- Middleware is `<pkg>.Middleware(required..., configure...)`.

**Definitions** (jobs, tasks, entries, steps, workflows, migrations, schemas,
queries) are immutable values, usually package-level variables. Queue jobs, SQL
queries, migrations, MCP tools, workflows/steps, LLM agents, search
indexes/queries, and schedule entries use the builder publication rules below.
Timing Spec operations stay immutable; validation schemas use the construction
drafts specified below:

- `New<Noun>(identity[, fn])`; the function only when the definition carries code.
- Settings belong to the construction draft in callback families and return that
  same draft. Published definitions expose getters, not configuration setters.
  Getters retain their existing `X()` or `XValue()` names.
- The first invalid input is kept and reported by `Err()`; registration
  (`Handle`, `Add`, `Handlers`) and use (`Dispatch`) return it wrapped with the
  package sentinel. Only `workflow.NewSignal` panics for domain validation;
  supplied callback panics and nil-callback misuse propagate as documented.
- A handler belongs in the definition only when an engine replays it
  (`workflow.New`, `workflow.NewStep`); otherwise register it separately
  (`consumer.Handle(job, fn)`), so dispatch-only processes skip its imports.
- A zero `queue.Job[T]` is invalid: construct it with `queue.NewJob`.
  `Err` detects the missing identity even after builder settings; `Handle` and
  `Dispatch` reject it with `ErrInvalidName` before registration or encoding.

**Outbound messages** use immutable construction without a registration name.
Pass mandatory inputs positionally: `mail.NewMessage(recipients, body, configure...)`
and `push.NewMessage(recipient, title, configure...)`. MessageBuilder methods set
optional values only; published Message values expose getters and Err, not fields
or setters. Capture and publish owned containers independently; mail attachments
also detach their bytes. Callbacks run once in order even for invalid messages.
Omitted mail From can be supplied by Mailer defaults; full validation happens
after those defaults are applied without changing the original message.
Push retention is per-call policy: facade zero selects its default and negative
disables retention; completed driver SendConfig uses zero for no retention.

**Migration definitions** use `NewMigration(id, configure ...MigrationBuilderFunc)`.
Callbacks run directly once, synchronously in order on one fresh draft, including
invalid declarations; nil entries panic, expanded nil/empty slices omit edits.
Up/Down copy their step slices and reject duplicate calls, empty input, and nil
steps as domain errors, without invoking steps. Up is required; Err and Add
report its absence. Only replaces the dialect restriction; an empty list is an
error. Publication detaches step/dialect slices, not functions or their captures.
NewGroup/Migrations and Exec/Schema remain; migrator construction uses Builder
callbacks, while per-call rollback options remain unchanged.
There is no Definition.Derive.

**Workflows and steps** keep positional names and typed handlers, followed by
Builder or StepBuilder callbacks. Callbacks run directly once,
synchronously in order on one fresh draft, even for invalid declarations; nil
entries panic, expanded nil/empty slices omit edits. Nil handlers remain
ErrNilFunc domain errors. Set Builder.MaxDuration before registration;
Handlers still reports its absence. Publish independent settings and snapshot
registered handlers. Step.Derive copies name, function, policy, and first error
before edits without reapplying defaults; use it for a local Context.Step policy.
There is no Workflow.Derive. Keep pointer handles, replay semantics, signals,
runtime records, and per-start/per-wait options.
Child acquires cancellation ownership only when it atomically creates the run;
joining preserves the existing run's owner. Replay retains that decision.
Drivers lacking this guarantee return ErrUnsupported before submission. Restate
and Hatchet reject Child; Spawn is detached work, not a lifetime-equivalent fallback.

**LLM agents** use `NewAgent[Out](name, configure ...AgentBuilderFunc[Out])`.
Callbacks run directly once, synchronously in order on one fresh draft; nil
entries panic, expanded nil/empty slices omit edits. Final validation follows
all callbacks; any output type other than exact string (including a named string)
requires Output. Detach owned tool containers and rendered schema bytes at input
and publication, not schema implementations or captures. No Agent.Derive is
added. Keep typed NewTool, raw driver records, separate model/embedder
construction roles, Run/RunStream options, fake APIs, and conformance-suite options.

**LLM tools** retain positional name, description, typed input schema and handler,
then optional nongeneric `ToolBuilderFunc` callbacks (`func(*ToolBuilder)`).
This explicitly extends the formerly settings-free NewTool; existing direct
calls remain valid, but exact constructor function-type assignments must include
the variadic callback parameter or use a forwarding wrapper. Render and clone
schema bytes before callbacks. Preserve positional validation precedence and
sticky errors; callbacks run even for invalid inputs. Available(nil) is invalid,
omission allows use. Detach draft settings, not functions/captures. Availability
has no cache: filter advertised tools once per Run/RunStream and recheck after
the concurrency slot, before decode/Parse/handler; retain hidden tools guarded.
Denial is model-safe ErrToolUnavailable; policy errors preserve causes under
ErrToolAvailability (execution also ErrPermanent), never exposing policy text.
Resource authorization stays in application handlers. No output schema or
result-encoding change; share schema/handler/policy with MCP, not tool converters.

**MCP tools** use `NewTool(name, typedHandler, configure ...ToolBuilderFunc[In, Out])`.
Publish immutable heterogeneous `Tool` values from typed drafts; keep handlers
positional and shared. Input parsing runs once. Output rules describe the emitted
object; adapters validate exact encoded bytes rather than normalize output.
Managed MCP uses validate.FromJSONSchemaStrict for exact numeric comparisons;
only validate.Errors denotes a schema mismatch. Cancellation, malformed JSON,
and validator resource refusals remain internal failures, never repair advice.
Annotations are hints, not policy. Preserve error identity in core and redact
internal failures at the adapter; only `ToolError` messages are client-safe.
Follow the declaration callback/ownership rules above and keep the SDK in the
driver module. See the shipped MCP guide for application wiring and security.

**SQL queries** publish `Query` values from `Table(name, configure...)`,
`FromRaw(source RawStatement, configure...)`, and `Query.Derive(configure...)`.
Callbacks share one mutable `*Builder`, run directly once synchronously in order,
and return no value. Nil entries panic; expanded nil/empty slices omit edits.
Settings return the same draft. Publication detaches owned containers and retained
root, predicate, and conflict drafts, not arbitrary SQL argument objects. Derive
preserves inherited errors; no callbacks preserves the base. Only Query and raw
statements compile or execute; drafts have no Statementer methods. Count and Seek
remain immutable Query transformations. EXISTS accepts published Query only.
Each Scope callback gets its own restricted `*PredicateBuilder`, protected by AND
against the whole user expression and other scopes. Empty scopes add nothing.
Ordinary groups accept variadic callbacks sharing one restricted predicate draft;
their final expression must be nonempty. Predicate drafts expose no source,
selection, joins, ordering, mutation, or compilation. OnConflict returns a
parent-attached `*Conflict`; DoUpdate and DoNothing return the parent `*Builder`.
Select replaces and AddSelect appends; Update replaces map values but retains
computed assignments; Returning replaces. Keep typed bulk maps and Values options.

**Search definitions** use `NewIndex(name, key, configure ...IndexBuilderFunc)`
and `NewQuery(index, text, configure ...QueryBuilderFunc)`; both builders are
nongeneric. Callbacks run directly once, synchronously in order on one fresh
draft, even for invalid required inputs; nil entries panic and expanded nil/empty
slices omit edits. Index lists accumulate and are copied at capture/publication;
getters and Schema sort/deduplicate. Index.Err is computed from the completed
schema/key, not a sticky missing-searchable-field error during construction.
Query.Derive detaches owned containers and preserves the first error without
reapplying defaults. WhereGroup callbacks share a restricted PredicateBuilder;
the final expression must contain conditions. It exposes no ordering and no
OrWhereGroup. Preserve typed memberships, scalar/time normalization, finite-number
checks, and nested/value-list copies from Inspect. Published Index/Query values
have no configuration setters. Custom Indexer implementations expose Err as well
as Schema. Validate Err before capturing their schema. No Index.Derive is added.
Raw Schema/Spec records remain unchanged. Engine Builder.Indexes appends
registrations separately.

**Schedule entries** keep positional names and execution functions, followed by
`EntryBuilder` callbacks. Callbacks run directly once, synchronously in order on
one fresh draft, even for invalid required inputs; nil entries panic and expanded
nil/empty slices omit edits. A nil execution function remains ErrInvalidEntry,
not callback misuse. When is required after all callbacks; invalid specs retain
the first error even after a later replacement. Publication copies entry settings
and immutable Spec, not execution functions, captures, or time.Location. Overlap
is enabled by default; zero timeout is unlimited and negative timeout is invalid.
No Entry.Derive is added. Keep Add atomicity, registration order, ErrRunning,
Spec/DST/Next behavior and deferred jobs unchanged. Scheduler and memory driver
construction use Builder callbacks; native drivers use their own builders.

**SQL schema declarations** retain their existing `Table`/child DSL.
`schema.Create`, `CreateIfNotExists`, and `Alter` accept zero or more
`func(*schema.Table)` declarations. Validate the table name before invoking
callbacks. Invoke each supplied callback directly, once, synchronously, in order
on one draft; nil entries and application panics propagate. An expanded nil or
empty slice supplies no callbacks. Validate the combined declaration after all
callbacks; omission or empty callbacks produce the normal invalid operation for
missing columns/actions. Snapshot the root, actions, and children before lazy SQL
rendering, so retained declaration pointers cannot change published operations.

**Per-call variation** is a variadic option on the call, `func(*<x>Config)`,
named after the call (`StartOption`, `BodyOption`):
`client.Start(ctx, w, id, in, workflow.Delay(d))`.

**Validation schemas** publish immutable schemas from variadic typed construction
callbacks. Infer constructor type arguments from concrete callbacks, including
`validate.Struct(func(object *validate.StructBuilder[User]) { ... })`.
Use explicit type arguments when callbacks provide no inference. Every draft
has its matching `BuilderFunc` alias. Configure children directly through typed
`Field`, `Keys`, and `Values` callbacks; slices and maps both use `Values`.
`Field` selects Go field names and checks exact types at construction.
Attach reusable published schemas through typed `Use` methods. `Ptr` remains an
explicit pointee adapter; direct pointer drafts configure their pointee with
`Value`. Settings return the same draft; published schemas expose execution and
inspection rather than configuration setters.
`Use` accepts same-role schema values or non-nil pointers. Schema-level rules
accumulate; overlapping child imports and replacement of imported children are
construction errors. Extend a shared child inside one child callback instead.
Callbacks run directly once, synchronously in order on one fresh draft. Supplied
nil callbacks panic; expanded nil/empty slices omit edits. Publication detaches
owned registrations, rules, children, and condition expressions from retained
drafts, not functions or arbitrary captured values. Preserve aggregated
construction diagnostics and concurrent schema reuse.
Cross-field conditional rules use one `ConditionBuilder` callback family.
Conditions refer to Go field paths relative to the enclosing struct; resolve
paths and check types at construction. Collection predicates retain index/value
or key/value arguments. Conditions are observations, not schema executions:
they neither normalize input nor emit violations. Keep collection assertions
separate from transformations and derived-count constraints validation-only.
See the validation guides for presence, nil traversal, grouping, and ordering.
This API does not change MCP, LLM, query, or component construction.

**Problem details** use `problem.New(configure ...problem.BuilderFunc)` and
immutable `Details.Derive`. Call supplied callbacks directly once, synchronously,
in order on one draft; nil entries panic, expanded nil/empty slices omit edits.
Builder settings return that same draft. New defaults zero status to 500, empty
type to about:blank, then empty title from the final status. Derive preserves
stored members, including inherited titles and decoded absent status. Standard
members have read-only accessors; Extension returns value/presence and Extensions
copies the outer map. Detach outer extension maps and header slices on input and
publication, not arbitrary nested any values. Error(nil) clears the cause;
Header(name) removes a header. Plain JSON omits zero status; HTTP completes it.
Decode fresh state and assign only on success, clearing unrelated old cause and
headers. Details matching remains the stored Type/Status/Title tuple, with normal
Unwrap/As pointer/value behavior. This rule does not migrate other constructors.
HTTP status helpers use New, so they store about:blank and match equivalent
New constructions. Preserve the explicit 422 "Unprocessable Content" title.
Raw zero and decoded absent members retain absence; Is performs no normalization.

## Collection results

Return collections where callers process data results. Accept standard
containers or iterators where interoperability matters. Keep execution,
delivery, and lifecycle protocols explicit.

- Application-facing eager results and detached inspection snapshots use
  `collection.Slice` or `collection.Map`; deferred fallible data producers use
  `collection.TryLazySlice` or `collection.TryLazyMap`. Prefer native results
  over parallel convenience accessors that only wrap the same data.
- Keep raw driver contracts, SDK records, registration inputs, and HTTP stream
  inputs in their standard types unless their own contract requires a change.
  Collection producers supply `Iter()` at standard iterator seams.
- Preserve existing copy depth, ordering, nil/empty representations, and result
  envelopes. Wrapping does not copy. Page transformations affect the returned
  window, not server totals, ordering, or navigation tokens; diagnostic cursors,
  loss/reset information, and completion records remain authoritative.
- Lazy sources own cancellation, cleanup, and repeat-traversal behavior. Stop
  after yield returns false, and document repeated I/O, partial prefixes on
  errors, and source limits. Collection JSON materializes finite sources;
  it does not provide network streaming or automatic resource management.
- Before changing DTO fields, test JSON shape, inferred schema fidelity, and
  exact-type validation. Preserve custom-marshaler opacity unless the type
  explicitly declares its wire shape. Foundation modules remain stdlib-only.
  Eager collections declare `JSONSchemaType() reflect.Type`; lazy sources remain
  opaque. Use the explicit `validate.Named`/`NamedBuilder` adapter for logical
  container rules. It preserves source exact-type requirements and does not
  turn logical byte-element constraints into a base64 wire schema.
- Introduce collections to remove caller complexity, not to rewrite ordinary
  internal loops or invent enumeration contracts. Queue settlement, event
  dispatch, lifecycle callbacks, and useful value-plus-error outcomes retain
  their specialized semantics.

## Configuration

- Application, event, app-owned MCP capture, and memorytelemetry use
  trailing role-specific callbacks. Invoke them directly once, synchronously
  in order on one fresh draft; supplied nil entries panic, expanded nil/empty
  slices omit edits. Settings return the same draft. Use domain parameters
  `events`, `tools`, and `store`, and the [application role](#application-role)
  for application construction; publication detaches owned
  roots/containers, not service pointers, loggers, or closure captures.
- `Composer.MCPTools(prefix, configure...)` returns native `[]mcp.Tool` and
  attaches capture before startup. Its prefix must be nonempty and is used
  directly for all app and telemetry names and CLI groups: `"app"` produces
  `app.info`, `app.logs`, and `app.traces`. Empty prefix returns
  `mcp.ErrInvalidTool` before callbacks or capture.
  `MCPToolsBuilder.Limits(application.CaptureLimits)`
  sizes new capture. Composition explicitly selects and registers returned tools;
  apply `Tool.WithAvailability` to copies for capability policy, and keep resource
  authorization in shared typed application handlers. Core remains SDK-free.
  CLI metadata belongs to each tool, not a parallel catalog or protocol.
  telemetrytest.New forwards memorytelemetry.Builder callbacks once; its suite
  options remain separate.
- Queue construction uses `NewJob[T](name, configure ...JobBuilderFunc)`,
  `NewConsumer(driver, configure ...ConsumerBuilderFunc)`, and
  `NewDispatcher(driver, configure ...DispatcherBuilderFunc)`. Supplied
  callbacks run directly once, synchronously in order on one fresh draft; nil
  entries panic, expanded nil/empty slices omit edits. Methods return the same
  draft. Publish detached roots and owned containers; retained drafts cannot
  change jobs or facades. Preserve the first job error and existing error timing.
  Reconstruct job variants with a shared policy and later local override; no
  Job.Derive. Keep TimeoutValue and DescriptionValue getters. Dispatcher exposes
  only Telemetry/System, consumer additionally Queues/UnknownNameAttempts/Events.
  No queue Config/Option/NewConfig/WithConfig or snapshot getter remains.
- HTTP construction uses role-specific builders for auth/gate/client/link,
  middleware, router.Recover, server and session, plus telemetry's HTTP roles.
  Callbacks run directly once in order on one draft; nil entries panic and
  expanded nil/empty slices omit edits. Publish detached owned settings, not
  live services, native internals, or function captures. Preserve initial
  recommended defaults versus later normalization, especially explicit empty
  security headers. Store request hooks; do not invoke them during construction
  or replay user callbacks through delegating constructors. Share immutable
  auth.NewSessionIdentity(load) across Authenticator, Login, and Logout; it binds
  the ID type to the loader. Zero definitions and nil loaders fail first with
  plain ErrInvalidSessionIdentity, never credential fallback. Check mutation
  errors before continuing; explicit nil sessions return session.ErrNoSession.
  Default session wiring stays handle-free. Custom context slots share one
  session.NewHandle() through session.Builder.Handle, auth.SessionBuilder.Handle,
  and request.WithSessionHandle. Zero Handle restores the default slot; copies
  keep identity, separate allocations differ. NewContextFor/FromContextFor are
  typed low-level forms. Handles do not isolate cookies, storage, or authorization.
  OAuth Callback(identity, mapper) and Logout(identity) select that session once
  before cookie consumption, exchange, or mapping, with no default fallback.
- Webhook `Event.Map` returns a new credential-free definition with the same
  provider key. Run the original decoder before pure, no-I/O conversion and
  preserve the first definition error; nil conversion is a domain error, not
  construction-callback misuse. Functions/captures stay shared. Conversion
  errors follow decoder semantics (503 or authenticated ErrSkip), never accept
  semantics (every error is 503). Prefer Map for application-owned queue DTOs
  rather than duplicate envelope decoding or add a public Decode operation.
- Broadcast publisher/consumer, cache, crypto, database/migrator, feature, hash,
  mail, secret and storage facades use trailing role-specific callbacks. Configurable
  memorybroadcast/cache/queue/secret/storage and slogmail use Builder callbacks.
  Policies run directly once in order on one fresh draft; nil entries panic,
  expanded nil/empty slices omit edits. Settings return the same draft. Detach
  owned roots and containers at capture/publication, not live dependencies.
  PublisherBuilder has only Telemetry/System; ConsumerBuilder adds Events.
  Crypto Previous appends interface slices, not cloned drivers; mail ReplyTo
  replaces and copies addresses. Mailer.Config remains a detached reporting
  record, never an input escape hatch. Memorysecret copies map and byte slices.
  Payment and push builders select Telemetry/Events; secret selects both for
  facade provider operations and mutation notifications. Hash and crypto select
  Telemetry for metrics only, without adding context-bearing operation methods.
- LLM ModelBuilder and EmbedderBuilder expose only applicable settings: both
  Telemetry/Events/Provider; model adds MaxTokens/MaxSteps, embedder adds
  Dimensions/Purpose. Config remains a reporting record from both getters,
  not a constructor input. Search Builder.Indexes appends copied registrations;
  Name/Telemetry/Events replace. Schedule Builder sets Logger/Telemetry/Events;
  workflow ClientBuilder sets Codec/Telemetry/Events, and RegistryBuilder sets
  Codec/Telemetry through NewRegistry for independent worker configuration.
  Memoryschedule/memoryworkflow Builder
  sets Logger, preserving the workflow registry as the first required argument.
  Callbacks run directly once in order on one fresh draft; nil entries panic,
  nil/empty slice expansion omits edits, and publication detaches owned state.
  Services, codecs and native objects remain shared; nil telemetry stays dynamic.
- Configurable native drivers use trailing Builder callbacks too. Shared hosts
  use Builder; workflow drivers/runners use DriverBuilder/RunnerBuilder;
  AWS broadcast SQS uses SQSBuilder; OAuth uses ClientBuilder/GoogleBuilder.
  Call policies directly once, synchronously in order on one fresh draft at the
  constructor's documented validation boundary. Required nil host/registry guards
  retain their original timing. Nil entries panic; nil/empty expansions omit edits.
  Methods return the same draft. Publish private completed settings and detach
  owned containers; delegating constructors never replay user policies.
- Settings-free constructors stay settings-free, including Azure, filesystem,
  Resend, SNS and the native conversions documented without callbacks.
  PostgreSQL database NewFrom(pool) has no callbacks: configure pgxpool first.
  MySQL/SQLite NewFrom take ownership and apply positive pool limits; Redis cache
  conversion and GCP Stop also close supplied native clients. Other ownership
  and applicability rules are package-specific, not inferred from NewFrom.
- Native SDK records/hooks, runtime/per-call options and conformance-suite
  options remain separate. LaunchDarkly SDK runs at Start. OpenSearch's Client
  configuration hook runs once during New, never NewFrom; owned native creation
  and SDK validation run at Start. Its Client accessor is nil before creation
  and after Stop; borrowed access remains immediate. OpenAI hooks run per request.
  Cloud SDK
  credential/configuration loading keeps its context and fallback chain.
  Native objects, providers, functions and captures remain shared. Retain
  existing redacted Config records as reporting only, not constructor inputs;
  do not add a universal settings getter or recursively sanitize native objects.
- AES/ChaCha AdditionalData replaces and copies at input/publication; keys stay
  privately owned. Argon2 zero settings restore native defaults; bcrypt zero
  selects DefaultCost before clamping. OTEL/Prometheus recommended booleans
  initialize before callbacks, so explicit false remains off. Temporal NewFrom
  honors NoWorker and WorkerStopTimeout; a nonzero native worker timeout wins.
- OpenAI NewFrom ignores connection settings but validates them as New does;
  API/StrictSchemas/EmbedBatchSize and request hooks apply. OpenFeature and
  LaunchDarkly NewFrom have no callbacks. OpenFeature ambient outer maps and
  OAuth Scopes/Audiences copy at input and publication, not arbitrary nested
  values. Explicit StrictSchemas(false), MaxRetries(0) and PKCE(false) retain
  their meanings; PKCE initializes enabled. GoogleBuilder.HostedDomain checks
  the verified hd claim, not only the chooser hint. Other OAuth presets have
  no optional builders; OAuth Config remains redacted diagnostics only.
- Zero numbers and durations use the default; negative means disabled or
  unlimited where that makes sense. Where empty must mean "off" (security
  headers), empty omits; construction begins with recommended values.
- Durations are `time.Duration`. Logging is `Logger *slog.Logger`, nil meaning
  `slog.Default()`; services get the logger from `app.Logger()`, never
  the `*application.Application`.
- One builder setting name takes one type everywhere (`Telemetry(*telemetry.Telemetry)`,
  `Logger(*slog.Logger)`).
- Queue `ConsumerBuilder.Queues` snapshots its input and constructors detach the
  completed restriction at publication. Each call replaces it; empty consumes
  all handled queues. Telemetry and event buses remain shared services, and nil
  telemetry resolves Default per operation. BackoffFunc(nil) is a valid setting,
  distinct from a supplied nil construction callback. Handle captures policy.

## Lifecycle and context

- At the top of each composition function or factory callback, bind the app
  handles it uses to direct locals: `events := compose.App().Events()`,
  `logger := compose.App().Logger()`, and `telemetry := compose.App().Telemetry()`.
  Add a blank line after this group, before construction and wiring. Extract only
  handles actually used; retaining only `app := compose.App()` is not a substitute.
  Keep an app local when using app-owned operations such as probe handlers.
  Apply this convention to templates, examples, and guides too. Preserve imported
  package names: choose another descriptive local if a handle would shadow one.
- Process entry points use `application.NewSignalContext(parent)` and immediately
  defer its stop function, including around startup failures. It delegates SIGINT
  and SIGTERM cancellation to `os/signal.NotifyContext`; stop releases signal
  registration even after parent cancellation. It leaves terminal settings and
  echoed `^C` unchanged. Keep signal workers and terminal detection out of
  generated entry points.
- I/O lives in `Start(ctx)` (connect), `Stop(ctx)` (release; with an expired
  ctx it still releases everything, and its doc says what it waits for), and
  `Ping(ctx)` (readiness). No `Shutdown` or `Close` aliases.
- Drivers never import `application`; composers wire `Startup`, `Cleanup`,
  `Run`, and `Check`. No `Component` functions or driver context-key options. Session
  lookup alone has request.WithSessionHandle for its per-call role.
- `application.New` resolves settings without lifecycle I/O. `Start` and `Run`
  accept ordered `ComposerFunc` callbacks. Composition completes before startup
  callbacks, then supervised runners and background checks launch. Only composers
  register lifecycle work; applications expose control and observation.
- Compose modules with typed function arguments and return values. Register
  cleanup once at the acquiring owner, immediately after acquisition. Cleanup
  runs in reverse order on composition failure, startup failure, and shutdown,
  before telemetry finalization. Startup callbacks may register acquired cleanup;
  they cannot add startup callbacks, runners, or checks.
- `Composer.Defer(func())` registers an infallible release on the Cleanup stack,
  not at the end of the composing Go function. It is valid during composition
  and executing Startup callbacks. `MustDefer(func(), error)` combines successful
  acquisition with Defer and aborts through Require on error; it is composition-only
  like Must. Failed acquisitions register nothing and own their partial cleanup.
  Both reject nil successful releases; use Cleanup for context-aware/fallible
  releases. Preserve reverse ordering, once-only execution, and panic handling.
- Application lifecycle failures are reported by the application before telemetry
  finalization. Rollback logs the startup/composition cause with cleanup errors;
  shutdown logs cleanup and runner-wait errors before stopping telemetry. Callers
  use Run's result for exit status or recovery, not duplicate post-Run logging.
  Pre-start failures and telemetry-finalization failures reach the configured
  handler only; the latter bypass the failed exporter.
- Starting is dispatched after composition and successful telemetry startup,
  before user Startup callbacks. Listeners registered during composition can
  log through active application telemetry. Composition/telemetry-start failures
  dispatch Failed without Starting. There is no built-in app starting log.
- Child composers use defined function types `Factory[T] func(*Composer) T`
  and `Action func(*Composer)`. Their private methods implement one typed
  execution protocol. These types intentionally are not aliases: their methods
  enable inference without reflection or accepting arbitrary `any` values.
  Root `ComposerFunc = func(*Composer)` remains an alias for `Start`/`Run`.
- `Composer.Use(operation)` invokes one Factory or Action inline, sharing context,
  cleanup ownership, and errors. A Factory returns its ordinary typed value;
  an Action has a void body and its internal empty completion result is normally
  ignored. Raw inline void callbacks need `application.Action(...)`; typed inline
  callbacks need `application.Factory[T](...)`. Use has no variadic or batch form.
  Bootstrap factories capture dependencies only; acquire resources and mutate
  routes inside callbacks, so skipped features acquire nothing. Returned values
  are constructed, not necessarily started. Returning them transfers no ownership
  and memoizes nothing. Use typed module-owned structs for related outputs.
- `UseWhen`, `UseDebug`, `UseIn`, and `UseInAny` accept either kind and return
  `(value, executed)`. Selected callbacks run in the same scope; skipped callbacks
  return zero and false without construction. A legitimate zero output still
  returns true. Errors abort through Must/MustDefer/Require, never masquerade as false.
  Nil interfaces, typed nil functions, recorded failures, and invalid phases are
  checked even on skipped branches. Environment matching is exact; UseDebug uses
  the construction flag, independently of environment or runtime log level.
- `Composer.Must`, `MustDefer`, and `Require` abort synchronous composition with a private,
  execution-specific panic recovered into the original error by `Start`/`Run`.
  This is the explicit exception to ordinary returned-error handling. Do not use
  these helpers in runners, startup callbacks, cleanup, or spawned goroutines.
  Unrelated composition panics propagate after acquired cleanup.
- A function takes `ctx` first only when it does I/O, may block, or must
  respect cancellation.
- Context helpers are `NewContext`/`FromContext` (`New<X>Context`/`<X>FromContext`
  when a package stores several). A package-level `With*` is always an option.

## Naming

- Driver types are `Driver` (one per package; `awsbroadcast` has `SNSDriver`
  and `SQSDriver`), interfaces are `*Driver`, receivers `driver`, `Config`
  parameters `config`. Receivers are full lowercase type names, never `c`.
- Native accessors: `Client()`, `DB()`/`SQL()`, or `Connection()`. Facades
  expose `Driver()` and `DriverAs[T]()` methods.
- Names never repeat the package (`cors.Builder`, `cache.ErrNotFound`).
- American spelling (`canceled`). Canonical initialisms (`ID`, `URL`, `HTTP`, `TTL`).

### Application role

Import `arkoren.dev/core/application` with its declared package name. Outside
that package, name application instances and construction callback parameters
`app`, for example `app := application.New()` and
`func(app *application.Builder)`. Same-package receivers may remain
`application`: they do not shadow an import of their own package. Keep the
general anti-shadowing and import-alias rules above for every other role.

## Errors

Persistent application key inputs use `arkoren.dev/foundation/key`: encryption
drivers and link/cursor signing take `key.Shared` with exactly 32 bytes, including
previous keys; use independent secrets for each purpose. VAPID and Apple Sign In
take ECDSA P-256 `key.Signing`; Apple keys remain Apple-issued. X25519
`key.Exchange` supplies native keys to existing protocols; callers own peer
authentication and KDF domain separation, not raw ECDH encryption keys. Keep
signing and exchange identities separate; the key package has no derivation API.
Preserve zero-value
rejection at consumer boundaries. Import raw/native material only through the key
constructors; keep format validation and ownership there. Provider-issued API and
webhook credentials retain their provider-specific representations. Generate new
material only during explicit provisioning, never to repair a missing startup key.

TLS identities and public CA bundles use `foundation/certificate`, independently
of application keys. Keep native `tls.Config` as the transport-policy interface:
`server.Builder.TLS` enables HTTPS and `client.Transport(base)` preserves a native
transport's TLS settings. `client.NewTransport(config)` clones the current native
default and shallow-clones the supplied TLS config; nil preserves default TLS.
Retire its owned pool through `http.Client.CloseIdleConnections`.
`Bundle.Pool` is private-only; `Bundle.SystemPool` adds system roots and reports
loading failures without private-only fallback. Neither changes OS trust.
Loading checks material and key matching, not hostname,
time, purpose, or external trust. Configure native values before sharing them;
native configurations and callbacks remain shared. Root provisioning and system
trust are explicit CLI actions, never startup repair or a loader side effect.

- Sentinels are `ErrX` variables; wrap with `%w` naming the operation; compare
  with `errors.Is`/`errors.As`. Drivers map backend not-found to the contract's
  `ErrNotFound`.
- Errors arkoren owns with HTTP meaning are `problem.Details` sentinels
  (`request.ErrInvalidJSON`), rendered by the router, and carry a string-literal
  `Builder.Type("tag:arkoren.dev,2026-09:<module>:<package path in module>:<kebab name>")`
  in their New callback, with directly inspectable explicit Title and Status calls
  for review; the generic templates in `foundation/problem` stay
  `about:blank`. Domain and contract
  sentinels stay plain errors; applications map them with `http/middleware/errormap`.
- Never discard an error silently; after a side effect, write `_ =` with a
  comment saying why.

## Async packages

`queue`, `broadcast`, `schedule`, `workflow`, and `event`:

- Names of jobs, tasks, entries, steps, workflows, and signals are `[a-z0-9-]`,
  1–64 bytes, no edge hyphen. Event and broadcast names are dotted identifiers;
  patterns add `*` and a final `#`. `internal/name` validates both.
- `Listen` registers a fan-out listener and returns `(Unsubscribe, error)`;
  `Handle` registers the single handler; `Publish` (broadcast) and `Dispatch`
  (events, queue) send; `Run(ctx)` blocks until ctx ends (fits `compose.Run`).
- `Permanent(err)` stops retries (`ErrPermanent`; in `llm` it ends a `Run`). Sentinels keep one meaning:
  `ErrInvalidName`, `ErrDuplicateName`, `ErrUnknownName`, `ErrRunning`.
- Queue `JobFailed` follows successful terminal settlement, not merely a failed
  handler. Shutdown rescheduling bypasses the immediate retry cap without
  discounting reported attempts. Scheduler Run retains ownership through drain;
  detached executions must cooperate with their own deadlines and cancellation.
- Only `application` publishes lifecycle events. Components that report what happened
  take `Builder.Events(*event.Bus)` (nil publishes nothing), publish package-level
  `event.NewTopic` topics (`event.NewSignal` without data) named
  `<area>.<noun>.<past-participle>` with `bus.Emit` (`bus.Signal`) after the
  outcome is known, and never let a listener change it.
  Free functions publish nothing (their caller does), runner exits are left to
  `application.Failed`, drivers never publish, and payloads carry no secrets, tokens,
  session IDs, contents, or full URLs.
  Mail, payment, push, search, secret, and LLM operation notifications use bounded failure
  classifications instead of raw errors. They report API observations, not
  settlement, delivery, atomic index updates, or external credential rollout.

## Telemetry

Inbound HTTP trace propagation defaults to TrustNone, including an explicit nil
TrustRemote policy. Opt into TrustInternal, TrustedNetworks, or another predicate
only when the deployment establishes trust; a proxy's private peer address alone
does not establish trust in client-supplied trace headers. Secureheaders omits
HSTS by default; applications or ingress operators explicitly choose a policy
after establishing HTTPS. Keep the generated starter on these package defaults.

Core facades doing I/O take `Builder.Telemetry` (role-specific for messaging and
LLM). Without an explicit service they follow
`telemetry.Default()` per call and cost nothing when off. Never record
payloads, keys, addresses, or secret values; personal identifiers are opt-in.
Database query text is off by default. `QueryText(true)` exports compiled SQL
verbatim, including literals and comments; enable it only for SQL audited as safe
to export. Separate bound arguments do not make SQL text itself private.

Hash and crypto keep their contextless APIs and record aggregate metrics only,
without synthetic root spans. Record each logical operation once: a hash mismatch
is not a backend error, and successful previous-key decryption is not a failed
decryption or proof of persisted rotation. Secret facade instrumentation covers
facade calls; raw driver access bypasses it.

SpanRecorder.Parent reports the actual backend-selected parent, including native
context precedence. Decorators forward it. Observer snapshots use that result
rather than infer the parent from the incoming facade context.

Workflow client spans observe commands and result waiting; worker spans observe
actual step attempts, not replayed orchestration or terminal execution. Configure
both processes independently. StartOptions.Trace is a copied serialized carrier,
separate from application Metadata; drivers forward it deterministically and
Registry.NewTraceContext extracts it only at native attempt boundaries. Keep
run IDs, payloads and metadata out of automatic telemetry. Upgrade Temporal
workers before enabling traced producers; old workers cannot decode traced
envelopes. Client events remain local action observations, not terminal events.

Span names: where an OpenTelemetry semantic convention defines the name,
follow it exactly (HTTP `{METHOD} {route}`, messaging `{operation}
{destination}`, database `{OPERATION} {collection}` with uppercase operations
such as `SELECT`, `LOCK`). Every other span is `{component} {operation}`, both
lowercase, the operation one snake_case word or phrase (`cache get`,
`cache try_lock`, `schedule run`), never a name, key, or other value; those go
in attributes (`arkoren.<component>.<field>`).

## Code

- Use string-backed enums with explicit string constants for API, configuration,
  and stored values, so their representation does not depend on declaration
  order. Keep numeric `iota` for internal states, AST tags, and bit flags;
  intrinsically numeric protocol values stay numeric. Preserve each API's empty
  zero behavior: document whether `""` is invalid, unspecified, or selects a
  default, separately from explicit values such as `"auto"` or `"unknown"`.
  Normalize defaults at the owning boundary; distinguish empty values from
  explicit constants and document changes to persisted representations.
- JSON is `encoding/json/v2`. Prefer `cmp.Or`, type switches, `maps.Clone`,
  `slices.Clone`, `strings.CutPrefix`, `strings.SplitSeq` over hand-written loops
  and single-use helpers. Guard clauses, no `else` after `return`.
- CLI commands emit one JSON result document, including local mutations and
  version reporting; follow emits NDJSON. Route local reports through the shared
  result renderer and retain stdout failure precedence. Errors and trust warnings
  are JSON records on stderr. Help and interactive prompts remain text; explicit
  key exports remain raw. Preserve these exceptions when adding output paths.
  CLI-owned summaries select task-relevant fields rather than dumping MCP
  catalogs or manuals. Default reports use counts and concise diagnostics;
  `--verbose` exposes complete safe context, file inventories, tool schemas,
  status details and safe build identifiers. Keep conflict paths as structured
  data, not comma-joined messages. Previews emit one stdout report with their exit
  verdict, without duplicate stderr lists. Preserve arbitrary remote domain
  payloads, raw exports, redaction and trust warnings in either mode.
- Value types holding maps or slices return modified copies; copy caller-owned
  input before keeping it and internal state before returning it.
- Detach owned containers at publication as well as input capture. Retained
  construction drafts must not change published settings. This is not recursive
  cloning of arbitrary `any` values (including schema defaults), functions,
  closure captures, clients, or service pointers; preserve their ownership rules.
- Document every exported identifier and struct field; comments start with
  the name and are full sentences.
- Packages and drivers that need tables ship `Migrations()` with short ids and
  never run DDL; the rules (groups, `Builder.Instance`, tables, readiness check)
  are in the `arkoren-codebase` skill under Decisions.
- Cache facades take immutable `cache.Key[T]` values. Prefer `NewKey[T]` with
  an exact name; use optional feature-owned `cache.Namespace[T]` definitions
  when related keys need a shared prefix and inherited TTL. Names are literal:
  the default `:` separator joins namespace and entry names without escaping
  or normalization. Configure TTL and separator in construction callbacks;
  `Derive` publishes independent variants, and `Subnamespace` composes a child
  prefix. Zero TTL means no expiry; reads do not refresh it. Namespaces do not
  isolate storage or provide bulk operations. Counters take `Key[int64]`;
  counter-window and lock-lease durations retain their separate semantics.
  Dots within names and standalone NewKey bytes stay unchanged. Preserve deployed
  dot-default joins with Separator("."); changing receipt names can admit replays,
  and Remember leases must follow the same final key. Built-in session/rate-limit
  namespaces already select colon explicitly and need no migration. Raw
  drivers continue to accept opaque strings and bytes.
  Guarded writes use `Driver.CompareAndPut` as one atomic comparison/write;
  wrappers transform both `Match.Key` and `Entry.Key`. Preserve mismatch
  nonmutation and uncertain-error semantics rather than falling back to Put.
- Feature evaluations and memory setup share `feature.NewFlag(name, fallback)`
  definitions. `Value` and `ValueDetail` take the definition and subject;
  memory `Set` and `SetFor` take that same definition and a matching value.
  Zero/empty definitions are invalid. Provider schemas remain external;
  preserve runtime conversion checks and fallback/error behavior.
- Structured-secret readers share immutable `secret.NewSecret[T](name)`
  definitions and call `store.Get(ctx, definition)`. The definition binds the
  provider name and JSON decoding type, not a value or pinned version. Raw
  drivers retain `Reference` and `Value`. JSON-compatible schema drift can
  still decode successfully; definitions do not enforce a provider schema.
  Preserve provider version strings. Concrete versions identify immutable
  payloads within one existing logical secret. Permanent deletion and recreation
  can reuse selectors. Recovery restores the retained secret instead.
- HTTP session fields use immutable `session.NewField[T](name)` definitions
  shared by reads, writes, and deletes. Validate definitions with `Err`; handle
  `Put` and `Delete` errors before proceeding. Decode persisted JSON as the
  field's type, but reject mismatched in-memory Go types rather than converting
  them through JSON. Preserve physical field names and the persisted format.
  Fields do not own TTL: middleware owns session renewal and absolute lifetime,
  independently of the cache namespace's write TTL. Shared field definitions
  do not isolate identity slots: auth.SessionIdentity binds login and loader ID
  types, but definitions still share the fixed arkoren:auth:user entry in a
  physical session. Login writes then rotates; Logout deletes then rotates,
  retaining unrelated fields. Preserve raw persisted JSON and middleware TTL.

Optional `secret.Namespace[T]`, `feature.Namespace[T Primitive]`, and
`session.Namespace[T]` compose ordinary definitions, not namespace-bound stores.
Prefer standalone definitions first. Secret requires positional prefix and
separator (`NewNamespace[T]("tenants", "/")` for AWS/Vault only); feature takes
prefix and exact inherited fallback; session takes prefix. Feature/session
default to `:`. Nongeneric NamespaceBuilder/NamespaceBuilderFunc configure only
Separator; Derive and Subnamespace publish detached variants. Parent separator
joins the child prefix before callbacks change future joins. Callbacks run once
in order even on invalid inputs; nil entries panic. Empty prefix/child and zero
namespaces are invalid; inherited errors reach leaves. Child Secret/Flag/Field
has no callback; Flag has no fallback override. Names are literal, empty separator
concatenates, and numeric suffixes require explicit string conversion. Validate
or encode untrusted components to avoid ambiguous joins; no escaping, provider
registration/schema, isolation, or authorization is added. Session field
namespaces differ from Builder.Namespace(cache.Namespace[jsontext.Value]) for
whole records and never alter TTL, handles, identity APIs, or arkoren:auth:user.
Check secret provider naming constraints and prefer feature subject targeting
over dynamic keys unless those flags exist. See the corresponding consumer
guides for provider qualifications and migration examples.

## Tests

- Standard `testing` only: `t.Fatal`/`t.Fatalf`, no assertion libraries.
  `t.Parallel()` first unless state is shared; `t.Cleanup`, not `defer`.
- One test function per scenario, named for it (`TestSessionRegenerateChangesID`).
  No table-driven tests. Avoid `t.Run`; use it only to group scenarios that
  share setup. A loop over plain values is fine for several checks on one
  result, or for inputs that all expect the same outcome under a name saying
  what they share (`TestEntryValidNames`); values needing different
  expectations make a table test, so split them.
- External test packages (`package foo_test`) by default.
- Control time with `testing/synctest`; drivers and fakes take no clock options.
- Each contract has `<contract>test.Run(t, newDriver, opts...)`, which every
  driver calls; infrastructure-backed drivers skip it under `testing.Short()`.
  Suite options express what a vendor cannot do (`ErrUnsupported`), never
  different semantics; a spec gap is settled in the contract's doc.
- Every operation lives in the contract's `Driver`; a vendor that cannot do one
  returns `ErrUnsupported`. No optional capability interfaces and no type
  assertions for extras: wrappers embed or forward the whole `Driver`.
- Contracts with effects leaving the process (mail, queue, broadcast, workflow,
  schedule) offer a `Fake`: `New()`, `Assert<Verb>(t, handle, preds...)` with
  the typed handle, past-tense accessors (`Dispatched()`), `Drain`, `FailNext`,
  `FailWith`, `Reset`. `llm` has no memory driver; `llmtest.Fake` scripts replies
  (`Reply`, `ReplyToolCall`) and records `Generated()`/`Embedded()`. Use fakes or memory drivers; hand-roll only vendor SDK fakes.
- HTTP: `httptest.NewRequest` with `handler.ServeHTTP`, or `router.Record(req)`.
