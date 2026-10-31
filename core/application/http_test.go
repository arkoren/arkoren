package application_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
)

// failedProbeWriter records headers and status but rejects body writes.
type failedProbeWriter struct {
	*httptest.ResponseRecorder

	// err is returned by every body write.
	err error
}

// Write rejects the probe body with the configured error.
func (writer *failedProbeWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

// TestProbeHealthyReturnsWriteError checks successful probe write failures.
func TestProbeHealthyReturnsWriteError(t *testing.T) {
	t.Parallel()

	app := application.New()

	mustNil(t, app.Start(context.Background()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	err := errors.New("probe write failed")
	writer := &failedProbeWriter{ResponseRecorder: httptest.NewRecorder(), err: err}

	if got := app.LiveHandler()(writer, httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(got, err) {
		t.Fatalf("handler error = %v, want %v", got, err)
	}

	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.Code)
	}
}

// TestProbeUnavailableReturnsWriteError checks unavailable probe write failures.
func TestProbeUnavailableReturnsWriteError(t *testing.T) {
	t.Parallel()

	app := application.New()

	err := errors.New("probe write failed")
	writer := &failedProbeWriter{ResponseRecorder: httptest.NewRecorder(), err: err}

	if got := app.LiveHandler()(writer, httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(got, err) {
		t.Fatalf("handler error = %v, want %v", got, err)
	}

	if writer.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", writer.Code)
	}
}

// get serves a request against handler and returns the status and body.
func get(t *testing.T, handler http.Handler, method string) (int, string, http.Header) {
	t.Helper()

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(method, "/", nil))

	response := recorder.Result()
	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	body, err := io.ReadAll(response.Body)

	mustNil(t, err)

	return response.StatusCode, string(body), response.Header
}

// TestProbesHealthyWhileRunning checks all probes on one running application.
// With no dependencies, each must report the same successful plain response.
func TestProbesHealthyWhileRunning(t *testing.T) {
	app := application.New()

	mustNil(t, app.Start(context.Background()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	handlers := []router.Handler{
		app.StartupHandler(),
		app.LiveHandler(),
		app.ReadyHandler(),
	}

	for _, handler := range handlers {
		if status, body, _ := get(t, handler, http.MethodGet); status != http.StatusOK || body != "ok\n" {
			t.Fatalf("probe = %d %q, want 200 ok", status, body)
		}
	}
}

// TestProbeHeaders checks the plain probe's cache and content-type safeguards.
// The response must remain non-cacheable and disable MIME sniffing.
func TestProbeHeaders(t *testing.T) {
	app := application.New()

	mustNil(t, app.Start(context.Background()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	_, _, header := get(t, app.LiveHandler(), http.MethodGet)

	if header.Get("Cache-Control") != "no-store" ||
		header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", header)
	}
}

// TestProbesBeforeStart checks all probes on one never-started application.
// Each must return unavailable rather than imply startup or liveness.
func TestProbesBeforeStart(t *testing.T) {
	app := application.New()

	handlers := []router.Handler{
		app.StartupHandler(),
		app.LiveHandler(),
		app.ReadyHandler(),
	}

	for _, handler := range handlers {
		if status, body, _ := get(t, handler, http.MethodGet); status != http.StatusServiceUnavailable ||
			body != "unavailable\n" {
			t.Fatalf("probe = %d %q, want 503 unavailable", status, body)
		}
	}
}

// TestProbeReadyFailsOnHealthCheck checks readiness independently of liveness.
// Dependency failure must not leak its private message into the plain body.
func TestProbeReadyFailsOnHealthCheck(t *testing.T) {
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			return errors.New("secret detail")
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	status, body, _ := get(t, app.ReadyHandler(), http.MethodGet)

	if status != http.StatusServiceUnavailable || body != "unavailable\n" {
		t.Fatalf("ready = %d %q, want 503 unavailable", status, body)
	}

	if status, _, _ := get(t, app.LiveHandler(), http.MethodGet); status != http.StatusOK {
		t.Fatalf("live = %d, want 200", status)
	}
}

// TestProbesAfterShutdown checks historical startup against current liveness.
// Shutdown clears liveness but must not erase successful startup.
func TestProbesAfterShutdown(t *testing.T) {
	app := application.New()

	mustNil(t, app.Start(context.Background()))
	mustNil(t, app.Shutdown(context.Background()))

	if status, _, _ := get(t, app.StartupHandler(), http.MethodGet); status != http.StatusOK {
		t.Fatalf("startup = %d, want 200", status)
	}

	if status, _, _ := get(t, app.LiveHandler(), http.MethodGet); status != http.StatusServiceUnavailable {
		t.Fatalf("live = %d, want 503", status)
	}
}

// TestProbeHeadHasNoBody checks HEAD through a real HTTP server.
// Its successful probe response must contain no body bytes on the wire.
func TestProbeHeadHasNoBody(t *testing.T) {
	app := application.New()

	mustNil(t, app.Start(context.Background()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	server := httptest.NewServer(app.LiveHandler())
	t.Cleanup(server.Close)

	response, err := http.Head(server.URL)

	mustNil(t, err)
	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	body, err := io.ReadAll(response.Body)

	mustNil(t, err)

	if response.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("head = %d %q, want 200 with empty body", response.StatusCode, body)
	}
}

// verboseReport is the decoded body of the verbose readiness probe.
type verboseReport struct {
	// Status is the overall readiness result reported to the probe client.
	Status string `json:"status"`
	// Reason explains unavailability without exposing private check errors.
	Reason string `json:"reason"`
	// CheckedAt identifies the cached pass included in this report.
	CheckedAt time.Time `json:"checked_at"`
	// Checks holds the per-dependency results keyed by registration name.
	Checks map[string]struct {
		// Status distinguishes a passing check from a failed one.
		Status string `json:"status"`
		// Error is disclosed only when HealthDetails enables it.
		Error string `json:"error"`
	} `json:"checks"`
}

// getVerbose serves ?verbose against the ready handler and decodes the
// report.
func getVerbose(t *testing.T, app *application.Application) (int, verboseReport, string) {
	t.Helper()

	recorder := httptest.NewRecorder()

	app.ReadyHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz?verbose", nil))

	var report verboseReport

	mustNil(t, json.Unmarshal(recorder.Body.Bytes(), &report))

	return recorder.Code, report, recorder.Body.String()
}

// waitForPass waits until the verbose report carries a completed pass.
func waitForPass(t *testing.T, app *application.Application) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		if _, report, _ := getVerbose(t, app); !report.CheckedAt.IsZero() {
			return
		}

		if time.Now().After(deadline) {
			t.Fatal("no health pass completed")
		}

		time.Sleep(time.Millisecond)
	}
}

// TestVerboseReadyPassing checks a completed pass with two healthy checks.
// The report must omit failure reasons and preserve each named success.
func TestVerboseReadyPassing(t *testing.T) {
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			return nil
		})
		scope.Check("cache", func(context.Context) error {
			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForReady(t, app, http.StatusOK)

	status, report, _ := getVerbose(t, app)

	if status != http.StatusOK || report.Status != "ok" || report.Reason != "" {
		t.Fatalf("verbose = %d %+v, want 200 ok", status, report)
	}

	if len(report.Checks) != 2 || report.Checks["db"].Status != "ok" || report.Checks["cache"].Status != "ok" {
		t.Fatalf("checks = %+v", report.Checks)
	}

	if report.Checks["db"].Error != "" {
		t.Fatalf("passing check has error %q", report.Checks["db"].Error)
	}
}

// TestVerboseReadyWithholdsReason checks the default disclosure boundary.
// A failing dependency's address must not appear in the verbose response.
func TestVerboseReadyWithholdsReason(t *testing.T) {
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			return nil
		})
		scope.Check("cache", func(context.Context) error {
			return errors.New("dial 10.0.3.12:6379 refused")
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForPass(t, app)

	status, report, body := getVerbose(t, app)

	if status != http.StatusServiceUnavailable || report.Status != "unavailable" {
		t.Fatalf("verbose = %d %+v, want 503 unavailable", status, report)
	}

	if got := report.Checks["cache"]; got.Status != "failed" || got.Error != "reason withheld" {
		t.Fatalf("cache = %+v, want failed with reason withheld", got)
	}

	if report.Checks["db"].Status != "ok" {
		t.Fatalf("db = %+v, want ok", report.Checks["db"])
	}

	if strings.Contains(body, "10.0.3.12") {
		t.Fatalf("body leaks the error: %s", body)
	}
}

// TestVerboseReadyWithHealthDetails checks explicit error disclosure.
// Enabling HealthDetails must expose the failing check's original message.
func TestVerboseReadyWithHealthDetails(t *testing.T) {
	app := startReadiness(t, func(app *application.Builder) {
		app.HealthDetails(true)
	})

	compose := func(scope *application.Composer) {
		scope.Check("cache", func(context.Context) error {
			return errors.New("connection refused")
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForPass(t, app)

	_, report, _ := getVerbose(t, app)

	if got := report.Checks["cache"]; got.Status != "failed" || got.Error != "connection refused" {
		t.Fatalf("cache = %+v, want the error", got)
	}
}

// TestVerboseReadyNotLive checks the never-started application's explanation.
// No pass timestamp or check collection should imply that checks ran.
func TestVerboseReadyNotLive(t *testing.T) {
	app := application.New()

	status, report, body := getVerbose(t, app)

	if status != http.StatusServiceUnavailable || report.Status != "unavailable" || report.Reason != "not live" {
		t.Fatalf("verbose = %d %+v", status, report)
	}

	if strings.Contains(body, "checks") || strings.Contains(body, "checked_at") {
		t.Fatalf("body = %s, want no checks", body)
	}
}

// TestVerboseReadyBeforeFirstPass checks the pending first-pass explanation.
// A blocked check must not be mistaken for a stale completed result.
func TestVerboseReadyBeforeFirstPass(t *testing.T) {
	app := startReadiness(t)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
	})

	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			<-release

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))

	status, report, _ := getVerbose(t, app)

	if status != http.StatusServiceUnavailable || report.Reason != "no health pass completed" {
		t.Fatalf("verbose = %d %+v", status, report)
	}
}

// TestVerboseReadyStale checks reporting after the cached pass expires.
// The last successful check stays visible alongside the stale-result reason.
func TestVerboseReadyStale(t *testing.T) {
	var calls atomic.Int64
	app := startReadiness(t, func(app *application.Builder) {
		app.ReadinessTimeout(time.Minute)
	})

	compose := func(scope *application.Composer) {
		scope.Check("db", func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				return nil
			}

			<-ctx.Done()

			return ctx.Err()
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForReady(t, app, http.StatusOK)
	waitForReady(t, app, http.StatusServiceUnavailable)

	status, report, _ := getVerbose(t, app)

	if status != http.StatusServiceUnavailable || report.Reason != "health result stale" {
		t.Fatalf("verbose = %d %+v", status, report)
	}

	if report.Checks["db"].Status != "ok" || report.CheckedAt.IsZero() {
		t.Fatalf("stale report = %+v, want the last pass", report)
	}
}

// TestVerboseReadyWithoutChecks checks the empty dependency report.
// A live application must encode checks as an empty object and remain ready.
func TestVerboseReadyWithoutChecks(t *testing.T) {
	app := startReadiness(t)

	mustNil(t, app.Start(context.Background()))

	status, report, body := getVerbose(t, app)

	if status != http.StatusOK || report.Status != "ok" || !strings.Contains(body, `"checks":{}`) {
		t.Fatalf("verbose = %d %s", status, body)
	}
}

// TestVerboseReadyHeaders checks JSON mode's response safeguards.
// Content type, cache policy, and MIME-sniffing protection must agree.
func TestVerboseReadyHeaders(t *testing.T) {
	app := startReadiness(t)

	mustNil(t, app.Start(context.Background()))

	recorder := httptest.NewRecorder()

	app.ReadyHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz?verbose=1", nil))

	header := recorder.Header()

	if header.Get("Content-Type") != "application/json" ||
		header.Get("Cache-Control") != "no-store" ||
		header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", header)
	}
}

// TestReadyWithoutVerboseStaysPlain checks that verbose reporting is opt-in.
// A failed dependency must leave the default plain unavailable response intact.
func TestReadyWithoutVerboseStaysPlain(t *testing.T) {
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			return errors.New("down")
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForPass(t, app)

	if status, body, _ := get(t, app.ReadyHandler(), http.MethodGet); status != http.StatusServiceUnavailable ||
		body != "unavailable\n" {
		t.Fatalf("ready = %d %q, want 503 unavailable", status, body)
	}
}

// TestReadinessLogsCheckTransitions checks transition-only logging.
// Repeated failures must not repeat records, and an unchanged sibling stays quiet.
func TestReadinessLogsCheckTransitions(t *testing.T) {
	var buf lockedBuffer
	var level slog.Leveler
	var failing atomic.Bool
	failing.Store(true)

	app := startReadiness(t, captureLogging(&buf, &level))
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			if failing.Load() {
				return errors.New("db down")
			}

			return nil
		})
		scope.Check("cache", func(context.Context) error {
			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForLog(t, &buf, `msg="health check failed"`)
	waitForLog(t, &buf, `msg="app not ready"`)

	// Several failing passes log the failure once.
	time.Sleep(5 * readinessInterval)

	if got := strings.Count(buf.String(), `msg="health check failed" env=development check=db err="db down"`); got != 1 {
		t.Fatalf("failure logged %d times, want 1:\n%s", got, buf.String())
	}

	failing.Store(false)
	waitForLog(t, &buf, `msg="health check recovered" env=development check=db`)
	waitForLog(t, &buf, `msg="app ready"`)

	if logs := buf.String(); strings.Contains(logs, "check=cache") {
		t.Fatalf("passing check logged:\n%s", logs)
	}
}

// waitForLog waits until buf contains want.
func waitForLog(t *testing.T, buf *lockedBuffer, want string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("logs missing %s:\n%s", want, buf.String())
		}

		time.Sleep(time.Millisecond)
	}
}

