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

1. Wiring and cross-package patterns: a **guide** in `references/guides/`. For existing projects, read [Use in an Existing Project](references/guides/first-steps/existing-project.md); preserve their layout, module boundaries, and dependency ownership.
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
    Share `auth.NewSessionIdentity(load)` across authentication, login/logout, and OAuth handlers; check mutation errors. Default wiring is handle-free. Read [session login](references/guides/authentication/session-login.md) and [custom session selection](references/guides/authentication/session-login.md#custom-session-store) before selecting handles or composing multiple sessions: context slots are not cookie, storage, identity, or authorization isolation.
    Before upgrading session writers or supplying a custom cache driver, read [Sessions](references/guides/http-capabilities/sessions.md) for atomic guarded persistence and coordinated writer rollout. Unguarded writes bypass retirement protection.
    Share `feature.NewFlag(name, fallback)` across evaluation and memory `Set`/`SetFor`. Read JSON secrets with `secret.NewSecret[T](name)` and `Get(ctx, definition)`; raw `Reference`/`Value` stay unchanged. Invalid definitions fail before I/O; external schemas and JSON-compatible drift still need runtime checks. Read the service guide before namespace use: cache/feature/session default to `":"`; secret requires a separator. Keep deployed cache dots with `Separator(".")`.
    Before pinning secret versions, read [Managing Secrets](references/guides/secrets/managing.md#versions). Pinning requires retention of the same logical secret and does not fence permanent deletion and recreation.
   CAPTCHA uses vendor-local `cloudflarecaptcha`/`gcpcaptcha` adapters, not a core contract. Read [CAPTCHA](references/guides/abuse-prevention/captcha.md) before provider selection, token extraction/verification, route middleware, or Google score/step-up policy; it covers browser integration and security boundaries.
4. Return errors from route handlers; the router renders them as Problem Details. Add `router.Recover()` and call `r.Use` before registering routes.
    Construct reusable problems with `problem.New` and a `func(*problem.Builder)` callback; derive occurrences with `Details.Derive` and read standard members with accessors. Construction follows rule 2's callbacks/publication. New defaults status to 500 and title from final status; Derive preserves stored fields and inherited titles. Outer extension maps and header slices detach, not arbitrary nested values. `Builder.Error` retains a non-serialized cause; errors.Is matches stored Type/Status/Title and traverses that cause.
     [HTTP](references/guides/http-fundamentals/routing.md). Route annotations describe behavior without enforcing it; explicitly register cached OpenAPI JSON and Swagger handlers. [OpenAPI](references/guides/openapi/overview.md)
     Before iterator-based HTTP streaming, read [Responses](references/guides/http-fundamentals/responses.md) for producer shutdown: the response waits for producer exit, so blocking work must cooperate with cancellation and shutdown.
    AsyncAPI: declare immutable documents explicitly, not from runtime inventories; register JSON handlers separately. JSONTopic/JSONJob capture actual definitions only for JSON-envelope transports, with base64 wire payloads and separate logical schemas. Metadata changes no runtime behavior. [AsyncAPI](references/guides/asyncapi/overview.md)
    Webhook `Event.Map` preserves provider decoding and metadata for application-owned queue DTOs; acceptance no-ops return nil, not `ErrSkip`. [Receiving Webhooks](references/guides/http-capabilities/webhooks.md)
    Before checkout/refunds/subscriptions or uncertain-outcome recovery, read [Payments](references/guides/payments/overview.md): immutable `New*Request`, checked money, and original attempt/key reuse. Before client handoffs, read [Mobile Payments](references/guides/payments/mobile-payments.md) for application-owned authorization, pricing, secrets, and fulfillment. Read [Payment Drivers](references/guides/payments/drivers.md) for provider limits, separate store billing, and Mollie's unsigned callback hints requiring authenticated lookup.
5. Publish SQL `Query` values with `Table(name, configure...)` or `FromRaw(Raw(...), configure...)` (RawNamed is also supported).
   Use `Query.Derive` for independent variants; no callbacks preserves the base and its sticky errors. Publication detaches owned containers and retained root/predicate/conflict drafts, not arbitrary SQL argument objects.
   Only published statements compile or execute. Build with `BuildFor(dialect)` or the database facade, not ANSI `Build()`; paginate with a unique last `OrderBy`.
   SQL text telemetry is off by default. Before enabling `QueryText(true)`, read [Query Monitoring](references/guides/database/query-monitoring.md): it exports SQL verbatim, including literals and comments.
   Count and Seek return immutable Query values. EXISTS takes published Query.
   Each Scope gets an independent restricted PredicateBuilder, ANDed with other scopes and the whole user expression. Empty scopes add nothing. Ordinary groups accept variadic callbacks on one restricted draft and reject an empty result.
   Predicate drafts expose only predicates and nested groups. OnConflict returns a parent-attached `*Conflict`; DoUpdate/DoNothing return the parent `*Builder`. Keep typed bulk maps, Values/Only/Except, and existing append/replace semantics. [Query builder](references/guides/database/query-builder.md), [Pagination](references/guides/database/pagination.md)
    Schema `Create`, `CreateIfNotExists`, and `Alter` accept ordered declarations on one table draft. Omission is invalid, not a no-op; nil callbacks panic. Published operations detach table and child builders, not arbitrary default values. [Schema builder](references/guides/database/schema-builder.md)
 6. Test with memory drivers, fakes, and `router.Record`: [Facades and drivers](references/guides/core-concepts/facades-and-drivers.md).
7. Search: declare `search.NewIndex` values with `IndexBuilder` callbacks and queries with `QueryBuilder` callbacks; publish local variants with Query.Derive.
   Groups share a predicate-only `PredicateBuilder`: no ordering or OrWhereGroup; the final expression must contain conditions. Index lists accumulate and getters sort/deduplicate; Index.Err validates the completed schema/key rather than storing an incomplete draft error.
   Custom `Indexer` implementations need both `Err()` and `Schema()`. Read [Search](references/guides/search/overview.md) before implementing one.
   Query errors remain sticky through Derive; publication and Inspect detach owned nested/value-list containers.
   Append indexes with `search.Builder.Indexes` in the engine callback. Run `Sync` in a release command (or `compose.Startup`) and engine `Check` in `compose.Startup`, never as a recurring `compose.Check`; query declared fields within `search.MaxHits`.
   [Deploying search](references/guides/search/deploying.md)
8. Build `compose.MCPTools("app", configure...)` during composition to attach capture; `application.MCPToolsBuilder.Limits(application.CaptureLimits)` sizes it. Require a nonempty prefix; it directly names every app and telemetry tool and CLI group (`app.info`, `app.logs`, `app.traces`; `app:logs`). Explicitly register native tools on a protected loopback MCP endpoint; select a subset when needed. Add capability policy with `Tool.WithAvailability`; keep resource Gate checks in shared typed application methods. Discover with `arkoren tools`; inspect one tool with `arkoren public:routes --help`. Command results are JSON, follow is NDJSON, and errors are JSON on stderr; check exit status even when a result was printed. Help/prompts remain text and explicit key exports remain raw. Read [CLI output and failures](references/guides/cli/reference.md#output-and-failures) before parsing output. Namespaced metadata supplies qualified `group:operation` commands without changing exact tool names. Follow uses bounded cursor calls. Gate traffic with `arkoren app:ready --wait --timeout 30s`; add `--fail-fast` to ready/healthy waits for fresh failed checks. Never replay mutations. Read [Exposing Diagnostics](references/guides/observability/exposing-diagnostics.md) and [Diagnosing an Application](references/guides/observability/diagnosing-an-application.md).
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
    Per-call, SDK and suite options stay separate, including WithTransaction/WithSession. Before workflow telemetry, read [Workflows](references/guides/workflows/overview.md): configure each pod independently, instrument attempts not replay, and upgrade Temporal workers first. [Migrations](references/guides/database/migrations.md), [Diagnostic tools](references/guides/observability/diagnostic-tools.md), [Agents and tools](references/guides/llm/agents-and-tools.md)
12. MCP: `core/mcp` defines tools; `driver/mcp/mcpserver` serves HTTP. Start with [a first server and client connection](references/guides/mcp/first-server.md); direct `Tool.Call` is for unit tests.
    Register explicitly with `mcpserver.Builder.Tools`. Admit with `auth.Middleware(auth.Bearer(verifyAccessToken))`, then `auth.Require[User]()` before `routes.Mount("/mcp", endpoint)`; verifier returns the same `User` policies read. Optional `gate.Require(canUseMCP)` gates the endpoint. CLI `ARKOREN_MCP_TOKEN` supplies an existing bearer credential; acquisition/refresh is external. Keep SDK imports in composition/adapter; ark factories are application patterns.
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
[named-container validation guide](references/guides/validation/collections.md#checking-named-containers).
Eager collections preserve inferred wire shapes; use `NamedBuilder` to attach
runtime rules without weakening exact field-type checks. Lazy sources remain
opaque to inference.

## Reference index

<!-- toc:begin -->

### Guides

#### Meet Arkoren

- [Introduction](references/guides/about/introduction.md): What Arkoren is, the ideas behind its design, what ships in the box, and how this documentation is organized.
- [Should I Choose Arkoren?](references/guides/about/choosing-arkoren.md): Decide whether Arkoren fits your Go service, how to adopt it alongside existing packages, and which limits to evaluate first.
- [Contributing to Arkoren](references/guides/about/contributing.md): How the repository is laid out, how to build and test each module, and the rules a change must follow to be accepted.

#### First Steps

- [Use in an Existing Project](references/guides/first-steps/existing-project.md): Add Arkoren to an existing Go application without the CLI or a generated service.
- [Installation](references/guides/first-steps/installation.md): Bootstrap a project with the Arkoren CLI and add modules as you need them.
- [Creating a Project](references/guides/first-steps/creating-a-project.md): Generate a small HTTP service with direct composition, preview its files safely, and install dependencies.
- [Quickstart](references/guides/first-steps/quickstart.md): Bootstrap an HTTP service with arkoren new, then add routes, a cache counter, and error responses.
- [Configuration](references/guides/first-steps/configuration.md): Configure components and native drivers with construction callbacks, preserve defaults, and translate application settings.
- [Project Layout](references/guides/first-steps/project-layout.md): Organize a service by feature, with one package per feature that owns its routes, rules, and storage, and a main function that wires them.
- [Deploying to Production](references/guides/first-steps/deploying.md): Build a service into a small binary or container, configure it through the environment, and wire its probes and shutdown to your platform.

#### Concepts

- [Facades and Drivers](references/guides/core-concepts/facades-and-drivers.md): How a service is split into a contract, a facade your code uses, and drivers per vendor; swapping drivers and reaching the vendor client.
- [Contracts](references/guides/core-concepts/contracts.md): What a contract promises, how documented behavior and errors make drivers interchangeable, and how conformance suites and fakes enforce it.
- [Arks](references/guides/core-concepts/arks.md): What an ark is, how it differs from core packages and drivers, and the availability of built-in arks.
- [Common Pitfalls](references/guides/core-concepts/common-pitfalls.md): Mistakes that recur when wiring Arkoren services, why each one happens, and the habit that avoids it.

#### Application

- [Application Overview](references/guides/application/overview.md): Decide when to use the optional application coordinator and understand composition, startup, and shutdown.
- [Application Lifecycle and Ownership](references/guides/application/lifecycle-and-ownership.md): Order startup, supervise background work, and release each owned resource on failure or shutdown.
- [Application Composition](references/guides/application/composition.md): Share typed dependencies, give their construction one owner, and select optional features before startup.
- [Application Health and Readiness](references/guides/application/health-and-readiness.md): Keep unusable instances out of traffic, tune background checks, and inspect cached readiness safely.

#### Drivers

- [Choosing a Driver](references/guides/drivers/choosing-a-driver.md): Which driver to pick for each service, from the in-process drivers for development to the vendor drivers for production.
- [Writing a Driver](references/guides/drivers/writing-a-driver.md): Implement a service contract for a new vendor, follow the constructor and lifecycle conventions, map errors, and pass the conformance suite.

#### Fundamentals

- [Routing](references/guides/http-fundamentals/routing.md): Register routes and error-returning handlers, use wildcards, group routes under a prefix or middleware, and mount net/http handlers.
- [Requests](references/guides/http-fundamentals/requests.md): Read path and query parameters, headers, cookies, JSON and XML bodies, and file uploads, with size limits and errors that become responses.
- [Responses](references/guides/http-fundamentals/responses.md): Write JSON, text, HTML, and binary responses, redirect safely, stream data and server-sent events, and know when a response is written.
- [Error Handling](references/guides/http-fundamentals/error-handling.md): How returned errors become Problem Details responses, how to choose the status and fields, map domain errors, and keep details private.
- [Middleware](references/guides/http-fundamentals/middleware.md): Apply middleware to routers, groups, and routes, choose a safe order, and write your own when the shipped middleware does not fit.
- [Body Size Limit](references/guides/http-fundamentals/body-limit.md): Cap the size of request bodies for a router or a group, before the handler reads them, and allow larger uploads only where you need them.
- [Content Type Checks](references/guides/http-fundamentals/content-type.md): Reject request bodies whose media type an endpoint does not accept, which also blocks cross-site form posts to JSON APIs.

#### Capabilities

- [Sessions](references/guides/http-capabilities/sessions.md): Keep per-visitor state across requests in a cache-backed session, regenerate the ID on login, and understand when changes are saved.
- [Localization](references/guides/http-capabilities/localization.md): Choose the language of each request and translate validation messages, including your own rule codes, with catalogs that fall back safely.
- [Locale Detection](references/guides/http-capabilities/locale.md): Pick each client's language from a query parameter, a cookie, or Accept-Language, so validation messages are rendered in it.
- [Secure Links](references/guides/http-capabilities/secure-links.md): Create links that cannot be forged and can expire, be used once, or be revoked, for email verification, password reset, and downloads.
- [Receiving Webhooks](references/guides/http-capabilities/webhooks.md): Authenticate provider notifications, bind typed event handlers, and acknowledge acceptance without assuming provider retry guarantees.
- [Provider Webhooks](references/guides/http-capabilities/provider-webhooks.md): Choose a provider webhook adapter by its verification boundary and stable delivery identity, then use shared receipt policy without rewriting provider keys.

#### Networking

- [Running the Server](references/guides/http-networking/server.md): Serve a router with safe timeouts, bind the port at startup, drain requests on shutdown, run several servers, and end streams cleanly.
- [HTTP Client](references/guides/http-networking/http-client.md): Make outgoing HTTP calls more reliable with a retrying transport that respects idempotency, Retry-After, and deadlines, and traces attempts.
- [TLS Certificates](references/guides/http-networking/certificates.md): Provision local TLS identities, load existing certificates, and configure private CA trust and mutual TLS with native Go settings.

#### Browser Security

- [Browser Security Overview](references/guides/browser-security/overview.md): Choose browser protections for framing, cross-origin response access, and forged state-changing requests.
- [Secure Headers](references/guides/browser-security/secure-headers.md): Set response headers that block framing and content sniffing, then deliberately enable persistent HTTPS policy for your deployment.
- [CORS](references/guides/browser-security/cors.md): Allow browser applications on other origins to call your API, with explicit origins, methods, headers, credentials, and preflight.
- [CSRF Protection](references/guides/browser-security/csrf-protection.md): Stop other sites from making requests as your signed-in users with header-based checks that need no tokens, and allow trusted origins.

#### Abuse Prevention

- [Abuse Prevention Overview](references/guides/abuse-prevention/overview.md): Combine request limits and CAPTCHA verification to protect sensitive operations without treating either as identity or permission.
- [Rate Limiting](references/guides/abuse-prevention/rate-limiting.md): Limit how many requests a client can make in a time window, per route or service-wide, with standard headers and a shared counter.
- [CAPTCHA](references/guides/abuse-prevention/captcha.md): Verify Turnstile tokens or Google reCAPTCHA assessments before accepting a submission, with explicit action, hostname, and risk policies.

#### Authentication

- [Authentication Overview](references/guides/authentication/overview.md): How Arkoren finds out who sent a request: your own user type, authenticators tried in order, the auth middleware, and required-user routes.
- [Session Login](references/guides/authentication/session-login.md): Sign people in from a browser with an email and password, keep them signed in with the session, sign them out, and handle stolen sessions.
- [API Tokens](references/guides/authentication/api-tokens.md): Authenticate programs with bearer tokens, issue and store API keys safely, revoke them, and weigh self-contained tokens such as JWTs.
- [Browser Login with OAuth and OpenID Connect](references/guides/authentication/oauth.md): Sign people in through an external provider, map issuer and subject to a local user, and keep them signed in with a session.
- [OIDC Bearer Verification](references/guides/authentication/oidc-bearer.md): Verify provider-issued JWT access tokens for an API, require the exact identity type, and map verified claims to an application user.

#### Authorization

- [Authorization Overview](references/guides/authorization/overview.md): Decide what a signed-in user may do with plain Go rules, protect routes with the gate, and check resources in handlers.
- [Writing Policies](references/guides/authorization/policies.md): Organize authorization rules by feature in policy structs, and express roles, permissions, teams, and token scopes as plain Go.
- [Row-Level Access](references/guides/authorization/row-access.md): Keep which records a user may see in query scopes applied by the store, answer 404 for rows outside them, and make writes atomic.
- [Handling Denials](references/guides/authorization/denials.md): Pick the right status for a denied or failed action, shape the 403 response, and log or count denials with the gate's events.

#### Hashing

- [Hashing Overview](references/guides/hashing/overview.md): Store passwords so they cannot be recovered, check them at login, tell a wrong password from a broken hash, and know what needs one.
- [Choosing the Cost](references/guides/hashing/cost.md): Pick password-hashing settings by measuring them on production hardware, bound memory use, and protect login routes from floods.
- [Upgrading Hashes](references/guides/hashing/upgrading.md): Move stored password hashes to stronger settings after login, migrate safely from bcrypt to Argon2id, and handle imported hashes.
- [Hashing Drivers](references/guides/hashing/drivers.md): Choose Argon2id for new services or bcrypt for compatibility, install either driver, and set its memory, iterations, or cost.

#### Keys

- [Application Keys](references/guides/keys/overview.md): Choose, provision, and load typed shared, signing, and exchange keys for your application.

#### Encryption

- [Encryption Overview](references/guides/encryption/overview.md): Encrypt values that must be read back later, decrypt them into typed values, detect tampering, and decide when encryption is the right tool.
- [Encryption Keys](references/guides/encryption/keys.md): Generate encryption keys, load them from a secret store, rotate them without breaking stored data, and use one key per purpose.
- [Storing Encrypted Data](references/guides/encryption/storing.md): Store encrypted values in binary or text columns, find rows by an encrypted value with a keyed hash, and re-encrypt after a key rotation.
- [Encryption Drivers](references/guides/encryption/drivers.md): Choose AES-GCM or ChaCha20-Poly1305, install the driver, pass it a key, and bind it to a purpose with additional data.

#### Secrets

- [Secrets Overview](references/guides/secrets/overview.md): Read database passwords and API keys from a secret store at startup, keep them out of logs and responses, and use the memory driver in tests.
- [Managing Secrets](references/guides/secrets/managing.md): Create, update, and delete secrets through the facade, observe mutation outcomes without payloads, and read specific versions through the driver.
- [Rotating Secrets](references/guides/secrets/rotation.md): Replace credentials without downtime by overlapping old and new values, rotate encryption keys and signing secrets, and reload live.
- [Secrets Drivers](references/guides/secrets/drivers.md): Choose the secret store of your cloud, Vault, or the memory driver, install and connect each, and know how each handles deletion.

#### Database

- [Database Overview](references/guides/database/overview.md): Create a database facade, choose SQLite, PostgreSQL, or MySQL, manage its pool, and map rows to structs.
- [Query Builder](references/guides/database/query-builder.md): Build parameterized SQL with the query package, run it through the database facade into typed rows, and filter, join, upsert, and scope.
- [Raw SQL and Locking](references/guides/database/raw-sql-and-locks.md): Run hand-written SQL through the facade, mix raw fragments into the builder, lock rows for update, and take named locks across processes.
- [Schema Builder](references/guides/database/schema-builder.md): Create, alter, and drop tables, columns, indexes, and foreign keys with operations that compile to PostgreSQL, MySQL, or SQLite SQL.
- [Migrations](references/guides/database/migrations.md): Define schema changes as ordered migrations, run them at startup or from a command, roll them back, and understand locking and failures.
- [Migration Steps](references/guides/database/migration-steps.md): Build migration steps from schema operations with migrate.Schema, from SQL statements with migrate.Exec, or as functions, and combine them in one migration.
- [Pagination](references/guides/database/pagination.md): Page through query results by page number or cursor, parse page parameters from a request, return JSON, and process large sets in batches.
- [Transactions](references/guides/database/transactions.md): Group statements into one atomic unit with WithTransaction, join it through the context, nest, lock rows, retry, and react to commits.
- [Multiple Databases](references/guides/database/multiple-databases.md): Work with several databases in one application, split reads from writes with a replica, tell them apart in telemetry, and migrate each.
- [Query Monitoring](references/guides/database/query-monitoring.md): Observe the database with transaction and migration events and per-query spans and metrics, and log slow queries.
- [Database Drivers](references/guides/database/drivers.md): Choose between the SQLite, PostgreSQL, and MySQL database drivers, connect and configure each, and see how locking and upserts differ.

#### Validation

- [Validation Overview](references/guides/validation/overview.md): Build a schema, check typed input, and keep normalized values with Parse.
- [Validation Rules and Normalizers](references/guides/validation/available-methods.md): Common string, number, boolean, time, and file rules, with a complete rule index by subject.
- [Validation Structs and Reuse](references/guides/validation/structs-and-reuse.md): Configure nested fields with typed child builders, reuse published schemas, and distinguish omission from zero.
- [Validating Slices and Maps](references/guides/validation/collections.md): Configure collection children and assert membership, uniqueness, and matching counts without rewriting input.
- [Conditional Validation](references/guides/validation/conditions.md): Require, prohibit, exclude, or configure fields with typed conditions and nested Go field paths.
- [Validation Normalizers](references/guides/validation/normalizers.md): Trim text, clamp numbers, and clean lists before checking the parsed copy.
- [Validation Schemas and Helpers](references/guides/validation/schemas-and-helpers.md): Choose typed construction callbacks, run schemas, and use locale, clock, and duration helpers.
- [Encoded JSON Validation](references/guides/validation/encoded-json.md): Check an exact JSON document without defaults, normalization, or floating-point rounding.
- [Advanced Validation](references/guides/validation/advanced.md): Custom checks, error translation, files, JSON Schema limits, and safe schema ownership.

#### Cache

- [Cache Overview](references/guides/cache/overview.md): Create a cache, store and read typed values with expiry, compute them on a miss with Remember, and learn what relies on the cache.
- [Cache Counters](references/guides/cache/counters.md): Keep atomic integer counters in the cache, including counters that expire with their window through `IncrementWithTTL`.
- [Cache Locks](references/guides/cache/locks.md): Take a named lease in the cache so one process at a time does a piece of work, extend it, and choose between cache and database locks.
- [Cache Drivers](references/guides/cache/drivers.md): Compare cache driver capabilities, multi-instance coordination, persistence, and operational requirements.

#### Search

- [Search Overview](references/guides/search/overview.md): Define an index for a document type, register it with a search engine, put documents in, and search them with typed hits and pages.
- [Search Queries](references/guides/search/queries.md): Write search queries with text syntax for words, phrases, exclusions, and prefixes, add filters, order results, and page within 1,000 hits.
- [Deploying Search](references/guides/search/deploying.md): Create and update search indexes with Sync, verify them with Check, and wire both into single- and multi-instance deploys.
- [Search Drivers](references/guides/search/drivers.md): The memory and Postgres search drivers, how to wire and test with them, the OpenSearch driver, and the Meilisearch driver.

#### Storage

- [File Storage Overview](references/guides/file-storage/overview.md): Store, read, list, and delete files behind one API on memory, the local filesystem, and Amazon S3, with portable keys and testable errors.
- [Uploads and Downloads](references/guides/file-storage/uploads-and-downloads.md): Accept uploaded files with size limits and safe generated keys, and serve them back as downloads or inline content with range support.
- [Signed URLs](references/guides/file-storage/signed-urls.md): Hand clients a temporary URL to download or upload one object directly against the storage backend, binding an upload's type and size.
- [File Storage Events and Telemetry](references/guides/file-storage/events-and-telemetry.md): React to objects written and deleted with events, see storage operations as spans and duration metrics, and what is never recorded.
- [File Storage Drivers](references/guides/file-storage/drivers.md): Choose between the memory, local filesystem, and Amazon S3 storage drivers, connect and configure each, and use S3-compatible services.

#### Collections

- [Collections Overview](references/guides/collections/overview.md): Transform, filter, group, and aggregate slices and maps with chainable generic collections, and stream large data lazily.
- [Lazy Collections](references/guides/collections/lazy-collections.md): How lazy collections defer work until you read the result, stop early, keep indexes through a chain, and behave when iterated twice.
- [Fallible Collections](references/guides/collections/fallible-collections.md): Process sources that can fail halfway, including database cursors, storage listings, model streams, and files, with TryLazySlice and TryLazyMap.
- [Available Collection Methods](references/guides/collections/available-methods.md): A short entry for every function and method of the collection package, with the types it works on and a tiny example.

#### Feature Flags

- [Feature Flags Overview](references/guides/feature-flags/overview.md): Create a feature flag client, evaluate flags for a subject with a safe default, read variants and reasons, and know the guarantees.
- [Feature Flag Testing](references/guides/feature-flags/testing.md): Set flags in application tests with the memory driver, and check a flag driver against the contract with the featuretest suite.
- [Feature Flag Drivers](references/guides/feature-flags/drivers.md): Choose between the memory, OpenFeature, and LaunchDarkly drivers, then wire and configure each; OpenFeature reaches flagd and others.

#### Events

- [Events Overview](references/guides/events/overview.md): Create the in-process event bus, define typed events, emit and listen to them, and understand ordering, synchronous delivery, and failures.
- [Writing Listeners](references/guides/events/listeners.md): Register typed and pattern-matching listeners, remove them, and write listeners that stay short, tolerate failure, and are safe concurrently.
- [Built-in Events](references/guides/events/built-in-events.md): Every event Arkoren's own services publish, grouped by service, with the option that enables it, when it fires, and its payload.
- [HTTP Events](references/guides/events/http-events.md): Observe HTTP server, client, middleware, session, signed-link, and webhook outcomes with their publishing conditions and payload limits.
- [Service Operation Events](references/guides/events/service-operations.md): Observe payment calls, push submissions, search administration, and secret mutations without treating API results as durable business transitions.
- [Auditing with Events](references/guides/events/auditing.md): Record application facts with listeners and distinguish best-effort trails from required durable audit records.

#### Broadcasting

- [Broadcasting Overview](references/guides/broadcasting/overview.md): Publish typed facts to other processes and listen by name or wildcard, with delivery semantics, listener errors, wiring, and testing.
- [Broadcast Metadata and Telemetry](references/guides/broadcasting/metadata-and-telemetry.md): How the trace context of a publish reaches listeners through message metadata, the spans and metrics recorded, and how to test it.
- [Broadcast Drivers](references/guides/broadcasting/drivers.md): Choose between the memory, Redis, NATS, RabbitMQ, MQTT, and SNS broadcast drivers, and install, connect, and run each.

#### Mail

- [Mail Overview](references/guides/mail/overview.md): Compose a message, send it through the mailer, add attachments and inline images, understand validation, errors, and receipts, and test it.
- [Mail Configuration](references/guides/mail/configuration.md): Set sender and reply-to defaults, override them per message, connect the mailer to events and telemetry, and keep credentials out of logs.
- [Mail Events and Telemetry](references/guides/mail/events-and-telemetry.md): Observe returned submission outcomes with events, measure send attempts, and understand panic and privacy boundaries.
- [Mail Drivers](references/guides/mail/drivers.md): Choose between the log, SMTP, Resend, Mailgun, Postmark, and Amazon SES mail drivers, and install, connect, and configure each.

#### Push

- [Push Overview](references/guides/push/overview.md): Submit push notifications through a portable sender and distinguish provider acceptance from browser display.
- [VAPID Keys](references/guides/push/vapid-keys.md): Generate a persistent P-256 identity once, load it safely, and publish only its Web Push public key.
- [Browser Integration](references/guides/push/browser-integration.md): Connect a browser subscription, an authenticated registration endpoint, and a service worker that displays Web Push notifications.
- [Testing Push](references/guides/push/testing.md): Assert submitted messages with pushtest.Fake and verify browser display separately from provider acceptance.
- [Push Drivers](references/guides/push/drivers.md): Choose the in-process test fake or standards-based Web Push, and configure secure provider submission.

#### Payments

- [Payments Overview](references/guides/payments/overview.md): Start hosted checkout, observe payment outcomes, issue refunds, and manage provider subscriptions without a local billing engine.
- [Testing Payments](references/guides/payments/testing.md): Exercise explicit local lifecycles or script provider outcomes without moving money.
- [Mobile Payments](references/guides/payments/mobile-payments.md): Prepare a client payment handoff, complete an approved checkout when required, and verify payment through the Go facade.
- [Reconciling Mollie Callbacks](references/guides/payments/mollie-callbacks.md): Treat unsigned Mollie callbacks as hints and verify persisted payment ownership and provider state before fulfillment.
- [Payment Drivers](references/guides/payments/drivers.md): Compare memory, Stripe, PayPal, and Mollie for hosted checkout, native mobile payment initialization, refunds, and subscriptions.

#### Queues

- [Queues Overview](references/guides/queues/overview.md): Define typed background jobs, dispatch them in one process and handle them in another, and understand delivery and retry limits.
- [Producing Jobs](references/guides/queues/producing-jobs.md): Define jobs with a stable name, typed payload, queue, retries, backoff, and timeout, then dispatch them and know what a dispatch promises.
- [Consuming Jobs](references/guides/queues/consuming-jobs.md): Register handlers, run a consumer, choose its queues, and control retry, reject, and permanent failure, including timeouts and shutdown.
- [Queue Metadata and Telemetry](references/guides/queues/metadata-and-telemetry.md): How a job's trace context travels in message metadata, the spans and metrics dispatching and consuming record, and how to test it.
- [Queue Drivers](references/guides/queues/drivers.md): Choose between the memory, RabbitMQ, NATS JetStream, Amazon SQS, and Cloudflare queue drivers, and install and configure each.

#### Scheduling

- [Scheduled Tasks Overview](references/guides/scheduled-tasks/overview.md): Define named calendar entries and typed one-off tasks, run them with a scheduler, control overlapping runs, and test with memory.
- [Time Zones and DST](references/guides/scheduled-tasks/time-zones-and-dst.md): Choose the time zone of a schedule, what happens to a wall-clock time skipped or repeated when clocks change, and which drivers differ.
- [Scheduled Task Errors](references/guides/scheduled-tasks/error-handling.md): What happens when an entry or task fails, panics, times out, or chooses not to run, how failures are reported, and what drivers do next.
- [Scheduler Drivers](references/guides/scheduled-tasks/drivers.md): Choose between the memory, Temporal, and Restate scheduler drivers, with pending Hatchet integration notes.

#### Workflows

- [Workflows Overview](references/guides/workflows/overview.md): Define workflows from steps, signals, sleeps, and child workflows, register them with a driver, start and signal runs, and know the rules.
- [Workflow Errors](references/guides/workflows/error-handling.md): How steps retry and when to stop, which errors end a run, how panics and timeouts surface, and why messages must not hold secrets.
- [Testing Workflows](references/guides/workflows/testing.md): Test code that starts workflows with a fake driver, and workflow logic on the memory driver with instant time.
- [Shared Worker Host](references/guides/workflows/shared-worker-host.md): Share a host between Temporal or Restate drivers, with pending Hatchet host notes.
- [Recovering Hatchet Runs](references/guides/workflows/hatchet-recovery.md): Pending, unpublished Hatchet integration notes on execution identity, signal history, and safe worker replacement.
- [Workflow Drivers](references/guides/workflows/drivers.md): Choose between the memory, Temporal, and Restate workflow drivers, with pending Hatchet integration notes.

#### Language Models

- [Language Models Overview](references/guides/llm/overview.md): Create a model, run an agent, generate a single turn, keep a conversation, handle errors, and see what is reported.
- [Agents and Tools](references/guides/llm/agents-and-tools.md): Give a model functions to call, define an agent that runs the tool loop, and control steps, concurrency, and errors.
- [Structured Output](references/guides/llm/structured-output.md): Make an agent return a typed Go value validated against a schema, with retries on invalid output.
- [Streaming Replies](references/guides/llm/streaming.md): Stream model output and agent progress as it arrives, and send it to a browser with response.SSEJSON.
- [Embeddings](references/guides/llm/embeddings.md): Turn text into vectors with an Embedder, choose dimensions and purpose, and understand batching.
- [Testing Language Models](references/guides/llm/testing.md): Test code that calls a model with a scripted llmtest.Fake, assert on requests, inject failures, and run the conformance suite.
- [Local Development with Ollama](references/guides/llm/local-development-with-ollama.md): Run models on your own machine with Ollama and point the OpenAI driver at it, including Chat Completions mode.
- [Language Model Drivers](references/guides/llm/drivers.md): Install, connect, and configure the OpenAI driver, which also serves Ollama, vLLM, and other compatible servers.

#### MCP

- [MCP Overview](references/guides/mcp/overview.md): Understand how typed application operations become discoverable MCP tools, then choose your implementation path.
- [Your First MCP Server](references/guides/mcp/first-server.md): Install the MCP modules, serve a tool over local HTTP, and discover and call it with an MCP client.
- [MCP Tools](references/guides/mcp/tools.md): Define validated application operations, test their behavior, and serve them through your existing HTTP service.
- [Securing MCP](references/guides/mcp/securing-mcp.md): Authenticate callers before dispatch and enforce TLS, Host, Origin, and remote OAuth boundaries outside the MCP adapter.
- [MCP Availability and Authorization](references/guides/mcp/availability-and-authorization.md): Offer capabilities by role or plan while keeping resource authorization in shared application services.

#### Skills

- [Working with Agents](references/guides/agent-skills/working-with-agents.md): Install the Arkoren agent skill offline with the CLI or from GitHub with gh or npx skills, and read the documentation through llms.txt and raw Markdown pages.

#### Observability

- [Telemetry](references/guides/observability/telemetry.md): How Arkoren reports traces, metrics, and log correlation, how to wire a driver into the application, and how to add your own.
- [Instrumented Components](references/guides/observability/instrumented-components.md): Look up built-in trace and metric names, labels, configuration, and observation boundaries.
- [Request Logging](references/guides/observability/request-logging.md): Log every failed request with its method, path, status, and error, correlated with traces, using any slog logger.
- [Request Tracing](references/guides/observability/request-tracing.md): Trace incoming requests with the server middleware and outgoing ones with the instrumented transport and correlate logs.
- [Telemetry Privacy](references/guides/observability/telemetry-privacy.md): Review recorded fields, trace propagation, log exposure, and telemetry export controls.
- [Telemetry Drivers](references/guides/observability/telemetry-drivers.md): Choose between the memory, OpenTelemetry, and Prometheus telemetry drivers, configure each, combine them, and run a collector locally.
- [Exposing Diagnostics](references/guides/observability/exposing-diagnostics.md): Attach bounded capture before startup, select owner tools, and register them on a protected MCP endpoint.
- [Diagnosing an Application](references/guides/observability/diagnosing-an-application.md): Find a route, correlate a failed request with traces and logs, check health, and control explicit operator actions.
- [Diagnostic Tools](references/guides/observability/diagnostic-tools.md): Look up owner-provided tools, prefixes, capture limits, condition failures, and bounded cursor behavior.

#### CLI

- [Arkoren CLI Overview](references/guides/cli/overview.md): Install the Arkoren CLI to call MCP tools or provision projects, keys, certificates, and bundled agent skills offline.
- [Connecting the CLI](references/guides/cli/connecting.md): Select a trusted MCP endpoint, supply an existing credential, and verify discovery.
- [Calling Tools](references/guides/cli/calling-tools.md): Discover available tools, construct inputs, and interpret native MCP results from the CLI.
- [Waiting and Following](references/guides/cli/waiting-and-following.md): Gate automation on observed conditions and follow bounded event or log records without hiding lost history.
- [CLI Reference](references/guides/cli/reference.md): Look up CLI commands, reserved flags, input conversion, result rendering, exit statuses, and retry rules.
- [Key Commands](references/guides/cli/keys.md): Generate keys offline, validate stored material, and export PEM or VAPID public keys.
- [Certificate Commands](references/guides/cli/certificates.md): Generate and check local TLS files offline, explicitly install or remove a root CA, and recover from partial operations.

#### Testing

- [Testing Overview](references/guides/testing/overview.md): How Arkoren services are tested with the standard testing package, real facades on in-memory drivers, and no mocking framework.
- [HTTP Tests](references/guides/testing/http-tests.md): Test routes, handlers, and middleware by sending requests through the router without a server, and check statuses, headers, and JSON.
- [Testing Authentication](references/guides/testing/auth-tests.md): Test routes that need a signed-in user with the request context or a test authenticator, and check your own authenticators.
- [Fakes](references/guides/testing/fakes.md): Test code that sends mail or push notifications, dispatches jobs, publishes messages, schedules tasks, starts workflows, or calls models, with fakes.
- [Controlling Time](references/guides/testing/time.md): Test expiry, timeouts, retries, and schedules in zero real time with testing/synctest, and pin the clock used by validation.
- [Integration Tests](references/guides/testing/integration-tests.md): Test against real databases, caches, and brokers started with Docker Compose, skip them with -short, and run them in CI.
- [Driver Conformance Suites](references/guides/testing/conformance-suites.md): Run a service's conformance suite to check portable contract behavior in new drivers and wrappers around existing ones.

#### OpenAPI

- [OpenAPI Overview](references/guides/openapi/overview.md): Describe your first HTTP endpoint and export its OpenAPI contract without starting a server.
- [Describing Operations](references/guides/openapi/operations.md): Describe request bodies, parameters, responses, examples, and security without changing handler behavior.
- [Serving OpenAPI Documentation](references/guides/openapi/serving.md): Export contract JSON, serve it on an explicit route, and add a locally bundled Swagger UI with appropriate access controls.
- [Documenting Built-in Handlers](references/guides/openapi/built-in-handlers.md): Annotate application probes and information endpoints, and describe integrations explicitly where no contract can be inferred.

#### AsyncAPI

- [AsyncAPI Overview](references/guides/asyncapi/overview.md): Describe the messages your application sends and receives, then generate your first AsyncAPI document without a broker connection.
- [Messages and Schemas](references/guides/asyncapi/messages-and-schemas.md): Distinguish logical values from wire envelopes, attach examples, and choose a supported schema for each message.
- [Queues and Operations](references/guides/asyncapi/queues-and-operations.md): Describe several job types on one physical queue and distinguish the messages each application sends or receives.
- [Bindings and Security](references/guides/asyncapi/bindings-and-security.md): Describe broker endpoints, protocol-specific metadata, and authentication requirements without configuring runtime security.
- [Serving AsyncAPI Documentation](references/guides/asyncapi/serving.md): Export offline JSON or explicitly serve a protected AsyncAPI endpoint, with construction and cache behavior made explicit.
<!-- toc:end -->
