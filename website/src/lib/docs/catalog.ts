/**
 * What the site showcases: the contracts and their drivers, the middleware
 * packages, and the foundation modules. The landing page and blog posts read
 * these lists, so a contract or driver is listed once.
 */

/** A contract card: its package name, overview guide, summary, and drivers. */
export interface Contract {
  name: string;
  /** Guide path below /docs/guides/. */
  guide: string;
  body: string;
  drivers: readonly string[];
}

export const contracts: readonly Contract[] = [
  {
    name: "cache",
    guide: "cache/overview",
    body: "Keep frequently used data close and back sessions or rate limits with typed values and atomic counters.",
    drivers: ["memory", "redis"],
  },
  {
    name: "database",
    guide: "database/overview",
    body: "Read and update SQL data with typed rows, transactions, pagination, and migrations.",
    drivers: ["postgres", "mysql", "sqlite"],
  },
  {
    name: "storage",
    guide: "file-storage/overview",
    body: "Streaming object storage with portable keys and a shared not-found error.",
    drivers: ["memory", "filesystem", "aws s3"],
  },
  {
    name: "queue",
    guide: "queues/overview",
    body: "Move slow work out of a request into typed jobs with retries, backoff, timeouts, and a failure path.",
    drivers: ["memory", "rabbitmq", "nats", "aws sqs", "cloudflare"],
  },
  {
    name: "broadcast",
    guide: "broadcasting/overview",
    body: "Cross-process messages on named topics, with trace propagation.",
    drivers: ["memory", "redis", "nats", "rabbitmq", "mqtt", "aws sns"],
  },
  {
    name: "mail",
    guide: "mail/overview",
    body: "Portable transactional email and MIME encoding. Logs instead of sending in development.",
    drivers: ["slog", "smtp", "resend", "mailgun", "postmark", "aws ses"],
  },
  {
    name: "push",
    guide: "push/overview",
    body: "Immutable visible notifications over standard Web Push, with explicit retention and a recording fake for tests.",
    drivers: ["web push"],
  },
  {
    name: "payment",
    guide: "payments/overview",
    body: "Prepare client SDK payments, start hosted checkout, issue refunds, and control existing-plan subscriptions.",
    drivers: ["memory", "stripe", "paypal", "mollie"],
  },
  {
    name: "llm",
    guide: "llm/overview",
    body: "Large language models with tool-calling agents, structured output, streaming, and embeddings. Scripted fake for tests.",
    drivers: ["openai"],
  },
  {
    name: "workflow",
    guide: "workflows/overview",
    body: "Durable, typed workflows with steps, signals, timers, and child runs.",
    drivers: ["memory", "temporal", "hatchet", "restate"],
  },
  {
    name: "schedule",
    guide: "scheduled-tasks/overview",
    body: "Run functions on a calendar and defer typed one-off tasks. Durable drivers fire once across replicas.",
    drivers: ["memory", "temporal", "hatchet", "restate"],
  },
  {
    name: "secret",
    guide: "secrets/overview",
    body: "Load credentials from a secret store with versioned values and typed retrieval.",
    drivers: ["memory", "vault", "aws", "gcp", "azure"],
  },
  {
    name: "feature",
    guide: "feature-flags/overview",
    body: "Control a rollout for selected users with read-only typed flags, targeting, and telemetry.",
    drivers: ["memory", "openfeature", "launchdarkly"],
  },
  {
    name: "search",
    guide: "search/overview",
    body: "Full-text search with typed documents, filters, sorting, and pages, defined once in code.",
    drivers: ["memory", "postgres", "meilisearch", "opensearch"],
  },
  {
    name: "crypto",
    guide: "encryption/overview",
    body: "Protect stored values with encryption that also detects tampering, behind one interface.",
    drivers: ["aes-gcm", "chacha20"],
  },
  {
    name: "hash",
    guide: "hashing/overview",
    body: "Password hashing with verification and rehash checks.",
    drivers: ["argon2", "bcrypt"],
  },
  {
    name: "telemetry",
    guide: "observability/telemetry",
    body: "Traces, metrics, and log correlation. A no-op until you configure it.",
    drivers: ["memory", "otel", "prometheus"],
  },
];

export const middleware = [
  "cors",
  "csrf",
  "ratelimit",
  "secureheaders",
  "session",
  "requestlog",
  "bodylimit",
  "contenttype",
  "errormap",
  "locale",
];

export const foundation = [
  {
    name: "validate",
    body: "Reject invalid signup data before saving it. Parse an existing Signup input with request context ctx; errors use JSON Pointer paths, with messages translated through context.",
    file: "signup.go",
    code: `
import "arkoren.dev/foundation/validate"

type Signup struct {
	Email    string
	Password string
}

var signup = validate.Struct(func(object *validate.StructBuilder[Signup]) {
	object.Field("Email", func(email *validate.StringBuilder[string]) {
		email.Required()
		email.Email()
	})
	object.Field("Password", func(password *validate.StringBuilder[string]) {
		password.Required()
		password.Min(12)
	})
})

value, err := signup.Parse(ctx, input)`,
  },
  {
    name: "query",
    body: "Fetch the latest posts for an existing author ID, held in author, without mixing values into SQL text. Build the query for Postgres; ANSI is the default, and MySQL is also supported. Schema changes have a separate API.",
    file: "posts.go",
    code: `
import "arkoren.dev/foundation/query"

posts := query.Table("posts", func(statement *query.Builder) {
	statement.Select("id", "title")
	statement.Where("author_id", author)
	statement.OrderByDesc("created_at")
	statement.Limit(20)
})
statement, err := posts.BuildFor(query.Postgres)

// SQL text and bound arguments stay separate:
// statement.SQL()  → SELECT ... WHERE "author_id" = $1 ...
// statement.Args() → []any{author}`,
  },
  {
    name: "problem",
    body: "Return a useful 404 without exposing the database error. In an HTTP handler with writer and request parameters, derive RFC 9457 Problem Details; the response negotiates problem+json, JSON, or text.",
    file: "errors.go",
    code: `
import (
	"database/sql"
	"net/http"

	"arkoren.dev/foundation/problem"
)

// ErrUserNotFound identifies a missing user.
var ErrUserNotFound = problem.New(func(details *problem.Builder) {
	details.Type("https://api.example.com/problems/user-not-found")
	details.Title("User not found")
	details.Status(http.StatusNotFound)
})

ErrUserNotFound.Derive(func(details *problem.Builder) {
	details.Error(sql.ErrNoRows)
	details.Detail("The requested user does not exist.")
}).ServeHTTP(writer, request)

// The cause stays in the error chain, not the response.`,
  },
  {
    name: "collection",
    body: "Parse a list of IDs and stop at the first invalid value, keeping the IDs already parsed. Lazy pipelines defer work until consumed; slice and map helpers also support filtering and transformation.",
    file: "users.go",
    code: `
import (
	"strconv"

	"arkoren.dev/foundation/collection"
)

values := []string{"12", "34", "invalid", "56"}
parsed := collection.NewSlice(values).Lazy().Try().Map(func(_ int, value string) (int, error) {
	return strconv.Atoi(value)
})

// Parsing starts here and stops before "56".
ids, err := parsed.Items()
// ids is []int{12, 34}; err identifies "invalid".`,
  },
];
