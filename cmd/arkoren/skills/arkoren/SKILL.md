---
name: arkoren
description: >
  Arkoren (arkoren.dev) usage for Go 1.27 services: application lifecycle, router and
  server, middleware, contracts with swappable drivers (cache, database, queue,
   schedule, workflow, broadcast, mail, push, secret, storage, feature flags, search,
   payment, crypto, hash, telemetry, llm), and foundation packages (certificate, key, query, validate, problem, collection).
   Use when code imports arkoren.dev, when creating, wiring, or reviewing an Arkoren service, when choosing a driver, serving MCP tools, or using the Arkoren CLI.
license: MIT
metadata:
  go: "1.27"
  module: arkoren.dev
---

# Arkoren

Arkoren is a modular Go toolkit: a standard-library core, portable foundation packages, and contracts with swappable drivers. Callbacks are explicit, never discovered or injected.

## Where to look

1. Wiring and cross-package patterns: a **guide** in `references/guides/`. For existing projects, read [Existing Applications](references/guides/first-steps/installation.md#existing-applications); preserve their layout, module boundaries, and dependency ownership.
2. Exact APIs: run `go doc arkoren.dev/<path>.<Symbol>` from the application's module directory. Go selects the source using that project's module graph; inspect its `go.mod` and active workspace when the selected version is uncertain. Read the declaration and its documentation before writing code.
3. Online reference: use the website's module-level pkg.go.dev catalog to find a module, then select the package and version you need. Guides explain integration and limits; neither the skill nor the website stores API signature snapshots.
4. Driver selection: [Choosing a driver](references/guides/drivers/choosing-a-driver.md), then its service matrix/provider setup. Qualifications and limits describe Arkoren's behavior, not vendor-wide coordination or durability guarantees.
5. Before `arkoren new`, read [Creating a Project](references/guides/first-steps/creating-a-project.md) for destination syntax, flags, confirmation, and dependency setup. Preview with `--dry-run`; application files and AGENTS.md remain user-owned.
6. Before `arkoren skills install|update|status`, read [Working with Agents](references/guides/agent-skills/working-with-agents.md): the bundle works offline; local edits block updates and AGENTS.md remains user-owned.
7. Application composition, lifecycle, errors, and health probes: start with [Application Overview](references/guides/application/overview.md), then follow its task links.

## Rules

Examples teach one operation with small, focused code tailored to the reader's need, not a fixed line count. Explain its purpose before the API and keep related setup, constraints, and results together. Reserve full apps for requested quickstarts/integration lessons. Fragments retain error results, omitting routine checks; keep security, recovery, ownership, test, and complete-function checks. See the [quickstart](references/guides/first-steps/quickstart.md).

Put function bodies, including callbacks, on separate opening/closing lines. Omit inferable type arguments. Keep fluent chains inline unless readability requires wrapping; multiline callbacks need no break before method calls. Mutable builders use one action per line.

Import package `application`; name `Application` instances and `Builder` drafts `app`. Use single-word role nouns without hiding imports: `details`, `listener`, `connection`, `policy`; `store`/`files` for cache/storage together. Avoid initials, composites and generic `builder`; keep receivers consistent. Alias imports only for SDK/same-name conflicts, never locals. Exact package stutter becomes `Builder`; distinct roles such as `workflow.ClientBuilder` remain.

Use callback aliases (`server.BuilderFunc`) in parameters/slices/returns; literals name drafts. For Go 1.27.1 recursive-alias importer cycles, spell identical function types at affected generic methods; retain aliases.

1. Pass extracted `func(*application.Composer)` callbacks to `app.Start` or `app.Run`. Register initialization with `compose.Startup`, context-aware/fallible release with `compose.Cleanup`, infallible `func()` release with `compose.Defer`, supervised runners with `compose.Run`, and probes with `compose.Check`. `compose.MustDefer` handles acquisitions returning `(func(), error)`. All releases share the same cleanup stack, not Go function-return timing. Composition succeeds before any startup callback runs. Compose modules with typed arguments and return values; the acquiring owner registers cleanup once. `Must`/`MustDefer`/`Require` use private scoped unwinding only during synchronous composition; startup callbacks return errors directly and may register acquired cleanup through Cleanup or Defer. Drivers never import `application`. Check provider ownership and [Lifecycle and Ownership](references/guides/application/lifecycle-and-ownership.md).
   Bind needed app handles (`events`, `logger`, `telemetry`) at the top of each composition callback, followed by a blank line before wiring. Use direct handle locals rather than only aliasing the whole app; retain `app` for its own operations and avoid shadowing imported package names.
   Child factories return `application.Factory[T]` for an ordinary typed output or `application.Action` for a genuine void callback. Both are defined function types with private execution adapters, not callback aliases or a registry. `compose.Use(operation)` invokes one synchronously; no variadic batch. Inline callbacks need explicit Action/Factory conversion. Root ComposerFunc callbacks passed to app.Start/Run remain plain void functions.
   `UseDebug`, `UseIn`, `UseInAny`, and `UseWhen` accept either kind and return `(value, executed)`. Skips return zero and false; legitimate zero outputs return true. Failures abort composition, not false. Nil interfaces, typed nil callbacks, expired scopes, and recorded failures remain invalid even when skipped. UseDebug uses the construction flag, not environment or runtime log level.
   Bootstrap factories capture typed dependencies only; acquire resources, read configuration, and mutate routes inside callbacks. All children share the root scope. Outputs are constructed before startup, not necessarily ready; passing them transfers no cleanup ownership. Return small typed bundles for related outputs, never a dependency registry or memoized cross-execution resource.
2. Give `New` existing values, required settings, then optional role-specific builder callbacks. Settings-free constructors have no builders; Config records report only.
   Callbacks run directly once in order on one fresh draft at the documented validation boundary. Nil entries panic; nil/empty expansions omit edits. Methods return the draft; publication detaches owned containers, not services, native handles or captures. Never replay policies.
   Before wrapping native clients/hooks, check ownership, applicable settings, validation, SDK loading, zero/false, and timing in the driver's guide and source documentation. [Configuration](references/guides/first-steps/configuration.md)
3. Build facades from drivers; native access uses `Driver()`, `DriverAs[T]()` or `NewFrom`. See [facades](references/guides/core-concepts/facades-and-drivers.md), [typed cache keys](references/guides/cache/overview.md), and [typed session fields](references/guides/http-capabilities/sessions.md).
    Share `auth.NewSessionIdentity(load)` across authentication, login/logout, and OAuth handlers; the user type implements `AuthID() K` (matching the loader's `K`) and `identity.Login(sess, user)` takes the user; check mutation errors. Default wiring is handle-free. Read [session login](references/guides/authentication/session-login.md) and [custom session selection](references/guides/authentication/session-login.md#select-sessions) before selecting handles or composing multiple sessions: context slots are not cookie, storage, identity, or authorization isolation.
    Before upgrading session writers or supplying a custom cache driver, read [Sessions](references/guides/http-capabilities/sessions.md) for atomic guarded persistence and coordinated writer rollout. Unguarded writes bypass retirement protection.
    Share `feature.NewFlag(name, fallback)` across evaluation and memory `Set`/`SetFor`. Read JSON secrets with `secret.NewSecret[T](name)` and `Get(ctx, definition)`; raw `Reference`/`Value` stay unchanged. Invalid definitions fail before I/O; external schemas and JSON-compatible drift still need runtime checks. Read the service guide before namespace use: cache/feature/session default to `":"`; secret requires a separator. Keep deployed cache dots with `Separator(".")`.
    Before pinning secret versions, read [Managing Secrets](references/guides/secrets/managing.md#version-lifetimes). Pinning requires retention of the same logical secret and does not fence permanent deletion and recreation.
   CAPTCHA uses vendor-local `cloudflarecaptcha`/`gcpcaptcha` adapters, not a core contract. Read [CAPTCHA](references/guides/abuse-prevention/captcha.md) before provider selection, token extraction/verification, route middleware, or Google score/step-up policy; it covers browser integration and security boundaries.
4. Return errors from route handlers; the router renders them as Problem Details. Add `router.Recover()` and call `r.Use` before registering routes.
    Construct reusable problems with `problem.New` and a `func(*problem.Builder)` callback; derive occurrences with `Details.Derive` and read standard members with accessors. Construction follows rule 2's callbacks/publication. New defaults status to 500 and title from final status; Derive preserves stored fields and inherited titles. Outer extension maps and header slices detach, not arbitrary nested values. `Builder.Error` retains a non-serialized cause; errors.Is matches stored Type/Status/Title and traverses that cause.
     [HTTP](references/guides/http-fundamentals/routing.md). Route annotations describe behavior without enforcing it; explicitly register cached OpenAPI JSON and Swagger handlers. [OpenAPI](references/guides/openapi/overview.md)
     Before iterator-based HTTP streaming, read [Responses](references/guides/http-fundamentals/responses.md) for producer shutdown: the response waits for producer exit, so blocking work must cooperate with cancellation and shutdown.
    AsyncAPI: declare immutable documents explicitly, not from runtime inventories; register JSON handlers separately. JSONTopic/JSONJob capture actual definitions only for JSON-envelope transports, with base64 wire payloads and separate logical schemas. Metadata changes no runtime behavior. [AsyncAPI](references/guides/asyncapi/overview.md)
    Webhook `Event.Map` preserves provider decoding and metadata for application-owned queue DTOs; acceptance no-ops return nil, not `ErrSkip`. [Receiving Webhooks](references/guides/webhooks/receiving-webhooks.md)
    Before checkout/refunds/subscriptions or uncertain-outcome recovery, read [Payments](references/guides/payments/overview.md): immutable `New*Request`, checked money, and original attempt/key reuse. Before client handoffs, read [Mobile Payments](references/guides/payments/mobile-payments.md) for application-owned authorization, pricing, secrets, and fulfillment. Read [Payment Drivers](references/guides/payments/drivers.md) for provider limits, separate store billing, and Mollie's unsigned callback hints requiring authenticated lookup.
5. Publish SQL `Query` values with `Table(name, configure...)` or `FromRaw(Raw(...), configure...)` (RawNamed is also supported).
   Use `Query.Derive` for independent variants; no callbacks preserves the base and its sticky errors. Publication detaches owned containers and retained root/predicate/conflict drafts, not arbitrary SQL argument objects.
   Only published statements compile or execute. Build with `BuildFor(dialect)` or the database facade, not ANSI `Build()`; paginate with a unique last `OrderBy`.
   SQL text telemetry is off by default. Before enabling `QueryText(true)`, read [Query Monitoring](references/guides/database/query-monitoring.md): it exports SQL verbatim, including literals and comments.
   Count and Seek return immutable Query values. EXISTS takes published Query.
   Each Scope gets an independent restricted PredicateBuilder, ANDed with other scopes and the whole user expression. Empty scopes add nothing. Ordinary groups accept variadic callbacks on one restricted draft and reject an empty result.
   Predicate drafts expose only predicates and nested groups. OnConflict returns a parent-attached `*Conflict`; DoUpdate/DoNothing return the parent `*Builder`. Keep typed bulk maps, Values/Only/Except, and existing append/replace semantics. [Query builder](references/guides/queries/query-builder.md), [Pagination](references/guides/queries/pagination.md)
    Schema `Create`, `CreateIfNotExists`, and `Alter` accept ordered declarations on one table draft. Omission is invalid, not a no-op; nil callbacks panic. Published operations detach table and child builders, not arbitrary default values. [Schema builder](references/guides/migrations/schema-builder.md)
 6. Test with memory drivers, fakes, and `router.Record`: [Facades and drivers](references/guides/core-concepts/facades-and-drivers.md).
7. Search: declare `search.NewIndex` values with `IndexBuilder` callbacks and queries with `QueryBuilder` callbacks; publish local variants with Query.Derive.
   Groups share a predicate-only `PredicateBuilder`: no ordering or OrWhereGroup; the final expression must contain conditions. Index lists accumulate and getters sort/deduplicate; Index.Err validates the completed schema/key rather than storing an incomplete draft error.
   Custom `Indexer` implementations need both `Err()` and `Schema()`. Read [Search](references/guides/search/overview.md) before implementing one.
   Query errors remain sticky through Derive; publication and Inspect detach owned nested/value-list containers.
   Append indexes with `search.Builder.Indexes` in the engine callback. Run `Sync` in a release command (or `compose.Startup`) and engine `Check` in `compose.Startup`, never as a recurring `compose.Check`; query declared fields within `search.MaxHits`.
   [Deploying search](references/guides/search/deploying.md)
8. Build `compose.MCPTools("app", configure...)` during composition to attach capture; `application.MCPToolsBuilder.Limits(application.CaptureLimits)` sizes it. Require a nonempty prefix; it directly names every app and telemetry tool and CLI group (`app.info`, `app.logs`, `app.traces`; `app:logs`). Explicitly register native tools on a protected loopback MCP endpoint; select a subset when needed. Add capability policy with `Tool.WithAvailability`; keep resource Gate checks in shared typed application methods. Discover with `arkoren tools`; inspect one tool with `arkoren public:routes --help`. Command results are JSON, follow is NDJSON, and errors are JSON on stderr; check exit status even when a result was printed. Help/prompts remain text and explicit key exports remain raw. Read [CLI output formats](references/guides/cli/reference.md#output-formats) before parsing output. Namespaced metadata supplies qualified `group:operation` commands without changing exact tool names. Follow uses bounded cursor calls. Gate traffic with `arkoren app:ready --wait --timeout 30s`; add `--fail-fast` to ready/healthy waits for fresh failed checks. Never replay mutations. Read [Capture Setup](references/guides/diagnostics/capture-setup.md) and [Application Diagnosis](references/guides/diagnostics/application-diagnosis.md).
   Request `--verbose` when you need complete schemas, file inventories, or diagnostic context. It changes CLI presentation only; never rerun a mutation merely to obtain verbose diagnostics.
  9. Queue construction follows rule 2. Queues copies input and publication copies again; later calls replace the restriction, and empty removes it.
   A zero Job is rejected before registration or encoding. Reconstruct variants from shared policies; no Job.Derive. [Producing jobs](references/guides/queues/producing-jobs.md), [Consuming jobs](references/guides/queues/consuming-jobs.md)
   Before retry accounting or failure listeners, read [Consuming jobs](references/guides/queues/consuming-jobs.md): shutdown rescheduling does not discount attempts, and `JobFailed` requires successful terminal settlement.
  10. Declare validation schemas with variadic typed callbacks and inferred constructor types. Configure fields directly: `object.Field("GoName", func(name *validate.StringBuilder[string]) { ... })`.
     Use `Values` for slices/maps, `Keys` for map keys, and typed `Use` for same-role reuse. Imported children cannot be replaced or overlapped. Published schemas expose execution and inspection, not configuration.
     `ConditionBuilder` callbacks observe nested Go field paths checked at construction; the shared construction/publication rules apply.
      Collection assertions report errors without transforming input. JSON names select error paths, not fields. Other packages retain their APIs. [Validation](references/guides/validation/overview.md)
      Before validating an already encoded JSON contract, read [Encoded JSON Validation](references/guides/validation/encoded-json.md): its strict null/default semantics, exact numeric comparisons, and operational resource failures are separate from application Parse.
11. Declare migrations, MCP tools, workflows/steps, and LLM agents/tools with role-specific callbacks under rule 2; functions, schema implementations, and captures stay shared.
    Execution handlers stay positional; nil handlers remain domain errors, distinct from nil construction callbacks, which panic. Migrations require Up; workflows require MaxDuration at registration; agents require Output unless their result type is exact string.
    LLM NewTool keeps positional inputs then optional nongeneric ToolBuilderFunc callbacks; Available filters once per run and rechecks before execution. Shared schema/handler/policy are not MCP converters; handlers authorize resources.
     Workflow Step.Derive preserves base policy/error without restoring defaults. SQL/search Query and problem Details retain Derive; jobs, migrations, MCP declarations, workflows, agents, indexes, and entries have none.
     Before using Child or upgrading workflows that call it, read [Workflow Drivers](references/guides/workflows/drivers.md) for create-only cancellation ownership, unsupported providers, and replay rollout. Spawn is detached work, not an equivalent fallback.
    Schedule EntryBuilder requires positional names/functions and final When validation. Nil execution functions are domain errors; invalid specs retain the first error. Overlap defaults on; zero timeout is unlimited and negative is invalid. Retained entry drafts cannot change published settings. Spec and scalar/composite/object-wide schemas stay immutable.
    Per-call, SDK and suite options stay separate, including WithTransaction/WithSession. Before workflow telemetry, read [Workflows](references/guides/workflows/overview.md): configure each pod independently, instrument attempts not replay, and upgrade Temporal workers first. [Migrations](references/guides/migrations/overview.md), [Diagnostic Tools](references/guides/diagnostics/diagnostic-tools.md), [Running Agents](references/guides/llm/running-agents.md)
12. MCP: `core/mcp` defines tools; `driver/mcp/mcpserver` serves HTTP. Start with [a first server and client connection](references/guides/mcp/first-server.md); direct `Tool.Call` is for unit tests.
    Register explicitly with `mcpserver.Builder.Tools`. Admit with `auth.Middleware(auth.Bearer(verifyAccessToken))`, then `auth.Require[User]()` before `routes.Mount("/mcp", endpoint)`; verifier returns the same `User` policies read. Optional `gate.Require(canUseMCP)` gates the endpoint; `gate.Bind(rules...)` adapts the same rules to `Available`, and `gate.HasScopes` checks `auth.Scoped` users. CLI `ARKOREN_MCP_TOKEN` supplies an existing bearer credential; acquisition/refresh is external. Keep SDK imports in composition/adapter; ark factories are application patterns.
    Authenticate HTTP; shared service handlers authorize resources with Gate. App-owned `Available` runs before decode and filters complete unpaginated discovery; policy errors fail closed, even wrapped `ToolError`. Annotations are hints. Input parses once; adapter validates output; no replay. MCP tools/list is the only catalog. CLIInfo/Role/Cursor/Positional/Example are optional namespaced metadata; ToolFailure is approved result metadata outside successful output. Native additions are unmanaged; OAuth metadata/challenges need separate setup. [MCP overview](references/guides/mcp/overview.md)

Install modules: [Installation](references/guides/first-steps/installation.md). For typed keys, read [Application Keys](references/guides/keys/overview.md). Provision explicitly; missing startup keys fail.

Before TLS generation/loading, private CA trust, or mTLS, read [TLS Certificates](references/guides/http-networking/certificates.md). Before CLI provisioning/trust/untrust, read [Certificate Commands](references/guides/cli/certificates.md). Loading is not verification; generation never installs trust.

Build immutable messages with positional recipients/body (mail) or recipient/title (push), then optional MessageBuilder callbacks; published messages have getters. Read [Mail](references/guides/mail/overview.md) for sender defaults and [Push Notifications](references/guides/push/overview.md) for retention, subscriptions, display, and `{title, body, data}` Web Push payloads. Never replay unknown outcomes automatically.
Before listeners/dashboards, read [Built-in Events](references/guides/events/built-in-events.md) and [Instrumented Components](references/guides/observability/instrumented-components.md): notifications are local best-effort API observations, not durable business facts; Hash/Crypto have metrics only.

## Collection results

Use native collections to transform data results and detached inspection
snapshots; use `Iter()` when a transport accepts a standard iterator. Keep page
and diagnostic envelopes: local transformations do not update server totals,
navigation tokens, or loss/reset information. Lazy sources own cancellation and
cleanup, and another traversal can repeat I/O. Check prefix-plus-error results;
ordinary JSON encoding materializes a lazy source rather than streaming it over
HTTP. Before collection-valued DTOs or schemas, read
[Collections Overview](references/guides/collections/overview.md) and the
[named-container validation guide](references/guides/validation/collections.md#named-containers).
Eager collections preserve inferred wire shapes; use `NamedBuilder` to attach
runtime rules without weakening exact field-type checks. Lazy sources remain
opaque to inference.

## Reference index

<!-- toc:begin -->

### Guides

#### Meet Arkoren

- [Introduction](references/guides/about/introduction.md): Understand Arkoren's modular approach and choose a starting point for your Go application.
- [Choosing Arkoren](references/guides/about/choosing-arkoren.md): Evaluate Arkoren's shared conventions, integration boundaries, and backend limits before adopting a capability.
- [Contributing](references/guides/about/contributing.md): Locate a toolkit change, preserve module and contract boundaries, and run the relevant contributor checks.

#### First Steps

- [Installation](references/guides/first-steps/installation.md): Choose Arkoren modules and add a capability to an existing Go application without changing its structure.
- [Creating Projects](references/guides/first-steps/creating-a-project.md): Preview and generate an owned HTTP starter, resolve its dependencies, and understand its development defaults.
- [Quickstart](references/guides/first-steps/quickstart.md): Extend the generated HTTP service with a greeting route, a process-local counter, and a checked error response.
- [Configuration](references/guides/first-steps/configuration.md): Translate application settings into typed construction callbacks while preserving component defaults and ownership.
- [Project Layout](references/guides/first-steps/project-layout.md): Grow an application into feature-owned packages while keeping dependency construction and resource ownership visible.
- [Deployment](references/guides/first-steps/deploying.md): Build and run a service with explicit production settings, health probes, bounded shutdown, and application-owned credentials.

#### Concepts

- [Facades and Drivers](references/guides/core-concepts/facades-and-drivers.md): Separate application-facing service calls from backend selection, configuration, and ownership.
- [Contracts](references/guides/core-concepts/contracts.md): Understand the behavioral guarantees, error identities, and verification boundaries behind a service interface.
- [Arks](references/guides/core-concepts/arks.md): Distinguish reusable domain features from service mechanisms, backend drivers, and application policy.
- [Common Pitfalls](references/guides/core-concepts/common-pitfalls.md): Recognize unsafe assumptions about ownership, configuration, HTTP behavior, data access, and deployment.

#### Application

- [Application Overview](references/guides/application/overview.md): Decide whether your service needs an application coordinator and understand its boundaries.
- [Application Composition](references/guides/application/composition.md): Construct typed dependencies once, keep ownership explicit, and select optional features before startup.
- [Lifecycle and Ownership](references/guides/application/lifecycle-and-ownership.md): Start dependencies in order, supervise running work, and release resources after failures or shutdown.
- [Health and Readiness](references/guides/application/health-and-readiness.md): Control traffic with cached dependency checks while keeping restart and startup decisions separate.

#### Drivers

- [Choosing a Driver](references/guides/drivers/choosing-a-driver.md): Compare backend requirements, find available providers, and follow the setup guide for your service.
- [Writing a Driver](references/guides/drivers/writing-a-driver.md): Implement an application-owned service driver or wrapper with explicit behavior, resource ownership, and contract tests.

#### Fundamentals

- [Routing](references/guides/http-fundamentals/routing.md): Register request handlers, capture path values, group routes, and mount standard HTTP handlers.
- [Requests](references/guides/http-fundamentals/requests.md): Read typed request inputs, decode bounded bodies, and preserve input errors and upload ownership.
- [Responses](references/guides/http-fundamentals/responses.md): Return ordinary handler results as JSON, text, HTML, bytes, or redirects without hiding response failures.
- [Streaming Responses](references/guides/http-fundamentals/streaming-responses.md): Send live bytes or server-sent events while managing producer cancellation, shutdown, and committed failures.
- [Error Handling](references/guides/http-fundamentals/error-handling.md): Turn returned errors into client-safe Problem Details and preserve causes for diagnostics.
- [Middleware](references/guides/http-fundamentals/middleware.md): Scope and order request policies, write wrappers, and preserve lifecycle behavior across native HTTP adapters.
- [Body Limits](references/guides/http-fundamentals/body-limit.md): Set request-body ceilings before parsing and scope larger inputs without weakening ordinary endpoints.
- [Content Types](references/guides/http-fundamentals/content-type.md): Reject request media types an endpoint does not accept before decoding its body.

#### Capabilities

- [Sessions](references/guides/http-capabilities/sessions.md): Store visitor state in a cache-backed session and understand identity rotation, expiry, and response-time persistence.
- [Localization](references/guides/http-capabilities/localization.md): Select a request language from client preferences and render validation messages with catalogs and explicit fallbacks.
- [Signed Links](references/guides/http-capabilities/secure-links.md): Issue expiring application URLs and enforce verification, single use, and state bindings without confusing signatures with authorization.

#### Webhooks

- [Receiving Webhooks](references/guides/webhooks/receiving-webhooks.md): Authenticate incoming provider notifications and bind reusable event definitions to typed application handlers.
- [Provider Adapters](references/guides/webhooks/provider-adapters.md): Choose the correct provider verifier, supply endpoint credentials, and preserve native event keys and delivery identities.
- [Delivery Policies](references/guides/webhooks/delivery-policies.md): Choose receipt retention, queued acceptance, failure responses, and operating limits for a webhook receiver.

#### Networking

- [HTTP Server](references/guides/http-networking/server.md): Bind HTTP listeners during startup, serve prepared handlers, and drain connections with explicit shutdown ownership.
- [HTTP Client](references/guides/http-networking/http-client.md): Send outgoing requests with explicit replay safety, bounded retries, response-body ownership, and trace-context policy.
- [TLS Certificates](references/guides/http-networking/certificates.md): Provision local identities, load certificate files, and configure server trust and mutual TLS without weakening verification.

#### Browser Security

- [Overview](references/guides/browser-security/overview.md): Choose browser defenses for response handling, cross-origin access, and forged cookie-backed actions.
- [Secure Headers](references/guides/browser-security/secure-headers.md): Set browser response policies for framing, content handling, resource loading, and deliberate HTTPS persistence.
- [CORS](references/guides/browser-security/cors.md): Grant explicit cross-origin browser response access and handle preflights without confusing CORS with request authorization.
- [CSRF Protection](references/guides/browser-security/csrf-protection.md): Reject forged cookie-backed browser actions with request-header checks and narrowly scoped cross-origin exceptions.

#### Abuse Prevention

- [Abuse Prevention Overview](references/guides/abuse-prevention/overview.md): Choose request budgets and browser verification for sensitive endpoints without confusing abuse checks with permission.
- [Rate Limiting](references/guides/abuse-prevention/rate-limiting.md): Apply cache-backed request budgets, choose trusted client keys, and interpret recovery headers and backend failures.
- [CAPTCHA](references/guides/abuse-prevention/captcha.md): Verify browser tokens with Turnstile or Google reCAPTCHA using trusted hostname, action, and score policies before protected effects.

#### Authentication

- [Authentication Overview](references/guides/authentication/overview.md): Resolve credentials into your application's identity and choose which routes require it.
- [Session Login](references/guides/authentication/session-login.md): Check application-owned credentials, rotate the browser session, and revoke account sessions.
- [API Tokens](references/guides/authentication/api-tokens.md): Accept bearer credentials and manage application-issued API keys with hashed storage and revocation.
- [OAuth Login](references/guides/authentication/oauth.md): Complete external browser sign-in and map the provider identity to a local account safely.
- [OIDC Tokens](references/guides/authentication/oidc-bearer.md): Verify provider-issued JWTs for an API and resolve the exact request identity your handlers need.

#### Authorization

- [Authorization Overview](references/guides/authorization/overview.md): Check actions with plain Go rules and keep permission decisions beside the resources they protect.
- [Writing Policies](references/guides/authorization/policies.md): Group feature rules in policy structs and distinguish tenant roles, application permissions, and delegated scopes.
- [Row-Level Access](references/guides/authorization/row-access.md): Enforce record visibility in store queries and preserve authorization boundaries during concurrent writes.
- [Handling Denials](references/guides/authorization/denials.md): Return accurate authorization responses and observe rule denials without collecting private request data.

#### Hashing

- [Hashing Overview](references/guides/hashing/overview.md): Store password hashes and distinguish a successful password check from a mismatch or a storage failure.
- [Hashing Drivers](references/guides/hashing/drivers.md): Configure Argon2id or bcrypt and understand their stored formats, input limits, and shared metrics.
- [Choosing Cost](references/guides/hashing/cost.md): Measure a known hash policy, tune its work settings, and bound the resources available to password requests.
- [Upgrading Hashes](references/guides/hashing/upgrading.md): Renew verified password hashes with conditional writes and migrate supported records without changing unverified credentials.

#### Keys

- [Application Keys](references/guides/keys/overview.md): Choose typed application keys, validate stored material, and separate public output from private exports.

#### Encryption

- [Encryption Overview](references/guides/encryption/overview.md): Encrypt recoverable application values and reject ciphertext that cannot be authenticated or decoded.
- [Encryption Drivers](references/guides/encryption/drivers.md): Choose and configure a local authenticated cipher, including purpose binding and cleanup ownership.
- [Encryption Keys](references/guides/encryption/keys.md): Keep current and previous encryption keys available and rotate writers without losing access to existing ciphertext.
- [Storing Data](references/guides/encryption/storing.md): Persist encrypted records with explicit schema, identity binding, encoding, and guarded re-encryption.

#### Secrets

- [Secrets Overview](references/guides/secrets/overview.md): Load private application credentials through a secret store without confusing storage with runtime configuration.
- [Secrets Drivers](references/guides/secrets/drivers.md): Choose and configure a secret provider with explicit payload, permission, retention, and lifecycle boundaries.
- [Managing Secrets](references/guides/secrets/managing.md): Write versioned values, verify retained versions, observe facade outcomes, and retire secrets without mistaking deletion for rotation.
- [Secret Rotation](references/guides/secrets/rotation.md): Coordinate credential replacement, consumer refresh, verification overlap, and retained encryption material.

#### Database

- [Overview](references/guides/database/overview.md): Use the database facade to read typed results, execute writes, and map SQL columns to Go values.
- [Drivers](references/guides/database/drivers.md): Choose and configure SQLite, PostgreSQL, or MySQL, with clear compatibility differences.
- [Transactions](references/guides/database/transactions.md): Group related operations atomically, including context propagation, nesting, isolation, and retries.
- [Locking](references/guides/database/locking.md): Coordinate concurrent work with row or named locks, with explicit ownership, scope, and lifetime.
- [Batching and Streaming](references/guides/database/batching-and-streaming.md): Process large datasets in batches or as a stream without loading every row into memory.
- [Monitoring](references/guides/database/query-monitoring.md): Observe database activity through events, spans, metrics, and privacy-conscious diagnostics.

#### Queries

- [Query Builder](references/guides/queries/query-builder.md): Build reads and writes, then introduce joins, reusable conditions, and advanced queries.
- [Raw SQL](references/guides/queries/raw-sql.md): Run handwritten SQL safely, bind parameters, and account for dialect differences.
- [Pagination](references/guides/queries/pagination.md): Return result windows with offset or cursor pagination, request parameters, and cursor safety.

#### Migrations

- [Overview](references/guides/migrations/overview.md): Define, register, apply, inspect, roll back, and test schema or data changes.
- [Schema Builder](references/guides/migrations/schema-builder.md): Define and change tables, columns, indexes, and constraints across database dialects.
- [Deployment](references/guides/migrations/deployment.md): Ship schema changes safely through rolling deployments, concurrent runners, and failure recovery.
- [Troubleshooting](references/guides/migrations/troubleshooting.md): Diagnose migration failures and follow targeted recovery procedures, including MySQL ledger repair.

#### Validation

- [Overview](references/guides/validation/overview.md): Define reusable rules for typed input and retain prepared values with Parse.
- [Scalar Rules](references/guides/validation/available-methods.md): Reject unsuitable text, numbers, booleans, times, and uploaded files without rewriting them.
- [Structs and Reuse](references/guides/validation/structs-and-reuse.md): Compose exact-type field rules, nested objects, shared policies, and optional pointer values.
- [Slices and Maps](references/guides/validation/collections.md): Check collection entries, membership, identities, and matching counts without removing data.
- [Conditions](references/guides/validation/conditions.md): Select field requirements and scoped rules from prepared values in the enclosing struct.
- [Normalizers](references/guides/validation/normalizers.md): Prepare cleaned values before checking presence and rules, without changing caller-owned input.
- [Custom Rules](references/guides/validation/custom-rules.md): Add application-specific value and object checks while preserving operational errors.
- [Validation Errors](references/guides/validation/errors.md): Read structured violations, translate their messages, and distinguish input failures from invalid definitions.
- [JSON Schema](references/guides/validation/json-schema.md): Export representable rules, infer output shapes, or validate decoded JSON with explicit interchange limits.
- [Encoded JSON](references/guides/validation/encoded-json.md): Check exact JSON document bytes without applying defaults, normalizers, or binary64 rounding.

#### Cache

- [Cache Overview](references/guides/cache/overview.md): Reuse typed values, compute missing results, and choose atomic operations for concurrent callers.
- [Cache Drivers](references/guides/cache/drivers.md): Choose process-local or shared caching and configure driver connections, ownership, and native access.
- [Cache Keys](references/guides/cache/keys.md): Declare typed cache identities, reuse expiry policies, and preserve names safely across deployments.
- [Cache Counters](references/guides/cache/counters.md): Update shared integer counts atomically and give request windows an expiry without a separate write.
- [Cache Locks](references/guides/cache/locks.md): Acquire, release, and renew expiring leases without mistaking them for resource-level write protection.
- [Cache Monitoring](references/guides/cache/monitoring.md): Interpret cache spans, hit ratios, errors, and privacy limits using the facade's telemetry.

#### Search

- [Search Overview](references/guides/search/overview.md): Query a configured search engine and read typed document hits without coupling application code to a backend.
- [Search Drivers](references/guides/search/drivers.md): Choose a search backend, configure its engine, and account for native ownership, deployment requirements, and matching limits.
- [Indexing Documents](references/guides/search/indexing-documents.md): Declare typed search documents and field policies, replace indexed records, and remove documents with explicit batch and retry boundaries.
- [Search Queries](references/guides/search/queries.md): Combine search text, typed filters, permission boundaries, and ordering within a bounded result window.
- [Deploying Search](references/guides/search/deploying.md): Synchronize backend indexes during deployment, verify definitions at startup, and recover safely from schema changes or partial failures.
- [Search Monitoring](references/guides/search/monitoring.md): Observe search operations with shared telemetry and bounded deployment events while preserving data and lifecycle boundaries.

#### Storage

- [Overview](references/guides/file-storage/overview.md): Write and read logical objects, manage stream ownership, and traverse portable keys with explicit error handling.
- [Drivers](references/guides/file-storage/drivers.md): Choose and configure memory, filesystem, or S3 storage with explicit lifecycle, ownership, and backend limits.
- [Uploads and Downloads](references/guides/file-storage/uploads-and-downloads.md): Store bounded uploads under safe keys and serve authorized objects with attachment, inline, and range behavior.
- [Signed URLs](references/guides/file-storage/signed-urls.md): Authorize direct S3 transfers, bind upload headers and exact size, and distinguish provider URLs from application-signed routes.
- [Events and Telemetry](references/guides/file-storage/events-and-telemetry.md): Observe successful object changes, interpret facade spans and metrics, and preserve storage privacy boundaries.

#### Collections

- [Overview](references/guides/collections/overview.md): Shape in-memory application records with eager slice and map collections while preserving clear ordering and ownership.
- [Lazy Collections](references/guides/collections/lazy-collections.md): Defer collection work until traversal and choose safe stopping, buffering, and replay behavior for your source.
- [Fallible Collections](references/guides/collections/fallible-collections.md): Preserve read and callback failures while choosing whether to stop, retain a partial result, or continue traversal.
- [Collection Methods](references/guides/collections/available-methods.md): Look up collection operations by task, including receiver forms, small examples, and consequential differences.

#### Feature Flags

- [Feature Flags Overview](references/guides/feature-flags/overview.md): Evaluate typed flags for explicit subjects and choose safe behavior when evaluation supplies no value or fails.
- [Feature Flag Drivers](references/guides/feature-flags/drivers.md): Choose and construct memory, OpenFeature, or LaunchDarkly drivers with explicit lifecycle and native-client ownership.
- [Feature Flag Testing](references/guides/feature-flags/testing.md): Arrange deterministic flag states, exercise evaluation failures, and check custom drivers with a disposable conformance harness.

#### Events

- [Overview](references/guides/events/overview.md): Publish typed local notifications and choose between best-effort observation and error-returning dispatch.
- [Writing Listeners](references/guides/events/listeners.md): Register local observers, own their removal, and keep failure and concurrency behavior explicit.
- [Built-in Events](references/guides/events/built-in-events.md): Find component notifications, their enabling policies, and the boundary each observation describes.
- [HTTP Events](references/guides/events/http-events.md): Inspect HTTP publication timing, request metadata, and diagnostic privacy boundaries.
- [Service Events](references/guides/events/service-operations.md): Interpret local payment, push, search, and secret operation results without inventing durable business outcomes.
- [Event Auditing](references/guides/events/auditing.md): Separate optional observations from required audit records that commit with application changes.

#### Broadcasting

- [Broadcasting Overview](references/guides/broadcasting/overview.md): Announce typed facts and route received notifications to independent listeners.
- [Broadcast Drivers](references/guides/broadcasting/drivers.md): Choose and connect a broadcast transport, including native resources and disposable local brokers.
- [Message Delivery](references/guides/broadcasting/message-delivery.md): Own subscriptions and consumer cancellation, and handle failed or repeated notifications.
- [Monitoring](references/guides/broadcasting/metadata-and-telemetry.md): Propagate trace context through notifications and interpret broadcast spans and metrics without recording message contents.

#### Mail

- [Mail Overview](references/guides/mail/overview.md): Send transactional email through a portable facade and distinguish submission acceptance from recipient delivery.
- [Mail Drivers](references/guides/mail/drivers.md): Configure outbound mail transports and optional provider notification or inbound receivers without confusing submission with delivery.
- [Composing Messages](references/guides/mail/composing-messages.md): Build immutable email messages with recipient groups, alternative bodies, attachments, and inline images.
- [Sender Defaults](references/guides/mail/configuration.md): Share sender and reply-to addresses across messages and override them without mutating published values.
- [Events and Telemetry](references/guides/mail/events-and-telemetry.md): Observe mail submission attempts with bounded outcome events, spans, and metrics while preserving privacy and uncertainty boundaries.

#### Push

- [Overview](references/guides/push/overview.md): Submit typed push messages, choose retention, and distinguish acceptance from browser display.
- [Drivers](references/guides/push/drivers.md): Choose a push backend and configure sender retention, VAPID credentials, and secure endpoint submission.
- [VAPID Keys](references/guides/push/vapid-keys.md): Keep a persistent Web Push signing identity and publish only its browser-ready public key.
- [Browser Integration](references/guides/push/browser-integration.md): Register browser subscriptions for authenticated users and display submitted notifications with a service worker.
- [Testing](references/guides/push/testing.md): Assert application submissions with the push fake and verify browser delivery through separate acceptance checkpoints.

#### Payments

- [Payments Overview](references/guides/payments/overview.md): Understand payment resources and read provider evidence before fulfilling an order.
- [Payment Drivers](references/guides/payments/drivers.md): Choose and configure a payment backend, with optional provider notifications and local call monitoring.
- [Hosted Checkout](references/guides/payments/hosted-checkout.md): Start provider-hosted approval, complete eligible checkouts explicitly, and verify collection before fulfillment.
- [Mobile Payments](references/guides/payments/mobile-payments.md): Prepare a protected client SDK handoff and verify its financial outcome on the backend.
- [Payment Refunds](references/guides/payments/refunds.md): Request explicit refund amounts and read the refund's own progress without confusing acceptance with completion.
- [Recurring Subscriptions](references/guides/payments/subscriptions.md): Enroll buyers in existing provider plans, observe agreement state, and cancel future renewals explicitly.
- [Payment Recovery](references/guides/payments/recovery.md): Preserve uncertain mutation outcomes and reconcile saved operations within provider-specific replay limits.
- [Mollie Callbacks](references/guides/payments/mollie-callbacks.md): Bound unsigned callback hints, verify saved merchant ownership, and reconcile authoritative provider reads durably.
- [Testing Payments](references/guides/payments/testing.md): Simulate payment lifecycles or script uncertain provider replies without network calls or money movement.

#### Queues

- [Overview](references/guides/queues/overview.md): Hand background work to a worker with typed jobs, dispatchers, and consumers.
- [Drivers](references/guides/queues/drivers.md): Choose a queue backend and configure its routing, resources, connections, and local checks.
- [Producing Jobs](references/guides/queues/producing-jobs.md): Define typed work, route it to a queue, and make the handoff without confusing acceptance with completion.
- [Consuming Jobs](references/guides/queues/consuming-jobs.md): Register typed handlers, choose worker queues, and run consumers with a controlled shutdown.
- [Delivery and Retries](references/guides/queues/delivery-and-retries.md): Choose retry and terminal outcomes while accounting for uncertain sends, duplicate effects, deadlines, and broker settlement.
- [Monitoring](references/guides/queues/metadata-and-telemetry.md): Trace jobs across processes, count delivery attempts, and observe accepted retry and failure outcomes.

#### Scheduling

- [Overview](references/guides/scheduled-tasks/overview.md): Register background work with a scheduler and distinguish calendar timing from handler execution.
- [Drivers](references/guides/scheduled-tasks/drivers.md): Choose process-local timers or an engine-backed scheduler and configure its delivery, ownership, and shutdown rules.
- [Calendar Schedules](references/guides/scheduled-tasks/calendar-schedules.md): Define recurring entries with calendar fields or cron and choose overlap and cooperative timeout policies.
- [Deferred Tasks](references/guides/scheduled-tasks/deferred-tasks.md): Register typed one-off handlers, defer payloads until later, and interpret submission and cancellation outcomes.
- [Time Zones](references/guides/scheduled-tasks/time-zones-and-dst.md): Choose UTC or named local time and account for skipped and repeated wall-clock times during daylight saving changes.
- [Error Handling](references/guides/scheduled-tasks/error-handling.md): Classify scheduled run outcomes, observe failures, and separate cooperative cancellation from driver recovery.

#### Workflows

- [Workflows Overview](references/guides/workflows/overview.md): Coordinate long-running processes with typed workflows and replay-safe steps.
- [Workflow Drivers](references/guides/workflows/drivers.md): Choose and configure memory, Temporal, or Restate for workflow execution.
- [Workflow Steps](references/guides/workflows/steps.md): Run external effects with explicit attempt policies and choose the right child-workflow lifetime.
- [Signals and Waiting](references/guides/workflows/signals-and-waiting.md): Wait for typed external messages or durable timers without blocking on application channels.
- [Running Workflows](references/guides/workflows/running-workflows.md): Start, inspect, signal, and cancel identified runs through the workflow client.
- [Worker Hosts](references/guides/workflows/shared-worker-host.md): Share native workflow and scheduler resources with explicit startup, readiness, and cleanup ownership.
- [Error Handling](references/guides/workflows/error-handling.md): Classify step failures, compensate for earlier effects, and recognize errors across worker boundaries.
- [Testing Workflows](references/guides/workflows/testing.md): Separate submission tests, in-process workflow decisions, and native replay or retention checks.

#### Language Models

- [Language Models Overview](references/guides/llm/overview.md): Generate a model reply and distinguish response data, agent execution, and application-owned conversation state.
- [Drivers](references/guides/llm/drivers.md): Configure hosted OpenAI or a compatible server and compare the driver's two API modes.
- [Conversation History](references/guides/llm/conversation-history.md): Continue and persist caller-owned conversations without losing tool results, reasoning signatures, or buffer ownership.
- [Running Agents](references/guides/llm/running-agents.md): Run reusable agent definitions with bounded turns, concurrent tools, and explicit failure outcomes.
- [Defining Tools](references/guides/llm/defining-tools.md): Expose typed application operations with validated arguments, capability policies, and resource authorization.
- [Structured Output](references/guides/llm/structured-output.md): Decode a model's final answer into a validated Go value and control correction attempts.
- [Streaming Replies](references/guides/llm/streaming.md): Display incremental replies and tool progress while preserving cancellation, completion, and browser-delivery boundaries.
- [Embeddings](references/guides/llm/embeddings.md): Generate ordered text vectors and keep model, dimensions, purpose, and storage responsibilities consistent.
- [Local Development](references/guides/llm/local-development-with-ollama.md): Run downloaded Ollama models through the OpenAI driver and verify local generation separately from application tests.
- [Testing Models](references/guides/llm/testing.md): Script model turns, inspect requests, and test application outcomes without provider calls.

#### MCP

- [Overview](references/guides/mcp/overview.md): Expose typed application operations as discoverable MCP tools while retaining application-owned policy and HTTP serving.
- [First Server](references/guides/mcp/first-server.md): Build a local HTTP MCP server and discover and call its first tool with an MCP client.
- [Tools](references/guides/mcp/tools.md): Define typed tool operations, validate their wire contracts, and register them with application-owned HTTP serving.
- [Securing MCP](references/guides/mcp/securing-mcp.md): Protect the MCP HTTP boundary with verified identity, TLS, Host and Origin policy, and deliberate OAuth integration.
- [Capability Access](references/guides/mcp/availability-and-authorization.md): Filter tool availability by caller capability while enforcing resource authorization in shared application operations.

#### Skills

- [Using Skills](references/guides/agent-skills/working-with-agents.md): Give a local coding agent Arkoren guidance, keep its documentation snapshot aligned with your application, or read selected guides online.

#### Observability

- [Telemetry](references/guides/observability/telemetry.md): Share a telemetry handle, record application work, and manage propagation, observation, and shutdown.
- [Drivers](references/guides/observability/telemetry-drivers.md): Choose local capture, OTLP export, or Prometheus scraping and manage each driver's resources.
- [Instrumented Components](references/guides/observability/instrumented-components.md): Look up built-in spans, metrics, labels, and the operation boundaries they measure.
- [Request Logging](references/guides/observability/request-logging.md): Record failed requests with the observed response status and optional trace correlation.
- [Request Tracing](references/guides/observability/request-tracing.md): Trace HTTP boundaries, correlate failures, and control incoming trust and outgoing propagation.
- [Telemetry Privacy](references/guides/observability/telemetry-privacy.md): Review automatic fields, logs, diagnostic capture, and propagation before retaining or exporting observations.

#### Diagnostics

- [Capture Setup](references/guides/diagnostics/capture-setup.md): Attach bounded application capture and publish selected diagnostics on a protected MCP endpoint.
- [Application Diagnosis](references/guides/diagnostics/application-diagnosis.md): Investigate request failures using retained traces, logs, and health before authorizing new execution.
- [Diagnostic Tools](references/guides/diagnostics/diagnostic-tools.md): Look up diagnostic names, typed inputs, limits, failures, and bounded cursor behavior.

#### CLI

- [Overview](references/guides/cli/overview.md): Choose remote MCP inspection or local provisioning and install the Arkoren CLI.
- [Connecting](references/guides/cli/connecting.md): Approve an MCP destination, supply an existing bearer credential, and verify discovery.
- [Calling Tools](references/guides/cli/calling-tools.md): Choose a discovered command, prepare its inputs, and interpret its result without replaying effects.
- [Waiting and Following](references/guides/cli/waiting-and-following.md): Bound condition waits and follow new records while accounting for cursor gaps and output failures.
- [CLI Reference](references/guides/cli/reference.md): Look up command forms, reserved flags, input conversion, output contracts, exit codes, and retries.
- [Key Commands](references/guides/cli/keys.md): Provision purpose-specific keys offline, check stored material, and export public PEM or VAPID values.
- [Certificate Commands](references/guides/cli/certificates.md): Generate local TLS identities, check file pairs, and explicitly manage exact-root system trust.

#### Testing

- [Overview](references/guides/testing/overview.md): Choose test boundaries for application code, HTTP routes, and external services using Go's standard testing package.
- [HTTP Tests](references/guides/testing/http-tests.md): Exercise route matching, middleware, and response contracts with in-process HTTP requests.
- [Authentication Tests](references/guides/testing/auth-tests.md): Test identity-dependent handlers, protected routes, login sessions, and custom authenticators at distinct boundaries.
- [Fakes](references/guides/testing/fakes.md): Record external requests, script failures, and assert application behavior through real service facades.
- [Controlling Time](references/guides/testing/time.md): Test process-local expiry and timers deterministically, synchronize background work, and pin validation time.
- [Integration Tests](references/guides/testing/integration-tests.md): Verify backend-specific behavior against disposable infrastructure with bounded operations and test-owned cleanup.
- [Driver Conformance](references/guides/testing/conformance-suites.md): Check custom drivers and wrappers against shared public contracts with suite-specific fixtures and lifecycle ownership.

#### OpenAPI

- [OpenAPI Overview](references/guides/openapi/overview.md): Describe an HTTP route and export its contract independently of request handling or documentation serving.
- [Describing Operations](references/guides/openapi/operations.md): Align typed requests and responses with explicit parameters, errors, examples, and security declarations.
- [Serving Documentation](references/guides/openapi/serving.md): Export an HTTP contract, serve cached JSON, and mount a bundled Swagger UI with explicit access controls.
- [Built-in Handlers](references/guides/openapi/built-in-handlers.md): Attach shipped application annotations and identify integrations that need an explicit, application-specific contract.

#### AsyncAPI

- [AsyncAPI Overview](references/guides/asyncapi/overview.md): Generate a messaging contract from typed definitions, payload schemas, destinations, and application operations.
- [Messages and Schemas](references/guides/asyncapi/messages-and-schemas.md): Describe complete wire messages, expose decoded payload shapes, and work within the supported Draft 7 schema profile.
- [Queues and Operations](references/guides/asyncapi/queues-and-operations.md): Describe shared queue destinations and select the jobs each application produces or consumes.
- [Bindings and Security](references/guides/asyncapi/bindings-and-security.md): Describe broker endpoints, authentication requirements, and protocol bindings without changing runtime infrastructure.
- [Serving Documentation](references/guides/asyncapi/serving.md): Export messaging contract files or serve protected JSON with explicit snapshot, caching, and failure behavior.
<!-- toc:end -->