// operationAt exports real route metadata and obtains one GET operation. Keeping
// export in the assertion path catches declaration errors in the built-in hints.
func operationAt(t *testing.T, routes *router.Router, path string) map[string]any {
	t.Helper()

	data, err := routes.OpenAPI("Application", "1")

	mustNil(t, err)
	var document map[string]any

	mustNil(t, json.Unmarshal(data, &document))

	return document["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
}

// responseSchema serves a real request and compares its status, representation,
// and fixed headers with the exported response, returning its output schema.
func responseSchema(t *testing.T, routes *router.Router, target string) map[string]any {
	t.Helper()

	recorder := httptest.NewRecorder()

	routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

	path, _, _ := strings.Cut(target, "?")
	operation := operationAt(t, routes, path)
	response := operation["responses"].(map[string]any)[strconv.Itoa(recorder.Code)].(map[string]any)

	for _, name := range []string{"Cache-Control", "X-Content-Type-Options"} {
		header := response["headers"].(map[string]any)[name].(map[string]any)

		if header["schema"].(map[string]any)["const"] != recorder.Header().Get(name) {
			t.Fatalf("%s header differs from annotation", name)
		}
	}

	media := response["content"].(map[string]any)[recorder.Header().Get("Content-Type")].(map[string]any)
	schema := media["schema"].(map[string]any)

	if fixed, ok := schema["const"]; ok && fixed != recorder.Body.String() {
		t.Fatalf("body = %q, schema const = %q", recorder.Body.String(), fixed)
	}

	return schema
}

// TestOpenAPIAppAnnotationsFollowRegistration checks that the callback describes
// its chosen route, not conventional paths, and leaves unannotated routes out.
func TestOpenAPIAppAnnotationsFollowRegistration(t *testing.T) {
	t.Parallel()

	app := application.New()
	routes := router.New()
	routes.Group("/operator", func(group *router.Router) {
		group.Get("/alive", app.LiveHandler(), application.LiveOperation)
		group.Get("/boot", app.StartupHandler(), application.StartupOperation)
		group.Get("/ready", app.ReadyHandler(), application.ReadyOperation)
		group.Get("/info", app.InfoHandler(), application.InfoOperation)
		group.Get("/private", app.InfoHandler())
	})

	data, err := routes.OpenAPI("Application", "1")

	mustNil(t, err)
	var document struct {
		Paths map[string]map[string]any `json:"paths"`
	}

	mustNil(t, json.Unmarshal(data, &document))

	if len(document.Paths) != 4 {
		t.Fatalf("paths = %v", document.Paths)
	}

	for _, path := range []string{"/operator/alive", "/operator/boot", "/operator/ready", "/operator/info"} {
		if _, ok := document.Paths[path]; !ok {
			t.Fatalf("missing annotated path %s", path)
		}
	}

	if bytes.Contains(data, []byte(`"security"`)) || bytes.Contains(data, []byte(`"examples"`)) {
		t.Fatalf("annotation inferred security or captured examples: %s", data)
	}
}

// TestOpenAPIProbeLifecycleMatchesHandlers exercises both declared statuses
// before startup, while running, and after stop, including historical startup.
func TestOpenAPIProbeLifecycleMatchesHandlers(t *testing.T) {
	t.Parallel()

	app := application.New()
	routes := router.New()
	routes.Get("/live", app.LiveHandler(), application.LiveOperation)
	routes.Get("/startup", app.StartupHandler(), application.StartupOperation)
	routes.Get("/ready", app.ReadyHandler(), application.ReadyOperation)

	for _, target := range []string{"/live", "/startup", "/ready", "/ready?verbose"} {
		responseSchema(t, routes, target)
	}

	mustNil(t, app.Start(t.Context()))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	for _, target := range []string{"/live", "/startup", "/ready", "/ready?verbose"} {
		responseSchema(t, routes, target)
	}

	mustNil(t, app.Shutdown(t.Context()))

	for _, target := range []string{"/live", "/startup", "/ready", "/ready?verbose"} {
		responseSchema(t, routes, target)
	}
}

// TestOpenAPIReadyVerbosePresenceMatchesOutput checks all presence spellings
// against JSON metadata, including false, which is not a boolean opt-out.
func TestOpenAPIReadyVerbosePresenceMatchesOutput(t *testing.T) {
	t.Parallel()

	app := application.New()
	routes := router.New()
	routes.Get("/ready", app.ReadyHandler(), application.ReadyOperation)

	for _, target := range []string{"/ready?verbose", "/ready?verbose=", "/ready?verbose=false", "/ready?verbose=anything"} {
		schema := responseSchema(t, routes, target)

		if schema["type"] != "object" || schema["readOnly"] != true {
			t.Fatalf("verbose schema = %v", schema)
		}

		if !slices.Equal(schema["required"].([]any), []any{"status"}) {
			t.Fatalf("required = %v", schema["required"])
		}
	}

	operation := operationAt(t, routes, "/ready")
	parameter := operation["parameters"].([]any)[0].(map[string]any)

	if parameter["name"] != "verbose" || parameter["in"] != "query" || parameter["required"] == true {
		t.Fatalf("verbose parameter = %v", parameter)
	}
}

// TestOpenAPIReadySchemaDoesNotCaptureFailures checks the cached failure wire
// shape while proving the exported contract has no live health content.
func TestOpenAPIReadySchemaDoesNotCaptureFailures(t *testing.T) {
	t.Parallel()

	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Check("private-database", func(context.Context) error {
			return errors.New("private-connection-error")
		})
	}

	mustNil(t, app.Start(t.Context(), compose))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.Background()))
	})

	routes := router.New()
	routes.Get("/ready", app.ReadyHandler(), application.ReadyOperation)

	schema := responseSchema(t, routes, "/ready?verbose=false")
	properties := schema["properties"].(map[string]any)
	check := properties["checks"].(map[string]any)["additionalProperties"].(map[string]any)

	if !slices.Equal(check["required"].([]any), []any{"status"}) {
		t.Fatalf("check required = %v", check["required"])
	}

	for _, name := range []string{"reason", "checked_at", "checks"} {
		if properties[name] == nil {
			t.Fatalf("missing optional property %s", name)
		}
	}

	data, err := routes.OpenAPI("Application", "1")

	mustNil(t, err)

	if bytes.Contains(data, []byte("private-database")) || bytes.Contains(data, []byte("private-connection-error")) {
		t.Fatalf("runtime health leaked: %s", data)
	}
}

