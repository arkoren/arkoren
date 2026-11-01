package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"text/template"

	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/scaffold"
	"arkoren.dev/cmd/arkoren/internal/skill"
)

// TestNewAgentDefaultsDescribeDirectComposition verifies consumer instructions.
func TestNewAgentDefaultsDescribeDirectComposition(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--module", "example.com/hello"}, env); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(env.Dir, "hello/AGENTS.md"))

	if err != nil {
		t.Fatal(err)
	}

	contains(t, string(content), "example.com/hello", ".agents/skills/arkoren/SKILL.md",
		"make check", "make run", "make help", "internal/bootstrap", "AGENTS.md belong to the project")
	contains(t, string(content), "Composition uses direct constructor calls")
}

// TestNewCustomAgentDirectoryUpdatesEveryPointer keeps all generated links aligned.
func TestNewCustomAgentDirectoryUpdatesEveryPointer(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--skills-dir", ".claude/skills"}, env); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"AGENTS.md", "README.md"} {
		content, err := os.ReadFile(filepath.Join(env.Dir, "hello", name))

		if err != nil {
			t.Fatal(err)
		}

		contains(t, string(content), ".claude/skills/arkoren", "--dir .claude/skills")

		if strings.Contains(string(content), ".agents/skills") {
			t.Fatalf("%s retains default path", name)
		}
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "hello/.claude/skills/arkoren/SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

// TestNewWithoutAgentFilesPreservesApplicationOnlyScaffold provides an explicit opt-out.
func TestNewWithoutAgentFilesPreservesApplicationOnlyScaffold(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--no-agent-files"}, env); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"AGENTS.md", ".agents"} {
		if _, err := os.Stat(filepath.Join(env.Dir, "hello", name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("opt-out wrote %s: %v", name, err)
		}
	}

	content, err := os.ReadFile(filepath.Join(env.Dir, "hello/README.md"))

	if err != nil || strings.Contains(string(content), "Agent instructions") || strings.Contains(string(content), "skills update") {
		t.Fatalf("opt-out README contains agent instructions: %s, %v", content, err)
	}
}

// TestNewAgentFileCollisionBlocksWholeScaffold protects existing user instructions.
func TestNewAgentFileCollisionBlocksWholeScaffold(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.WriteFile(filepath.Join(env.Dir, "AGENTS.md"), []byte("my rules"), 0o600); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{".", "--yes"}, env))

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 1 {
		t.Fatalf("collision partially wrote scaffold: %v, %v", entries, err)
	}
}

// TestNewAgentSkillCollisionBlocksWholeScaffold includes bundled paths in preflight.
func TestNewAgentSkillCollisionBlocksWholeScaffold(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	path := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren/SKILL.md")

	if err := files.WriteFile(t.Context(), path, []byte("existing")); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{".", "--yes"}, env))

	if _, err := os.Stat(filepath.Join(env.Dir, "go.mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skill collision partially wrote scaffold: %v", err)
	}
}

// TestNewDryRunListsAllAgentFiles exposes bundle and instruction paths for review.
func TestNewDryRunListsAllAgentFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--dry-run", "--verbose"}, env); err != nil {
		t.Fatal(err)
	}

	contains(t, env.Stdout.(*bytes.Buffer).String(), "AGENTS.md", ".agents/skills/arkoren/SKILL.md",
		".agents/skills/arkoren/references/", skill.SkillManifestName)

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run wrote files: %v, %v", entries, err)
	}
}

// TestNewRejectsUnsafeAgentDirectories prevents escapes and unsafe shell snippets.
func TestNewRejectsUnsafeAgentDirectories(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"", "../escape", "/absolute", "a/../b", "a\\b", "a`id`", "$(id)", "path with space"} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runNew(t.Context(), []string{"hello", "--skills-dir", path}, env))
	}
}

// TestNewRejectsContradictoryAgentFlags makes opt-out mistakes visible.
func TestNewRejectsContradictoryAgentFlags(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	requireNewUsage(t, runNew(t.Context(), []string{"hello", "--skills-dir", ".claude/skills", "--no-agent-files"}, env))
}

// TestNewSkillManifestIsLast makes partial scaffolds detectable before ownership
// is recorded, matching the standalone installer's completion boundary.
func TestNewSkillManifestIsLast(t *testing.T) {
	t.Parallel()

	inventory, err := scaffold.Render(t.Context(), scaffold.Options{Folder: "hello", Module: "hello"}, newTemplates, skillSource)

	if err != nil {
		t.Fatal(err)
	}

	if inventory[len(inventory)-1].Path != filepath.Join(skill.DefaultSkillsDir, "arkoren", skill.SkillManifestName) {
		t.Fatalf("last generated path = %s", inventory[len(inventory)-1].Path)
	}
}

