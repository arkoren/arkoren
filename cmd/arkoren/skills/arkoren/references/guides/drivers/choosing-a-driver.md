---
title: Choosing a Driver
description: Compare backend requirements, find available providers, and follow the setup guide for your service.
order: 10
summary:
  - Choose a backend from state scope, operational requirements, and portable capability limits.
  - Find available service drivers and follow their canonical provider setup guides.
  - Distinguish protocol adapters and driver-free features from service backends.
---

# Choosing a Driver

A driver connects a service facade to its backend. Choose it from the behavior
your application needs, not only from whether it implements the interface.
[Facades and Drivers](../core-concepts/facades-and-drivers.md) explains which
responsibilities stay portable and which belong to the backend.

## State Scope

Start with where state must live. A memory cache is useful for isolated tests or
disposable data in one process. It cannot share a receipt between two API
replicas: each replica has its own cache. A shared Redis cache gives both replicas
access to the same keys, but persistence and failover durability still depend on
Redis configuration. Sharing state is not the same as preserving it indefinitely.

Local SQLite and filesystem storage can suit a single-host deployment. They do
not become shared services merely because several application replicas use the
same driver type. Choose a backend all relevant replicas can reach when they
must observe the same records, objects, counters, or work.

## Backend Requirements

Reuse a service your team can operate when it meets the contract you need. Then
check the linked provider guide for credentials, permissions, supported server
versions, resource provisioning, and startup requirements. A driver constructor
is not proof of connectivity or permission to perform an operation.

Compare the service's compatibility matrix before committing to a provider.
An interface method can return `ErrUnsupported`; vendor-native capabilities do
not automatically become portable operations. For example, a durable scheduler
can preserve timers and coordinate their delivery without guaranteeing
exactly-once application side effects. Keep handlers idempotent where repeated
delivery or an uncertain external result could duplicate work.

## Service Drivers

These are the available choices and their main prerequisites. Each Docs link
opens the canonical provider setup, including its compatibility evidence and
limits; this catalog does not duplicate those matrices. In-process drivers are
included with core. Vendor modules are installed from your application's module
directory as shown in the linked guide.

Hatchet is pending and unpublished. It is not an installable choice; its
development material belongs to the contributor archive, not application setup.

