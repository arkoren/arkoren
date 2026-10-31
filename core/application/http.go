package application

import (
	"net/http"
	"time"

	"arkoren.dev/core/application/internal/health"
	"arkoren.dev/core/application/internal/website"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
)

// Conventional probe paths, following the Kubernetes "z-pages" naming. Use
// them when registering the probe handlers so that routes and probe
// configuration stay in sync:
//
//	r.Get(application.LivePath, app.LiveHandler())
//	r.Get(application.ReadyPath, app.ReadyHandler())
//	r.Get(application.StartupPath, app.StartupHandler())
const (
	// LivePath is the liveness probe path served by [Application.LiveHandler].
	// Example: routes.Get(application.LivePath, app.LiveHandler())
	LivePath = "/livez"

	// ReadyPath is the readiness probe path served by [Application.ReadyHandler].
	// Example: routes.Get(application.ReadyPath, app.ReadyHandler())
	ReadyPath = "/readyz"

	// StartupPath is the startup probe path served by [Application.StartupHandler].
	// Example: routes.Get(application.StartupPath, app.StartupHandler())
	StartupPath = "/startupz"
)

// LiveHandler returns a handler that answers 200 while the application is
// live (see Application.Live) and 503 otherwise. It reads lifecycle state only,
// never runs checks, and writes a non-cacheable, newline-terminated plain body.
//
//	routes.Get(application.LivePath, app.LiveHandler(), application.LiveOperation)
func (application *Application) LiveHandler() router.Handler {
	return probeHandler(func(*http.Request) bool {
		return application.Live()
	})
}

// ReadyHandler returns a handler that answers 200 while the application is
// live and the latest background health pass succeeded, and 503 otherwise.
// It never runs checks itself: while running, the application runs every
// check every [Builder.ReadinessInterval], starting immediately, and each pass
// receives a deadline from [Builder.ReadinessTimeout]. Checks must honor it;
// the application cannot forcibly interrupt callbacks. It is not ready until
// the first pass succeeds, and a result older than three intervals counts as
// failed. Without registered checks readiness equals liveness.
//
// With the verbose query parameter (?verbose, any value) it answers the
// same status with a JSON report of the latest pass instead of "ok" or
// "unavailable":
//
//	{
//	  "status": "unavailable",
//	  "checked_at": "2026-10-10T09:00:05Z",
//	  "checks": {
//	    "cache": {"status": "failed", "error": "reason withheld"},
//	    "database": {"status": "ok"}
//	  }
//	}
//
// Failed checks report "reason withheld" unless [Builder.HealthDetails] is
// enabled; the reason is logged when a check starts failing either way.
// When the result cannot be trusted the report carries a reason ("not
// live", "no health pass completed", or "health result stale"), and checks
// only when a stale pass exists.
//
//	routes.Get(application.ReadyPath, app.ReadyHandler(), application.ReadyOperation)
func (application *Application) ReadyHandler() router.Handler {
	plain := probeHandler(func(*http.Request) bool {
		return application.ready()
	})

	return func(writer http.ResponseWriter, incoming *http.Request) error {
		if !request.HasQuery(incoming, "verbose") {
			return plain(writer, incoming)
		}

		report := application.readyReport()
		status := http.StatusOK

		if report.Status != statusOK {
			status = http.StatusServiceUnavailable
		}

		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")

		return response.JSON(writer, status, report)
	}
}

// StartupHandler returns a handler that answers 200 once startup has
// completed, including while stopping and after stop, and 503 otherwise.
// It reads lifecycle state without running checks; failed startup stays 503.
//
//	routes.Get(application.StartupPath, app.StartupHandler(), application.StartupOperation)
func (application *Application) StartupHandler() router.Handler {
	return probeHandler(func(*http.Request) bool {
		state := application.State()

		return state == StateRunning || state == StateStopping || state == StateStopped
	})
}

