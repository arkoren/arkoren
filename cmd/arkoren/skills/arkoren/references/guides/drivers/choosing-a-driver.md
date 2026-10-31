---
title: Choosing a Driver
description: Which driver to pick for each service, from the in-process drivers for development to the vendor drivers for production.
order: 10
---

# Choosing a Driver

## Introduction

When you deploy to several replicas or need state to survive a restart, the
backend matters even though your application calls stay the same. Arkoren's
service contracts let you replace a driver without rewriting those calls.
Many services provide an in-process driver for development; others, such as
the database and language-model services, need a vendor driver from the start.
[Facades and drivers](../core-concepts/facades-and-drivers.md) explains that
separation.

When you choose, consider three things. First, **what you already run**: if
your team operates PostgreSQL and Redis, their drivers reuse those backends.
Second, **whether the data must persist or be shared**: in-process drivers keep
state in one process and lose it on restart. A durable backend can preserve it;
local SQLite or filesystem storage can serve one host, while multiple replicas
need a backend they can all reach. Third, **delivery and capability limits**:
memory and Redis both support atomic counters, but only Redis shares them
across processes; engine-backed schedulers persist timers across restarts.
Some operations return `ErrUnsupported` on particular drivers, so check the
linked driver guide before relying on one.

## Drivers by service