// TestNewEnvironmentDefaultsStayPrivate checks development defaults and keeps
// both unused secrets and application tests out of the generated starter.
func TestNewEnvironmentDefaultsStayPrivate(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{".", "--no-agent-files"}, env); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(env.Dir, ".env")
	content, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	contains(t, string(content), "APP_DEBUG=true", "ARKOREN_MCP_URL=http://127.0.0.1:8080/mcp")

	info, err := os.Stat(path)

	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private .env permissions: %v, %v", info, err)
	}

	example, err := os.ReadFile(filepath.Join(env.Dir, ".env.example"))

	if err != nil {
		t.Fatal(err)
	}

	contains(t, string(example), "APP_DEBUG=false", "ARKOREN_MCP_URL=http://127.0.0.1:8080/mcp")

	if strings.Contains(string(content)+string(example), "TOKEN") {
		t.Fatal("starter contains an unused token setting")
	}

	err = filepath.WalkDir(env.Dir, func(path string, _ os.DirEntry, err error) error {
		if strings.HasSuffix(path, "_test.go") {
			t.Errorf("unexpected generated test: %s", path)
		}

		return err
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestNewBootstrapReadsEnvironmentInCallback protects deferred settings reads
// in application construction and server composition, not factory acquisition.
func TestNewBootstrapReadsEnvironmentInCallback(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/server.go")

	contains(t, source, "listener.Addr(os.Getenv(\"HTTP_ADDR\"))")
	contains(t, renderedNewSource(t, "internal/bootstrap/app.go"), "app.Debug(os.Getenv(\"APP_DEBUG\") == \"true\")")
}

// TestNewMCPReturnsFactory keeps construction and mounting inside the selected
// composition callback, with debug selection owned by the root composer.
func TestNewMCPReturnsFactory(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/mcp.go")
	contains(t, source, "application.Factory[*mcpserver.Server]", "return func(compose *application.Composer) *mcpserver.Server",
		`routes.MCPTools("http")`, `routes.Mount("/mcp", endpoint)`)

	_, implementation, _ := strings.Cut(source, "func MCP(")

	if strings.Contains(implementation, ".UseDebug(") || strings.Contains(implementation, "auth.") || strings.Contains(implementation, "TOKEN") {
		t.Fatal("MCP factory owns debug policy or adds authentication to the starter")
	}

	composition := renderedNewSource(t, "internal/bootstrap/bootstrap.go")

	contains(t, composition, "compose.UseDebug(MCP(routes))")
}

// TestNewMainAutoloadsEnvironment keeps dotenv loading out of the run helper.
func TestNewMainAutoloadsEnvironment(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "cmd/server/main.go")

	contains(t, source, `_ "github.com/joho/godotenv/autoload"`)

	if strings.Contains(source, "godotenv.Load") {
		t.Fatal("main explicitly loads dotenv")
	}
}

// writeNewRuntimeTest adds a verification harness only to the temporary build
// fixture. These tests never become part of the generated starter files.
func writeNewRuntimeTest(t *testing.T, destination string) {
	t.Helper()

	path := filepath.Join(destination, "internal/bootstrap/runtime_test.go")

	if err := os.WriteFile(path, []byte(newRuntimeTest), 0o600); err != nil {
		t.Fatal(err)
	}

	path = filepath.Join(destination, "internal/bootstrap/startup_test.go")

	if err := os.WriteFile(path, []byte(newStartupRuntimeTest), 0o600); err != nil {
		t.Fatal(err)
	}

	path = filepath.Join(destination, "internal/bootstrap/security_test.go")

	if err := os.WriteFile(path, []byte(newSecurityRuntimeTest), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newRuntimeTest verifies actual server, application, and MCP adapter behavior in
// the generated project using its real module dependencies.
const newRuntimeTest = `package bootstrap_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/server"
	"arkoren.dev/driver/mcp/mcpserver"
	"example.com/hello/internal/bootstrap"
)

// TestLoggerRedirectedOutputUsesJSON verifies output and mutable level wiring.
func TestLoggerRedirectedOutputUsesJSON(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "logs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})
	previous := os.Stderr
	os.Stderr = output
	t.Cleanup(func() { os.Stderr = previous })
	t.Setenv("APP_DEBUG", "false")
	app := application.New(bootstrap.Application())
	app.Logger().Debug("hidden")
	app.SetLogLevel(slog.LevelDebug)
	app.Logger().Debug("visible")
	content, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "hidden") || !strings.Contains(string(content), "\"msg\":\"visible\"") {
		t.Fatalf("unexpected log output: %s", content)
	}
}

// TestComposeStartsAndStops verifies the typed router and server graph with debug
// disabled. The returned listener binds an ephemeral port and cleanup releases it.
func TestComposeStartsAndStops(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	app := application.New()
	var listener *server.Server
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		routes := compose.Use(bootstrap.Router())
		listener = compose.Use(bootstrap.Server(routes))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://"+listener.Addr()+application.StartupPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		response.Body.Close()
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("startup status = %d", response.StatusCode)
	}
	address := listener.Addr()
	if err := app.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listener not released: %v", err)
	}
	t.Cleanup(func() {
		probe.Close()
	})
}

// TestComposeDebugStartsWithoutToken protects local development MCP without
// requiring authentication secrets or a configured telemetry driver.
func TestComposeDebugStartsWithoutToken(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("ARKOREN_MCP_TOKEN", "")
	app := application.New(func(app *application.Builder) {
		app.Debug(true)
	})
	if err := app.Start(t.Context(), bootstrap.Compose); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
}

// TestMCPFactoryDefersMounting checks lazy construction and the typed endpoint
// returned by Use without starting a separate transport or owning a listener.
func TestMCPFactoryDefersMounting(t *testing.T) {
	t.Parallel()
	app := application.New()
	routes := router.New()
	feature := bootstrap.MCP(routes)
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("MCP factory mounted before composition: %d", response.Code)
	}
	var endpoint *mcpserver.Server
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		endpoint = compose.Use(feature)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if endpoint == nil {
		t.Fatal("factory did not return the mounted endpoint")
	}
	response = httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code == http.StatusNotFound {
		t.Fatal("MCP composer did not mount the endpoint")
	}
}

// TestMCPFactorySkippedWithoutRouter proves that a skipped debug factory does
// not dereference dependencies, construct its endpoint, or mount any routes.
func TestMCPFactorySkippedWithoutRouter(t *testing.T) {
	t.Parallel()
	feature := bootstrap.MCP(nil)
	app := application.New()
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		endpoint, selected := compose.UseDebug(feature)
		if endpoint != nil || selected {
			t.Fatal("disabled factory returned an endpoint")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

// TestMCPComposerPreservesSubgroupMiddleware checks that the returned shared
// router keeps its welcome page and routes MCP through inherited middleware.
func TestMCPComposerPreservesSubgroupMiddleware(t *testing.T) {
	t.Parallel()
	app := application.New()
	var routes *router.Router
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router())
		group := routes.With(func(next router.Handler) router.Handler {
			return func(writer http.ResponseWriter, request *http.Request) error {
				writer.Header().Set("X-MCP-Group", "true")

				return next(writer, request)
			}
		})
		compose.Use(bootstrap.MCP(group))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	public := httptest.NewServer(routes)
	t.Cleanup(public.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, public.URL+"/mcp", strings.NewReader(
			"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{}}"))
	if err != nil {
		t.Fatal(err)
	}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		response.Body.Close()
		client.CloseIdleConnections()
	})
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "http.routes") {
		t.Fatalf("tool listing = %d %s, error = %v", response.StatusCode, body, err)
	}
	for _, name := range []string{"app.traces", "app.trace", "app.logs", "app.metrics"} {
		if !strings.Contains(string(body), name) {
			t.Fatalf("missing %s in catalog: %s", name, body)
		}
	}
	if response.Header.Get("X-MCP-Group") != "true" {
		t.Fatal("MCP bypassed subgroup middleware")
	}
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, public.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		missing.Body.Close()
	})
	welcome, err := io.ReadAll(missing.Body)
	if err != nil || missing.StatusCode != http.StatusOK || !strings.Contains(missing.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("welcome page = %d %s, error = %v", missing.StatusCode, welcome, err)
	}
}

// TestServerFactoryDefersConfiguration checks even environment-derived server
// construction happens inside composition, not while obtaining the callback.
func TestServerFactoryDefersConfiguration(t *testing.T) {
	t.Setenv("HTTP_ADDR", "invalid-address")
	feature := bootstrap.Server(http.NotFoundHandler())
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	app := application.New()
	var listener *server.Server
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		listener = compose.Use(feature)
		if listener.Addr() != "127.0.0.1:0" {
			t.Fatalf("server started during composition: %s", listener.Addr())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if listener.Addr() == "127.0.0.1:0" {
		t.Fatal("returned server handle did not observe startup")
	}
}

// TestRouterFactoryDefersConstruction checks the factory produces the complete
// shared router only when the enclosing composition invokes it.
func TestRouterFactoryDefersConstruction(t *testing.T) {
	t.Parallel()
	feature := bootstrap.Router()
	var routes *router.Router
	app := application.New()
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		routes = compose.Use(feature)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if len(routes.Routes()) != 4 {
		t.Fatalf("registered routes = %v", routes.Routes())
	}
}

// TestDebugTelemetryCapturesRequestsAndLogs checks real middleware and MCP
// tool calls without an exporter, including rejection of incoming trace IDs.
func TestDebugTelemetryCapturesRequestsAndLogs(t *testing.T) {
	t.Parallel()
	app := application.New(func(app *application.Builder) {
		app.Debug(true)
	})
	var routes *router.Router
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router())
		routes.Get("/logged", func(writer http.ResponseWriter, request *http.Request) error {
			app.Logger().InfoContext(request.Context(), "local request marker")

			return nil
		})
		compose.UseDebug(bootstrap.MCP(routes))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if app.Telemetry().Driver() != nil {
		t.Fatal("debug capture configured an export driver")
	}
	request := httptest.NewRequest(http.MethodGet, "/logged", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	trace := response.Header().Get("traceresponse")
	if trace == "" || strings.Contains(trace, "11111111111111111111111111111111") {
		t.Fatalf("missing local trace ID or trusted caller trace: %s", trace)
	}
	traces := callDiagnostic(t, routes, "app.traces")
	if !strings.Contains(traces, "GET /logged") {
		t.Fatalf("request span not captured: %s", traces)
	}
	logs := callDiagnostic(t, routes, "app.logs")
	if !strings.Contains(logs, "local request marker") || !strings.Contains(logs, strings.Split(trace, "-")[1]) {
		t.Fatalf("correlated request log not captured: %s", logs)
	}
	metrics := callDiagnostic(t, routes, "app.metrics")
	if !strings.Contains(metrics, "http.server.request.duration") {
		t.Fatalf("request metrics not captured: %s", metrics)
	}
}

// callDiagnostic invokes a real diagnostic tool through the generated endpoint.
func callDiagnostic(t *testing.T, routes *router.Router, name string) string {
	t.Helper()
	body := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\""+name+"\",\"arguments\":{}}}"
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "\"isError\":true") {
		t.Fatalf("tool %s = %d %s", name, response.Code, response.Body.String())
	}

	return response.Body.String()
}

// TestDebugDisabledLeavesTelemetryInactive checks that middleware alone neither
// records telemetry nor exposes diagnostics when debug composition is skipped.
func TestDebugDisabledLeavesTelemetryInactive(t *testing.T) {
	t.Parallel()
	app := application.New()
	var routes *router.Router
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router())
		compose.UseDebug(bootstrap.MCP(routes))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Header().Get("traceresponse") != "" {
		t.Fatal("debug-off request generated trace IDs")
	}
	response = httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("debug-off application exposed diagnostics")
	}
	_, span := app.Telemetry().Tracer("test").Start(t.Context(), "disabled")
	defer span.End()
	if span.IsRecording() || !span.Context().TraceID.IsZero() {
		t.Fatal("debug-off telemetry is active")
	}
}

// TestApplicationReadsEnvironmentWhenApplied checks that acquiring the policy
// does not snapshot APP_DEBUG and that only the exact true value enables it.
func TestApplicationReadsEnvironmentWhenApplied(t *testing.T) {
	t.Setenv("APP_DEBUG", "false")
	policy := bootstrap.Application()
	t.Setenv("APP_DEBUG", "true")
	if !application.New(policy).Debug() {
		t.Fatal("debug not enabled")
	}
	t.Setenv("APP_DEBUG", "false")
	if application.New(policy).Debug() {
		t.Fatal("debug not disabled")
	}
}
`

// newMakeDirectory writes the rendered Makefile without resolving dependencies.
func newMakeDirectory(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(renderedNewSource(t, "Makefile")), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
}

// runNewMake executes generated recipes independently of the outer Make process.
// Clear inherited flags, recursion state, injected files, and variable overrides;
// fixture command-line overrides remain explicit in args.
func runNewMake(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "make", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"MAKEFLAGS=", "MFLAGS=", "GNUMAKEFLAGS=", "MAKELEVEL=0", "MAKEFILES=", "MAKEOVERRIDES=",
	)

	return command.CombinedOutput()
}

// TestNewMakeIgnoresParentEnvironment keeps outer Make recipes from changing
// fixture commands, output, or the Makefiles loaded by a generated project.
func TestNewMakeIgnoresParentEnvironment(t *testing.T) {
	dir := newMakeDirectory(t)
	inherited := filepath.Join(t.TempDir(), "inherited.mk")

	if err := os.WriteFile(inherited, []byte("$(error inherited parent Makefile)\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MAKELEVEL", "37")
	t.Setenv("MAKEFLAGS", "n")
	t.Setenv("MFLAGS", "-n")
	t.Setenv("GNUMAKEFLAGS", "s")
	t.Setenv("MAKEFILES", inherited)
	t.Setenv("MAKEOVERRIDES", "GO=false")

	output, err := runNewMake(t, dir, "-n", "run", "GO=custom-go")

	if err != nil {
		t.Fatalf("make inherited parent configuration: %v\n%s", err, output)
	}

	want := "custom-go build -o bin/server ./cmd/server\n./bin/server\n"

	if string(output) != want {
		t.Fatalf("isolated run commands = %q, want %q", output, want)
	}
}

// TestNewMakeDefaultsToHelp ensures listing tasks does not require Go or Docker.
func TestNewMakeDefaultsToHelp(t *testing.T) {
	t.Parallel()

	dir := newMakeDirectory(t)

	output, err := runNewMake(t, dir, "GO=false", "DOCKER=false")

	if err != nil {
		t.Fatalf("make: %v\n%s", err, output)
	}

	contains(t, string(output), "help", "run", "build", "fmt-check", "test-race", "check", "docker-build", "docker-run")
}

// TestNewMakeCheckRejectsFormattingWithoutRewriting protects nonmutating checks.
func TestNewMakeCheckRejectsFormattingWithoutRewriting(t *testing.T) {
	t.Parallel()

	dir := newMakeDirectory(t)
	path := filepath.Join(dir, "main.go")
	source := "package main\nfunc main(){}\n"

	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := runNewMake(t, dir, "check", "GO=false")

	if err == nil || !strings.Contains(string(output), "main.go") {
		t.Fatalf("check = %v\n%s", err, output)
	}

	content, err := os.ReadFile(path)

	if err != nil || string(content) != source {
		t.Fatalf("check changed source: %q, %v", content, err)
	}

	output, err = runNewMake(t, dir, "fmt", "fmt-check")

	if err != nil {
		t.Fatalf("format and verify: %v\n%s", err, output)
	}
}

// TestNewMakeCommandsRespectOverrides checks real recipe execution without
// downloading modules, starting a service, or requiring a Docker daemon.
func TestNewMakeCommandsRespectOverrides(t *testing.T) {
	t.Parallel()

	dir := newMakeDirectory(t)
	script := "#!/bin/sh\n" +
		"printf '%s|%s\\n' \"$*\" \"$CGO_ENABLED\" >> commands.log\n" +
		"if [ \"$1\" = build ] && [ \"$2\" = -o ]; then\n" +
		"  mkdir -p bin && cp \"$0\" bin/server\n" +
		"fi\n"

	if err := os.WriteFile(filepath.Join(dir, "record"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runNewMake(t, dir, "check", "build", "run", "test-race", "docker-build", "docker-run",
		"GO=./record", "DOCKER=./record", "IMAGE=custom:dev", "PORT=9090")

	if err != nil {
		t.Fatalf("make: %v\n%s", err, output)
	}

	content, err := os.ReadFile(filepath.Join(dir, "commands.log"))

	if err != nil {
		t.Fatal(err)
	}

	contains(t, string(content),
		"vet ./...|", "test ./...|", "build -o bin/server ./cmd/server|",
		"test -race ./...|1", "build -t custom:dev .|",
		"run --rm -p 127.0.0.1:9090:8080 -e HTTP_ADDR=:8080 -e APP_DEBUG=false custom:dev|",
	)

	if strings.Contains(string(content), "mod tidy") || strings.Contains(string(content), "-count=1") {
		t.Fatalf("recipes rewrite dependencies or bypass caching:\n%s", content)
	}
}

// TestNewMakeRunBuildsThenRunsBinary keeps the Go wrapper out of signal handling.
func TestNewMakeRunBuildsThenRunsBinary(t *testing.T) {
	t.Parallel()

	dir := newMakeDirectory(t)

	output, err := runNewMake(t, dir, "-n", "run", "GO=custom-go")

	if err != nil {
		t.Fatalf("make: %v\n%s", err, output)
	}

	want := "custom-go build -o bin/server ./cmd/server\n./bin/server\n"

	if string(output) != want {
		t.Fatalf("run commands = %q, want %q", output, want)
	}
}

// TestNewMakeRunPreservesBuildFailure prevents a stale binary from running.
func TestNewMakeRunPreservesBuildFailure(t *testing.T) {
	t.Parallel()

	dir := newMakeDirectory(t)

	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "bin", "server"), []byte("#!/bin/sh\necho stale-server-ran\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "fail-build"), []byte("#!/bin/sh\necho build-failed >&2\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runNewMake(t, dir, "run", "GO=./fail-build")

	exit, ok := errors.AsType[*exec.ExitError](err)

	if !ok || exit.ExitCode() <= 0 || !strings.Contains(string(output), "build-failed") {
		t.Fatalf("build failure = %v\n%s", err, output)
	}

	if strings.Contains(string(output), "./bin/server") || strings.Contains(string(output), "stale-server-ran") {
		t.Fatalf("run continued after build failure:\n%s", output)
	}
}

// TestNewMakeRunPreservesServerFailure keeps application failures visible.
func TestNewMakeRunPreservesServerFailure(t *testing.T) {
	t.Parallel()

	dir := newMakeDirectory(t)

	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "bin", "server"), []byte("#!/bin/sh\necho server-failed >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runNewMake(t, dir, "run", "GO=true")

	exit, ok := errors.AsType[*exec.ExitError](err)

	if !ok || exit.ExitCode() <= 0 || !strings.Contains(string(output), "server-failed") {
		t.Fatalf("server failure = %v\n%s", err, output)
	}
}

// TestNewRouterRecoversHandlerPanics executes the generated public Router
// factory against an in-memory HTTP request. The fixture uses checkout-only
// modules with the proxy disabled; no dependency installation or listener is
// needed, and no application tests become part of the scaffold.
func TestNewRouterRecoversHandlerPanics(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if code := run(t.Context(), []string{"new", ".", "--no-agent-files"}, env); code != 0 {
		t.Fatalf("scaffolding exit = %d", code)
	}

	output := runGeneratedRouterProbe(t, env.Dir)

	if strings.TrimSpace(output) != "recovered" {
		t.Fatalf("generated router probe = %s", output)
	}
}

// runGeneratedRouterProbe compiles only the generated routing factory in a
// temporary consumer module. The companion executable observes HTTP behavior,
// not source spelling, and its local replacements never modify repository modules.
func runGeneratedRouterProbe(t *testing.T, scaffold string) string {
	t.Helper()

	routing, err := os.ReadFile(filepath.Join(scaffold, "internal/bootstrap/router.go"))

	if err != nil {
		t.Fatal(err)
	}

	fixture := t.TempDir()

	if err := os.Mkdir(filepath.Join(fixture, "bootstrap"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(fixture, "bootstrap/router.go"), routing, 0o600); err != nil {
		t.Fatal(err)
	}

	checkout, err := filepath.Abs("../..")

	if err != nil {
		t.Fatal(err)
	}

	module := "module example.com/recovery\n\ngo 1.27\n\nrequire arkoren.dev/core v0.0.0\n"

	for _, path := range []string{"core", "foundation/collection", "foundation/key", "foundation/problem", "foundation/query", "foundation/validate"} {
		module += fmt.Sprintf("\nreplace arkoren.dev/%s => %s\n", path, strconv.Quote(filepath.ToSlash(filepath.Join(checkout, path))))
	}

	if err := os.WriteFile(filepath.Join(fixture, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(fixture, "main.go"), []byte(generatedRouterProbe), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "go", "run", "-mod=mod", ".")
	command.Dir = fixture
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")

	output, err := command.CombinedOutput()

	if err != nil {
		t.Fatalf("generated router execution: %v\n%s", err, output)
	}

	return string(output)
}

// generatedRouterProbe calls the unchanged public factory from a temporary
// consumer executable. It verifies a pre-commit panic becomes a private HTTP
// 500 problem instead of escaping ServeHTTP or exposing the panic value.
const generatedRouterProbe = `package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
	"example.com/recovery/bootstrap"
)

// main exercises the generated router without a live network listener.
func main() {
	app := application.New(func(app *application.Builder) {
		app.LogHandler(func(level slog.Leveler) slog.Handler {
			return slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})
		})
	})
	var routes *router.Router
	if err := app.Start(context.Background(), func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router())
		routes.Get("/panic", func(http.ResponseWriter, *http.Request) error {
			panic("private-panic-value")
		})
	}); err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "private-panic-value") {
		panic(fmt.Sprintf("unexpected panic response: %d %s", response.Code, response.Body))
	}
	if err := app.Shutdown(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println("recovered")
}
`

// renderedNewSource renders one generated file for structural regressions.
// It uses a stable module and name without writing files or resolving modules.
func renderedNewSource(t *testing.T, path string) string {
	t.Helper()

	content, err := scaffold.RenderFile(path, map[string]string{
		"Module": "example.com/hello", "Name": "hello",
	}, newTemplates)

	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}

// TestNewConditionalsPreserveSpacing checks raw output before gofmt can hide
// blank lines introduced by standalone template actions.
func TestNewConditionalsPreserveSpacing(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"go.mod", "README.md", "internal/bootstrap/bootstrap.go",
		"internal/bootstrap/router.go", "internal/bootstrap/server.go",
	} {
		definition, err := template.ParseFS(newTemplates, "templates/"+path+".tmpl")

		if err != nil {
			t.Fatal(err)
		}

		var buffer bytes.Buffer

		err = definition.Execute(&buffer, map[string]string{
			"Module": "example.com/hello", "Name": "hello",
		})

		if err != nil {
			t.Fatal(err)
		}

		source := buffer.String()

		if strings.Contains(source, "\n\n\n") || !strings.HasSuffix(source, "\n") {
			t.Fatalf("%s has extra blank lines or lacks a final newline:\n%s", path, source)
		}
	}
}

// TestNewEditorConfigPreservesGoAndMarkdownConventions checks editor defaults
// without imposing a line-length limit or stripping Markdown hard breaks.
func TestNewEditorConfigPreservesGoAndMarkdownConventions(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, ".editorconfig")

	contains(t, source,
		"root = true", "charset = utf-8", "end_of_line = lf",
		"insert_final_newline = true", "trim_trailing_whitespace = true",
		"indent_style = space\nindent_size = 2",
		"[*.go]\nindent_style = tab\nindent_size = 4",
		"[go.mod]\nindent_style = tab\nindent_size = 4",
		"[Makefile]\nindent_style = tab\nindent_size = 4",
		"[*.md]\ntrim_trailing_whitespace = false",
	)
}

// TestNewContainerUsesCachedStaticBuild protects the minimal runtime and the
// fixed server command path.
func TestNewContainerUsesCachedStaticBuild(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "Dockerfile")

	contains(t, source,
		"FROM --platform=$BUILDPLATFORM golang:1.27 AS build",
		"ARG TARGETOS", "ARG TARGETARCH", "ENV CGO_ENABLED=0 GOWORK=off",
		"--mount=type=bind,target=.", "--mount=type=cache,target=/go/pkg/mod",
		"--mount=type=cache,target=/root/.cache/go-build",
		`GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/server`,
		"FROM gcr.io/distroless/static-debian13:nonroot",
		"COPY --link --from=build /out/app /app", "EXPOSE 8080", `ENTRYPOINT ["/app"]`,
	)
}

// TestNewIgnoresKeepDependencyInputs ensures local secrets and artifacts stay
// out while checksums, examples, and vendored pre-release dependencies remain.
func TestNewIgnoresKeepDependencyInputs(t *testing.T) {
	t.Parallel()

	git := renderedNewSource(t, ".gitignore")

	contains(t, git, ".env\n", ".env.*\n", "!.env.example\n", "/bin/\n", "/go.work\n")

	docker := renderedNewSource(t, ".dockerignore")

	contains(t, docker, ".git\n", ".env\n", ".env.*\n", "bin\n", "go.work\n")

	for _, pattern := range []string{"vendor", "go.sum", "*.sum"} {
		if strings.Contains(git+docker, "\n"+pattern+"\n") {
			t.Fatalf("ignore files exclude dependency input %s", pattern)
		}
	}
}

// TestNewBootstrapSeparatesResponsibilities keeps constructors separate from
// the composition root.
func TestNewBootstrapSeparatesResponsibilities(t *testing.T) {
	t.Parallel()

	composition := renderedNewSource(t, "internal/bootstrap/bootstrap.go")

	requireNewNoConstructors(t, composition)

	routing := renderedNewSource(t, "internal/bootstrap/router.go")

	if !strings.Contains(routing, "app.WelcomeHandler()") {
		t.Fatal("router does not mount the application welcome page")
	}

	serving := renderedNewSource(t, "internal/bootstrap/server.go")

	if !strings.Contains(serving, "listener := server.New(handler, func(listener *server.Builder)") {
		t.Fatal("server does not own listener policy")
	}
}

// requireNewNoConstructors keeps route and server definitions out of the
// composition file so lifecycle wiring remains easy to scan.
func requireNewNoConstructors(t *testing.T, source string) {
	t.Helper()

	for _, declaration := range []string{
		"func newRouter", "func newServer", "func provideRouter", "func provideServer",
	} {
		if strings.Contains(source, declaration) {
			t.Fatalf("composition contains %s", declaration)
		}
	}
}

// TestNewRouterAnnotatesExistingRoutes checks the router opts in
// to metadata without automatically publishing documentation or operator info.
func TestNewRouterAnnotatesExistingRoutes(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/router.go")

	contains(t, source,
		`routes.Get("/", app.WelcomeHandler(), application.WelcomeOperation)`,
		"routes.Get(application.LivePath, app.LiveHandler(), application.LiveOperation)",
		"routes.Get(application.ReadyPath, app.ReadyHandler(), application.ReadyOperation)",
		"routes.Get(application.StartupPath, app.StartupHandler(), application.StartupOperation)",
	)

	if strings.Count(source, "routes.Get(") != 4 {
		t.Fatal("router added routes beyond the welcome page and probes")
	}

	unwanted := strings.Contains(source, "OpenAPIHandler") || strings.Contains(source, "InfoHandler") ||
		strings.Contains(source, "swagger")

	if unwanted {
		t.Fatal("router automatically exposes documentation or operator info")
	}
}

// TestNewApplicationConstructionIsSeparate keeps settings in bootstrap and
// construction in the executable. Bootstrap receives this application, not a
// container-owned replacement or an error-returning application factory.
func TestNewApplicationConstructionIsSeparate(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "cmd/server/main.go")

	contains(t, source,
		`"arkoren.dev/core/application"`,
		"app := application.New(bootstrap.Application())",
	)

	if strings.Contains(source, "app.Name(") {
		t.Fatal("main owns application identity instead of bootstrap")
	}

	contains(t, renderedNewSource(t, "internal/bootstrap/app.go"),
		"func Application() application.BuilderFunc",
		`app.Name("hello")`,
		`app.Debug(os.Getenv("APP_DEBUG") == "true")`,
	)
}

// TestNewLoggerFactoryPreservesOutputPolicy checks the generated logging hook
// and keeps its dependencies in the application rather than the CLI module.
func TestNewLoggerFactoryPreservesOutputPolicy(t *testing.T) {
	t.Parallel()

	contains(t, renderedNewSource(t, "internal/bootstrap/app.go"), "app.LogHandler(Logger())")

	source := renderedNewSource(t, "internal/bootstrap/logging.go")

	contains(t, source,
		"func Logger() func(slog.Leveler) slog.Handler",
		"!isatty.IsTerminal(os.Stderr.Fd())",
		"slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})",
		"tint.NewTextHandler(os.Stderr, &tint.Options{",
		"Level:     level,", `NoColor:   os.Getenv("NO_COLOR") != ""`,
	)
	contains(t, renderedNewSource(t, "go.mod"),
		"github.com/lmittmann/tint v1.2.1", "github.com/mattn/go-isatty v0.0.24")
}

// TestNewBootstrapComposesSuppliedApplication protects the startup boundary:
// main executes the lifecycle while helpers register work through the composer.
func TestNewBootstrapComposesSuppliedApplication(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/bootstrap.go")

	contains(t, source,
		"func Compose(compose *application.Composer) {", "routes := compose.Use(Router())",
		"compose.UseDebug(MCP(routes))", "compose.Use(Server(routes))",
	)

	if strings.Contains(source, "func New(") {
		t.Fatal("bootstrap still creates the application")
	}

	if strings.Contains(source, "app.OnStart(") || strings.Contains(source, "app.OnStop(") || strings.Contains(source, "app.Go(") {
		t.Fatal("composition still owns component lifecycle bindings")
	}

	for _, path := range []string{"internal/bootstrap/server.go"} {
		component := renderedNewSource(t, path)

		contains(t, component, "compose.Cleanup(listener.Stop)", "compose.Startup(listener.Start)", "compose.Run(listener.Serve)")
	}

	contains(t, renderedNewSource(t, "cmd/server/main.go"), "app.Run(ctx, bootstrap.Compose)")
}

// TestNewManualBootstrapUsesPublicConstructors keeps direct composition small.
func TestNewManualBootstrapUsesPublicConstructors(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/bootstrap.go")

	contains(t, source, "routes := compose.Use(Router())", "compose.Use(Server(routes))", "compose.UseDebug(MCP(routes))")
}

// TestNewServerOwnsPolicy keeps listener settings beside lifecycle registration,
// with environment access deferred until composition invokes the factory.
func TestNewServerOwnsPolicy(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/server.go")

	contains(t, source, "func Server(handler http.Handler) application.Factory[*server.Server]",
		`listener.Addr(os.Getenv("HTTP_ADDR"))`)

	for _, obsolete := range []string{"type Config", "func Load", "HTTPAddr"} {
		if strings.Contains(source, obsolete) {
			t.Fatalf("obsolete configuration API %s", obsolete)
		}
	}
}

// TestNewMainDefersSignalCleanupBeforeExit keeps os.Exit outside the helper
// that owns the signal resource. The immediate defer covers bootstrap errors.
func TestNewMainDefersSignalCleanupBeforeExit(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "cmd/server/main.go")
	mainSource, runSource, found := strings.Cut(source, "func run() error {")

	if !found {
		t.Fatal("missing run helper")
	}

	if !strings.Contains(mainSource, "if run() != nil {\n\t\tos.Exit(1)") {
		t.Fatal("main does not map the returned error to exit status")
	}

	if strings.Contains(runSource, "os.Exit") {
		t.Fatal("run bypasses deferred cleanup")
	}

	acquisition := "ctx, stop := application.NewSignalContext(context.Background())"

	if !strings.Contains(runSource, acquisition+"\n\tdefer stop()") {
		t.Fatal("signal cleanup is not deferred immediately")
	}
}

// TestNewMainDelegatesSignalHandling keeps process signal ownership in core,
// rather than generating another worker or terminal implementation per project.
func TestNewMainDelegatesSignalHandling(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "cmd/server/main.go")

	contains(t, source, "application.NewSignalContext(context.Background())")

	for _, obsolete := range []string{"func signalContext", "os/signal", "syscall", "sync", "go-isatty"} {
		if strings.Contains(source, obsolete) {
			t.Fatalf("main contains local signal machinery %q", obsolete)
		}
	}
}

// TestNewMainReturnsLifecycleError leaves reporting and flushing with the
// application and returns its result directly after deferred signal cleanup.
func TestNewMainReturnsLifecycleError(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "cmd/server/main.go")

	contains(t, source,
		"app := application.New(", "return app.Run(ctx, bootstrap.Compose)",
		"func run() error", "if run() != nil {", "os.Exit(1)",
	)

	if strings.Contains(source, "slog") || strings.Contains(source, "bootstrap.New") {
		t.Fatal("main bypasses application creation")
	}

	if strings.Contains(source, "app.Logger()") {
		t.Fatal("main logs after telemetry has stopped")
	}
}