// probeHandler answers 200 "ok" when healthy reports true and 503
// "unavailable" otherwise.
func probeHandler(healthy func(*http.Request) bool) router.Handler {
	return func(writer http.ResponseWriter, incoming *http.Request) error {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")

		if !healthy(incoming) {
			return response.String(writer, http.StatusServiceUnavailable, "unavailable\n")
		}

		return response.String(writer, http.StatusOK, "ok\n")
	}
}

// Statuses reported by the verbose readiness probe.
const (
	// statusOK marks a passing report or check.
	statusOK = "ok"

	// statusUnavailable marks a failing report.
	statusUnavailable = "unavailable"

	// statusFailed marks a failing check.
	statusFailed = "failed"

	// reasonWithheld replaces a check's error unless details are enabled.
	reasonWithheld = "reason withheld"
)

// readyReport is the body of the verbose readiness probe.
type readyReport struct {
	// Status is "ok" when the application is ready and "unavailable"
	// otherwise.
	Status string `json:"status"`

	// Reason explains an "unavailable" status the checks cannot.
	Reason string `json:"reason,omitzero"`

	// CheckedAt is when the reported pass finished.
	CheckedAt time.Time `json:"checked_at,omitzero"`

	// Checks maps each check name to its outcome in the reported pass.
	Checks map[string]checkReport `json:"checks,omitzero"`
}

// checkReport is one check's outcome in a [readyReport].
type checkReport struct {
	// Status is "ok" or "failed".
	Status string `json:"status"`

	// Error is the failure, or "reason withheld" without health details.
	Error string `json:"error,omitzero"`
}

// readyReport builds the verbose readiness report from the latest pass. Its
// status agrees with [Application.ready].
func (application *Application) readyReport() readyReport {
	if !application.Live() {
		return readyReport{Status: statusUnavailable, Reason: "not live"}
	}

	snapshot := application.health.Snapshot()

	if !snapshot.Enabled {
		return readyReport{Status: statusOK, Checks: map[string]checkReport{}}
	}

	if snapshot.CompletedAt.IsZero() {
		return readyReport{Status: statusUnavailable, Reason: "no health pass completed"}
	}

	report := readyReport{
		Status:    statusOK,
		CheckedAt: snapshot.CompletedAt.UTC(),
		Checks:    make(map[string]checkReport, len(snapshot.Results)),
	}

	for _, result := range snapshot.Results {
		report.Checks[result.Name] = application.checkReport(result)
	}

	status := snapshot.HealthStatus(application.health.MaxAge())

	if status != statusOK {
		report.Status = statusUnavailable

		if status == "stale" {
			report.Reason = "health result stale"
		}
	}

	return report
}

// checkReport reports result, withholding its error unless health details
// are enabled.
func (application *Application) checkReport(result health.Result) checkReport {
	if result.Err == nil {
		return checkReport{Status: statusOK}
	}

	if !application.healthDetails {
		return checkReport{Status: statusFailed, Error: reasonWithheld}
	}

	return checkReport{Status: statusFailed, Error: result.Err.Error()}
}

// ready evaluates the same cached health used by diagnostic observations.
func (application *Application) ready() bool {
	return application.Live() && application.health.Snapshot().HealthStatus(application.health.MaxAge()) == statusOK
}

// readyReportSchema describes the output-only readiness wire format, not a
// validation input. Omitted fields depend on lifecycle and cached health state;
// no check names, failure messages, or runtime snapshots are captured here.
const readyReportSchema = `{
  "type":"object", "readOnly":true, "required":["status"],
  "properties":{
    "status":{"type":"string","enum":["ok","unavailable"]},
    "reason":{"type":"string"},
    "checked_at":{"type":"string","format":"date-time"},
    "checks":{"type":"object","additionalProperties":{
      "type":"object","required":["status"],"properties":{
        "status":{"type":"string","enum":["ok","failed"]},
        "error":{"type":"string"}
      }
    }}
  }
}`