// TestOpenAPIInfoMatchesOutput checks the actual Info field types and omission
// contract while leaving build values and application identity out of metadata.
func TestOpenAPIInfoMatchesOutput(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Name("private-service").Version("private-version")
	})

	routes := router.New()
	routes.Get("/info", app.InfoHandler(), application.InfoOperation)

	schema := responseSchema(t, routes, "/info")
	properties := schema["properties"].(map[string]any)

	if properties["uptime_seconds"].(map[string]any)["type"] != "integer" {
		t.Fatalf("uptime schema = %v", properties["uptime_seconds"])
	}

	if properties["uptime"].(map[string]any)["type"] != "string" {
		t.Fatalf("uptime schema = %v", properties["uptime"])
	}

	build := properties["build"].(map[string]any)["properties"].(map[string]any)

	if build["vcs_modified"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("build schema = %v", build)
	}

	if !slices.Equal(schema["required"].([]any), []any{"environment", "state", "log_level", "build"}) {
		t.Fatalf("required = %v", schema["required"])
	}

	data, err := routes.OpenAPI("Application", "1")

	mustNil(t, err)

	if bytes.Contains(data, []byte("private-service")) || bytes.Contains(data, []byte("private-version")) {
		t.Fatalf("runtime info leaked: %s", data)
	}
}