// TestNewRouterUsesLocalTelemetry keeps tracing explicit and incoming caller
// context untrusted, including when a public proxy connects from a private IP.
func TestNewRouterUsesLocalTelemetry(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/router.go")

	contains(t, source, "routes.Use(telemetry.Middleware())",
		"routes.Use(secureheaders.Middleware())")

	if strings.Index(source, "routes.Use(") > strings.Index(source, "routes.Get(") {
		t.Fatal("tracing registered after routes")
	}
}

// TestNewMCPAttachesReadOnlyLocalCapture checks startup-owned capture and
// explicit telemetry sharing without selecting an external driver.
func TestNewMCPAttachesReadOnlyLocalCapture(t *testing.T) {
	t.Parallel()

	source := renderedNewSource(t, "internal/bootstrap/mcp.go")

	contains(t, source, `listener.Tools(compose.Must(compose.MCPTools("app"))...)`,
		`listener.Tools(routes.MCPTools("http")...)`,
		"listener.Telemetry(telemetry)")
}

// TestNewGoReturnSpacing checks every emitted Go file. A return needs a
// preceding blank unless it is its block's sole
// statement; sole returns must not have a redundant blank after the brace.
func TestNewGoReturnSpacing(t *testing.T) {
	t.Parallel()

	inventory, err := scaffold.Render(t.Context(), scaffold.Options{
		Folder: "hello",
		Module: "example.com/hello",
	}, newTemplates, skillSource)

	if err != nil {
		t.Fatal(err)
	}

	for _, file := range inventory {
		requireNewReturnSpacing(t, file)
	}
}