// infoSchema describes Info's JSON representation without inspecting a live
// application or binary. Fields with omitzero tags remain optional, including
// uptime_seconds, whose non-nil pointer emits an integer even when zero.
const infoSchema = `{
  "type":"object", "readOnly":true,
  "required":["environment","state","log_level","build"],
  "properties":{
    "name":{"type":"string"},
    "version":{"type":"string"},
    "environment":{"type":"string"},
    "state":{"type":"string"},
    "log_level":{"type":"string"},
    "started_at":{"type":"string","format":"date-time"},
    "uptime":{"type":"string","description":"Elapsed uptime truncated to whole seconds in Go duration string format."},
    "uptime_seconds":{"type":"integer","format":"int64"},
    "build":{"type":"object","properties":{
      "go_version":{"type":"string"},
      "module_path":{"type":"string"},
      "module_version":{"type":"string"},
      "vcs_revision":{"type":"string"},
      "vcs_time":{"type":"string"},
      "vcs_modified":{"type":"boolean"}
    }}
  }
}`

// WelcomeOperation describes [Application.WelcomeHandler] at the caller's
// chosen path. It advertises HTML without capturing the application name or
// mounting an endpoint; the annotation does not change handler behavior.
//
// Example:
//
//	routes.Get("/", app.WelcomeHandler(), application.WelcomeOperation)
func WelcomeOperation(operation *router.OperationBuilder) {
	operation.Summary("Application welcome")
	operation.Description("Returns a welcome page with the application name and Arkoren resource links; " +
		"no runtime, configuration, or build information is included.")
	operation.MediaResponse(http.StatusOK, "text/html; charset=utf-8", router.RawSchema(`{"type":"string"}`))
	probeHeaders(operation, http.StatusOK)
}

// LiveOperation describes [Application.LiveHandler] on the route chosen by the
// caller. It advertises plain 200/503 responses and probe headers, without
// running the handler, registering routes, or changing access policy.
//
// Example:
//
//	routes.Get(application.LivePath, app.LiveHandler(), application.LiveOperation)
func LiveOperation(operation *router.OperationBuilder) {
	operation.Summary("Application liveness")
	operation.Description("Returns 200 while the application is live, otherwise 503.")
	probeResponses(operation)
}

// StartupOperation describes [Application.StartupHandler] without sampling
// lifecycle state. Success records completed startup, including during shutdown
// and after stop; this annotation installs no handler or middleware.
//
// Example:
//
//	routes.Get(application.StartupPath, app.StartupHandler(), application.StartupOperation)
func StartupOperation(operation *router.OperationBuilder) {
	operation.Summary("Application startup")
	operation.Description("Returns 200 once startup completed, including while stopping and after stop; otherwise 503.")
	probeResponses(operation)
}

// ReadyOperation describes [Application.ReadyHandler], including the optional
// presence-based verbose query parameter. The output schema includes optional
// cached health fields, never actual check names or failures. Descriptions do
// not enable health details or infer authentication requirements.
//
// Example:
//
//	routes.Get(application.ReadyPath, app.ReadyHandler(), application.ReadyOperation)
func ReadyOperation(operation *router.OperationBuilder) {
	operation.Summary("Application readiness")
	operation.Description("Returns 200 when live with fresh passing cached health, otherwise 503. " +
		"Without checks readiness equals liveness. The presence of verbose with any value " +
		"(including empty or false) selects JSON instead of plaintext. No checks run on request. " +
		"Failure messages are withheld unless HealthDetails is enabled.")
	operation.Parameter("verbose", "query", router.RawSchema(`{
		"type":"string","description":"Presence selects JSON; any value, including empty, is accepted."
	}`))
	probeResponses(operation)
	operation.JSONResponse(http.StatusOK, router.RawSchema(readyReportSchema))
	operation.JSONResponse(http.StatusServiceUnavailable, router.RawSchema(readyReportSchema))
}