// TestOpenAPIReadyCachedFailureMatchesSchema compares a completed failed pass
// with the optional output fields. Virtual time lets the background runner
// settle without polling or real sleeps; the schema contains no runtime errors.
func TestOpenAPIReadyCachedFailureMatchesSchema(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app := application.New()

		mustNil(t, app.Start(t.Context(), func(scope *application.Composer) {
			scope.Check("database", func(context.Context) error {
				return errors.New("private connection details")
			})
		}))
		t.Cleanup(func() {
			mustNil(t, app.Shutdown(context.Background()))
		})

		synctest.Wait()

		routes := router.New()
		routes.Get("/ready", app.ReadyHandler(), application.ReadyOperation)
		responseSchema(t, routes, "/ready?verbose")

		recorder := httptest.NewRecorder()

		routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ready?verbose", nil))

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", recorder.Code)
		}

		var report map[string]any

		mustNil(t, json.Unmarshal(recorder.Body.Bytes(), &report))

		check := report["checks"].(map[string]any)["database"].(map[string]any)

		if check["status"] != "failed" || check["error"] != "reason withheld" {
			t.Fatalf("check = %v", check)
		}

		if report["checked_at"] == nil || report["reason"] != nil {
			t.Fatalf("cached failure report = %v", report)
		}
	})
}

