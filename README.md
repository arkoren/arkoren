![Introducing Arkoren: wordmark and stepped A logo on a rose grid.](website/src/assets/images/blog/introducing-arkoren.svg)

# arkoren

A modular Go toolkit for HTTP services, data access, background work, and
integrations. Use one package in an existing application or compose a service
from the pieces you need.

Arkoren is a toolkit, not a framework: standard Go types, stdlib-first design,
and explicit composition around `net/http`, `context.Context`, and `log/slog`.
No dependency injection container, automatic discovery, or required lifecycle.
Core has no third-party dependencies; vendor drivers are independent Go modules.

[Documentation](https://arkoren.dev/docs/guides/about/introduction) ·
[Quickstart](https://arkoren.dev/docs/guides/first-steps/quickstart) ·
[API reference](https://pkg.go.dev/arkoren.dev/core) ·
[Introducing Arkoren](website/src/content/blog/introducing-arkoren.mdx)

## Services, with your choice of backend

Keep application code against shared service contracts and choose the backend
at construction time. The tables below list shipped implementations, not a
promise of identical provider capabilities. Each service links to its detailed
compatibility matrix; [choosing a driver](cmd/arkoren/skills/arkoren/references/guides/drivers/choosing-a-driver.md)
explains persistence, sharing across replicas, and operational trade-offs.

### Data

| Service | Use it for | Local development | Backend drivers |
| --- | --- | --- | --- |
| [Cache](cmd/arkoren/skills/arkoren/references/guides/cache/drivers.md) | Typed values, expiry, counters, and locks | Memory | Redis |
| [Database](cmd/arkoren/skills/arkoren/references/guides/database/drivers.md) | SQL queries, transactions, and migrations | SQLite, including in-memory databases | PostgreSQL, MySQL / MariaDB, SQLite |
| [Storage](cmd/arkoren/skills/arkoren/references/guides/file-storage/drivers.md) | Read, write, and serve objects | Memory, filesystem | Filesystem, Amazon S3 and compatible stores |
| [Search](cmd/arkoren/skills/arkoren/references/guides/search/drivers.md) | Typed indexes, text queries, filters, and sorting | Memory | PostgreSQL, Meilisearch, OpenSearch |

### Application

| Service | Use it for | Local development | Backend drivers |
| --- | --- | --- | --- |
| [Broadcasting](cmd/arkoren/skills/arkoren/references/guides/broadcasting/drivers.md) | Publish and subscribe across processes | Memory | Redis, NATS, RabbitMQ, MQTT (experimental), Amazon SNS / SQS |
| [Mail](cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md) | Send messages and attachments | Log output, test fake | SMTP, Resend, Mailgun, Postmark, Amazon SES |
| [Push](cmd/arkoren/skills/arkoren/references/guides/push/drivers.md) | Submit encrypted browser notifications | Test fake | Web Push; application-owned subscriptions and service workers |
| [Payments](cmd/arkoren/skills/arkoren/references/guides/payments/drivers.md) | Checkout, refunds, and provider-managed subscriptions where supported | Memory simulation, test fake | Stripe, PayPal, Mollie |
| [Feature Flags](cmd/arkoren/skills/arkoren/references/guides/feature-flags/drivers.md) | Typed flags and subject targeting | Memory | OpenFeature providers, LaunchDarkly |

### Background

| Service | Use it for | Local development | Backend drivers |
| --- | --- | --- | --- |
| [Queues](cmd/arkoren/skills/arkoren/references/guides/queues/drivers.md) | Dispatch jobs and control retries | Memory | RabbitMQ, NATS JetStream, Amazon SQS, Cloudflare Queues |
| [Scheduling](cmd/arkoren/skills/arkoren/references/guides/scheduled-tasks/drivers.md) | Recurring tasks and deferred runs | Memory | Temporal, Restate |
| [Workflows](cmd/arkoren/skills/arkoren/references/guides/workflows/drivers.md) | Coordinate steps, signals, and timers | Memory | Temporal, Restate |

### Security

| Service | Use it for | Local / test option | Driver choices |
| --- | --- | --- | --- |
| [Encryption](cmd/arkoren/skills/arkoren/references/guides/encryption/drivers.md) | Authenticated encryption | Same local algorithms | AES-GCM, ChaCha20-Poly1305 |
| [Hashing](cmd/arkoren/skills/arkoren/references/guides/hashing/drivers.md) | Hash, verify, and detect rehash needs | Same local algorithms | Argon2id, bcrypt |
| [Secrets](cmd/arkoren/skills/arkoren/references/guides/secrets/drivers.md) | Named secrets and versioned reads | Memory | HashiCorp Vault KV v2, AWS Secrets Manager, Google Secret Manager, Azure Key Vault |

### AI

| Service | Use it for | Local / test option | Driver choices |
| --- | --- | --- | --- |
| [Language Models](cmd/arkoren/skills/arkoren/references/guides/llm/drivers.md) | Generation, streaming, tools, and embeddings | Test fake; compatible local servers | OpenAI, OpenAI-compatible APIs (capabilities vary) |

### Operations

| Service | Use it for | Local / test option | Driver choices |
| --- | --- | --- | --- |
| [Observability](cmd/arkoren/skills/arkoren/references/guides/observability/telemetry-drivers.md) | Traces, metrics, and logs | Memory, test fake | OpenTelemetry / OTLP, Prometheus (metrics) |

Memory drivers keep state in one process and lose it on restart; they do not
coordinate replicas. Test fakes record or simulate calls without contacting a
provider. Every service contract ships a conformance suite in its
`<service>test` package; see [testing](cmd/arkoren/skills/arkoren/references/guides/testing/overview.md).

## More than service drivers

HTTP capabilities and protocol adapters compose alongside those services;
they are not interchangeable backend drivers.

| Capability | What's included |
| --- | --- |
| [HTTP](cmd/arkoren/skills/arkoren/references/guides/http-fundamentals/routing.md) | Routing, error-returning handlers, request / response helpers, and middleware on `net/http` |
| [Authentication](cmd/arkoren/skills/arkoren/references/guides/authentication/oauth.md) | OAuth 2 / OpenID Connect login and bearer tokens |
| [Sessions](cmd/arkoren/skills/arkoren/references/guides/http-capabilities/sessions.md) | Session state backed by your chosen cache; no separate session driver |
| [Events](cmd/arkoren/skills/arkoren/references/guides/events/overview.md) | In-process application events; separate from cross-process broadcasting |
| [Secure links](cmd/arkoren/skills/arkoren/references/guides/http-capabilities/secure-links.md) | Signed links, with storage only when single-use enforcement needs it |
| [CAPTCHA](cmd/arkoren/skills/arkoren/references/guides/abuse-prevention/captcha.md) | Cloudflare Turnstile and Google reCAPTCHA; application-owned acceptance policy |
| [Webhooks](cmd/arkoren/skills/arkoren/references/guides/http-capabilities/webhooks.md#provider-adapters) | Receivers for Stripe, PayPal, Resend, Mailgun, Postmark, and OpenAI; explicitly registered handlers |
| [MCP](cmd/arkoren/skills/arkoren/references/guides/mcp/tools.md) | Typed application tools, served through the optional official-SDK protocol adapter |
| [Application lifecycle](cmd/arkoren/skills/arkoren/references/guides/application/lifecycle-and-ownership.md) | Optional startup, supervised work, and cleanup coordination |
| [Foundation libraries](https://arkoren.dev/docs/api) | Independent, stdlib-only modules for collections, SQL queries, validation, Problem Details, cryptographic keys, and TLS certificates |

[Application keys](cmd/arkoren/skills/arkoren/references/guides/keys/overview.md)
and [TLS certificates](cmd/arkoren/skills/arkoren/references/guides/http-networking/certificates.md)
support offline provisioning. Existing key and certificate files are not
overwritten; changing system root trust is a separate, explicit action.

## Get started

Requires **Go 1.27 or later**. Add core to your existing Go module:

```sh
go get arkoren.dev/core
```

Add only the drivers you use—for example, `go get arkoren.dev/driver/redis`.
The [installation guide](cmd/arkoren/skills/arkoren/references/guides/first-steps/installation.md)
covers module choices; the [quickstart](cmd/arkoren/skills/arkoren/references/guides/first-steps/quickstart.md)
builds a first HTTP service. Native clients remain accessible for vendor-specific
features. Documentation snippets keep error results visible; application code
must check them before using the results.

Working with a coding agent? The [agent guide](cmd/arkoren/skills/arkoren/references/guides/agent-skills/working-with-agents.md)
explains how to install the bundled documentation skill. The website and skill
share the same authored guides; API declarations live on pkg.go.dev.

## Contribute

Run `make` for the available commands. From a checkout:

```sh
make test MODULE=core PKG=./http/...
make check MODULE=core
```

Shared Go recipes use `GOWORK=off` to check each module independently. Race tests
require a C compiler. After guide edits, run `make generate` and `make docs`;
see the [website contributor guide](website/README.md) for Node dependencies
and browser setup.

Repository automation lives in the stdlib-only development command
`cmd/arkoren-ci`, not the consumer CLI. Run `make maintenance-check` with the
pinned git-cliff installed to exercise real Git/SSH publication and recovery
fixtures. `release notes` manages the signed notes PR; `release publish` separates
tag writes, credential-free public builds and fresh-token prerelease writes.
Neither website nor maintenance-only changes propose module versions.

## License

[MIT](LICENSE).