// InfoOperation describes the JSON [Info] returned by [Application.InfoHandler]
// without embedding application or binary values. Serve this endpoint only to
// operators: the annotation neither configures nor infers authentication.
//
// Example:
//
//	routes.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation)
func InfoOperation(operation *router.OperationBuilder) {
	operation.Summary("Application information")
	operation.Description("Returns application and binary information. Expose only to operators; " +
		"authentication and authorization must be configured separately.")
	operation.JSONResponse(http.StatusOK, router.RawSchema(infoSchema))
	probeHeaders(operation, http.StatusOK)
}

// probeResponses shares the two plaintext outcomes across all three probes.
// Exact media types and newline-terminated values match probeHandler, while
// readiness may add JSON representations to these same statuses.
func probeResponses(operation *router.OperationBuilder) {
	operation.MediaResponse(http.StatusOK, "text/plain; charset=utf-8",
		router.RawSchema(`{"type":"string","const":"ok\n"}`))
	operation.MediaResponse(http.StatusServiceUnavailable, "text/plain; charset=utf-8",
		router.RawSchema(`{"type":"string","const":"unavailable\n"}`))
	probeHeaders(operation, http.StatusOK)
	probeHeaders(operation, http.StatusServiceUnavailable)
}

// probeHeaders records the non-cacheable, non-sniffable response contract shared
// by the probes and InfoHandler. It only describes headers, never writes them.
func probeHeaders(operation *router.OperationBuilder, status int) {
	operation.ResponseHeader(status, "Cache-Control", router.RawSchema(`{"type":"string","const":"no-store"}`))
	operation.ResponseHeader(status, "X-Content-Type-Options", router.RawSchema(`{"type":"string","const":"nosniff"}`))
}

// WelcomeHandler returns a self-contained HTML welcome page with documentation,
// getting-started, and source links. Only the configured application name is
// displayed, escaped as text; an empty name selects a generic welcome heading.
// Choose a name suitable for public display. No configuration, runtime state,
// version, or build information is included, and no assets are fetched.
//
// The handler registers no routes and does not depend on application startup or
// debug mode. Rendering completes in memory before writing the response, so
// template failures return without a partial page. Template and response-write
// errors are returned to the router. Successful responses disable caching and
// sniffing, prohibit framing and scripts, and allow only the inline stylesheet
// and embedded data fonts and images used by the page.
//
// Example:
//
//	app := application.New(func(app *application.Builder) {
//		app.Name("billing")
//	})
//	routes := router.New()
//	routes.Get("/", app.WelcomeHandler(), application.WelcomeOperation)
func (application *Application) WelcomeHandler() router.Handler {
	return website.Handler(application.name)
}

// InfoHandler returns a handler that answers 200 with a JSON description of
// the application and the running binary:
//
//	{
//	  "name": "billing",
//	  "version": "v1.4.0",
//	  "environment": "production",
//	  "state": "running",
//	  "log_level": "INFO",
//	  "started_at": "2026-10-10T09:00:00Z",
//	  "uptime": "1h0m0s",
//	  "uptime_seconds": 3600,
//	  "build": {
//	    "go_version": "go1.27.1",
//	    "module_path": "example.com/billing",
//	    "module_version": "v1.4.0",
//	    "vcs_revision": "abc123",
//	    "vcs_time": "2026-10-09T18:00:00Z"
//	  }
//	}
//
// Empty fields are omitted; started_at, uptime, and uptime_seconds appear once the
// application has reached [StateRunning]. log_level follows
// [Application.SetLogLevel]. The body exposes versions and revisions, so
// serve it where only operators can reach it.
//
//	internalRoutes.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation)
func (application *Application) InfoHandler() router.Handler {
	return func(writer http.ResponseWriter, _ *http.Request) error {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")

		return response.JSON(writer, http.StatusOK, application.Info())
	}
}
