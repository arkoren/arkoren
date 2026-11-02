# Arkoren Guides

How the guides under `cmd/arkoren/skills/arkoren/references/guides/` are organized and
written. The same files are the agent skill (read offline) and the website's
`/docs/guides/...` pages, so every rule here serves both readers.

## Contents

- [Consumer audience](#consumer-audience)
- [The tree](#the-tree)
- [Files and frontmatter](#files-and-frontmatter)
- [Page shape](#page-shape)
- [Drivers pages](#drivers-pages)
  - [Compatibility matrices](#compatibility-matrices)
  - [Per-driver structure](#per-driver-structure)
  - [Additional capabilities](#additional-capabilities)
- [Voice and depth](#voice-and-depth)
- [Code examples](#code-examples)
- [Links](#links)
- [After writing](#after-writing)

## Consumer audience

Guides teach consumers how to understand and use Arkoren in their own
applications. Explain the capability, when it helps, how to configure and use
it, and the behavior or constraints that affect the application.

Use published import paths and paths in the reader's own project when needed.
Arkoren's source folders, internal dependency rules, package-maintenance duties,
and contributor build/test workflows are not consumer guidance. Keep those in
explicit contributor documentation, such as the contributing guide. Extending
Arkoren through public interfaces, including writing a custom driver for an
application, is still a consumer task and must not require repository knowledge.

For each section, ask what the consumer can understand, decide, or do with it.
Remove details that only help someone modify the toolkit. Describe architecture
through observable responsibilities and application integration, not through a
tour of Arkoren's repository. For unavailable features, state their availability
and owner-confirmed plans without inventing installation commands or APIs.

## The tree

The guides are one level of **groups**, each a folder of pages, ordered as a
learning path. Consecutive groups share a **section**, a short subject
sidebar heading that only labels them: it is not a folder, a page, or collapsible.
The website sidebar shows every group as a collapsible list under its section,
collapsed except for the group of the current page. There are no topics or
nested folders: a subject big enough for several pages becomes its own group.
Guides teach application tasks; Go doc comments and pkg.go.dev provide API
declarations and never replace a guide. The website API catalog links canonical
pkg.go.dev module pages by family; do not add a parallel local API corpus.

| Section | Group (folder) | Order | What belongs there |
| --- | --- | --- | --- |
| Getting Started | `about` | 10 | Introduction, contributing, release notes and upgrading. |
| Getting Started | `first-steps` | 20 | Installation and existing-project adoption, creating projects, quickstart, configuration, project layout, deployment. |
| Getting Started | `core-concepts` | 30 | Facades and drivers, contracts, arks, common pitfalls. |
| Getting Started | `application` | 32 | Application overview, typed composition, lifecycle and ownership, health and readiness. |
| Getting Started | `drivers` | 35 | Choosing a driver, writing a driver. |
| HTTP | `http-fundamentals` | 40 | Routing, requests, buffered responses, streaming responses, error handling, middleware, body limits, and content types. |
| HTTP | `http-capabilities` | 43 | Sessions, localization including locale detection, and signed links. |
| HTTP | `webhooks` | 44 | Receiving webhooks, provider adapters, and delivery policies. Provider-specific business reconciliation stays beside its service. |
| HTTP | `http-networking` | 46 | Running the HTTP server and making outgoing HTTP calls. |
| Security | `browser-security` | 52 | Overview, secure headers, CORS, and CSRF protection. |
| Security | `abuse-prevention` | 55 | Overview, rate limiting, and CAPTCHA. |
| Security | `authentication` | 60 | Overview, session login, API tokens, OAuth login, and OIDC bearer verification. |
| Security | `authorization` | 65 | Overview (rules and the gate), policies, row-level access, denials. |
| Security | `hashing` | 70 | Overview, choosing the cost, upgrading hashes, drivers. |
| Security | `keys` | 72 | Typed application keys, provisioning, and loading. |
| Security | `encryption` | 73 | Overview, keys, storing encrypted data, drivers. |
| Security | `secrets` | 76 | Overview, managing secrets, rotation, drivers. |
| Data | `database` | 80 | Facade overview, drivers, transactions, locking, batching and streaming, monitoring. |
| Data | `queries` | 81 | Query builder, raw SQL, pagination. |
| Data | `migrations` | 82 | Migration overview and steps, schema builder, deployment, troubleshooting. |
| Data | `validation` | 85 | Overview, scalar rules, structs and reuse, slices and maps, conditions, normalizers, custom rules, validation errors, JSON Schema, and exact encoded JSON. |
| Data | `cache` | 90 | Facade overview, drivers, key definitions and namespaces, counters, locks, monitoring. |
| Data | `search` | 97 | Facade overview, drivers, indexing documents, search queries, deployment, monitoring. |
| Data | `file-storage` | 100 | Overview, uploads and downloads, signed URLs, events and telemetry, drivers. |
| Data | `collections` | 102 | Overview, lazy collections, fallible collections, available methods. |
| Application | `feature-flags` | 105 | Overview, testing, drivers. |
| Application | `events` | 110 | The in-process event bus. |
| Application | `broadcasting` | 120 | Facade overview, drivers, message delivery, monitoring and metadata propagation. |
| Application | `mail` | 130 | Facade overview, drivers, composing messages, sender defaults, events and telemetry. |
| Application | `push` | 132 | Visible notifications, browser subscriptions and service workers, retention, testing, and Web Push drivers. |
| Application | `payments` | 135 | Facade overview, drivers, hosted checkout, mobile payments, refunds, recurring subscriptions, payment recovery, Mollie callbacks, testing. |
| Background | `queues` | 140 | Facade overview, drivers, producing jobs, consuming jobs, delivery and retries, monitoring. |
| Background | `scheduled-tasks` | 150 | Overview, drivers, calendar schedules, deferred tasks, time zones and DST, error handling. |
| Background | `workflows` | 160 | Overview, drivers, workflow steps, signals and waiting, running workflows, worker hosts shared with Scheduling, errors, testing. Pending vendor development notes stay outside consumer navigation. |
| AI | `llm` | 165 | Facade overview, drivers, conversation history, running agents, defining tools, structured output, streaming replies, embeddings, local development, testing. |
| AI | `mcp` | 167 | Overview, first HTTP server through client verification, tools (definition and serving together), endpoint security, and availability/resource authorization. Keep direct Go calls in testing, after a working MCP connection. |
| AI | `agent-skills` | 169 | Installing and using the Arkoren documentation skill and website Markdown indexes. |
| Operations | `observability` | 170 | Telemetry, drivers, instrumented components, request logging, request tracing, privacy. |
| Operations | `diagnostics` | 172 | Capture setup, application diagnosis, diagnostic tool lookup. |
| Operations | `cli` | 175 | Overview, connecting, calling tools, waiting and following, and reference. |
| Operations | `testing` | 180 | Overview, HTTP tests, testing auth, fakes, time, integration tests, driver conformance. |
| Documentation | `openapi` | 190 | HTTP contract overview, operation descriptions, JSON and Swagger serving, and built-in handler annotations. |
| Documentation | `asyncapi` | 200 | Messaging contract overview, messages and schemas, queue operations, bindings and security, and JSON serving. |

A service with several distinct reader tasks can justify its own group. Follow
[Category Review](page-writing.md#category-review) to decide its pages and order;
add an overview only when it teaches a necessary mental model, and put driver
setup where the reader needs it rather than always last. A small service can
remain one page in the group whose theme it fits, with a `Drivers` section when
needed. A new group needs an agreed decision; add its row here in an existing
section when one fits. A new section is rare: use a short subject label.

Organize guides by reader task, not implementation mechanism. Middleware
composition belongs in HTTP Fundamentals; individual middleware guides belong
with browser security, abuse prevention, localization, or observability as
appropriate. Keep API references aligned with package paths. For unpublished
guide reorganizations, update links directly without adding legacy redirects.

## Files and frontmatter

- `guides/<group>/index.md` holds only frontmatter: `title` (the sidebar
  group name), `description` (one sentence, used by the website's group card
  and `llms.txt`), `order`, `section` (its row's section in the table above;
  sections follow the learning order). It is metadata, not a page; keep its body empty.
  Add it with the group's first page, never for an empty group.
- `guides/<group>/<page>.md` is a page. Nothing nests deeper.
  A group's first page is usually `overview.md`.
- Every page has `title`, `description`, and `order`. Orders step by 10 so
  pages can be inserted.
- Completed new or materially revised pages require `summary`; follow
  [Page Summary](page-writing.md#page-summary) for its meaning, timing, and format.
  Group indexes and unfinished placeholders do not need a summary.
- The website route is the path without `.md`: `guides/cache/counters.md` is
  `/docs/guides/cache/counters`.
- A page belongs to its source folder's group. When moving it, update every
  relative link, website link, and agent pointer to its new path. Navigation has
  no group override; source paths determine HTML and Markdown URLs.
- Split a page when its subjects require different reader tasks, not merely
  to satisfy a size target.

### Titles and sidebar labels

- `title` is the page heading (the `# H1`, which must match it), the browser
  title, and the pager label. `nav` is the shorter sidebar label, set only
  when it differs from the title.
- Use Title Case for titles, headings, `nav` labels, and group names ("Query
  Builder", "Reading Data"). Preserve official names and code identifiers.
- A label must make sense under its group name without the page open:
  "Request Tracing", not "HTTP", under Observability.
- Keep titles, headings, and labels short: usually two or three words, four
  when needed for clarity. Context-clear titles such as `Overview` or `Drivers`
  need not repeat the category name. Qualify an ambiguous title when it helps
  readers identify the subject; it is not a universal naming requirement.
  Use "&" only in labels.
- Write our own names. Laravel's docs are a model for structure and rhythm,
  but do not copy their section or page names ("Prologue", "The Basics",
  "Digging Deeper", "Directory Structure").

## Page shape

Follow [Writing a Page](page-writing.md) for the mandatory authoring workflow.
Use [Page Patterns](page-patterns.md) and the [review's page types](revision-and-links.md#page-types)
to select a useful shape, not a list of sections to fill. Opening prose can sit
directly below the H1. A facade overview teaches the facade; driver-specific
setup and lifecycle belong on Drivers unless needed for the page's own task.

Keep choices before their implementation, prerequisites before use, and
consequential constraints beside the affected action. Add testing, operations,
or advanced sections only when they belong to this page's agreed scope.

End a package guide with the relevant pkg.go.dev package. For a non-package
guide, link the immediate next task when useful; do not invent an API destination.
For example, in `guides/queues/`:

```markdown
For every function and option, see [the `queue` API reference](https://pkg.go.dev/arkoren.dev/core/service/queue).
```

Use GitHub alerts for asides, on their own line:

```markdown
> [!NOTE]
> A clarification the reader needs but might miss.

> [!WARNING]
> A concrete risk, its cause, and the safe action.
```

## Drivers pages

A group's drivers live on its `drivers.md` page, ordered where setup serves
the reader journey; a single-page service in a shared group can use a
`## Drivers` section instead. The page opens by comparing when to choose each
driver, without requiring an `Introduction` heading, then one `##` per
driver, in-process first when available. Use the repeated provider sections in
`cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md` as the structural model, not
as proof that every provider supports the same operations or settings.

The `drivers` group holds only the pages about drivers in general. Point the
"Docs" buttons on Choosing a Driver at the driver's section on its group's
drivers page.

### Compatibility matrices

Every service drivers page, including `observability/telemetry-drivers.md`,
requires a compatibility matrix after its introduction and before provider setup.
An embedded `Drivers` section requires the same comparison before provider setup.
General driver-writing and discovery catalogs link these service comparisons
instead of duplicating them.

On every edit to a drivers page, recheck its matrix against the provider sections
and current implementation/tests. When a contract, driver, configuration default,
or shipped companion capability changes, update the affected matrix in the same
change. Account for every documented driver, including in-process implementations.
Review the matrix as part of G11; presence checks cannot verify behavioral truth.

Use a `Compatibility matrix` heading, linked driver names in rows, and meaningful
capabilities in columns. Compare behavior exposed by Arkoren, not the vendor's
entire product. Explain the comparison's scope in adjacent prose. Distinguish
shared coordination from independent replicas and conditional persistence from
guaranteed durability. Preserve essential limits in prose rather than hiding
them only in tooltips.

Each capability cell contains plain `Yes`, `No`, `Unknown`, or `Partial`, optionally
followed by ` — ` and a useful qualification. `No` means unsupported, `Partial`
means conditional or limited support, and `Unknown` means not established; explain
the uncertainty. Keep formatting and links out of capability cells so the shared
renderer recognizes them. Raw Markdown must remain readable without the website.

The shared website renderer and styles own presentation for every matrix:

- Use compact actual SVG icons: check for Yes, cross for No, question mark for
  Unknown, and a half-filled circle for Partial. Keep accessible status names,
  row/column associations, and horizontal keyboard scrolling on narrow screens.
- Plain status icons are neutral and noninteractive, with no tooltip or tab stop.
  Qualified icons are controls in the rose accent (`var(--primary-muted)`), so
  readers can distinguish extra information. Keep the hover background transparent.
- Show a tooltip only for a useful qualification, never merely to repeat a status.
  Hover and focus wait 300ms; tap opens immediately. Use a 10px offset and 120ms
  fade/slide entry and exit, respecting reduced motion. Support keyboard focus,
  Escape dismissal, and moving the pointer into the tooltip.
- Reuse the shared renderer, tooltip controller, and CSS; use contextual tooltips
  instead of a typed legend. Keep collection rendering deferred so unchanged
  Markdown cannot retain obsolete rendered icons after plugin changes.

Vendor-local HTTP adapters without a service contract or a related contract
capability use the relevant feature guide instead of a contract's drivers page.
For example, CAPTCHA belongs in `abuse-prevention/captcha.md`, with provider choices
before setup and examples. List these integrations under Choosing a Driver's
protocol adapters and link their pkg.go.dev package pages; do not invent a core API
or memory driver for navigation.

### Per-driver structure

Each driver section covers these responsibilities in reader order. Use parallel
subsection labels across providers where they have the same needs; combine brief
explanations or omit inapplicable headings rather than adding empty sections.
Task and capability headings name the actual work. For an embedded `## Drivers`
section, shift provider and subsection headings down one level, staying within H4.

1. **Provider introduction:** what it is, when to choose it, and provider-side
   prerequisites such as an account, verified domain, service, or permissions.
2. **Installation:** the canonical module `go get` command, run from the
   application's module directory. For a bundled in-process driver, state that
   it is included and omit a redundant installation subsection.
3. **Connecting:** show the exact import path and a focused constructor example.
   Name the credential/configuration inputs, establish facade wiring or link
   wiring already introduced, and explain construction errors and any required
   lifecycle hooks. Distinguish construction from connecting over the network.
4. **Configuration:** show meaningful settings as code and explain their
   defaults and effects. Cover required versus optional settings, timeouts,
   retry policies, and native-client configuration/ownership when relevant.
   If a driver has no optional settings, omit the configuration subsection;
   state that fact beside construction when useful. Do not invent builder
   methods or a native-client escape hatch.
5. **Task examples:** use an action heading such as `Sending mail`, `Creating a
   checkout`, or `Receiving notifications`, not a generic `Examples` dump.
   Establish the situation, show the smallest useful operation, and explain the
   result. When ordinary use is identical through the contract, link the shared
   guide's concrete task example instead of repeating it for every provider.
   Provider-specific operations and requirements need their own examples here.
6. **Behavior and limits:** explain differences from the portable contract,
   unsupported operations, provider constraints, result/identifier semantics,
   and relevant failure, concurrency, or production behavior. Keep essential
   safety constraints beside the first affected example, not only in this section.
7. **Additional capabilities:** add a task-named subsection for each shipped
   provider capability readers need to discover, such as webhook notifications,
   inbound mail, or native SDK operations outside the contract. Follow the
   capability guidance below. Omit this part when there are no additional
   capabilities; do not add an empty `Additional capabilities` heading.
8. **Running locally:** when supported, give the compose/emulator setup, local
   connection example, success check, and cleanup that consumers can reproduce
   without cloning Arkoren. Provide the needed configuration or consumer tooling;
   repository integration-test commands belong in contributor documentation.
   Otherwise omit this subsection rather than inventing an emulator or lifecycle.
9. **API references:** end with the driver's pkg.go.dev page and links to any companion
   adapter references discussed in the section. Link detailed references inline
   as needed as well; an API link does not replace setup or a teaching example.

Configuration and task examples may share the setup established in Connecting.
Avoid repeating entire constructors merely to demonstrate the same wiring.
Imports, assumed variables, consequential error checks, and interpretation follow
[Examples and reference entries](examples-and-reference.md#example-completeness).

### Additional capabilities

Before writing or reviewing a drivers page, inspect the relevant vendor packages,
their public APIs, examples, and existing guides. Account for every driver
and shipped companion capability relevant to the contract. A driver constructor
alone is not sufficient coverage. Record this inventory in the review summary;
apply [G11](revision-and-links.md#rules) separately to each provider. A capability
that exists but is undocumented is a review failure, not an N/A. Inventory what
Arkoren ships or exposes, not the provider's entire product catalog or native
SDK. Do not present provider-only features as portable contract operations.

For each capability, explain when to use it, whether it is optional, its package
and installation, required configuration, a focused integration example, the
observable result, and the relevant security/failure constraints. Keep extended
generic behavior in its canonical guide, linked where needed. Document enough
provider-specific setup here to use that guide without guessing.

For example, Stripe payment notifications and Resend mail notifications belong
in their provider sections even though they use independent webhook adapters.
Distinguish outbound credentials from receiver verification credentials. Explain
provider dashboard/endpoint setup, handler binding and route mounting, supported
event/payload scope, and the acceptance/retry/deduplication boundary. Show exact
imports and identify application-owned handlers. Keep consequential error checks
in the example. Link the shared webhook guide and the companion adapter's API.
Do not imply that constructing an outbound driver registers a webhook, starts
a receiver, or requires that optional adapter.

## Voice and depth

- Write for a person reading in order: full paragraphs that explain why, not
  bullet fragments. Bullets are for genuinely list-shaped content.
- Add depth where it changes the reader's understanding or implementation.
  Keep the ordinary path connected; place unrelated lookup detail in its canonical
  section or API reference rather than making every page exhaustive.
- Do not become API-driven: no signatures, no option tables, no exhaustive
  method lists in task pages. Show a function by using it; explain an option
  where a reader needs it. Collections and validation also have dedicated
  available-methods guides: link each name in a GFM table or list to its
  explanation, noting receiver-specific differences once per name.
- Follow [Prose style](prose-style.md) for sentence patterns and modal verbs.
- Keep explanation and examples connected. Use code when it demonstrates the
  action or distinction the reader needs, not to satisfy a fixed prose/code rhythm.

## Code examples

- Show only the operation being taught, with enough context to adapt it safely.
  Keep blocks small and focused without a numeric limit or a code quota per
  subsection. Package guides assume existing collaborators; only an explicitly
  requested walkthrough grows an application or file step by step.
- Show JSON and other structured output pretty-printed on several lines.
- Every identifier must exist: check with `go doc arkoren.dev/...` and the
  source before writing it. Review existing examples against the current API
  rather than treating published snippets as proof that a call is valid.
- A complete program (quickstart, a full wiring example, a "Running locally"
  program) must compile: build it in a temporary module in the scratchpad
  with `replace` directives to the local modules, never in the repository.
- Fragments show imports when the package is not obvious from the name.

## Links

- Internal documentation links use relative `.md` paths, optionally with
  fragments; the website rewrites them to routes. Same-page fragments and
  external HTTPS sources are allowed.
- Link another guide inline where the concept becomes necessary, not in a
  trailing "see also" list.
- End package guides with the relevant pkg.go.dev page, and link it inline where a
  reader needs the full list of options. Non-package pages may end with their
  immediate next task; catalogs may establish shared reference links once.

## After writing

Complete the [mandatory guide review and validation](revision-and-links.md#mandatory-guide-review)
before delivering a new or materially revised guide. Run `make generate` for
the consumer guide index (requires installed website dependencies), and
`make docs` for metadata validation, index freshness and fresh rendered website
tests. Review offline source links and editorial source conventions manually.