// TestWelcomePage checks explicit registration, browser-safe headers, and useful
// resource destinations before startup. The page must work without remote assets.
func TestWelcomePage(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Name("billing")
	})

	routes := router.New()
	routes.Get("/", app.WelcomeHandler(), application.WelcomeOperation)

	recorder := httptest.NewRecorder()

	routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	for _, text := range []string{
		"<!doctype html>", "Welcome to billing",
		`href="https://arkoren.dev/docs"`,
		`href="https://arkoren.dev/docs/guides/first-steps/quickstart"`,
		`href="https://github.com/arkoren/arkoren"`,
	} {
		if !strings.Contains(recorder.Body.String(), text) {
			t.Fatalf("missing page content %q", text)
		}
	}

	if recorder.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}

	for _, name := range []string{"Cache-Control", "X-Content-Type-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if recorder.Header().Get(name) == "" {
			t.Fatalf("missing header %s", name)
		}
	}

	for _, text := range []string{"<script", "<img", "@import"} {
		if strings.Contains(recorder.Body.String(), text) {
			t.Fatalf("unexpected asset or script dependency %q", text)
		}
	}

	for _, tag := range strings.Split(recorder.Body.String(), "<link")[1:] {
		if !strings.HasPrefix(tag, ` rel="icon" type="image/svg+xml" href="data:image/svg+xml,`) {
			t.Fatal("unexpected external asset link")
		}
	}
}