| Service | Driver | Pick when | Guide |
| --- | --- | --- | --- |
| Cache | Memory | Development and tests; a single process. | [Docs](../cache/drivers.md#memory) |
| | Redis | Shared entries, counters, and locks; requires Redis 6.2 or later. | [Docs](../cache/drivers.md#redis) |
| Feature flags | Memory | Development and tests; flags set in code. | [Docs](../feature-flags/drivers.md#memory) |
| | OpenFeature | Flags in flagd, GO Feature Flag, ConfigCat, Flagsmith, or any service with an OpenFeature Go provider. | [Docs](../feature-flags/drivers.md#openfeature) |
| | LaunchDarkly | Flags in LaunchDarkly, evaluated with its official Go SDK. | [Docs](../feature-flags/drivers.md#launchdarkly) |
| Search | Memory | Development and tests; a single process. | [Docs](../search/drivers.md#memory) |
| | PostgreSQL | Full-text search in an existing database; application-owned table migrations. | [Docs](../search/drivers.md#postgres) |
| | Meilisearch | Dedicated prefix search; requires Meilisearch 1.15 or later. | [Docs](../search/drivers.md#meilisearch) |
| | OpenSearch | Large indexes on an OpenSearch cluster. | [Docs](../search/drivers.md#opensearch) |
| Database | PostgreSQL | A PostgreSQL server and pgx connection pool. | [Docs](../database/drivers.md#postgresql) |
| | MySQL | An existing MySQL deployment. | [Docs](../database/drivers.md#mysql) |
| | SQLite | An embedded database, tests, or an in-memory database. | [Docs](../database/drivers.md#sqlite) |
| File storage | Memory | Development and tests; a single process. | [Docs](../file-storage/drivers.md#memory) |
| | Filesystem | Files on a local disk, on a single host. | [Docs](../file-storage/drivers.md#filesystem) |
| | Amazon S3 | S3 and S3-compatible object stores. | [Docs](../file-storage/drivers.md#amazon-s3) |
| Queues | Memory | Development and tests; a single process. | [Docs](../queues/drivers.md#memory) |
| | RabbitMQ | Durable AMQP work queues, publisher confirms, and manual acknowledgements. | [Docs](../queues/drivers.md#rabbitmq) |
| | NATS | JetStream streams and durable pull consumers, not Core NATS alone. | [Docs](../queues/drivers.md#nats) |
| | Amazon SQS | AWS queues, credentials, and a logical-name-to-URL mapping. | [Docs](../queues/drivers.md#amazon-sqs) |
| | Cloudflare | Cloudflare Queues with HTTP pull consumers and an API token. | [Docs](../queues/drivers.md#cloudflare) |
| Broadcasting | Memory | Development and tests; a single process. | [Docs](../broadcasting/drivers.md#memory) |
| | Redis | Redis pub/sub. | [Docs](../broadcasting/drivers.md#redis) |
| | NATS | NATS subjects. | [Docs](../broadcasting/drivers.md#nats) |
| | RabbitMQ | An AMQP topic exchange. | [Docs](../broadcasting/drivers.md#rabbitmq) |
| | MQTT | An MQTT 5 broker; check session and delivery limits. | [Docs](../broadcasting/drivers.md#mqtt) |
| | Amazon SNS | Publishing to SNS and consuming from SQS. | [Docs](../broadcasting/drivers.md#sns) |
| Mail | Log | Development: writes messages to the log instead of sending them. | [Docs](../mail/drivers.md#log) |
| | SMTP | An SMTP server; encrypted delivery is the default. | [Docs](../mail/drivers.md#smtp) |
| | Resend | A Resend API key and an authorized sender. | [Docs](../mail/drivers.md#resend) |
| | Mailgun | A sending domain and private API key. | [Docs](../mail/drivers.md#mailgun) |
| | Postmark | A server token and an authorized sender. | [Docs](../mail/drivers.md#postmark) |
| | Amazon SES | AWS credentials and SES sending permissions. | [Docs](../mail/drivers.md#amazon-ses) |
| Push notifications | Web Push | Visible browser notifications through standard encrypted Web Push, with application-owned subscriptions and service workers. | [Docs](../push/drivers.md#web-push) |
| Payments | Memory | Local simulation without financial effects. | [Docs](../payments/drivers.md#memory) |
| | Stripe | Stripe credentials; hosted checkout, existing recurring Prices, and card SDK preparation. | [Docs](../payments/drivers.md#stripe) |
| | PayPal | Client credentials; Orders, explicit capture, and existing subscription Plans. | [Docs](../payments/drivers.md#paypal) |
| | Mollie | An API key and enabled creditcard profile; one-off hosted card payments and explicit refunds. | [Docs](../payments/drivers.md#mollie) |
| Scheduled tasks | Memory | Development and tests; a single process. | [Docs](../scheduled-tasks/drivers.md#memory) |
| | Temporal | A Temporal service and worker host; persisted schedules and deferred workflows. | [Docs](../scheduled-tasks/drivers.md#temporal) |
| | Restate | A Restate deployment and registered endpoint; persisted tick chains and deferred jobs. | [Docs](../scheduled-tasks/drivers.md#restate) |
| Workflows | Memory | Development and tests; a single process. | [Docs](../workflows/drivers.md#memory) |
| | Temporal | Temporal workflows and activities, with signals and timers. | [Docs](../workflows/drivers.md#temporal) |
| | Restate | Restate workflow services, with signals and timers. | [Docs](../workflows/drivers.md#restate) |
| Secrets | Memory | Development and tests. | [Docs](../secrets/drivers.md#memory) |
| | Vault | A Vault KV v2 mount and token or login method with access to it. | [Docs](../secrets/drivers.md#hashicorp-vault) |
| | AWS | AWS Secrets Manager and credentials with secret access. | [Docs](../secrets/drivers.md#aws-secrets-manager) |
| | Google Cloud | Secret Manager resources and Google credentials with secret access. | [Docs](../secrets/drivers.md#google-cloud-secret-manager) |
| | Azure | A Key Vault URL and an identity with secret access. | [Docs](../secrets/drivers.md#azure-key-vault) |
| Encryption | AES | AES-GCM, the usual choice on hardware with AES support. | [Docs](../encryption/drivers.md#aes-gcm) |
| | ChaCha20 | ChaCha20-Poly1305, for hardware without AES support. | [Docs](../encryption/drivers.md#chacha20-poly1305) |
| Hashing | Argon2 | Passwords. The recommended choice. | [Docs](../hashing/drivers.md#argon2id) |
| | bcrypt | Passwords, when you need compatibility with existing bcrypt hashes. | [Docs](../hashing/drivers.md#bcrypt) |
| Telemetry | Memory | Trace IDs in logs and in-process inspection, without a backend. | [Docs](../observability/telemetry-drivers.md#memory) |
| | OpenTelemetry | Exporting traces, metrics, and logs over OTLP. | [Docs](../observability/telemetry-drivers.md#opentelemetry) |
| | Prometheus | Metrics scraped by Prometheus. | [Docs](../observability/telemetry-drivers.md#prometheus) |
| Language models | OpenAI | OpenAI credentials, or a compatible Responses/Chat Completions endpoint; capabilities vary by server and model. | [Docs](../llm/drivers.md#openai) |

## Protocol Adapters

Some driver modules adapt a protocol rather than implement a service's `Driver`
interface. The MCP adapter serves application-owned `mcp.Tool` definitions from
`arkoren.dev/core/mcp`
over HTTP using the official Go SDK; it is not a storage backend or a language
model driver. [Your First MCP Server](../mcp/first-server.md) shows registration
and HTTP serving; [Securing MCP](../mcp/securing-mcp.md) explains the authentication
and authorization boundaries.

The [`mcpserver` API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver)
provides the adapter's constructor and transport settings.

For browser login or OIDC bearer verification, use the
[OAuth adapter](../authentication/oauth.md). It integrates with HTTP
authentication rather than implementing a service `Driver` interface. Provider
registration, callback URLs, and accepted token audiences remain explicit setup.

Webhook adapters authenticate provider requests and expose typed event definitions.
They do not start receivers when you construct an outbound mail, payment, or LLM
driver. Bind the events you accept and mount the endpoint yourself:

| Adapter | Notifications | |
| --- | --- | --- |
| Stripe and PayPal | Payments and subscriptions. | [Docs](../payments/drivers.md) |
| Resend | Email lifecycle and inbound email. | [Docs](../mail/drivers.md#resend) |
| Mailgun | Email delivery and engagement. | [Docs](../mail/drivers.md#mailgun) |
| Postmark | Email delivery, engagement, and inbound email. | [Docs](../mail/drivers.md#postmark) |
| OpenAI | Background response lifecycle. | [Docs](../llm/drivers.md#response-notifications) |

Follow [Receiving Webhooks](../webhooks/receiving-webhooks.md) for acceptance,
retries, and deduplication, and [Provider Adapters](../webhooks/provider-adapters.md)
for authentication boundaries. The provider guides above own credential setup.
Outbound calls do not require a webhook endpoint.

CAPTCHA adapters also live in vendor modules without a shared service contract.
Choose the provider before its browser integration; tokens and acceptance
policies differ:

| Adapter | Pick when | |
| --- | --- | --- |
| Cloudflare Turnstile | You need token validity with hostname and action checks. | [Docs](../abuse-prevention/captcha.md#cloudflare-turnstile) |
| Google reCAPTCHA | You need a CreateAssessment risk score and an application-owned threshold or step-up policy. | [Docs](../abuse-prevention/captcha.md#google-recaptcha) |

Both include optional route middleware. The [CAPTCHA guide](../abuse-prevention/captcha.md)
shows decode-once handler verification and header-based middleware separately.
For adapter options, see the
[`cloudflarecaptcha` API](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarecaptcha)
and [`gcpcaptcha` API](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpcaptcha).

## Driver-Free Features

A few features need no driver of their own. [Sessions](../http-capabilities/sessions.md)
store their data in any cache, so they use whichever cache driver you configured.
[Events](../events/overview.md) are delivered in process by the application's
event bus. [Secure links](../http-capabilities/secure-links.md) are computed from
a secret and need no storage unless they are single-use.

After choosing a backend, follow its Docs link for construction, lifecycle
ownership, and a first operation. If no available driver meets the contract you
need, [write an application-owned driver](writing-a-driver.md).