// requireNewReturnSpacing inspects source positions rather than formatting
// comments or guessing from text. Every containing block follows the same
// rule, including nested guards and function literals.
func requireNewReturnSpacing(t *testing.T, file files.File) {
	t.Helper()

	if !strings.HasSuffix(file.Path, ".go") {
		return
	}

	positions := token.NewFileSet()
	syntax, err := parser.ParseFile(positions, file.Path, file.Content, 0)

	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(file.Content), "\n")

	ast.Inspect(syntax, func(node ast.Node) bool {
		if block, ok := node.(*ast.BlockStmt); ok {
			requireNewBlockReturnSpacing(t, block, positions, lines)
		}

		return true
	})
}

// requireNewBlockReturnSpacing checks only direct statements in one block.
// Other blocks are visited separately so a guard's sole return stays adjacent
// to its brace, while returns sharing a block with other statements stay apart.
func requireNewBlockReturnSpacing(t *testing.T, body *ast.BlockStmt, positions *token.FileSet, lines []string) {
	t.Helper()

	for _, node := range body.List {
		statement, ok := node.(*ast.ReturnStmt)

		if !ok {
			continue
		}

		position := positions.Position(statement.Pos())
		blank := strings.TrimSpace(lines[position.Line-2]) == ""
		wantBlank := len(body.List) > 1

		if blank != wantBlank {
			t.Errorf("%s: blank before return = %t, want %t", position, blank, wantBlank)
		}
	}
}

