/** Historical core page names, captured before the authored API tree retired. */
const CORE_PACKAGES = {
  application: "application",
  app: "application",
  asyncapi: "asyncapi",
  auth: "http/auth",
  gate: "http/auth/gate",
  link: "http/link",
  client: "http/client",
  request: "http/request",
  response: "http/response",
  router: "http/router",
  server: "http/server",
  session: "http/session",
  swagger: "http/swagger",
  webhook: "http/webhook",
  event: "event",
  mcp: "mcp",
  pagination: "pagination",
  migrate: "service/database/migrate",
  llmtest: "service/llm/llmtest",
  paymenttest: "service/payment/paymenttest",
  pushtest: "service/push/pushtest",
};
/** Former facade pages belong to core/service, regardless of their flat slug. */
const SERVICES = [
  "broadcast",
  "cache",
  "crypto",
  "database",
  "feature",
  "hash",
  "llm",
  "mail",
  "payment",
  "push",
  "queue",
  "schedule",
  "search",
  "secret",
  "storage",
  "telemetry",
  "workflow",
];
/** Former middleware pages used flat routes for nested packages. */
const MIDDLEWARE =
  "bodylimit contenttype cors csrf errormap locale ratelimit requestlog secureheaders";
/** Memory pages remain package redirects, never catalog entries. */
const MEMORY =
  "broadcast cache feature payment queue schedule search secret storage telemetry workflow";
/** Vendor/package pairs published by the former API tree, not future packages. */
const DRIVERS = [
  "aes/aescrypto",
  "argon2/argon2hash",
  "aws/awsbroadcast",
  "aws/awsmail",
  "aws/awsqueue",
  "aws/awssecret",
  "aws/awsstorage",
  "azure/azuresecret",
  "bcrypt/bcrypthash",
  "chacha20/chacha20crypto",
  "cloudflare/cloudflarecaptcha",
  "cloudflare/cloudflarequeue",
  "filesystem/filesystemstorage",
  "gcp/gcpcaptcha",
  "gcp/gcpsecret",
  "hatchet/hatchetschedule",
  "hatchet/hatchetworkflow",
  "launchdarkly/launchdarklyfeature",
  "mailgun/mailgunmail",
  "mailgun/mailgunwebhook",
  "mcp/mcpserver",
  "meilisearch/meilisearchsearch",
  "mollie/molliepayment",
  "mqtt/mqttbroadcast",
  "mysql/mysqldatabase",
  "nats/natsbroadcast",
  "nats/natsqueue",
  "oauth/oauthauth",
  "openai/openaillm",
  "openai/openaiwebhook",
  "openfeature/openfeaturefeature",
  "opensearch/opensearchsearch",
  "otel/oteltelemetry",
  "paypal/paypalpayment",
  "paypal/paypalwebhook",
  "postgres/postgresdatabase",
  "postgres/postgressearch",
  "postmark/postmarkmail",
  "postmark/postmarkwebhook",
  "prometheus/prometheustelemetry",
  "rabbitmq/rabbitmqbroadcast",
  "rabbitmq/rabbitmqqueue",
  "redis/redisbroadcast",
  "redis/rediscache",
  "resend/resendmail",
  "resend/resendwebhook",
  "restate/restateschedule",
  "restate/restateworkflow",
  "smtp/smtpmail",
  "sqlite/sqlitedatabase",
  "stripe/stripepayment",
  "stripe/stripewebhook",
  "temporal/temporalschedule",
  "temporal/temporalworkflow",
  "vault/vaultsecret",
  "webpush/webpushpush",
];

/**
 * Redirect retired HTML and raw API URLs directly to their canonical reference.
 * Concept material leads to a guide rather than an invented Go package.
 * @example
 * console.log(legacyApiRedirects()["/docs/api/core/cache"]);
 */
export function legacyApiRedirects(): Record<string, string> {
  /** Pairs preserve historical URLs independently of future package discovery. */
  const packages = [
    ...Object.entries(CORE_PACKAGES).map(([name, path]) => [`core/${name}`, `core/${path}`]),
    ...SERVICES.map((name) => [`core/${name}`, `core/service/${name}`]),
    ...MIDDLEWARE.split(" ").map((name) => [`core/${name}`, `core/http/middleware/${name}`]),
    ...MEMORY.split(" ").map((name) => [
      `memory/memory${name}`,
      `core/service/${name}/memory${name}`,
    ]),
    ["memory/slogmail", "core/service/mail/slogmail"],
    ...DRIVERS.map((path) => [`drivers/${path}`, `driver/${path}`]),
    ..."certificate collection key problem query validate"
      .split(" ")
      .map((name) => [`foundation/${name}`, `foundation/${name}`]),
    ["foundation/schema", "foundation/query/schema"],
  ];
  /** Raw and HTML aliases share destinations without redirect chains. */
  const destinations = packages.map(([route, path]) => [
    route,
    `https://pkg.go.dev/arkoren.dev/${path}`,
  ]);
  destinations.push(["core/testing", "/docs/guides/testing/overview"]);
  return Object.fromEntries(
    destinations.flatMap(([route, target]) => [
      [`/docs/api/${route}`, target],
      [`/docs/api/${route}.md`, target],
    ]),
  );
}
