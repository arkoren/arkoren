# Request Logging

`arkoren.dev/core/http/middleware/requestlog` logs returned handler errors and
observed HTTP 5xx responses. The concrete path and raw error can contain
identifiers or secrets. Choose appropriate redaction, access controls, and
retention before sending records to a log destination or telemetry exporter.

With an existing router named `routes` and your `*slog.Logger` named `logger`,
install logging before recovery so recovered panics reach the logger as returned
errors:

```go
routes.Use(
	requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(logger)
	}),
	router.Recover(),
)
```

Omitting the logger resolves `slog.Default()` for each request. To correlate
records with traces, put telemetry middleware before
logging and use its telemetry-enabled logger.

A successful connection hijack does not produce a failure record. If the handler
returns an error after hijacking, status is `0` when no final HTTP status passed
through the writer. Bytes written directly to the caller-owned connection are
not observable through HTTP response hooks; the middleware does not infer 500.

See the [Request Logging guide](../../../../cmd/arkoren/skills/arkoren/references/guides/observability/request-logging.md)
for telemetry correlation and logger placement.
