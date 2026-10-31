package application

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// mustNil stops an information test before a failed lifecycle step is observed.
func mustNil(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

// infoBody is the decoded body of the info handler.
type infoBody struct {
	// Name is the optional application identity served by the handler.
	Name string `json:"name"`
	// Version is the configured application release, not the Go version.
	Version string `json:"version"`
	// Environment reports the deployment environment selected at construction.
	Environment string `json:"environment"`
	// State is the textual lifecycle state at request time.
	State string `json:"state"`
	// LogLevel is the current mutable logging threshold.
	LogLevel string `json:"log_level"`
	// StartedAt is absent before successful startup.
	StartedAt time.Time `json:"started_at"`
	// Uptime is the elapsed time in Go duration string format.
	Uptime string `json:"uptime"`
	// UptimeSeconds distinguishes an omitted uptime from a running zero uptime.
	UptimeSeconds *int64 `json:"uptime_seconds"`
	// Build retains just the build metadata needed by these assertions.
	Build struct {
		// GoVersion identifies the toolchain embedded in the binary.
		GoVersion string `json:"go_version"`
	} `json:"build"`
}

// getInfo serves the info handler and decodes its body.
func getInfo(t *testing.T, app *Application) (infoBody, string, http.Header) {
	t.Helper()

	recorder := httptest.NewRecorder()

	app.InfoHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, InfoPath, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("info = %d, want 200", recorder.Code)
	}

	var body infoBody

	mustNil(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	return body, recorder.Body.String(), recorder.Header()
}

// TestInfoBeforeStart checks configured identity and safe response headers.
// Startup timestamps must be absent while build metadata remains available.
func TestInfoBeforeStart(t *testing.T) {
	app := New(
		func(app *Builder) {
			app.Name("billing")
			app.Version("1.4.0")
			app.Environment(Production)
		},
	)

	body, raw, header := getInfo(t, app)

	if body.Name != "billing" || body.Version != "1.4.0" || body.Environment != "production" ||
		body.State != "new" || body.LogLevel != "INFO" {
		t.Fatalf("info = %+v", body)
	}

	if strings.Contains(raw, "started_at") || strings.Contains(raw, "uptime") {
		t.Fatalf("info before start = %s, want no start time", raw)
	}

	if body.Build.GoVersion == "" {
		t.Fatalf("info = %s, want a go version", raw)
	}

	if header.Get("Content-Type") != "application/json" ||
		header.Get("Cache-Control") != "no-store" ||
		header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", header)
	}
}