// TestWelcomeFontsAreEmbedded prevents missing or filtered font URLs from
// silently replacing the website typography with browser defaults.
func TestWelcomeFontsAreEmbedded(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()

	err := application.New().WelcomeHandler()(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	mustNil(t, err)

	fonts := strings.Split(recorder.Body.String(), `url("`)[1:]

	if len(fonts) != 3 {
		t.Fatalf("font count = %d, want 3", len(fonts))
	}

	for _, font := range fonts {
		encoded, _, found := strings.Cut(font, `")`)

		if !found {
			t.Fatal("unclosed font URL")
		}

		encoded, found = strings.CutPrefix(encoded, "data:font/woff2;base64,")

		if !found {
			t.Fatal("font must be embedded, not fetched remotely")
		}

		decoded, err := base64.StdEncoding.DecodeString(encoded)

		mustNil(t, err)

		if !strings.HasPrefix(string(decoded), "wOF2") {
			t.Fatal("embedded font is not WOFF2")
		}
	}

	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "font-src data:;") {
		t.Fatal("font policy must allow only embedded fonts")
	}
}

// TestWelcomeFaviconIsEmbedded checks that the browser receives a standalone
// SVG favicon and a policy that permits it without allowing remote images.
func TestWelcomeFaviconIsEmbedded(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()

	err := application.New().WelcomeHandler()(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	mustNil(t, err)

	_, icon, found := strings.Cut(recorder.Body.String(), `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,`)

	if !found {
		t.Fatal("missing embedded SVG favicon")
	}

	icon, _, found = strings.Cut(icon, `"`)

	if !found {
		t.Fatal("unclosed favicon URL")
	}

	decoded, err := url.PathUnescape(icon)

	mustNil(t, err)

	if !strings.HasPrefix(decoded, `<svg xmlns="http://www.w3.org/2000/svg"`) || !strings.HasSuffix(decoded, "</svg>") {
		t.Fatal("favicon URL does not contain an SVG document")
	}

	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "img-src data:;") {
		t.Fatal("favicon policy must allow only embedded images")
	}
}