// newSecurityRuntimeTest verifies the generated router's security policies using
// real middleware. The fixture is not emitted into consumer projects.
const newSecurityRuntimeTest = `package bootstrap_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
	"example.com/hello/internal/bootstrap"
)

// securityRouter composes the generated router and a test-only write endpoint.
func securityRouter(t *testing.T) *router.Router {
	t.Helper()
	app := application.New()
	var routes *router.Router
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router())
		routes.Post("/write", func(writer http.ResponseWriter, _ *http.Request) error {
			writer.WriteHeader(http.StatusNoContent)

			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return routes
}

// requireSecurityHeaders checks the common policy without constraining page CSP.
func requireSecurityHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	if headers.Get("X-Content-Type-Options") != "nosniff" ||
		headers.Get("X-Frame-Options") != "DENY" ||
		headers.Get("X-XSS-Protection") != "0" {
		t.Fatalf("missing security headers: %v", headers)
	}
	if headers.Get("Referrer-Policy") == "" ||
		!strings.Contains(headers.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("missing referrer or framing policy: %v", headers)
	}
	if headers.Get("Strict-Transport-Security") != "" {
		t.Fatal("starter enabled HSTS before deployment policy was configured")
	}
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Fatalf("unexpected CORS permission: %s", name)
		}
	}
}

// TestRouterWelcomeRetainsRestrictiveCSP protects self-contained page rendering.
func TestRouterWelcomeRetainsRestrictiveCSP(t *testing.T) {
	t.Parallel()
	routes := securityRouter(t)
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://service.test/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("welcome status = %d", response.Code)
	}
	requireSecurityHeaders(t, response.Header())
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "default-src 'none'") || !strings.Contains(policy, "style-src 'unsafe-inline'") {
		t.Fatalf("welcome policy overwritten: %s", policy)
	}
}

// TestRouterUnknownRoutesHaveSecurityHeaders protects default error responses.
func TestRouterUnknownRoutesHaveSecurityHeaders(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	securityRouter(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://service.test/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing route status = %d", response.Code)
	}
	requireSecurityHeaders(t, response.Header())
}

// TestRouterRejectsCrossOriginWrites protects browser write endpoints by default.
func TestRouterRejectsCrossOriginWrites(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "http://service.test/write", nil)
	request.Header.Set("Origin", "https://untrusted.test")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()
	securityRouter(t).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write status = %d", response.Code)
	}
	requireSecurityHeaders(t, response.Header())
}

// TestRouterCSRFRejectionsAreTraced keeps blocked requests inside telemetry.
func TestRouterCSRFRejectionsAreTraced(t *testing.T) {
	t.Parallel()
	app := application.New()
	var routes *router.Router
	if err := app.Start(t.Context(), func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router())
		compose.Must(compose.MCPTools("app"))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	request := httptest.NewRequest(http.MethodPost, "http://service.test/", nil)
	request.Header.Set("Origin", "https://untrusted.test")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Header().Get("traceresponse") == "" {
		t.Fatalf("untraced CSRF rejection: status %d, headers %v", response.Code, response.Header())
	}
	requireSecurityHeaders(t, response.Header())
}

// TestRouterAllowsSameOriginWrites preserves ordinary browser form submission.
func TestRouterAllowsSameOriginWrites(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "http://service.test/write", nil)
	request.Header.Set("Origin", "http://service.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	securityRouter(t).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin write status = %d", response.Code)
	}
	requireSecurityHeaders(t, response.Header())
}

// TestRouterAllowsRequestsWithoutBrowserHeaders preserves service clients.
func TestRouterAllowsRequestsWithoutBrowserHeaders(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	securityRouter(t).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://service.test/write", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("non-browser write status = %d", response.Code)
	}
}

// TestRouterHTTPSOmitsHSTS leaves persistent transport policy to the deployment.
func TestRouterHTTPSOmitsHSTS(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	securityRouter(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://service.test/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("HTTPS welcome status = %d", response.Code)
	}
	requireSecurityHeaders(t, response.Header())
}

// TestRouterProbesRemainAccessible permits cross-origin safe health requests.
func TestRouterProbesRemainAccessible(t *testing.T) {
	t.Parallel()
	routes := securityRouter(t)
	for _, path := range []string{application.LivePath, application.ReadyPath, application.StartupPath} {
		request := httptest.NewRequest(http.MethodGet, "http://service.test"+path, nil)
		request.Header.Set("Origin", "https://untrusted.test")
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("probe %s status = %d", path, response.Code)
		}
		requireSecurityHeaders(t, response.Header())
	}
}
`

