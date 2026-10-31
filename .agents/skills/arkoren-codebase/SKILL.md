---
name: arkoren-codebase
description: >
  How the Arkoren repository fits together (contracts, facades, drivers,
  internal plumbing, HTTP path), its domain terms, and the files each kind of
  change must also touch. Use before a change that crosses packages or adds
  a package or driver.
---

# Arkoren codebase

Use glob and grep to locate packages, declarations, implementations and callers,
then read their source/tests or use `go doc` for a public contract. `AGENTS.md`
defines shared verification commands; no generated package or contract map is
maintained.

## Modules and boundaries

The repository has no root module; `arkoren.dev` is only a namespace. Every
path reads `<module>/vN/<package>`, so a core major version never looks like
"everything is v2".

| Tier | Path | Modules | Holds |
|---|---|---|---|
| Core | `core/` | one: `arkoren.dev/core` | Lifecycle, the HTTP path, services. No third-party dependencies. |
| Drivers | `driver/<vendor>/` | one per vendor | Vendor implementations of core contracts, and HTTP protocol adapters that have no `Driver` (`oauth` plugs into `http/auth`; `mcp/mcpserver` serves `core/mcp.Tool`; a provider webhook adapter would return a `webhook.Source` and publish `webhook.Event` definitions). |
| Arks | `ark/<name>/` | one per ark, opt in with `go get` | Domain features with their own tables, routes, or flows. None yet. |
| Foundation | `foundation/<name>/` | one per package | Standard-library-only building blocks. |
| CLI | `cmd/<name>/` | one: `arkoren.dev/cmd/arkoren` | The `arkoren` command, installed with `go install arkoren.dev/cmd/arkoren@latest` or recorded with `go get -tool arkoren.dev/cmd/arkoren@latest`. It uses the pinned official MCP SDK as a protocol client, without driver dependencies. Keep this footprint small because `go tool` dependencies land in the user's `go.mod`. Core remains SDK-free and never imports the CLI. |

Separate modules live only under `cmd/`, `driver/`, `ark/`, and `foundation/`,
never below a `core/` path, so core can move its folders in a new major.

During incremental imports, `scripts/imported-modules.sh` supplies the temporary
`imports.yml` workflow with available canonical modules; zero modules is valid.
Validate the exact committed tree, since excluded local modules can hide missing
dependencies. Keep the required `website` job in its separate workflow. Replace
the import workflow with full CI/audits only when the complete inventory lands.

`scripts/ci-modules.sh` owns the complete-source CI inventory, not a Go dependency planner. It lists
core, the CLI and direct driver/foundation/optional ark roots;
Compose markers select integration suites. Routine CI checks all modules,
workflow tooling and rendered docs. The separate scheduled/manual audit owns
toolchain vet, vulnerability scans and generator end-to-end
coverage. Keep module paths aligned with Make and run `make workflow-check`
after inventory or workflow changes.

Place new code by asking, in order:

1. Needs a vendor? Use a core contract in `core/service/` plus drivers for
   interchangeable service behavior. Keep provider-specific HTTP adapters in
   `driver/<vendor>/` when their protocol and policy are the interface, as with
   OAuth, webhooks, and CAPTCHA. A vendor alone does not justify a new contract.
2. A pure algorithm, standard library only, useful without Arkoren?
   `foundation/`.
3. Assumes the app's domain or product flow (users, teams, plans, roles;
   signup, checkout; its own routes, screens, or email wording)? An ark.
4. Needs a third-party library? Not core: a driver or an ark.
5. Otherwise core: a mechanism any app could use. It may own an
   infrastructure table (an outbox, a job table), never a domain table.

Core services that compose contracts without a `Driver` of their own (such
as a future notification dispatcher or outbox) also live in `core/service/`.
An ark cannot import `core/internal`; core makes public what arks need.

## How the pieces fit

Paths below are relative to `core/` unless they start with `cmd/`, `driver/`,
`ark/`, or `foundation/`.


**Contract packages** live in `service/<contract>/` and start from this layout; larger ones (`schedule`,
`telemetry`, `workflow`) split the main file by topic, such as
`driver.go` and `errors.go`:

| File | Holds |
|---|---|
| `doc.go` | Package comment: what the contract promises and what telemetry it emits. |
| `<contract>.go` | Sentinel errors, the `Driver` interface (or `PublisherDriver`, `ConsumerDriver`, ...) whose doc comments are the spec (every operation lives there; a vendor that cannot do one returns `ErrUnsupported`, and there are no optional capability interfaces), and the facade (`Cache`, `Storage`) built with `New(driver, configure...)`. |
| `config.go` | Private settings and role-specific construction builders. Mail and LLM retain Config solely as reporting records. Configurable native vendors use construction builders too. |
| `telemetry.go` | The facade's spans and metrics. |
| `memory<contract>/` | In-process driver: standard library and core only. |
| `<contract>test/` | `Run(t, newDriver, opts...)`, the conformance suite, and `Fake` when effects leave the process. |

The facade adds typed values and telemetry on top of the raw driver and
exposes `Driver()` and `DriverAs[T]()`, which looks through wrapping drivers
with `internal/unwrap`.