// TestWelcomeLogoHasAccessibleLink checks that the self-contained brand artwork
// links to Arkoren with an accessible name. The decorative SVG must not create
// a second screen-reader announcement or an extra keyboard stop.
func TestWelcomeLogoHasAccessibleLink(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()

	err := application.New().WelcomeHandler()(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	mustNil(t, err)

	_, link, found := strings.Cut(recorder.Body.String(), `<a href="https://arkoren.dev" aria-label="Arkoren home"`)

	if !found {
		t.Fatal("logo link is missing its destination or accessible name")
	}

	link, _, found = strings.Cut(link, "</a>")

	if !found {
		t.Fatal("logo link is not closed")
	}

	for _, text := range []string{"<svg ", `aria-hidden="true"`, `focusable="false"`} {
		if !strings.Contains(link, text) {
			t.Fatalf("logo link is missing %q", text)
		}
	}
}

// TestWelcomeLogoInsideCard checks that the brand replaces the eyebrow before
// the welcome heading instead of appearing outside the card.
func TestWelcomeLogoInsideCard(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()

	err := application.New().WelcomeHandler()(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	mustNil(t, err)

	body := recorder.Body.String()
	_, card, found := strings.Cut(body, `<section class="welcome" aria-labelledby="welcome-heading">`)

	if !found {
		t.Fatal("welcome card is missing")
	}

	card, _, found = strings.Cut(card, `<h1 id="welcome-heading">`)

	if !found || !strings.Contains(card, `class="brand"`) {
		t.Fatal("logo must appear inside the card before the heading")
	}

	if strings.Contains(body, "A place to begin") {
		t.Fatal("logo must replace the eyebrow text")
	}
}

// TestWelcomeEscapesName prevents application identity from injecting markup in
// either the document title or heading. Names remain ordinary template strings.
func TestWelcomeEscapesName(t *testing.T) {
	t.Parallel()

	name := `<script>alert("billing")</script> & 'team'`
	app := application.New(func(app *application.Builder) {
		app.Name(name)
	})

	recorder := httptest.NewRecorder()

	err := app.WelcomeHandler()(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	mustNil(t, err)

	if strings.Contains(recorder.Body.String(), name) {
		t.Fatal("unescaped name in HTML")
	}

	if strings.Count(recorder.Body.String(), html.EscapeString(name)) != 2 {
		t.Fatal("escaped name missing from title or heading")
	}
}

// TestWelcomeWithoutName checks that default application construction produces
// a usable page without relying on build identity or other implicit information.
func TestWelcomeWithoutName(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()

	err := application.New().WelcomeHandler()(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	mustNil(t, err)

	if !strings.Contains(recorder.Body.String(), "Welcome to your application") {
		t.Fatal("missing generic welcome")
	}
}

// TestWelcomeWithholdsDiagnostics checks that private construction settings and
// binary metadata cannot enter a page intended for public visitors.
func TestWelcomeWithholdsDiagnostics(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Name("billing")
		app.Version("private-release-42")
		app.Environment(application.Environment("private-deployment"))
		app.Debug(true)
	})

	recorder := httptest.NewRecorder()

	err := app.WelcomeHandler()(recorder, httptest.NewRequest(http.MethodGet, "/?verbose", nil))

	mustNil(t, err)

	for _, text := range []string{
		"private-release-42", "private-deployment", "DEBUG", "go_version",
		"module_path", "vcs_revision", "started_at", "uptime", "log_level", "/infoz", "/mcp",
		app.Info().Build.GoVersion,
	} {
		if text != "" && strings.Contains(recorder.Body.String(), text) {
			t.Fatalf("diagnostic information %q in welcome page", text)
		}
	}
}

// TestWelcomeReturnsWriteError ensures the router can observe a failed network
// write instead of treating an incomplete response as a successful page.
func TestWelcomeReturnsWriteError(t *testing.T) {
	t.Parallel()

	err := errors.New("welcome write failed")
	writer := &failedProbeWriter{ResponseRecorder: httptest.NewRecorder(), err: err}
	got := application.New().WelcomeHandler()(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(got, err) {
		t.Fatalf("handler error = %v, want %v", got, err)
	}
}

// TestWelcomeDoesNotRegisterRoutes prevents handler construction from silently
// exposing either a welcome endpoint or the separate full information endpoint.
func TestWelcomeDoesNotRegisterRoutes(t *testing.T) {
	t.Parallel()

	app := application.New()
	_ = app.WelcomeHandler()
	_ = app.InfoHandler()
	routes := router.New()

	for _, path := range []string{"/", application.InfoPath} {
		recorder := httptest.NewRecorder()

		routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, recorder.Code)
		}
	}
}

// TestOpenAPIWelcomeMatchesOutput compares declared media type and shared
// security/cache headers with the page served at an application-selected path.
func TestOpenAPIWelcomeMatchesOutput(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/hello", application.New().WelcomeHandler(), application.WelcomeOperation)

	schema := responseSchema(t, routes, "/hello")

	if schema["type"] != "string" {
		t.Fatalf("HTML schema = %v", schema)
	}
}