| Service | Driver | Pick when | Guide |
| --- | --- | --- | --- |
| Cache | Memory | Development and tests; a single process. | [Docs](../cache/drivers.md#memory) |
| | Redis | A cache shared by every replica, with atomic counters for rate limiting. | [Docs](../cache/drivers.md#redis) |
| Feature flags | Memory | Development and tests; flags set in code. | [Docs](../feature-flags/drivers.md#memory) |
| | OpenFeature | Flags in flagd, GO Feature Flag, ConfigCat, Flagsmith, or any service with an OpenFeature Go provider. | [Docs](../feature-flags/drivers.md#openfeature) |
| | LaunchDarkly | Flags in LaunchDarkly, evaluated with its official Go SDK. | [Docs](../feature-flags/drivers.md#launchdarkly) |
| Search | Memory | Development and tests; a single process. | [Docs](../search/drivers.md#memory) |
| | Postgres | Search inside the database you already run, for small and medium indexes. | [Docs](../search/drivers.md#postgres) |
| | Meilisearch | A dedicated search engine, quick to run, built for search-as-you-type. | [Docs](../search/drivers.md#meilisearch) |
| | OpenSearch | Large indexes on an OpenSearch cluster. | [Docs](../search/drivers.md#opensearch) |
| Database | PostgreSQL | Most production services. | [Docs](../database/drivers.md#postgresql) |
| | MySQL | MySQL or MariaDB. | [Docs](../database/drivers.md#mysql) |
| | SQLite | An embedded database, tests, or an in-memory database. | [Docs](../database/drivers.md#sqlite) |
| File storage | Memory | Development and tests; a single process. | [Docs](../file-storage/drivers.md#memory) |
| | Filesystem | Files on a local disk, on a single host. | [Docs](../file-storage/drivers.md#filesystem) |
| | Amazon S3 | S3 and S3-compatible object stores. | [Docs](../file-storage/drivers.md#amazon-s3) |
| Queues | Memory | Development and tests; a single process. | [Docs](../queues/drivers.md#memory) |
| | RabbitMQ | AMQP, with retries and a failure queue. | [Docs](../queues/drivers.md#rabbitmq) |
| | NATS | NATS JetStream. | [Docs](../queues/drivers.md#nats) |
| | Amazon SQS | Queues on AWS. | [Docs](../queues/drivers.md#amazon-sqs) |
| | Cloudflare | Cloudflare Queues, consumed over HTTP. | [Docs](../queues/drivers.md#cloudflare) |
| Broadcasting | Memory | Development and tests; a single process. | [Docs](../broadcasting/drivers.md#memory) |
| | Redis | Redis pub/sub. | [Docs](../broadcasting/drivers.md#redis) |
| | NATS | NATS subjects. | [Docs](../broadcasting/drivers.md#nats) |
| | RabbitMQ | An AMQP topic exchange. | [Docs](../broadcasting/drivers.md#rabbitmq) |
| | MQTT | MQTT brokers. Experimental. | [Docs](../broadcasting/drivers.md#mqtt) |
| | Amazon SNS | Publishing to SNS and consuming from SQS. | [Docs](../broadcasting/drivers.md#sns) |
| Mail | Log | Development: writes messages to the log instead of sending them. | [Docs](../mail/drivers.md#log) |
| | SMTP | Any SMTP server. | [Docs](../mail/drivers.md#smtp) |
| | Resend | The Resend API. | [Docs](../mail/drivers.md#resend) |
| | Mailgun | The Mailgun API. | [Docs](../mail/drivers.md#mailgun) |
| | Postmark | The Postmark API. | [Docs](../mail/drivers.md#postmark) |
| | Amazon SES | Mail on AWS. | [Docs](../mail/drivers.md#amazon-ses) |
| Push notifications | Web Push | Visible browser notifications through standard encrypted Web Push, with application-owned subscriptions and service workers. | [Docs](../push/drivers.md#web-push) |
| Payments | Memory | Local simulation without financial effects. | [Docs](../payments/drivers.md#memory) |
| | Stripe | Hosted checkout, existing recurring Prices, and card-backed SDK preparation through the payment facade. | [Docs](../payments/drivers.md#stripe) |
| | PayPal | Hosted or native SDK Orders, explicit capture, and existing subscription Plans. | [Docs](../payments/drivers.md#paypal) |
| | Mollie | Automatic one-off hosted card payments and explicit partial refunds. | [Docs](../payments/drivers.md#mollie) |
| Scheduled tasks | Memory | Development and tests; a single process. | [Docs](../scheduled-tasks/drivers.md#memory) |
| | Temporal | Tasks that run once across replicas, and durable one-off runs. | [Docs](../scheduled-tasks/drivers.md#temporal) |
| | Hatchet | Hatchet crons (in UTC) and durable one-off runs. | [Docs](../scheduled-tasks/drivers.md#hatchet) |
| | Restate | Durable schedules with at-least-once execution. | [Docs](../scheduled-tasks/drivers.md#restate) |
| Workflows | Memory | Development and tests; a single process. | [Docs](../workflows/drivers.md#memory) |
| | Temporal | Temporal workflows and activities, with signals and timers. | [Docs](../workflows/drivers.md#temporal) |
| | Hatchet | Hatchet durable tasks, signals, and timers; no child workflows. | [Docs](../workflows/drivers.md#hatchet) |
| | Restate | Restate workflow services, with signals and timers. | [Docs](../workflows/drivers.md#restate) |
| Secrets | Memory | Development and tests. | [Docs](../secrets/drivers.md#memory) |
| | Vault | HashiCorp Vault (KV version 2). | [Docs](../secrets/drivers.md#hashicorp-vault) |
| | AWS | AWS Secrets Manager. | [Docs](../secrets/drivers.md#aws-secrets-manager) |
| | Google Cloud | Google Secret Manager. | [Docs](../secrets/drivers.md#google-cloud-secret-manager) |
| | Azure | Azure Key Vault. | [Docs](../secrets/drivers.md#azure-key-vault) |
| Encryption | AES | AES-GCM, the usual choice on hardware with AES support. | [Docs](../encryption/drivers.md#aes-gcm) |
| | ChaCha20 | ChaCha20-Poly1305, for hardware without AES support. | [Docs](../encryption/drivers.md#chacha20-poly1305) |
| Hashing | Argon2 | Passwords. The recommended choice. | [Docs](../hashing/drivers.md#argon2id) |
| | bcrypt | Passwords, when you need compatibility with existing bcrypt hashes. | [Docs](../hashing/drivers.md#bcrypt) |
| Telemetry | Memory | Trace IDs in logs and in-process inspection, without a backend. | [Docs](../observability/telemetry-drivers.md#memory) |
| | OpenTelemetry | Exporting traces, metrics, and logs over OTLP. | [Docs](../observability/telemetry-drivers.md#opentelemetry) |
| | Prometheus | Metrics scraped by Prometheus. | [Docs](../observability/telemetry-drivers.md#prometheus) |
| Authentication | OAuth | Login with an OAuth 2 or OpenID Connect provider, and bearer tokens. | [Docs](../authentication/oauth.md) |
| Language models | OpenAI | OpenAI models, or any server with an OpenAI-compatible API such as Ollama or vLLM. | [Docs](../llm/drivers.md#openai) |

## Protocol adapters

Some driver modules adapt a protocol rather than implement a service's `Driver`
interface. The MCP adapter serves application-owned `mcp.Tool` definitions from
`arkoren.dev/core/mcp`
over HTTP using the official Go SDK; it is not a storage backend or a language
model driver. [Your First MCP Server](../mcp/first-server.md) shows registration
and HTTP serving; [Securing MCP](../mcp/securing-mcp.md) explains the authentication
and authorization boundaries.

Webhook adapters provide optional authenticated sources and typed event definitions. They are
independent of outbound service drivers and require explicit application
registration and acceptance policy:

| Adapter | Notifications | |
| --- | --- | --- |
| Stripe and PayPal | Payments and subscriptions. | [Docs](../payments/drivers.md) |
| Resend | Email lifecycle and inbound email. | [Docs](../mail/drivers.md#resend) |
| Mailgun | Email delivery and engagement. | [Docs](../mail/drivers.md#mailgun) |
| Postmark | Email delivery, engagement, and inbound email. | [Docs](../mail/drivers.md#postmark) |
| OpenAI | Background response lifecycle. | [Docs](../llm/drivers.md#receiving-background-notifications) |

The [webhook guide](../http-capabilities/webhooks.md) covers
shared receiving policy; the driver guides above cover provider setup and authentication. Sending mail, making payments,
and calling a language model do not require a webhook endpoint.

CAPTCHA adapters also live in vendor modules without a shared service contract.
Choose the provider before its browser integration; tokens and acceptance
policies differ:

| Adapter | Pick when | |
| --- | --- | --- |
| Cloudflare Turnstile | You need token validity with hostname and action checks. | [Docs](../abuse-prevention/captcha.md#cloudflare-turnstile) |
| Google reCAPTCHA | You need a CreateAssessment risk score and an application-owned threshold or step-up policy. | [Docs](../abuse-prevention/captcha.md#google-recaptcha) |

Both include optional route middleware. The [CAPTCHA guide](../abuse-prevention/captcha.md)
shows decode-once handler verification and header-based middleware separately.

## Services without drivers

A few features need no driver of their own. [Sessions](../http-capabilities/sessions.md)
store their data in any cache, so they use whichever cache driver you configured.
[Events](../events/overview.md) are delivered in process by the application's
event bus. [Secure links](../http-capabilities/secure-links.md) are computed from
a secret and need no storage unless they are single-use.

After choosing a backend, follow its linked driver guide for installation,
configuration, lifecycle ownership, and a first operation. See
[Configuration](../first-steps/configuration.md) for the shared construction pattern.