// newStartupRuntimeTest verifies startup logging in the temporary generated
// module without emitting application tests into consumer projects.
const newStartupRuntimeTest = `package bootstrap

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/application"
)

// startupApplication records real lifecycle logs in a file safe for concurrent writes.
func startupApplication(t *testing.T) (*application.Application, *os.File) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "startup-logs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})
	app := application.New(func(app *application.Builder) {
		app.LogHandler(func(level slog.Leveler) slog.Handler {
			return slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
		})
	})

	return app, output
}

// TestStartupLogsBoundAddressAfterAppStarted verifies order and the assigned port.
func TestStartupLogsBoundAddressAfterAppStarted(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	app, output := startupApplication(t)
	if err := app.Start(t.Context(), Compose); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	content, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	address := startupAddressFromLogs(t, string(content))
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logged address returned %d", response.StatusCode)
	}
}

// startupAddressFromLogs verifies one bound-address record follows app started.
func startupAddressFromLogs(t *testing.T, content string) string {
	t.Helper()
	started := strings.Index(content, "\"msg\":\"app started\"")
	httpStarted := strings.Index(content, "\"msg\":\"http server started\"")
	if started < 0 || httpStarted <= started {
		t.Fatalf("startup log ordering: %s", content)
	}
	if strings.Count(content, "\"msg\":\"http server started\"") != 1 {
		t.Fatalf("expected exactly one HTTP startup log: %s", content)
	}
	lineStart := strings.LastIndex(content[:httpStarted], "\n") + 1
	line, _, _ := strings.Cut(content[lineStart:], "\n")
	var record struct {
		// Address is the actual socket address emitted by the Started observer.
		Address string ` + "`json:\"address\"`" + `
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(record.Address)
	if err != nil {
		t.Fatal(err)
	}
	if port == "" || port == "0" {
		t.Fatalf("address lacks actual bound port: %s", record.Address)
	}

	return record.Address
}

// TestStartupFailureAfterBindOmitsAddress prevents success logging after rollback.
func TestStartupFailureAfterBindOmitsAddress(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	app, output := startupApplication(t)
	failure := errors.New("later startup failed")
	err := app.Start(t.Context(), Compose, func(compose *application.Composer) {
		compose.Startup(func(context.Context) error {
			return failure
		})
	})
	if !errors.Is(err, failure) {
		t.Fatalf("startup error = %v", err)
	}
	content, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "server started") {
		t.Fatalf("failed startup logged success: %s", content)
	}
}

// TestCompositionFailureOmitsAddress prevents success logging before startup.
func TestCompositionFailureOmitsAddress(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	app, output := startupApplication(t)
	failure := errors.New("later composition failed")
	err := app.Start(t.Context(), Compose, func(compose *application.Composer) {
		compose.Require(failure)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("composition error = %v", err)
	}
	content, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "server started") {
		t.Fatalf("failed composition logged success: %s", content)
	}
}
`