For data-result, pagination, inspection-snapshot, or iterator interface changes,
apply [Collection results](../arkoren-go/SKILL.md#collection-results). That rule
distinguishes native collection outputs from standard driver/transport inputs
and preserves ownership, schema fidelity, and execution protocols.

Cache value operations use immutable `Key[T]` definitions, not independently
typed string lookups. Start with `NewKey[T]` and an exact name. Optional
`Namespace[T]` definitions share literal prefixes and inherited write TTL for
related keys; they join names with `:` by default. Construction callbacks and
`Derive` publish detached settings; `Subnamespace` composes a child prefix.
Namespaces do not isolate storage or provide bulk operations. Drivers remain
string/byte based. HTTP consumers accept namespaces rather than literal prefix
settings. Preserve consumed-link, deduplication, counter, session-record and
lock identities unless an explicit storage migration is intended; session
records retain their custom raw-JSON codec and lifetime policy.

Secret, feature, and session also own immutable Namespace definitions that
compose ordinary Secret/Flag/Field leaves, with no driver-interface change.
Keep these in their definition packages, not common provider plumbing. Secret
requires a positional separator chosen for the provider; feature/session use
colon by default. Feature children inherit the exact fallback. Session field
namespaces prefix fields inside a record, unlike Builder.Namespace's cache
namespace for whole records. Preserve auth's fixed arkoren:auth:user entry.
Namespace joining is literal, not isolation, authorization, provider registration,
or schema enforcement. Preserve deployed cache dot joins with Separator(".")
when upgrading: changing receipt identities can admit replays; Remember locks
must keep the same final key identity. Built-in session/rate counters already
choose colon explicitly. The arkoren-go skill records callback/error rules.

Queue `NewJob`, `NewConsumer`, and `NewDispatcher` publish immutable settings
from variadic role-specific callbacks. The consumer owns queue restriction
copies; dispatcher builders expose no consumer-only settings. Retained drafts
cannot change published values. Callback policies replace the former queue
Config/Option family. Raw driver and delivery operations and conformance/drain
options remain unchanged. Configurable
memory and vendor drivers now use their own construction callbacks too.

**Driver packages** live at `driver/<vendor>/<vendor><contract>/`. Prefer
`config.go` for private settings, public builders, and their function aliases;
`driver.go` for the public driver, constructors, lifecycle, and native accessor;
and focused single-word files for substantial operation families. Keep public
declarations in their existing package. Extract cohesive private mechanisms into
package-local `internal/` packages; use vendor-parent internals only when sibling
packages actually share the implementation. Tests match their real source files,
including conformance invocations guarded by `testing.Short()`.

**Shared plumbing** lives under the narrowest common owner. Core-wide
`internal/` contains `clientop` (facade telemetry), `errtype` and `semconv`
(telemetry names), `name`
(identifier rules for jobs, entries, topics, and events), `unwrap`, and `hook`
(request lifecycle hooks shared with telemetry). Queue and broadcast share
`service/internal/messaging`; HTTP readers share `http/internal/bodysize`.
LLM-only attribute translation lives in `service/llm/internal/genai`.

**HTTP path** (`http/`): `server` serves an `http.Handler` with safe timeouts →
`router` runs handlers that return `error` and renders `problem.Details`
errors centrally → `http/middleware/*` are `func(router.Handler) router.Handler`
→ handlers use `request` and `response`; `auth` resolves the user (any
`Authenticator` checked with `http/auth/authtest.Run`), `http/auth/gate`
authorizes it, and `link` signs URLs. Pagination types (DTOs and small
helpers shared by `service/database` and `request`) live in `pagination`; `request` reads their query parameters. `http/webhook` receives provider notifications: a `Source` (credentials, authentication) and credential-free `Event` definitions come from a vendor package, library or ark, and the application's `Builder` binds handlers and owns policy (deduplication scope, event bus, limits); it has no name, `Driver` or receiver object.
`Event.Map` composes pure payload conversion with the original decoder for
application-owned queue DTOs; it preserves the provider key and leaves the
original definition reusable. Webhook receipts commit safe failures locally
(including the native 413 problem) before returning errors that preserve class
and cause. `application` owns execution, probes, and lifecycle events.
Its `Composer` alone registers `Startup`, `Cleanup`, `Defer`, `MustDefer`, `Run`, and `Check`.
All composition succeeds before startup callbacks execute; runners launch only
after startup succeeds. Compose modules with ordinary typed parameters and
return values; shared resources have one explicit cleanup owner.

Streaming responses tolerate deadline-setting errors only when they match
`http.ErrNotSupported`. Other failures return before initial commitment or the
next data write. Preserve the router's committed-response handling: a later
stream failure must not append a replacement problem body. Propagate available
flush errors. Iterator producers are joined, not abandoned; they must stop after
yield returns false and use cooperative shutdown for blocking work outside yield.

**CAPTCHA adapters** live in the existing Cloudflare and GCP modules as
`cloudflarecaptcha` and `gcpcaptcha`. They expose vendor-local verifiers and
optional route middleware, not a core `Driver`, facade, or memory implementation.
Google uses CreateAssessment and retains valid low-score assessments for
application policy. Keep token extraction explicit, verify once, and fail closed
without automatic retries. See the consumer CAPTCHA guide for integration and
security constraints.

**Payments** use one outbound `payment.Driver` for SDK preparation, hosted
purchases, explicit refunds, and existing-plan subscription controls. Author
immutable requests through `New*Request` constructors and role-specific drafts;
published getters and context-first validation replace mutable request fields.
`NewMoney(currency, minor)` validates integer units; `ParseMoney` parses exact
decimals. Observations remain records. `PreparePayment` belongs to the facade
and returns typed, protocol-labeled authorization/order/session handoffs with
only observed references. Preparation never implies authorization or capture.
Unsupported operations return `ErrUnsupported` and have independent conformance
coverage. Facade notifications report provider-call outcomes, not settlement or
durable reconciliation work. Keep SDK handoff secrets outside telemetry,
notifications and ordinary diagnostic formatting. Mollie's classic callbacks are
unsigned hints requiring authenticated resource lookup, not authenticated
`webhook.Source` deliveries. Applications own order authorization, persistence,
reconciliation, and fulfillment; these integrations introduce no billing ark or
driver-owned tables. See the consumer payments guides for supported profiles.

**HTTP documentation** belongs to the router's actual registrations. Optional
operation callbacks describe requests and responses; they do not change handler,
validation, error-mapping, or authentication behavior. Keep the public route-list
and MCP diagnostic wire shapes unchanged. OpenAPI JSON and Swagger UI handlers
are explicitly registered by the application. The Swagger UI package lives at
`core/http/swagger`, with pinned frontend assets and no third-party Go dependency.
Keep its assets and rendering out of router imports. Document schema-export
limitations instead of claiming that annotations enforce wire conformance.

**AsyncAPI documentation** lives in `core/asyncapi`: explicit immutable documents,
not runtime inventories. Keep broker behavior unchanged; this package ships no viewer.
JSONTopic/JSONJob capture actual definitions for JSON-envelope transports: base64
wire payloads plus plain logical schema components linked by an x- pointer.
Preserve the self-contained draft-07 schema gate (references, identifiers and dependencies
are rejected) and once-cached JSON/Handler export, including errors. See
`cmd/arkoren/skills/arkoren/references/guides/asyncapi/overview.md` for application wiring.

**MCP tools** live in `core/mcp`, not `service/`: immutable SDK-independent
typed definitions, with `driver/mcp/mcpserver` as the HTTP protocol adapter.
Services may expose their own `[]mcp.Tool` methods; composition registers tools
explicitly. Keep SDK dependencies out of core and do not add a contract-map row.
Applications own `ToolBuilder.Available` capability policy; shared typed service
handlers own resource Gate checks for all transports. Policies filter complete,
unpaginated discovery and guard calls before decode; failures fail closed even
when wrapping `ToolError`. Keep SDK imports in adapter/composition and authenticate
outer HTTP with `auth.Middleware(auth.Bearer(verifyAccessToken))` followed by
`auth.Require[User]()` before `routes.Mount("/mcp", endpoint)`; use the same
concrete application user type in the verifier and policy lookups. Optional
`gate.Require(canUseMCP)` controls endpoint admission, not resource access.
For an ark client without app permissions, wrap its typed operation
in an app-owned authorized handler, then build the tool; see the shipped guide
for optional hypothetical ark factories. OAuth metadata/challenges need separate setup, not a driver
dependency. Native SDK additions are unmanaged and remain visible in native
discovery. MCP `tools/list` is the only catalog. See the shipped MCP guide for
transport limits.

**Async packages** (`queue`, `broadcast`, `schedule`, `workflow`,
`event`) share names, verbs, and sentinels; see the `arkoren-go` skill. `llm`
agent and tool names use the same rules, and `llm.Permanent` follows the same
meaning. `llm` has no memory driver: `llmtest.Fake` is its in-process driver, and
its span and metric names are `gen_ai.*` constants in `core/internal/semconv`.

Workflow clients and workers configure telemetry independently through
ClientBuilder and NewRegistry/RegistryBuilder. Instrument only client calls and
actual step-handler attempts, never replayed orchestration. StartOptions.Trace
is separate serialized propagation data; each workflow driver copies and
forwards it to supported child/spawn and native attempt boundaries. Temporal
traced envelopes require workers-first rollout. Local action events do not
report authoritative engine completion.

**Outbound messages** (`mail`, `push`) are immutable values built with positional
mandatory inputs and optional MessageBuilder callbacks. Mail keeps recipients
and text/HTML alternatives positional; From can come from facade defaults.
Push keeps recipient and visible title positional, with retention in SendOption.
Its driver receives completed SendConfig settings and submits one destination;
subscription storage, browser display, fan-out and retry policy belong to the
application. Standard Web Push lives in `driver/webpush/webpushpush`; native
mobile drivers are separate future integrations. Like LLM, push uses its
recording `pushtest.Fake` rather than a memory delivery engine. Optional facade
notifications report submission acceptance/failure, never device delivery.

**Diagnostics** are native `core/mcp.Tool` definitions. Owners expose
`MCPTools(prefix) []mcp.Tool` (`router`, `schedule`, `memorytelemetry`);
`Composer.MCPTools(prefix, ...MCPToolsBuilderFunc) ([]mcp.Tool, error)`
attaches capture before startup. `MCPToolsBuilder.Limits(application.CaptureLimits)`
sizes new capture. Composer requires a nonempty prefix and uses it directly for
every app and telemetry tool: `MCPTools("app")` yields `app.info`, `app.logs`,
and `app.traces`, all in CLI group `app`. The consumer explicitly selects tools, adds capability policy
to copies with `Tool.WithAvailability`, and registers them through the MCP adapter.
Resource Gate checks stay in shared typed application methods. There is no
parallel inspection protocol, manifest, extension registry, or SSE endpoint.
CLI presentation belongs to ToolInfo.CLI and namespaced MCP metadata, independent
of protocol tool names. Cursor follow uses repeated bounded tool calls.
Safe structured failures belong to result metadata, never successful output.

The app owns started/healthy/ready condition tools and their server-side
wait/timeout inputs. Keep their outputs distinct and share readiness evaluation with
the probes. Reads observe cached health without running checks; health with fresh
remains the explicit diagnostic operation. Waits are bounded and cancelable, with
the last typed output in safe failure details on timeout or terminal state.
Healthy/ready alone accept fail_fast with wait=true: fresh failed background passes
(including cached ones) return safe tool failures with result and check details; pending/stale
health keeps waiting. Default waits tolerate failures. Started has no fail_fast input.

Application/event/memorytelemetry also publish copied roots from construction callbacks;
telemetrytest.New forwards memorytelemetry.Builder callbacks once. Follow the
[application role](../arkoren-go/SKILL.md#application-role) for application values
and construction callbacks. Dependencies, loggers and closure
captures remain shared. HTTP construction also uses dedicated builders, including
telemetry's server middleware and outgoing transport. Client retry construction
composes completed propagation settings through one internal telemetry callback,
never replaying user policies. Session middleware owns cookie/persistence policy;
auth.NewSessionIdentity owns the loader/ID binding and SessionBuilder.Handle;
request lookup owns per-call WithSessionHandle. Share one session.NewHandle()
across those roles; zero selects the default context slot. Handles do not isolate
cookies, cache records, or authorization. OAuth Callback(identity, mapper) and
Logout(identity) resolve the selected session once before protocol effects, with
no default fallback. Session data uses
shared immutable `Field[T]` definitions; persisted JSON retains its existing
names and format. Session middleware, not fields or cache namespace TTL, owns
renewal and absolute lifetime. SessionIdentity binds login and loader types, not
separate stored identities: preserve arkoren:auth:user and its raw JSON. Validate
identity configuration before credentials; mutation success is not durable save.
Broadcast, cache,
crypto, database/migrator, feature, mail and storage facades and configurable
memorybroadcast/cache/queue/secret/storage and slogmail use direct construction
policies too. PublisherBuilder omits Events; consumer alone exposes it. Crypto
Previous appends copied interfaces, mail ReplyTo replaces copied addresses,
and memorysecret Values copies both map and byte slices. Mailer.Config remains
a detached reporting record, not a constructor input. LLM ModelBuilder and
EmbedderBuilder separate generation from embedding settings and preserve both
Config reporting getters. Search Builder.Indexes appends copied registrations;
schedule Builder sets Logger/Telemetry/Events, workflow ClientBuilder sets
Codec/Events, and memoryschedule/memoryworkflow Builder sets Logger. Required
workflow registry position stays unchanged. These constructors use direct ordered
once-only callbacks and detached publication too.

Configurable native vendors use the same direct, ordered once-only construction
rule on fresh private drafts at the constructor's documented validation boundary.
Required nil host/registry guards retain their original timing. Shared hosts use
Builder; workflow components use DriverBuilder/RunnerBuilder; AWS SQS broadcast
uses SQSBuilder; OAuth uses ClientBuilder/GoogleBuilder. Publish completed
settings through private composition, never replay user callbacks. Copy owned
maps/slices at input and publication; keep live clients/providers/hooks shared.
Preserve SDK credential-loading contexts and environment chains, request-time
OpenAI hooks, Start-time LaunchDarkly hooks and New-only OpenSearch hooks.
OpenSearch defers owned native construction and SDK validation to Start; its
Client accessor is nil before creation and after Stop. NewFrom keeps immediate
borrowed access and never invokes the configuration hook.
Existing redacted Config records stay reporting-only. Native SDK records and
per-call/runtime/suite options remain unchanged. Settings-free Azure, filesystem,
Resend, SNS and native conversions have no empty builders. NewFrom ownership and
applicable settings are package-specific: SQL pools and Redis cache conversions
are closed by Stop; borrowed S3 and shared-host clients remain caller-owned.
Temporal NewFrom honors NoWorker and WorkerStopTimeout too; a nonzero native
worker timeout takes precedence.

OpenAI NewFrom ignores connection settings but validates them as New does;
API/StrictSchemas/EmbedBatchSize and request hooks apply. OpenFeature and
LaunchDarkly NewFrom have no callbacks. OpenFeature ambient outer maps and OAuth
scope/audience slices copy at input and publication, not arbitrary nested values.
Explicit StrictSchemas(false), MaxRetries(0) and PKCE(false) keep their meanings;
PKCE starts enabled. GoogleBuilder.HostedDomain checks the verified hd claim,
not only the chooser hint. Other OAuth presets have no optional builders; OAuth
Config remains redacted diagnostics only. Only Temporal DriverBuilder exposes
Registry; RunnerBuilder has none and NewRunner's positional registry supplies
both runner and driver. SDK option records retain their native shallow ownership.

Migration definitions, workflows/steps, and LLM agents also publish independent
settings from declaration callbacks. Required execution functions remain stored,
not invoked as construction policies. Workflow registration snapshots handlers
and still requires MaxDuration; Step.Derive provides local policy variants without
reapplying defaults. Search indexes and queries publish from nongeneric
IndexBuilder/QueryBuilder callbacks; groups share a restricted PredicateBuilder
with no ordering or OrWhereGroup. Query.Derive preserves inherited errors and
detaches nested conditions/value lists. Index.Err validates the completed
schema/key rather than storing an incomplete draft's missing-field error.
Schedule entries publish from EntryBuilder callbacks, with required When checked
after callbacks; nil execution functions remain domain errors. Entry settings
copy immutable Spec, not execution functions or captures. All these callbacks
run directly once, synchronously in order on one fresh draft; nil entries panic,
expanded nil/empty slices omit edits, and retained drafts cannot change published
definitions. No migration/workflow/agent/index/entry Derive is added. Engine
and scheduler construction use their component Builder callbacks. Spec,
scalar/composite/object-wide schemas, per-call options, SDK hooks, and
conformance-suite options remain separate concerns.
See the arkoren-go skill for each declaration family's validation and ownership.

**Application keys** live in the stdlib-only `foundation/key` module. Immutable
Shared (exactly 32 symmetric bytes), Signing (ECDSA P-256), and Exchange (X25519)
values centralize generation, strict parsing,
explicit file loading, redacted formatting, and detached exports. Encryption,
link/cursor signing (including previous secrets), Web Push, and Apple Sign In
require typed inputs; changing these boundaries updates all callers and examples.
The offline `arkoren key generate|check|public` command uses the same package,
creates private files exclusively, and bypasses MCP settings. Provider credentials
remain provider-issued (including Apple's signing key). CLI selectors are only
`shared`, `signing`, and `exchange`; generation requires explicit `--out`.
Asymmetric public output defaults to SPKI PEM; `--format vapid` is signing-only.
Exchange supplies native keys to existing protocols: callers authenticate peers
and own KDF domain separation; raw ECDH output is not an encryption key. Keep
signing and exchange identities separate. There is no key-derivation API. Provider tokens
and TLS certificate management are separate concerns. See the consumer
Application Keys and Key Commands guides before changing formats or provisioning.

**Certificates** live in the independent stdlib-only `foundation/certificate`
module. Immutable Identity values contain certificate chains and matching private
keys; Bundle values contain public certificates. Local root/server generation is
separate from loading existing server or mTLS client identities. Native exports
detach owned material. Parsing does not establish time, hostname, purpose, or trust
validity. Keep OS trust operations in the offline CLI, not the library or server.
`server.Builder.TLS(*tls.Config)` opts into HTTPS through the existing lifecycle;
`client.NewTransport(*tls.Config)` clones the native default with retry/telemetry
and standard client idle-pool cleanup; `client.Transport(base)` wraps custom
transports. `Bundle.Pool` is private-only; `Bundle.SystemPool` adds system roots
without changing OS trust and preserves system-loading errors. Core needs no
certificate-module dependency. CLI trust/untrust targets machine-wide stores and
matches exact certificates; tests use isolated backends, never the host trust
store. Before changing these paths, read the consumer TLS Certificates and
Certificate Commands guides for supported formats, platforms, and recovery.

**Foundation modules** (`foundation/certificate`, `collection`, `key`, `problem`, `query`,
`validate`) use the standard library only and know nothing of `arkoren.dev`.
Query publishes immutable `Query` values from mutable root and restricted
predicate callbacks; only published statements compile or execute. Keep concrete
Query checks in database row-return guards, telemetry, and pagination in sync.
Pagination derives local window settings, while Count and Seek remain immutable
transformations. Scopes retain independent protected AND boundaries; ordinary
groups share one restricted draft and reject a final empty expression. Raw source
statements retain positional/named binding through the existing dialect compiler.
SQL schema declarations in `foundation/query/schema` publish detached table,
action, and child snapshots before lazy rendering. Their variadic callbacks
share one draft; name validation precedes invocation and whole-declaration
validation follows it. Keep this publication boundary when changing renderers;
arbitrary default values are not recursively cloned.

Validation schemas in `foundation/validate` publish immutable definitions from
variadic typed builder callbacks. `StructBuilder[T]` registers direct typed child
callbacks with its generic `Field` method; composite drafts use `Keys`, `Values`,
and pointer `Value`. Constructor types are inferred from typed callbacks.
Registrations use Go names; duplicates are errors, not replacements. Typed `Use`
attaches reusable published schemas. Follow the callback, publication, and
condition rules in `arkoren-go`; consumers continue to accept `Schema[T]`.
One `ConditionBuilder` composes cross-field and collection observations. Bind
nested Go field paths at construction and keep pure predicate evaluation
separate from violation reporting. Collection assertions never transform input.
The module remains standard-library-only; reuse collection naming and callback
conventions without importing `foundation/collection`. Preserve JSON error
paths, input ownership, and deterministic diagnostics. This API does not alter
MCP, LLM, query, or other construction APIs.
Unprepared execution prepares each value once before entering prepared execution;
prepared execution never repeats normalization or child preparation. Fatal state
stops execution before another unprepared value is mutated. Preserve the existing
default, presence, rule, and child-check ordering when changing these paths.

HTTP problem sentinels expose immutable standard-member accessors, not public
wire fields. Construct with problem.New and derive occurrences with Details.Derive;
keep literal toolkit Type/Title/Status settings directly in sentinel callbacks for
review of stable identity and shared presentation. JSON v2 delegates to one
private tagged representation; HTTP rendering and mutable ResponseContext remain
runtime concerns. MCP metadata records retain their public fields.

**The CLI** (`cmd/arkoren`, `package main`) combines offline project creation and skill management with
a native MCP protocol client. It initializes a connection, reads `tools/list`,
and invokes exact advertised names with `tools/call`. Optional
`_meta["arkoren.dev/cli"]` supplies `group:operation` commands, roles, positional
arguments, examples, and cursor support. Tools without this metadata remain
callable by exact name. Do not infer diagnostic roles from names or hardcode
owner operations. Local command names win only unqualified. Schema-guided JSON
input remains server-validated. Snake_case properties accept kebab-case flag
aliases; exact properties win and map keys remain literal. Follow uses bounded
cursor calls, not a second transport. Never replay a mutation automatically.
Keep output failures, safe structured failures, timeout budgets, and cancellation
explicit in the CLI and its end-to-end MCP tests.

The root package owns process entry, static command composition, and embedded
assets. Private mechanisms live in `internal/remote` (discovery, exact wire JSON,
input, wait/follow, and connection ownership), `internal/console` (rendering and
output failures), `internal/files` (confined traversal and safe creation),
`internal/trust` (native trust backends), `internal/skill` (managed bundles and
manifests), and `internal/scaffold` (project rendering and creation).
Scaffolding consumes skill and file mechanisms, not the reverse. Keep embed
anchors at the module root and inject `fs.FS`; the authored skills and template
trees retain their existing locations. Move platform implementations together
with their selection policy and preserve equivalent build constraints.

Exact generic JSON is captured at the SDK's public Connection boundary and
restored only after SDK success. Capture correlates request IDs, but retains one
result per method: preserve sequential `tools/list` and `tools/call` requests.
Before adding concurrent calls of either method, make capture and restoration
per-request rather than per-method. Capture does not bypass SDK decoding errors
or authorize replay after a failed call.

Static commands use `github.com/urfave/cli/v3` in the CLI module only. Build a
fresh command tree per invocation with injected streams and context; Arkoren
owns error reporting and process exit. Keep reserved-global extraction before
dispatch and pass remote arguments directly to the schema-aware MCP parser.
Do not generate a parallel command tree from discovered tools. Resolve endpoint
settings lazily so offline commands and their help never read configuration.
Use urfave's generated static help, including nested command help; keep safety
guidance in command descriptions rather than duplicate handwritten help pages.
Terminal help decorates urfave's default section labels with bold using
per-command templates; keep shared framework templates unchanged and honor
`--no-color` and nonempty `NO_COLOR`. Nonterminal help remains plain.
Bare invocation and `--help` show offline root help with exit 0. `--version`
uses urfave's local version output without configuration or discovery; endpoint
and negotiated protocol information belongs to `status`, not a version command.
Help attaches to the selected command with `-h`/`--help`; there is no explicit
`help` command. Remote discovery uses `tools`, with `--json` for the native
catalog and CLI rules. A remote command's help requires discovery; local help
stays offline.
Remote names are exact MCP names or metadata-derived `group:operation` names,
without built-in diagnostic command aliases. Roles still drive diagnostic
semantics, not command-name shortcuts.

`new` and `new --help` run offline before MCP endpoint settings. Creation requires exactly one
positional destination (`arkoren new billing` or `arkoren new .`), with optional flags before or after it: module
(absolute destination basename by default, invalid names fail without normalization),
skills-dir (project-relative, default `.agents/skills`), no-agent-files,
dry-run, and yes flags. Bare `new` reports usage with exit 2 without writing; `--folder` is
removed. Help needs no destination. It writes application files: a cmd executable
(main.go for infallible application.New construction/identity, signals, logging, and exit status),
internal/bootstrap (bootstrap.go for composition/lifecycle, app.go for application
settings, logging.go for log handlers, router.go for routes, server.go for server
settings and lifecycle, mcp.go for debug diagnostics), go.mod, README, Makefile,
Dockerfile, .dockerignore, .gitignore, .editorconfig, .env and .env.example.
No application tests are generated. Bootstrap factories Router, MCP, and Server
return Factory[T] callbacks with real outputs. The direct void Compose root
obtains its shared router with compose.Use(Router()).
By default it also writes user-owned AGENTS.md and a complete managed skill under
`<skills-dir>/arkoren`, including its manifest. `--no-agent-files` skips both;
a custom skills-dir and opt-out conflict. AGENTS.md describes consumer conventions
and direct composition, not toolkit-contributor rules. Existing instruction and skill
paths participate in the same no-overwrite preflight as application files.
Templates mirror the output tree under cmd/arkoren/templates, with .tmpl suffixes and fixed cmd/server for
the command directory. Rendering discovers files recursively in lexical order; add templates at their
output-relative paths rather than maintaining a separate file list. bootstrap.Server reads HTTP_ADDR inside its builder
callback; empty/unset preserves :8080. The executable blank-imports
godotenv/autoload: initialization loads .env, preserves existing environment values,
and ignores loading errors. bootstrap.Application enables debug only for APP_DEBUG=true.
Router(), MCP(routes), and Server(handler) only capture explicit dependencies.
Their returned callbacks create the router with probes and app.WelcomeHandler(), construct and
mount diagnostics, and construct the listener with Startup/Cleanup/Run bindings.
Each returns its actual constructed handle through typed Use. Server owns its
Started subscription through MustDefer and unsubscribe cleanup; an inline
callback logs Addr as address after `app started` and runner launch. Compose
ignores the server result. Keep logging separate from Serve and readiness
transitions; do not format browser URLs or infer schemes, hosts, or public domains.
Compose selects MCP through UseDebug; skipped output is explicitly absent as (zero, false).
Constructors, environment reads, and route mutations belong inside callbacks;
obtaining a factory must not acquire a resource or expose an endpoint.
The welcome handler serves embedded, self-contained HTML with documentation links
at the explicitly registered root route. Full application InfoHandler remains
opt-in and unmounted; applications own access controls for its operational data.
The unauthenticated endpoint selects read-only application, route, and telemetry
tools. Its factory registers `compose.Must(compose.MCPTools("app"))` and
`routes.MCPTools("http")` with separate appending `listener.Tools` calls,
without a redundant read-only filter. Composer.MCPTools attaches bounded
in-memory capture with no external export. Router registers the application's telemetry
middleware before routes with the default TrustNone; without debug capture or a configured
driver it is a no-op. MCP shares the same telemetry handle, main listener and
inherited middleware, with no separate server or lifecycle bindings.
The starter registers secureheaders, telemetry, then same-origin csrf middleware
before routes. Package defaults omit HSTS pending deployment-owned HTTPS policy,
grants no CORS permissions or extra trusted origins, and preserves the welcome
handler's stronger CSP. Safe methods must remain free of state changes; scope
exceptional cross-origin routes explicitly. CSRF is not authentication and
allows requests without browser-origin headers. These defaults do not add host
validation, proxy trust, cookie policy, or protection for exposed debug tools.
HTTP_ADDR controls diagnostics exposure, so documentation requires debug off in
production and loopback binding for development.
The generated .env selects loopback and debug, is exclusive-created with mode 0600,
and is gitignored. The example disables debug. Neither needs secrets; no unused keys
or tokens are generated. ARKOREN_MCP_URL points the CLI at /mcp on the HTTP listener.
The starter uses direct constructor calls only: no DI flag, providers, or container
dependency. app.Run(ctx, bootstrap.Compose) composes and runs the supplied app; MCP
construction errors and real lifecycle failures propagate. The application reports
failures before telemetry finalization; pre-start and finalization errors reach
the configured handler only. The executable's run helper returns app.Run directly,
using application.NewSignalContext(context.Background()) and deferring its stop
function before main maps failure to exit 1 (0 on clean shutdown). Core owns
SIGINT/SIGTERM registration through os/signal.NotifyContext; terminal echo is
unchanged and generated code owns no signal worker.
The generated app owns the lifecycle. Missing/empty destinations proceed;
nonempty destinations including `.git` require default-no confirmation or explicit `--yes` noninteractively.
Generated-path collisions always fail. Dry-run lists choices, files, and conflicts without prompting, writing,
or network access. Generated application files and AGENTS.md are user-owned: no application generator state, driver/feature generation,
regeneration, dependency downloads, or Git initialization. The generated README installs dependencies
with `go get arkoren.dev/core arkoren.dev/driver/mcp`, then runs `go mod tidy`.
See `cmd/arkoren/skills/arkoren/references/guides/first-steps/creating-a-project.md` for the user workflow.

`skills install|update|status` and `skills --help` also bypass
endpoint settings. `--dir` names the skills parent (default `.agents/skills`)
relative to the current directory, with no parent search. `--dry-run` is read-only.
The CLI directly embeds the sole authored consumer tree at
`cmd/arkoren/skills/arkoren/`, so go install works independently. `make generate`
updates only that skill's guide index; it does not generate an archive or API pages.
Never bundle contributor skills or fetch GitHub/main/latest automatically.
The manifest `.arkoren-skill.json` records schema, name, content revision, and
managed path hashes. Revisions identify content, not Go-module compatibility.
Install requires an empty directory or matching unchanged installation; update
requires valid ownership. Local edits/deletions, unsafe links, and new-path
collisions block all writes. Preserve unrelated files; remove only unchanged
obsolete managed files. Recheck hashes before writes, replace files atomically,
and commit the manifest last. The whole update is not transactional; errors name
partial work for manual recovery. No force-overwrite or AGENTS.md mutation.
Run only one installer at a time. Updating the CLI supplies new documentation;
there is no independent remote skill release/lookup path.

`run` takes an `environment` (streams, env lookup, working
directory, terminal flag) so the end-to-end tests drive it against a real
MCP endpoint over httptest. CLI bearer precedence is `-token`, then environment
`ARKOREN_MCP_TOKEN`, then the same key in `.env`. Supply an existing bearer API
key, JWT, or OAuth access token;
token acquisition/refresh, cookies, and mutual TLS are not automatic client features.
The retired inspection-token variable has no compatibility alias.
All stdout paths share a first-error boundary: short writes become
`io.ErrShortWrite`, and output failures, including EPIPE, take precedence over
command status and exit with code 1. The executable ignores SIGPIPE so broken
pipes reach this policy. Final stderr diagnostics are best effort; output failure
is never successful follow cancellation.

## Terms

Use these words in docs, errors, and APIs.

- **Arkoren**: a modular Go toolkit: libraries the application imports and
  calls, built on standard types (`http.Handler`, `*slog.Logger`,
  `context.Context`), each module usable alone, with drivers the application
  chooses. Never "framework": no dependency injection, and nothing calls
  application code except functions it registers. Name its own code
  "core", "Arkoren packages", or the package itself.
- **Transaction**: the outermost unit of work on a database, committed or
  rolled back as a whole; code joins it by running with a context inside it.
  Not "tx context" or "unit of work".
- **Nested transaction**: a transaction started inside another on the same
  database; it rolls back only its own work, which is final only when the
  outermost commits. Savepoints are the mechanism, not the term.
- **After-commit hook**: a best-effort callback run once the outermost
  transaction commits, lost if the process crashes first. Not "on-commit
  callback".
- **Migration group**: an ordered sequence of migrations shipped by one
  package, ordered and checked apart from the application's own. Not
  "namespace".

## Decisions

Settled; reopen only with the owner.

- **Transactions travel in the context.** `WithTransaction(ctx, fn)` hands
  `fn` a context carrying the transaction, and every `*Database` call joins
  the one its context carries; there is no `tx` handle. Service layers that
  only take `ctx` compose into one unit of work, and other contracts (an
  outbox, a database queue) can enlist without knowing the driver. Nested
  calls are savepoints, so an inner failure undoes only inner work on every
  database. Consequences: create one `*Database` per driver; a goroutine
  outliving the transaction gets `sql.ErrTxDone`; no transaction options
  (isolation, read-only) until needed.
- **Sessions pin execution, not commits.** `WithSession(ctx, fn)` carries one
  retained physical connection through the context. All facade SQL, locking,
  and transaction creation join its ordered session/transaction scope chain;
  completed matching scopes fail instead of falling back to a root pool.
  Sessions never reconnect or replay work. Nested sessions borrow the connection
  but expire their own handles and release their new nontransactional locks.
  Transaction acquisitions belong to the outermost transaction and survive
  nested rollback/panic; preexisting session locks remain session-owned. Locks
  are non-reentrant by native key. Close cursors and release locks before pool
  return; discard uncertain sessions. Confirmed commit followed by cleanup
  failure wraps `ErrAfterCommit`, so the facade still emits committed and runs
  hooks. A callback-origin marker still requests rollback. Root/native access
  bypasses scopes. Wrappers preserve wrapping in both callback methods.
- **The dialect is a runtime value.** Every `database.Driver` reports
  `Dialect()` from a closed set (ANSI, PostgreSQL, MySQL, SQLite); there is no
  `Database[D]` type parameter and no ANSI fallback. Portable code compiles for
  whatever the driver reports, so the same app runs on SQLite in tests and
  PostgreSQL in production. Dialect-specific code checks at runtime
  (`migrate.MigrationBuilder.Only`, `db.Dialect()` in a constructor).

- **Packages ship migrations; they never run DDL.** Drivers never run SQL
  DDL. A driver or package that needs tables exposes
  `Migrations() []migrate.Definition`, and the application adds it to its
  migrator.
  Define each item with `migrate.NewMigration` and a callback whose parameter is
  `migration *migrate.MigrationBuilder`; put Up/Down/Only settings in that callback.
  Published definitions keep their
  getters and grouping behavior, not fluent configuration setters. Construction
  copies owned step/dialect slices without invoking migration functions; the
  application's migrator remains the only DDL execution owner. Migrator and
  driver construction use their role-specific callbacks; rollback options remain.
  - Instances: optional `Builder.Instance(name)`; default `""`, otherwise
    `[a-z0-9_]`, 1-40 bytes.
  - Group: `arkoren.dev:<module>:<package>`, plus `:<instance>` when set
     (driver migration tests check the shipped group identities and behavior). Two instances without
    `Builder.Instance` collide at `Add` with `ErrDuplicateID`.
  - Ids: short, stable step names (`documents-create-table`). `migrate`
    prefixes them with the group; never write `group/` by hand (a slash in a
    grouped id is `ErrInvalidMigration`).
  - Tables: `<table>` for the default instance, `<instance>_<table>`
    otherwise. Table names are not configurable. Renaming an instance is
    unsupported.
  - Each shipped migration touches one table and creates it strictly, without
    `IF NOT EXISTS`, so a leftover table fails loudly.
  - The constructor's readiness check (`Ping` or `Start`) verifies the tables
    exist, read-only, and fails with the contract's `ErrNotFound`, naming
    `Migrations()`. It never creates them.

## Publishing

Consumer documentation uses canonical released-style module installation commands
without checkout prerequisites or prerelease fences. This policy applies even
while the release-engineering state below is unreleased; keep contributor build
instructions separate from consumer installation.

Keep the committed root `go.work` for shared development. Its relative `use`
paths name only imported modules and expand as modules land. Module manifests
declare real dependency versions without local `replace` directives; verify
each module with `GOWORK=off`, independently of the workspace. For future core
imports, resolve canonical foundation pseudo-versions from the merged foundation
import commit instead of inventing versions. Source imports create no release
tags. The ignored `go.local.work` may retain excluded local modules; select it
per command with `GOWORK="$PWD/go.local.work"` from the repository root, or use
its absolute path elsewhere. Keep `go.local.work.sum` ignored.
The website must answer `?go-get=1` on every path below `arkoren.dev` (a
catch-all route) with
`<meta name="go-import" content="arkoren.dev git https://github.com/arkoren/arkoren">`;
Go maps `arkoren.dev/core` to the `core/` folder by path. Each module is
tagged with its folder as prefix (`core/v1.2.0`, `driver/aws/v1.0.0`) and
versioned independently; a contract change releases as one set, with every
module tagged on the same merge commit. If the repository is ever split,
repository names follow the first path segment (`arkoren.dev/<repo>` is
`github.com/arkoren/<repo>`). The consumer skill tree, `website/`, and `.agents/`
hold no Go code.

## Make a change

Each row lists what must change together. Finish with `make check MODULE=...`
for each affected module. After guide changes also run
`make generate` and `make docs`. API declarations live in Go doc comments and
on pkg.go.dev; the website's API catalog links modules rather than copying them.

| Change | Also touch |
|---|---|
| Fix a bug in one package | A test reproducing it, plus affected project-skill guides, API notes, and contributor instructions when documented behavior or safety constraints change. For driver/contract behavior changes, recheck the service's compatibility matrix under the developer-documentation matrix rules. |
| Change a contract's interface or documented behavior | The `Driver` doc comments, `<contract>test` suite, memory implementation, every vendor implementation found through grep/Go tooling, relevant guides and the service drivers page's compatibility matrix. |
| Add a driver to an existing vendor module | Copy a sibling's layout; package doc comments and examples, README discoverability, Choosing a Driver, and the provider section and compatibility-matrix row on the contract's drivers guide. |
| Add a vendor-local HTTP adapter without a service contract | Package doc comments/examples, the feature guide and Choosing a Driver's protocol-adapter catalog, and README discoverability. Update consumer safety rules; no memory driver is required. |
| Add a vendor module | As above, plus `driver/<vendor>/go.mod` requiring a real core version, a relative `use` line in `go.work` for shared development, and `driver/<vendor>/compose.yaml` when tests need services (fixed `127.0.0.1` ports not used by another module). Verify with `GOWORK=off`; never require another driver module or add local replaces. |
| Add a core package or middleware | Follow the boundary rules above; include `doc.go`, public examples and relevant consumer guides. Middleware takes `Middleware(required..., configure...)`. |
| Change a public API | Update Go doc comments/examples, all callers, relevant consumer guides and affected contributor rules. Keep matching public `BuilderFunc` aliases consistent with drafts and callback signatures. |
| Add an ark | `ark/<name>/go.mod` requiring a real core version, a relative `use` line in `go.work` for shared development, package docs/examples and relevant guides. Verify with `GOWORK=off`; use no local replaces. It may not import `core/internal` or another ark. Update the Arks guide's availability statement and the website module catalog when the first ark ships. |
| Add a driver or package that needs tables | Ship `Migrations()` with stable groups and short ids; validate `Builder.Instance`; use read-only readiness checks returning `ErrNotFound` and naming `Migrations()`. Tests must execute migrations before use and exercise two instances. Document application-owned migration execution in package docs and driver guides. Preserve existing group identities and test actual behavior rather than just source spelling. |
| Add diagnostic MCP tools to a package | `MCPTools(prefix)` on the owner, direct tool/adapter tests, Go doc examples and diagnostic guides. Composition selects and authorizes tools. |
| Change MCP CLI metadata or safe failures | The `cmd/arkoren` decoding and renderers, its end-to-end MCP tests, and the `guides/cli/` pages. |
| Change a foundation module | The module's README, Go doc comments/examples and relevant guides; for `validate`, run `GOWORK=off go generate ./...` in it (a test fails on drift). |
| Add, remove, or rename a module | Update relative `use` paths in `go.work`, real dependency requirements and the website module API catalog. Keep paths canonical for `scripts/ci-modules.sh`; run its fixtures through `make workflow-check` and inspect both emitted inventories. |

Refresh the only generated consumer documentation with:

```sh
make generate
make docs
```

Generation uses the website's Node dependencies and shared navigation tree.
The website build checks index freshness without writing. Review public builder
aliases, stable problem identities and dependency boundaries explicitly; there
is no repository-wide AST policy suite. Module compilation and executable
examples verify indirect callers, method values, generic inference and behavior.