// TestInfoWhileRunning checks startup time and nonnegative uptime reporting.
// An unconfigured name must remain omitted from the JSON body.
func TestInfoWhileRunning(t *testing.T) {
	app := New()
	before := time.Now()

	mustNil(t, app.Start(context.Background()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	body, raw, _ := getInfo(t, app)

	if body.State != "running" || body.UptimeSeconds == nil || *body.UptimeSeconds < 0 {
		t.Fatalf("info = %s", raw)
	}

	uptime, err := time.ParseDuration(body.Uptime)

	mustNil(t, err)

	if uptime < 0 || int64(uptime/time.Second) != *body.UptimeSeconds {
		t.Fatalf("inconsistent uptime: %s", raw)
	}

	if body.StartedAt.Before(before.Add(-time.Second)) || body.StartedAt.After(time.Now()) {
		t.Fatalf("started_at = %v, want about %v", body.StartedAt, before)
	}

	if strings.Contains(raw, `"name"`) {
		t.Fatalf("info = %s, want empty name omitted", raw)
	}
}

// TestInfoFollowsLogLevel checks that info reads the live logging threshold.
// A runtime change must appear without reconstructing the
func TestInfoFollowsLogLevel(t *testing.T) {
	app := New()
	app.SetLogLevel(slog.LevelDebug)

	if body, _, _ := getInfo(t, app); body.LogLevel != "DEBUG" {
		t.Fatalf("log_level = %q, want DEBUG", body.LogLevel)
	}
}

// TestInfoMatchesHandlerFields checks the direct reporting API before startup.
// It must expose the same identity and absence of uptime as the HTTP handler.
func TestInfoMatchesHandlerFields(t *testing.T) {
	app := New(func(app *Builder) {
		app.Name("billing")
		app.Version("1.4.0")
		app.Environment(Production)
	})

	info := app.Info()

	if info.Name != "billing" || info.Version != "1.4.0" || info.Environment != Production ||
		info.State != "new" || info.Uptime != "" || info.UptimeSeconds != nil || info.Build.GoVersion == "" {
		t.Fatalf("info = %+v", info)
	}
}

// TestInfoUptimeFormatsDuration truncates subsecond precision in both uptime
// fields, including immediately after startup completes.
func TestInfoUptimeFormatsDuration(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app := New()

		mustNil(t, app.Start(t.Context()))
		t.Cleanup(func() {
			mustNil(t, app.Shutdown(context.Background()))
		})

		body, raw, _ := getInfo(t, app)

		if body.Uptime != "0s" || body.UptimeSeconds == nil || *body.UptimeSeconds != 0 {
			t.Fatalf("initial uptime = %s", raw)
		}

		time.Sleep(time.Hour + 2*time.Minute + 3*time.Second + 500*time.Millisecond)

		body, raw, _ = getInfo(t, app)

		if body.Uptime != "1h2m3s" || body.UptimeSeconds == nil || *body.UptimeSeconds != 3723 {
			t.Fatalf("elapsed uptime = %s", raw)
		}
	})
}

// TestMainVersionTagged checks release version discovery from build metadata.
// Tagged versions must remain unchanged for application identity.
func TestMainVersionTagged(t *testing.T) {
	t.Parallel()

	build := &debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}}

	if got := mainVersion(build, true); got != "v1.4.0" {
		t.Fatalf("mainVersion(%+v) = %q, want v1.4.0", build, got)
	}
}

// TestMainVersionDevel checks that the toolchain's development marker is
// omitted rather than advertised as an application release version.
func TestMainVersionDevel(t *testing.T) {
	t.Parallel()

	build := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}

	if got := mainVersion(build, true); got != "" {
		t.Fatalf("mainVersion(%+v) = %q, want empty", build, got)
	}
}

// TestMainVersionEmpty checks metadata with no main-module version.
// Missing release information must remain an empty identity field.
func TestMainVersionEmpty(t *testing.T) {
	t.Parallel()

	build := &debug.BuildInfo{}

	if got := mainVersion(build, true); got != "" {
		t.Fatalf("mainVersion(%+v) = %q, want empty", build, got)
	}
}

// TestMainVersionNoBuildInfo checks binaries without readable build metadata.
// The absence flag must permit a nil metadata pointer.
func TestMainVersionNoBuildInfo(t *testing.T) {
	t.Parallel()

	if got := mainVersion(nil, false); got != "" {
		t.Fatalf("mainVersion(nil) = %q, want empty", got)
	}
}

// TestNewBuildInfo checks projection of toolchain, module, and VCS metadata.
// Missing metadata must produce the zero reporting record.
func TestNewBuildInfo(t *testing.T) {
	build := newBuildInfo(&debug.BuildInfo{
		GoVersion: "go1.27.1",
		Main:      debug.Module{Path: "example.com/billing", Version: "v1.4.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
			{Key: "vcs.time", Value: "2026-10-09T18:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)

	want := BuildInfo{
		GoVersion:     "go1.27.1",
		ModulePath:    "example.com/billing",
		ModuleVersion: "v1.4.0",
		VCSRevision:   "abc123",
		VCSTime:       "2026-10-09T18:00:00Z",
		VCSModified:   true,
	}

	if build != want {
		t.Fatalf("newBuildInfo = %+v, want %+v", build, want)
	}

	if newBuildInfo(nil, false) != (BuildInfo{}) {
		t.Fatal("newBuildInfo without build info must be empty")
	}
}