// TestSkillBundleMatchesAuthoredTree compares embedded installation bytes with
// the on-disk source, not a second call to the bundle loader. Extra, missing,
// or stale embedded files fail even when installer and loader agree.
func TestSkillBundleMatchesAuthoredTree(t *testing.T) {
	t.Parallel()

	bundle, err := skill.LoadBundle(t.Context(), skillSource)

	if err != nil {
		t.Fatal(err)
	}

	source := os.DirFS("skills/arkoren")
	want := make(map[string]string)

	err = fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		content, err := fs.ReadFile(source, path)

		if err != nil {
			return err
		}

		want[path] = fmt.Sprintf("%x", sha256.Sum256(content))

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(bundle.Manifest.Files, want) {
		t.Fatal("embedded paths or hashes differ from the authored tree")
	}

	if len(bundle.Files) != len(want) {
		t.Fatalf("embedded file count=%d, want %d", len(bundle.Files), len(want))
	}

	for _, file := range bundle.Files {
		content, err := fs.ReadFile(source, file.Path)

		if err != nil || !bytes.Equal(file.Content, content) {
			t.Fatalf("embedded content differs: %s: %v", file.Path, err)
		}
	}
}

// TestSkillBundleStripsSourcePrefix verifies that neither the embedding root
// nor directory entries can leak into installation paths or manifest keys.
func TestSkillBundleStripsSourcePrefix(t *testing.T) {
	t.Parallel()

	bundle, err := skill.LoadBundle(t.Context(), skillSource)

	if err != nil {
		t.Fatal(err)
	}

	if _, ok := bundle.Manifest.Files["SKILL.md"]; !ok {
		t.Fatal("missing skill-relative SKILL.md")
	}

	for _, file := range bundle.Files {
		if strings.HasPrefix(file.Path, "skills/arkoren/") || !fs.ValidPath(file.Path) {
			t.Fatalf("invalid installation-relative path: %q", file.Path)
		}
	}
}

// TestSkillBundleCanceled checks cancellation at the production entry point,
// before any complete skill bundle can be returned to the installer.
func TestSkillBundleCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	bundle, err := skill.LoadBundle(ctx, skillSource)

	if !errors.Is(err, context.Canceled) || len(bundle.Files) != 0 {
		t.Fatalf("bundle=%+v error=%v", bundle, err)
	}
}
