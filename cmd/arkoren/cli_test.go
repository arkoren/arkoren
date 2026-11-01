package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/remote"
	"arkoren.dev/cmd/arkoren/internal/skill"
	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/memoryschedule"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/validate"
)

// TestGlobalFlagsRemainReservedOverKebabAliases checks reserved alias names.
// Help must not advertise an input alias that global flag extraction consumes.
func TestGlobalFlagsRemainReservedOverKebabAliases(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	remote.WriteOperationHelp(&output, "custom", &remote.Operation{
		Name:  "check",
		Kind:  "read",
		Input: []byte(`{"type":"object","properties":{"request_timeout":{"type":"string"}}}`),
	}, false)

	if strings.Contains(output.String(), "-request_timeout, -request-timeout") {
		t.Fatalf("help advertises a reserved alias: %s", output.String())
	}

	flags, rest, err := parseGlobals([]string{"custom:check", "--request-timeout", "1s"})

	if err != nil {
		t.Fatal(err)
	}

	if !flags.TimeoutSet || len(rest) != 1 {
		t.Fatalf("globals = %+v, rest = %v", flags, rest)
	}
}

// TestDuplicateCommandAliasesNeverSelectAnAction protects optional metadata
// collisions. Exact native names remain callable; an ambiguous alias must not
// select whichever action happens to appear first in tools/list.
func TestDuplicateCommandAliasesNeverSelectAnAction(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "aliases", Version: "1"}, nil)

	for _, name := range []string{"first", "second"} {
		server.AddTool(&sdkmcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"},
			Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "change"}}},
			func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				calls.Add(1)

				return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"called": true}}, nil
			})
	}

	url := nativeServerURL(t, server)

	output := runConnection(t, t.Context(), url, "", "vendor:change")

	if output.code != 2 || calls.Load() != 0 || !strings.Contains(output.err, "ambiguous") {
		t.Fatalf("ambiguous alias = %+v; calls = %d", output, calls.Load())
	}

	catalog := runConnection(t, t.Context(), url, "", "tools")
	nativeNames := strings.Contains(catalog.out, `"command": "first"`) && strings.Contains(catalog.out, `"command": "second"`)

	if catalog.code != 0 || strings.Contains(catalog.out, `"command": "vendor:change"`) ||
		!nativeNames {
		t.Fatalf("catalog = %+v", catalog)
	}
}

// TestDuplicateAliasesLeaveExactNamesCallable preserves native MCP interop when
// optional CLI metadata collides. Selecting a known native name still invokes
// only the chosen tool, regardless of how both aliases were advertised.
func TestDuplicateAliasesLeaveExactNamesCallable(t *testing.T) {
	t.Parallel()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "aliases", Version: "1"}, nil)

	for _, name := range []string{"first", "second"} {
		server.AddTool(&sdkmcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"},
			Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "change"}}},
			func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"selected": name}}, nil
			})
	}

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "second", "--compact")

	if output.code != 0 || output.out != "{\"selected\":\"second\"}\n" {
		t.Fatalf("exact native selection = %+v", output)
	}
}

// TestPropertyAliasesPreserveExactPropertiesAndMapKeys verifies the existing
// property-alias contract through MCP rather than private parser assertions.
// Literal properties win over kebab aliases, nested declared names normalize,
// and arbitrary map keys retain the spelling supplied by the caller.
func TestPropertyAliasesPreserveExactPropertiesAndMapKeys(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "echo", InputSchema: jsontext.Value(
		`{"type":"object","properties":{"fail-fast":{"type":"string"},"fail_fast":{"type":"boolean"},"health_policy":{"type":"object","properties":{"fail_fast":{"type":"boolean"}}},"labels":{"type":"object","additionalProperties":{"type":"string"}}}}`)},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
			}}, nil
		})
	url := nativeServerURL(t, server)

	output := runConnection(t, t.Context(), url, "", "echo", "--compact", "--fail-fast", "literal",
		"--fail_fast", "--health-policy.fail-fast", "--labels.connection-name", "value")

	if output.code != 0 || output.out != "{\"fail-fast\":\"literal\",\"fail_fast\":true,\"health_policy\":{\"fail_fast\":true},\"labels\":{\"connection-name\":\"value\"}}\n" {
		t.Fatalf("property aliases = %+v", output)
	}

	help := runConnection(t, t.Context(), url, "", "echo", "--help")

	if help.code != 0 || strings.Contains(help.out, "-fail_fast, -fail-fast") {
		t.Fatalf("help advertises a shadowed alias: %+v", help)
	}
}

// TestGroupHelpDoesNotRecommendShadowedAliases extends exact-name precedence
// to group help. Its operation listing must recommend the same safe spelling as
// tools discovery when another native identifier shadows the owner's alias.
func TestGroupHelpDoesNotRecommendShadowedAliases(t *testing.T) {
	t.Parallel()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "aliases", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "native.change", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "change"}}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			t.Error("help invoked an action")

			return &sdkmcp.CallToolResult{}, nil
		})
	server.AddTool(&sdkmcp.Tool{Name: "vendor:change", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			t.Error("help invoked the shadowing action")

			return &sdkmcp.CallToolResult{}, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "vendor:", "--help")

	if output.code != 0 || !strings.Contains(output.out, "native.change") || strings.Contains(output.out, "vendor:change") {
		t.Fatalf("group help recommends a shadowed alias: %+v", output)
	}
}

// cliRoundTripper supplies an owned HTTP boundary for protocol tests. It runs
// the real SDK server handler without external network I/O, allowing synctest
// to control deadline expiry while preserving native HTTP/MCP messages.
type cliRoundTripper func(*http.Request) (*http.Response, error)

// RoundTrip implements the public HTTP transport seam; each fixture supplies
// only the deterministic response behavior required by its scenario.
func (transport cliRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestSDKCleanupHonorsCallerCancellation reproduces the SDK's detached DELETE
// context through a complete public CLI call. Cancellation at teardown must not
// advance the fake clock to the SDK's independent five-second cleanup timeout.
func TestSDKCleanupHonorsCallerCancellation(t *testing.T) {
	// http.DefaultTransport is process-wide, so this test must remain serial.
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		handler := cleanupProtocolHandler(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
			})

		original := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = original })

		deleted := false
		http.DefaultTransport = cliRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodDelete {
				deleted = true
				cancel()
				<-request.Context().Done()
			}

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			return response.Result(), nil
		})

		startedAt := time.Now()
		output := runConnection(t, ctx, "http://fixture.invalid/mcp", "", "echo", "--compact")

		if !deleted || output.code != 0 || output.out != "{\"ok\":true}\n" {
			t.Fatalf("cleanup fixture = %+v; DELETE = %v", output, deleted)
		}

		if elapsed := time.Since(startedAt); elapsed != 0 {
			t.Fatalf("caller cancellation did not bound SDK teardown: %s", elapsed)
		}
	})
}

// TestSDKCleanupBoundDoesNotShortenToolCalls keeps the cleanup-only upper bound
// separate from a deliberately unbounded tool request. All time is virtual: the
// tool takes two seconds, followed by exactly one second of cooperative cleanup.
func TestSDKCleanupBoundDoesNotShortenToolCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := cleanupProtocolHandler(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				<-time.After(2 * time.Second)

				return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
			})
		original := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = original })

		http.DefaultTransport = cliRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodDelete {
				<-request.Context().Done()
			}

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			return response.Result(), nil
		})

		startedAt := time.Now()
		output := runConnection(t, t.Context(), "http://fixture.invalid/mcp", "", "echo", "--compact", "--request-timeout=0")

		if output.code != 0 || output.out != "{\"ok\":true}\n" {
			t.Fatalf("cleanup shortened the tool call: %+v", output)
		}

		if elapsed := time.Since(startedAt); elapsed != 3*time.Second {
			t.Fatalf("tool plus bounded cleanup elapsed = %s, want 3s", elapsed)
		}
	})
}

// TestSDKCleanupSharesRemainingWaitBudget keeps teardown inside a condition
// command's existing deadline. A successful ten-millisecond tool observation
// leaves only ten milliseconds for cleanup, not a fresh independent budget.
func TestSDKCleanupSharesRemainingWaitBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := cleanupProtocolHandler(&sdkmcp.Tool{Name: "echo", Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"wait": map[string]any{"type": "boolean"}, "timeout": map[string]any{"type": "string"},
			}},
		}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			<-time.After(10 * time.Millisecond)

			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
		})
		original := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = original })

		http.DefaultTransport = cliRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodDelete {
				<-request.Context().Done()
			}

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			return response.Result(), nil
		})

		startedAt := time.Now()
		output := runConnection(t, t.Context(), "http://fixture.invalid/mcp", "", "echo", "--compact", "--wait", "--timeout=20ms")

		if output.code != 0 || output.out != "{\"ok\":true}\n" {
			t.Fatalf("completed observation lost its result: %+v", output)
		}

		if elapsed := time.Since(startedAt); elapsed != 20*time.Millisecond {
			t.Fatalf("cleanup exceeded the remaining command budget: %s", elapsed)
		}
	})
}

// deadlineResponseBody is a cooperative stalled HTTP response used to observe
// command ownership at the public transport seam. No worker goroutine or real
// socket is involved; the request context is the only way its read completes.
type deadlineResponseBody struct {
	// context belongs to the HTTP request supplied by the CLI transport.
	context context.Context
}

// Read waits for the owned request deadline and returns its exact cause.
func (body deadlineResponseBody) Read([]byte) (int, error) {
	<-body.context.Done()

	return 0, body.context.Err()
}

// Close releases no resources because this fixture owns no background work.
func (deadlineResponseBody) Close() error { return nil }

// TestDisconnectedToolBodyAndCleanupShareCommandDeadline injects a stalled
// tools/call response after real MCP discovery. The total wait budget bounds body
// consumption and subsequent SDK cleanup; cancellation never reports success.
func TestDisconnectedToolBodyAndCleanupShareCommandDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := cleanupProtocolHandler(&sdkmcp.Tool{Name: "echo", Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"wait": map[string]any{"type": "boolean"}, "timeout": map[string]any{"type": "string"},
			}},
		}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			t.Error("transport fault unexpectedly reached the tool handler")

			return &sdkmcp.CallToolResult{}, nil
		})
		original := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = original })

		http.DefaultTransport = cliRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodPost && mcpMethod(request) == "tools/call" {
				return &http.Response{StatusCode: http.StatusOK,
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   deadlineResponseBody{context: request.Context()}, Request: request,
				}, nil
			}

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			return response.Result(), nil
		})

		startedAt := time.Now()
		output := runConnection(t, t.Context(), "http://fixture.invalid/mcp", "", "echo", "--wait", "--timeout=20ms")

		if output.code != 1 || output.out != "" {
			t.Fatalf("disconnected observation became success: %+v", output)
		}

		if elapsed := time.Since(startedAt); elapsed != 20*time.Millisecond {
			t.Fatalf("body/cleanup exceeded the command deadline: %s", elapsed)
		}
	})
}

// cleanupProtocolHandler serves a real stateful SDK session without TCP. The
// legacy protocol intentionally exercises session-termination DELETE, which
// stateless current-protocol sessions do not send.
func cleanupProtocolHandler(tool *sdkmcp.Tool, handler sdkmcp.ToolHandler) http.Handler {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "cleanup", Version: "1"},
		&sdkmcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	server.AddTool(tool, handler)

	return sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, &sdkmcp.StreamableHTTPOptions{JSONResponse: true})
}

// TestStaticCLIRootHelpIsOffline verifies generated root pages ignore endpoint
// configuration and malformed project files without even looking up environment
// values. The detailed remote rules remain exclusive to machine-readable help.
func TestStaticCLIRootHelpIsOffline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ARKOREN_MCP_URL='unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"--url=invalid", "--env-file=missing", "--help"}} {
		var stdout, stderr bytes.Buffer

		code := run(t.Context(), args, console.Environment{
			Stdout: &stdout, Stderr: &stderr, Dir: dir,
			Env: func(string) (string, bool) {
				t.Error("root help consulted the environment")

				return "", false
			},
		})

		if code != console.ExitOK || stderr.Len() != 0 {
			t.Fatalf("exit = %d; stderr = %s", code, &stderr)
		}

		contains(t, stdout.String(), "NAME:", "USAGE:", "COMMANDS:", "GLOBAL OPTIONS:", "--url", "--token", "--request-timeout", "--version", "arkoren tools", "remote MCP tools")
	}
}

// TestStaticCLIRootHelpOmitsCustomRules keeps the human root page generated
// rather than mixing the remote JSON contract into framework output.
func TestStaticCLIRootHelpOmitsCustomRules(t *testing.T) {
	t.Parallel()

	page := newRig(t).run("--help").expectOK(t).stdout

	for _, unwanted := range []string{"Local commands:", "Input:", "Output:", "Exit codes:", "version  "} {
		if strings.Contains(page, unwanted) {
			t.Fatalf("root help includes %q: %s", unwanted, page)
		}
	}
}

// TestStaticCLIVersionRemainsANativeTool verifies removal of the local version
// command without reserving the remote tool name or its version input property.
func TestStaticCLIVersionRemainsANativeTool(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{
		Name: "version",
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"version": map[string]any{"type": "boolean"},
			},
		},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var input map[string]any

		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}

		return &sdkmcp.CallToolResult{StructuredContent: input}, nil
	})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "version", "--version=false", "--compact")

	if result.code != console.ExitOK || result.out != "{\"version\":false}\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestStaticCLIHelpRemainsANativeTool verifies that removing local help frees
// its exact native name. JSON is an ordinary schema input, not a help format.
func TestStaticCLIHelpRemainsANativeTool(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{
		Name: "help",
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"json": map[string]any{"type": "boolean"},
			},
		},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var input map[string]any

		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}

		return &sdkmcp.CallToolResult{StructuredContent: input}, nil
	})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "help", "--json", "--compact")

	if result.code != console.ExitOK || result.out != "{\"json\":true}\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestNativeHelpFlagDescribesWithoutInvocation protects the newly unreserved
// native help tool. Its help flag must discover its schema, never call its action.
func TestNativeHelpFlagDescribesWithoutInvocation(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{
		Name: "help", Description: "Native help operation",
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"json": map[string]any{"type": "boolean"},
			},
		},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		t.Error("remote help invoked the operation")

		return &sdkmcp.CallToolResult{}, nil
	})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "help", "--help")

	if result.code != console.ExitOK {
		t.Fatalf("result = %+v", result)
	}

	contains(t, result.out, "Native help operation", "-json")
}

// TestNativeHelpNameWinsOverExtension preserves invocation precedence in help.
// A native tool that shares its hinted group name must describe its own schema,
// not silently select the group's operation listing.
func TestNativeHelpNameWinsOverExtension(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{
		Name: "public", Description: "Inspect the public router",
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "public", "operation": "routes"}},
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"method": map[string]any{"type": "string"},
			},
		},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		t.Error("remote help invoked the operation")

		return &sdkmcp.CallToolResult{}, nil
	})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "public", "--help")

	if result.code != console.ExitOK {
		t.Fatalf("result = %+v", result)
	}

	contains(t, result.out, "arkoren public:routes", "-method")
}

// TestToolsRejectsTargetsBeforeConfiguration makes the whole-catalog boundary
// explicit. Neither JSON mode nor a positional local name authorizes discovery.
func TestToolsRejectsTargetsBeforeConfiguration(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"tools", "new", "--json"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) (string, bool) {
			t.Error("invalid tools grammar consulted configuration")

			return "", false
		},
	})

	if code != console.ExitUsage || stdout.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stderr.String(), "tools takes no arguments")
}

// TestLocalHelpDoesNotOfferJSON preserves the framework's human-only help.
// Machine discovery is available only through tools, not local help targets.
func TestLocalHelpDoesNotOfferJSON(t *testing.T) {
	t.Parallel()

	page := newRig(t).run("key", "generate", "--help").expectOK(t).stdout

	if strings.Contains(page, "--json") {
		t.Fatalf("local help offers JSON discovery: %s", page)
	}
}

// TestStaticCLIInspectorHelpIsOffline keeps native command usage independent
// from discovery, even for commands whose ordinary actions need a connection.
func TestStaticCLIInspectorHelpIsOffline(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"tools", "status"} {
		var stdout, stderr bytes.Buffer

		code := run(t.Context(), []string{name, "--help", "--url=invalid", "--env-file=missing"}, console.Environment{
			Stdout: &stdout, Stderr: &stderr,
			Env: func(string) (string, bool) {
				t.Error("static inspector help consulted the environment")

				return "", false
			},
		})

		if code != console.ExitOK || stderr.Len() != 0 {
			t.Fatalf("%s: exit = %d; stderr = %s", name, code, &stderr)
		}

		contains(t, stdout.String(), "NAME:", "arkoren "+name, "USAGE:")
	}
}

// TestStaticCLIVersionIsOffline verifies the framework's standard version text
// without reading configuration. Each invocation owns its parser and flag state.
func TestStaticCLIVersionIsOffline(t *testing.T) {
	t.Parallel()

	for range 2 {
		var stdout, stderr bytes.Buffer

		code := run(t.Context(), []string{"--env-file=missing", "--url=invalid", "--version"}, console.Environment{
			Stdout: &stdout, Stderr: &stderr,
			Env: func(string) (string, bool) {
				t.Error("version consulted the environment")

				return "", false
			},
		})

		if code != console.ExitOK || stderr.Len() != 0 || (result{stdout: stdout.String()}).json(t)["version"] != "(devel)" {
			t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
		}
	}
}

// TestStaticCLIVersionOutputFailureRetainsPrecedence covers the native version
// printer, whose write errors must still cross Arkoren's stdout boundary.
func TestStaticCLIVersionOutputFailureRetainsPrecedence(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	code := run(t.Context(), []string{"--version"}, console.Environment{
		Stdout: &failingOutput{}, Stderr: &stderr,
	})

	if code != console.ExitFailure || !strings.Contains(stderr.String(), io.ErrShortWrite.Error()) {
		t.Fatalf("exit = %d; stderr = %s", code, &stderr)
	}
}

// TestStaticCLIRunsKeepFlagStateIndependent exercises concurrent complete runs,
// not concurrent reuse of one mutable urfave tree. Each plan must retain its own
// destination and must never resolve inspector environment values.
func TestStaticCLIRunsKeepFlagStateIndependent(t *testing.T) {
	t.Parallel()

	var workers sync.WaitGroup

	for range 8 {
		dir := t.TempDir()
		workers.Go(func() {
			var stdout, stderr bytes.Buffer

			code := run(t.Context(), []string{
				"new", "billing", "--module=example.com/billing", "--dry-run", "--no-agent-files",
			}, console.Environment{
				Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Dir: dir,
				Env: func(string) (string, bool) {
					t.Error("offline plan consulted the environment")

					return "", false
				},
			})

			if code != console.ExitOK {
				t.Errorf("exit = %d; stderr = %s", code, &stderr)
			}

			if !strings.Contains(stdout.String(), "example.com/billing") {
				t.Errorf("missing independent plan: %s", &stdout)
			}
		})
	}

	workers.Wait()
}

// TestStaticCLIFamilyHelpSkipsAllConfiguration verifies all offline roots bypass
// missing explicit env files and missing required arguments.
// Both help spellings must still produce the same generated family page.
func TestStaticCLIFamilyHelpSkipsAllConfiguration(t *testing.T) {
	t.Parallel()

	for _, family := range []string{"new", "skills", "key", "certificate"} {
		var stdout, stderr bytes.Buffer
		env := console.Environment{
			Stdout: &stdout, Stderr: &stderr, Dir: t.TempDir(),
			Env: func(string) (string, bool) {
				t.Error("offline help consulted the environment")

				return "", false
			},
		}

		code := run(t.Context(), []string{
			"--env-file", "missing.env", family, "--help",
		}, env)

		if code != console.ExitOK || stderr.Len() != 0 {
			t.Fatalf("%s: exit = %d; stderr = %s", family, code, &stderr)
		}

		page := stdout.String()

		stdout.Reset()
		code = run(t.Context(), []string{family, "-h"}, env)

		if code != console.ExitOK || page != stdout.String() {
			t.Fatalf("%s: help mismatch; exit = %d; stdout = %s; stderr = %s", family, code, &stdout, &stderr)
		}
	}
}

// TestStaticCLIKeyGenerateHelpRetainsLeaf exercises both help entry points with
// invalid endpoint configuration. Only leaf flags belong in
// the generated page, and no key or endpoint work may happen.
func TestStaticCLIKeyGenerateHelpRetainsLeaf(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"key", "generate", "--help"},
		{"key", "generate", "-h"},
	} {
		var stdout, stderr bytes.Buffer

		code := run(t.Context(), append(args, "--url=invalid", "--env-file=missing"), console.Environment{
			Stdout: &stdout, Stderr: &stderr, Dir: t.TempDir(),
			Env: func(string) (string, bool) {
				t.Error("offline help consulted the environment")

				return "", false
			},
		})

		if code != console.ExitOK || stderr.Len() != 0 {
			t.Fatalf("exit = %d; stderr = %s", code, &stderr)
		}

		contains(t, stdout.String(), "arkoren key generate", "TYPE", "--out", "mode 0600", "stdout")

		if strings.Contains(stdout.String(), "--format") {
			t.Fatalf("leaf help includes unrelated flags: %s", &stdout)
		}
	}
}

// TestStaticCLITrustHelpBypassesApprovalValidation verifies native nested help
// before missing FILE validation or the raw approval guard. The help page
// must retain the machine-wide, default-no, never-elevate safety contract.
func TestStaticCLITrustHelpBypassesApprovalValidation(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"certificate", "root", "trust", "--help"},
		{"certificate", "root", "trust", "-h"},
	} {
		var stdout, stderr bytes.Buffer

		code := run(t.Context(), append(args, "--url=invalid", "--env-file=missing"), console.Environment{
			Stdout: &stdout, Stderr: &stderr, Dir: t.TempDir(),
			Env: func(string) (string, bool) {
				t.Error("offline help consulted the environment")

				return "", false
			},
		})

		if code != console.ExitOK || stderr.Len() != 0 {
			t.Fatalf("exit = %d; stderr = %s", code, &stderr)
		}

		contains(t, stdout.String(), "arkoren certificate root trust", "FILE", "--yes", "machine-wide", "default-no", "Never elevates")

		if strings.Contains(stdout.String(), "--ca-key") {
			t.Fatalf("trust help includes generation flags: %s", &stdout)
		}
	}
}

// TestLiteralHelpDoesNotBypassCertificateApproval distinguishes native help
// from a filename after --. Literal operands cannot disable the raw --yes guard.
func TestLiteralHelpDoesNotBypassCertificateApproval(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{
		"certificate", "root", "trust", "--yes=true", "--", "--help",
	}, console.Environment{Stdout: &stdout, Stderr: &stderr})

	if code != console.ExitUsage || stdout.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stderr.String(), "approval accepts only bare --yes")
}

// TestStaticCLIParserRedactsUnknownFlags keeps arbitrary flag names out of
// diagnostics, since pasted tokens may themselves contain bearer credentials.
// Arkoren, rather than the framework, prints exactly one diagnostic.
func TestStaticCLIParserRedactsUnknownFlags(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"new", "billing", "--bearer-private-token"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr,
	})

	if code != console.ExitUsage || stdout.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	if strings.Contains(stderr.String(), "private-token") || errorDocument(t, stderr.String())["message"] == "" {
		t.Fatalf("unsafe or duplicated diagnostic: %s", &stderr)
	}

	if strings.Contains(stderr.String(), "Incorrect Usage") {
		t.Fatalf("framework printed its own error: %s", &stderr)
	}
}

// TestStaticCLIParserRedactsFlagValues exercises the framework's bool parser,
// whose native error quotes invalid input. Only the static command name is safe.
func TestStaticCLIParserRedactsFlagValues(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"new", "billing", "--dry-run=private-token"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr,
	})

	if code != console.ExitUsage || stdout.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	if strings.Contains(stderr.String(), "private-token") || errorDocument(t, stderr.String())["message"] == "" {
		t.Fatalf("unsafe or duplicated diagnostic: %s", &stderr)
	}
}

// TestStaticCLIGlobalParserRedactsFlagValues applies the same policy before
// urfave routing, where reserved globals retain their independent parser.
func TestStaticCLIGlobalParserRedactsFlagValues(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--request-timeout=private-token", "--compact=private-token"} {
		var stdout, stderr bytes.Buffer

		code := run(t.Context(), []string{flag, "new", "--help"}, console.Environment{
			Stdout: &stdout, Stderr: &stderr,
		})

		if code != console.ExitUsage || stdout.Len() != 0 {
			t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
		}

		if strings.Contains(stderr.String(), "private-token") {
			t.Fatalf("unsafe diagnostic: %s", &stderr)
		}
	}
}

// TestStaticCLIHelpOutputFailureRetainsPrecedence covers the framework's help
// renderer, which does not itself propagate writes. The shared output recorder
// must still normalize its short write to exit 1, not successful help.
func TestStaticCLIHelpOutputFailureRetainsPrecedence(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	code := run(t.Context(), []string{"certificate", "--help"}, console.Environment{
		Stdout: &failingOutput{}, Stderr: &stderr,
	})

	if code != console.ExitFailure || !strings.Contains(stderr.String(), io.ErrShortWrite.Error()) {
		t.Fatalf("exit = %d; stderr = %s", code, &stderr)
	}
}

// TestStaticCLIToolsJSONKeepsCatalog confirms discovery lists only remote tools,
// while local command usage remains in offline --help.
func TestStaticCLIToolsJSONKeepsCatalog(t *testing.T) {
	t.Parallel()

	document := newRig(t).run("tools", "--json").expectOK(t).json(t)

	for _, key := range []string{"inspector", "tools"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("JSON help lacks %q: %v", key, document)
		}
	}

	for _, command := range document["tools"].([]any) {
		name := command.(map[string]any)["command"]

		if name == "version" || name == "help" {
			t.Fatalf("JSON tools advertises removed local command %s", name)
		}
	}
}

// TestStaticCLINativeNameResolvesBeforeColonGrammar protects exact SDK names
// even when their prefix is an offline family and their syntax is not a CLI
// group/operation. Remote JSON and schema flags never enter the static parser.
func TestStaticCLINativeNameResolvesBeforeColonGrammar(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{
		Name: "certificate:vendor:echo",
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"dry_run": map[string]any{"type": "boolean"},
			},
		},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var input map[string]any

		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}

		return &sdkmcp.CallToolResult{StructuredContent: input}, nil
	})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "",
		"certificate:vendor:echo", "--dry-run=false", "--compact")

	if result.code != console.ExitOK || result.out != "{\"dry_run\":false}\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestStaticCLIDoesNotReserveCompletion keeps shell completion out of this
// migration. A native tool named completion remains callable by its exact name.
func TestStaticCLIDoesNotReserveCompletion(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "completion", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"native": true}}, nil
		})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "completion", "--compact")

	if result.code != console.ExitOK || result.out != "{\"native\":true}\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestRemoteHelpUsesTerminalHeadings keeps discovered-command help consistent
// with generated local help while preserving its text format.
func TestRemoteHelpUsesTerminalHeadings(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	for _, command := range []string{"billing:", "billing:payments"} {
		output := rig.runWith(t.Context(), runOptions{terminal: true}, command, "--help").expectOK(t)

		if !strings.Contains(output.stdout, "\x1b[1m") {
			t.Fatalf("%s help has no styled headings: %q", command, output.stdout)
		}
	}
}

// TestRemoteHelpHonorsNoColor covers the explicit opt-out on both extension
// tables and schema-driven operation help.
func TestRemoteHelpHonorsNoColor(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	for _, command := range []string{"billing:", "billing:payments"} {
		output := rig.runWith(t.Context(), runOptions{terminal: true}, command, "--help", "--no-color").expectOK(t)

		if output.stdout == "" || strings.Contains(output.stdout, "\x1b") {
			t.Fatalf("%s help = %q", command, output.stdout)
		}
	}
}

// TestRemoteHelpHonorsNoColorEnvironment applies NO_COLOR to remote help as
// well as JSON reports and local generated help.
func TestRemoteHelpHonorsNoColorEnvironment(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	options := runOptions{terminal: true, env: map[string]string{remote.EnvURL: rig.url, "NO_COLOR": "1"}}

	for _, command := range []string{"billing:", "billing:payments"} {
		output := rig.runWith(t.Context(), options, command, "--help").expectOK(t)

		if output.stdout == "" || strings.Contains(output.stdout, "\x1b") {
			t.Fatalf("%s help = %q", command, output.stdout)
		}
	}
}

// TestHelpHeadingsAreBoldOnTerminals preserves generated root help while
// distinguishing its headings from the unstyled command content.
func TestHelpHeadingsAreBoldOnTerminals(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"--help"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, IsTerminal: true,
		Env: func(name string) (string, bool) {
			if name != "NO_COLOR" {
				t.Errorf("unexpected environment lookup: %s", name)
			}

			return "", false
		},
	})

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stderr = %s", code, &stderr)
	}

	contains(t, stdout.String(), "\x1b[1mNAME:\x1b[0m", "\x1b[1mUSAGE:\x1b[0m", "\x1b[1mGLOBAL OPTIONS:\x1b[0m")

	if strings.Contains(cli.RootCommandHelpTemplate, "\x1b") {
		t.Fatal("help changed the shared framework template")
	}
}

// TestNestedHelpHeadingsAreBold covers a leaf reached through its help flag.
func TestNestedHelpHeadingsAreBold(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"key", "generate", "--help"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, IsTerminal: true,
	})

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stderr = %s", code, &stderr)
	}

	contains(t, stdout.String(), "\x1b[1mOPTIONS:\x1b[0m", "\x1b[1mDESCRIPTION:\x1b[0m", "arkoren key generate")
}

// TestFamilyHelpHeadingsAreBold covers the framework's subcommand template.
func TestFamilyHelpHeadingsAreBold(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"key", "--help"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, IsTerminal: true,
	})

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stderr = %s", code, &stderr)
	}

	contains(t, stdout.String(), "\x1b[1mCOMMANDS:\x1b[0m", "generate", "check", "public")
}

// TestNestedFamilyHelpHeadingsAreBold covers the parent-rendering adaptation
// below the first family level, without mutating framework-wide templates.
func TestNestedFamilyHelpHeadingsAreBold(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"certificate", "root", "--help"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, IsTerminal: true,
	})

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stderr = %s", code, &stderr)
	}

	contains(t, stdout.String(), "\x1b[1mCOMMANDS:\x1b[0m", "generate", "trust", "untrust")
}

// TestPipedHelpRemainsPlain also ensures offline nonterminal help performs no
// environment lookup just to decide its presentation.
func TestPipedHelpRemainsPlain(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"--help"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) (string, bool) {
			t.Error("piped help consulted the environment")

			return "", false
		},
	})

	if code != console.ExitOK || stderr.Len() != 0 || strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("exit = %d; stdout = %q; stderr = %s", code, stdout.String(), &stderr)
	}
}

// TestHelpNoColorFlagDisablesBold honors the explicit presentation opt-out.
func TestHelpNoColorFlagDisablesBold(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"--no-color", "--help"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, IsTerminal: true,
	})

	if code != console.ExitOK || stderr.Len() != 0 || strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("exit = %d; stdout = %q; stderr = %s", code, stdout.String(), &stderr)
	}
}

// TestHelpNoColorEnvironmentDisablesBold honors a nonempty NO_COLOR value.
func TestHelpNoColorEnvironmentDisablesBold(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"--help"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, IsTerminal: true,
		Env: func(string) (string, bool) {
			return "1", true
		},
	})

	if code != console.ExitOK || stderr.Len() != 0 || strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("exit = %d; stdout = %q; stderr = %s", code, stdout.String(), &stderr)
	}
}

// TestBoldHelpOutputFailureRetainsPrecedence ensures styling does not bypass
// the shared first-error output boundary when the framework ignores writes.
func TestBoldHelpOutputFailureRetainsPrecedence(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	code := run(t.Context(), []string{"--help"}, console.Environment{
		Stdout: &failingOutput{}, Stderr: &stderr, IsTerminal: true,
	})

	if code != console.ExitFailure || !strings.Contains(stderr.String(), io.ErrShortWrite.Error()) {
		t.Fatalf("exit = %d; stderr = %s", code, &stderr)
	}
}

// TestToolsDiscoveryUsesJSONStyle respects NO_COLOR for terminal discovery.
func TestToolsDiscoveryUsesJSONStyle(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"tools"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, IsTerminal: true, Dir: t.TempDir(),
		Env: func(name string) (string, bool) {
			if name == "NO_COLOR" {
				return "1", true
			}

			return "", false
		},
	})

	if code != console.ExitOK || stderr.Len() != 0 || strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("exit = %d; stdout = %q; stderr = %s", code, stdout.String(), &stderr)
	}
}

// contains fails the test unless text holds every fragment.
func contains(t *testing.T, text string, fragments ...string) {
	t.Helper()

	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Fatalf("output lacks %q:\n%s", fragment, text)
		}
	}
}

// stubInspector serves a fixed handshake body at /inspect/ and returns its
// URL, for the cases a real inspector cannot produce.
func stubInspector(t *testing.T, body string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server.URL + "/inspect"
}

// deadURL returns the URL of a server that has been shut down.
func deadURL(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()

	return address + "/inspect"
}

// TestHelpListsEveryCommand checks discovery across remote commands.
// Native initialization metadata and operation traits remain visible without
// invoking an application identity tool or requesting each operation's help.
func TestHelpListsEveryCommand(t *testing.T) {
	t.Parallel()

	out := newRig(t).run("tools").expectOK(t).stdout
	document := (result{stdout: out}).json(t)

	inspector := document["inspector"].(map[string]any)

	if inspector["state"] != remote.StateOK || inspector["protocol"] != "2026-07-28" || inspector["arkoren"] != "test" {
		t.Fatalf("discovery metadata = %v", inspector)
	}

	if _, ok := inspector["app"]; ok {
		t.Fatalf("discovery included tool-derived identity: %v", inspector)
	}

	contains(t, out, "List recent payments", "billing:payments", "billing:record-payment", `"kind": "read"`, `"follow": true`, "public:routes", "app:trace")

	if strings.Contains(out, "Aliases:") {
		t.Fatalf("help publishes command aliases:\n%s", out)
	}
}

// TestRootHelpFlagMatchesBareInvocation checks the offline help entry points.
// Remote discovery remains the separate tools command rather than a flag alias.
func TestRootHelpFlagMatchesBareInvocation(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	if rg.run("-h").expectOK(t).stdout != rg.run().expectOK(t).stdout {
		t.Fatal("-h differs from bare invocation")
	}
}

// TestHelpOperationShowsFlagsExamplesAndOutput checks schema-driven help.
// Nested flags, defaults, examples, and reserved names must remain discoverable.
func TestHelpOperationShowsFlagsExamplesAndOutput(t *testing.T) {
	t.Parallel()

	out := newRig(t).run("billing:record-payment", "--help").expectOK(t).stdout

	contains(t, out,
		"arkoren billing:record-payment [flags] [invoice] [cents]",
		"Kind: action",
		"Follow: no",
		"-address.city", "(required)", "-labels.<key>", "-items.<n>.sku", "-tags", "array of string",
		"(one of eur, usd)", `(default "eur")`,
		"share a name with a global flag, so only -input can set them: token",
		"arkoren billing:record-payment INV-1 500 -address.city Paris -items.0.qty 2 -items.0.sku A -labels.env prod -tags a -tags b",
		`arkoren billing:record-payment -input '{"address":{"city":"Paris"}`,
		"Output (JSON Schema):")
}

// TestHelpOperationShowsFollow checks a streaming read's help contract.
// The listing includes filters and the schema of one streamed item.
func TestHelpOperationShowsFollow(t *testing.T) {
	t.Parallel()

	out := newRig(t).run("billing:payments", "--help").expectOK(t).stdout

	contains(t, out, "Follow: yes", "-status", "(one of failed, paid)", "(default 10)",
		"arkoren billing:payments failed", "Output (JSON Schema)")
}

// TestHelpDashHAfterTheCommandIsOperationHelp checks trailing help flags.
// They must describe the operation rather than invoke it.
func TestHelpDashHAfterTheCommandIsOperationHelp(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	if rg.run("billing:payments", "-h").stdout != rg.run("billing:payments", "--help").stdout {
		t.Fatal("-h differs from help for the operation")
	}
}

// TestHelpExtensionListsOperations checks extension-scoped discovery.
// Its summary and operation names must appear together.
func TestHelpExtensionListsOperations(t *testing.T) {
	t.Parallel()

	out := newRig(t).run("billing", "--help").expectOK(t).stdout

	contains(t, out, "arkoren billing:<operation>", "Payments and invoices", "boom", "record-payment")
}

// TestHelpRejectsFormerShortcut checks that roles do not create help targets.
// Callers must use an advertised qualified command or exact native tool name.
func TestHelpRejectsFormerShortcut(t *testing.T) {
	t.Parallel()

	newRig(t).run("trace", "--help").expectFail(t, 2, `unknown command "trace"`)
}

// TestHelpLocalCommand checks help for a command implemented by the CLI.
// Its usage must not depend on a matching remote operation.
func TestHelpLocalCommand(t *testing.T) {
	t.Parallel()

	contains(t, newRig(t).run("status", "--help").expectOK(t).stdout, "arkoren status")
}

// TestHelpUnknownOperationSuggests checks typo diagnostics in help targets.
// Suggested commands must use executable qualified names.
func TestHelpUnknownOperationSuggests(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("billing:paymnts", "--help").expectFail(t, 2, `unknown operation "paymnts"`, "did you mean billing:payments", "arkoren billing: --help")
	rg.run("billin:payments", "--help").expectFail(t, 2, "did you mean billing:payments")
}

// TestToolsJSONListsCallableCommands checks the tailored discovery inventory.
func TestToolsJSONListsCallableCommands(t *testing.T) {
	t.Parallel()

	document := newRig(t).run("tools", "--json").expectOK(t).json(t)

	for _, key := range []string{"inspector", "tools"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("tools --json lacks %q: %v", key, document)
		}
	}

	if _, ok := document["aliases"]; ok {
		t.Fatalf("help publishes command aliases: %v", document)
	}

	if len(document["tools"].([]any)) < 10 {
		t.Fatalf("tools = %v", document["tools"])
	}

	if document["inspector"].(map[string]any)["state"] != "ok" {
		t.Fatalf("inspector = %v", document["inspector"])
	}
}

// TestHelpWithoutURLListsLocalCommands checks offline general help.
// Missing configuration must hide remote commands without failing local help.
func TestHelpWithoutURLListsLocalCommands(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	out := rg.runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "tools").expectOK(t).stdout

	document := (result{stdout: out}).json(t)

	if document["inspector"].(map[string]any)["state"] != remote.StateNotConfigured {
		t.Fatalf("inspector = %v", document["inspector"])
	}

	if strings.Contains(out, "billing") {
		t.Fatalf("help lists extensions without an inspector:\n%s", out)
	}
}

// TestHelpWhenUnreachableListsLocalCommands checks discovery failure fallback.
// Local commands stay usable while the unreachable URL is explained.
func TestHelpWhenUnreachableListsLocalCommands(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = deadURL(t)

	out := rg.run("tools").expectOK(t).stdout
	document := (result{stdout: out}).json(t)

	inspector := document["inspector"].(map[string]any)

	if inspector["state"] != remote.StateUnreachable || !strings.Contains(inspector["error"].(map[string]any)["message"].(string), rg.env[remote.EnvURL]) {
		t.Fatalf("inspector = %v", inspector)
	}
}

// TestToolsWithoutInspectorReportsUnavailableCatalog distinguishes unavailable
// discovery from an available endpoint with no visible tools.
func TestToolsWithoutInspectorReportsUnavailableCatalog(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	document := rg.runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "tools", "--json").expectOK(t).json(t)

	if tools := document["tools"].([]any); len(tools) != 0 {
		t.Fatalf("tools present without an inspector: %v", tools)
	}

	if document["inspector"].(map[string]any)["state"] != "not configured" {
		t.Fatalf("inspector = %v", document["inspector"])
	}
}

// TestHelpOperationWithoutURLIsUsage checks targeted help without discovery.
// Unlike general help, remote operation help requires an inspector URL.
func TestHelpOperationWithoutURLIsUsage(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "billing:payments", "--help").expectFail(t, 2, "no inspector URL")
}

// TestURLIsRequired checks the diagnostic for an unconfigured remote command.
// It must identify the supported URL configuration sources.
func TestURLIsRequired(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "public:routes").expectFail(t, 2,
		"no inspector URL: pass -url, or set ARKOREN_MCP_URL in your shell or ./.env")
}

// TestURLWithQueryOrFragmentIsUsage checks invalid inspection roots.
// Query strings, fragments, and unsupported schemes must fail before invocation.
func TestURLWithQueryOrFragmentIsUsage(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("-url", rg.url+"?a=1", "public:routes").expectFail(t, 2, "without a query or fragment")
	rg.run("-url", rg.url+"#x", "public:routes").expectFail(t, 2, "without a query or fragment")
	rg.run("-url", "ftp://host", "public:routes").expectFail(t, 2, "invalid inspector URL")
}

// TestURLTrailingSlashIsIrrelevant checks inspection-root normalization.
// Either spelling must reach the same operation endpoints.
func TestURLTrailingSlashIsIrrelevant(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("-url", rg.url+"/", "public:routes").expectOK(t)
	rg.run("-url", rg.url, "public:routes").expectOK(t)
}

// TestRoutesFiltersByFlag checks flag and positional input through qualification.
// Only the matching route should survive the server-side filters.
func TestRoutesFiltersByFlag(t *testing.T) {
	t.Parallel()

	got := newRig(t).run("public:routes", "-method", "POST", "/users").expectOK(t).json(t)

	items := got["items"].([]any)

	if len(items) != 1 || items[0].(map[string]any)["pattern"] != "/users" {
		t.Fatalf("items = %v", items)
	}
}

// TestFormerShortcutsAreUnknown checks every removed diagnostic shortcut.
// A role alone must not make a bare command executable.
func TestFormerShortcutsAreUnknown(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	for _, command := range []string{"routes", "traces", "trace", "logs", "metrics", "health"} {
		rg.run(command).expectFail(t, 2, "unknown command")
	}
}

// TestFormerShortcutWithoutToolsIsUnknown checks an empty native catalog.
// General help must not publish unavailable compatibility shortcuts.
func TestFormerShortcutWithoutToolsIsUnknown(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = nativeServerURL(t, sdkmcp.NewServer(&sdkmcp.Implementation{Name: "empty", Version: "1"}, &sdkmcp.ServerOptions{
		Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}},
	}))

	rg.run("routes").expectFail(t, 2, `unknown command "routes"`)

	if out := rg.run("tools").expectOK(t).stdout; strings.Contains(out, "Aliases:") {
		t.Fatalf("help publishes aliases: %s", out)
	}
}

// TestSharedRolesDoNotCreateCommands checks that roles never select a command.
// Both owners remain reachable through their advertised qualified names.
func TestSharedRolesDoNotCreateCommands(t *testing.T) {
	t.Parallel()

	admin := router.New()
	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Tools(admin.MCPTools("admin")...)
	})

	rg.run("routes").expectFail(t, 2, `unknown command "routes"`)
	rg.run("admin:routes").expectOK(t)
	rg.run("public:routes").expectOK(t)
}

// TestUnknownCommandSuggests checks local and qualified typo suggestions.
// A bare extension is not an executable command.
func TestUnknownCommandSuggests(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("statu").expectFail(t, 2, `unknown command "statu"`, "did you mean status")
	rg.run("billin:payments").expectFail(t, 2, "did you mean billing:payments")
	rg.run("billing").expectFail(t, 2, "unknown command", "<extension>:<operation>")
}

// TestOperationRunsWithDefaultsApplied checks positional read filters.
// The server applies the remaining schema defaults before answering.
func TestOperationRunsWithDefaultsApplied(t *testing.T) {
	t.Parallel()

	got := newRig(t).run("billing:payments", "failed").expectOK(t).json(t)
	items := got["items"].([]any)

	if len(items) != 1 || items[0].(map[string]any)["id"] != "p2" {
		t.Fatalf("items = %v", items)
	}
}

// TestTraceFlowFindsTheFailedTrace checks trace listing and prefix lookup.
// The selected tree must retain the failing child span and its log message.
func TestTraceFlowFindsTheFailedTrace(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	id := rg.failedTrace(t.Context())

	listed := rg.run("app:traces", "-status", "error").expectOK(t).json(t)

	if len(listed["items"].([]any)) != 1 {
		t.Fatalf("traces = %v", listed)
	}

	tree := rg.run("app:trace", id[:12]).expectOK(t).json(t)

	contains(t, rg.run("app:trace", id[:12]).stdout, "payments charge", "payment declined")

	if tree["trace_id"] == nil {
		t.Fatalf("trace = %v", tree)
	}
}

// TestUnknownTraceIsExitOne checks the remote not-found exit classification.
// A missing trace is an operation failure, not invalid CLI syntax.
func TestUnknownTraceIsExitOne(t *testing.T) {
	t.Parallel()

	newRig(t).run("app:trace", "deadbeef00").expectFail(t, 1, "No recorded trace has that ID")
}

// TestActionRunsWithoutConfirmation checks noninteractive remote actions.
// Action dispatch must print the server's result without a local approval step.
func TestActionRunsWithoutConfirmation(t *testing.T) {
	t.Parallel()

	got := newRig(t).run("jobs:run", "nightly-report").expectOK(t).json(t)

	if got["name"] != "nightly-report" {
		t.Fatalf("run = %v", got)
	}
}

// TestServerErrorIsExitOne checks presentation of a remote operation failure.
// Its problem detail must survive the HTTP boundary.
func TestServerErrorIsExitOne(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:boom").expectFail(t, 1, "ledger unreachable")
}

// TestValidationErrorNamesTheFlag checks request-origin diagnostics.
// Both explicit flags and positional arguments must name their invalid value.
func TestValidationErrorNamesTheFlag(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("billing:record-payment", "-input", `{"invoice":"I"}`, "-cents", "0").expectFail(t, 1,
		"Invalid input", "-cents", "run 'arkoren billing:record-payment --help'")
	rg.run("billing:record-payment", "INV", "0").expectFail(t, 1, "<cents>")
}

// TestValidationErrorNamesTheFlagOfAJSONSubtree checks nested origin reporting.
// The diagnostic names the supplying flag and the remaining JSON pointer.
func TestValidationErrorNamesTheFlagOfAJSONSubtree(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "5", "-address", `{"zip":"1"}`).expectFail(t, 1, "-address (at /city)")
}

// TestValidationErrorFromInputShowsThePointer checks base-input diagnostics.
// Values with no flag origin must retain the server's JSON pointer.
func TestValidationErrorFromInputShowsThePointer(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "-input", `{"invoice":"I","cents":0}`).expectFail(t, 1, "/cents")
}

// TestUnauthorizedIsExitFour checks bearer-token failure and recovery.
// Flags and environment settings must both authenticate a subsequent run.
func TestUnauthorizedIsExitFour(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token("secret")
	})

	rg.run("public:routes").expectFail(t, 4, "pass -token or set ARKOREN_MCP_TOKEN")
	rg.run("public:routes", "-token", "wrong").expectFail(t, 4)
	rg.run("-token", "secret", "public:routes").expectOK(t)

	rg.env[remote.EnvToken] = "secret"

	rg.run("public:routes").expectOK(t)
}

// TestRemoteHelpUnauthorizedIsExitFour applies ordinary discovery authentication
// to schema help. Help never downgrades a 401 into a successful catalog page.
func TestRemoteHelpUnauthorizedIsExitFour(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token("secret")
	})

	rg.run("public:routes", "--help").expectFail(t, console.ExitAuth, "pass -token")
	rg.run("public:routes", "--help", "--token", "secret").expectOK(t)
}

// TestToolsUnauthorizedIsExitFour retains the catalog probe's error state while
// requiring the same authentication failure status as operation discovery.
func TestToolsUnauthorizedIsExitFour(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token("secret")
	})

	rg.run("tools", "--json").expectFail(t, console.ExitAuth, "pass -token")
}

// TestUnreachableIsExitOneWithAHint checks connection-failure guidance.
// The diagnostic identifies the target and common port-forwarding remedies.
func TestUnreachableIsExitOneWithAHint(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = deadURL(t)

	output := rg.run("public:routes")

	output.expectFail(t, 1, "cannot reach the inspector at", rg.env[remote.EnvURL], "start the admin server", "forward the admin port")

	if strings.Contains(output.stderr, "initialize") || strings.Contains(output.stderr, "rejected by transport") {
		t.Fatalf("diagnostic leaked SDK wrappers: %s", output.stderr)
	}
}

// TestNativeLegacyNegotiation checks SDK negotiation with a legacy MCP server.
// No CLI-specific version gate may reject an otherwise supported MCP version.
func TestNativeLegacyNegotiation(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "legacy", Version: "9"}, &sdkmcp.ServerOptions{
		Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}}, SupportedProtocolVersions: []string{"2025-11-25"},
	})
	rg.env[remote.EnvURL] = nativeServerURL(t, server)

	status := rg.run("status").expectOK(t).json(t)

	if status["protocol"] != "2025-11-25" {
		t.Fatalf("status = %v", status)
	}
}

// TestUnknownMetadataMembersAreIgnored checks native discovery extensibility.
// Unrelated tool metadata and server versions must not create a version gate.
func TestUnknownMetadataMembersAreIgnored(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "external", Version: "v9.9.9"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "external", InputSchema: map[string]any{"type": "object"}, Meta: sdkmcp.Meta{"unrelated.dev/new": true}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
		})
	rg.env[remote.EnvURL] = nativeServerURL(t, server)

	document := rg.run("tools").expectOK(t).json(t)

	if document["inspector"].(map[string]any)["arkoren"] != "v9.9.9" {
		t.Fatalf("inspector = %v", document["inspector"])
	}

	if rg.run("external").expectOK(t).json(t)["ok"] != true {
		t.Fatal("external tool failed")
	}
}

// TestNotAnInspectorIsReported checks a non-JSON handshake response.
// The failure must identify the URL as something other than an inspector.
func TestNotAnInspectorIsReported(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = stubInspector(t, `<html>`)

	rg.run("public:routes").expectFail(t, 1, "failed to decode response")
}

// TestTimeoutIsExitOne checks request-deadline diagnostics.
// The test server waits for cancellation rather than sleeping to simulate delay.
func TestTimeoutIsExitOne(t *testing.T) {
	t.Parallel()

	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // Drain the POST so disconnect cancellation is observable.
		<-r.Context().Done()
	}))
	t.Cleanup(slow.Close)

	rg := newRig(t)
	rg.env[remote.EnvURL] = slow.URL

	rg.run("-request-timeout", "50ms", "public:routes").expectFail(t, 1, "did not answer within 50ms")
}

// TestUnhealthyAppIsExitOne checks interpretation of a failing health report.
// The report stays on stdout without an additional stderr diagnostic.
func TestUnhealthyAppIsExitOne(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.compose = func(composition *application.Composer) {
			composition.Check("cache", func(context.Context) error {
				return os.ErrDeadlineExceeded
			})
		}
	})

	run := rg.run("app:health", "-fresh")

	if run.code != 1 || run.json(t)["status"] != "failing" {
		t.Fatalf("health: code %d, %s", run.code, run.stdout)
	}

	if run.stderr != "" {
		t.Fatalf("stderr = %q", run.stderr)
	}
}

// TestHealthyAppIsExitZero checks the successful health exit status.
// A healthy application must be usable as a shell readiness gate.
func TestHealthyAppIsExitZero(t *testing.T) {
	t.Parallel()

	newRig(t).run("app:health").expectOK(t)
}

// TestStatusShowsHandshakeHealthAndSources checks the status document.
// Credential provenance is included, but the bearer token itself is not.
func TestStatusShowsHandshakeHealthAndSources(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token("s3cret")
	})
	rg.env[remote.EnvToken] = "s3cret"

	run := rg.run("status").expectOK(t)

	status := run.json(t)

	if status["arkoren"] == nil || status["protocol"] != "2026-07-28" || status["extensions"] != float64(4) {
		t.Fatalf("status = %v", status)
	}

	if status["app"].(map[string]any)["name"] != "shop" || status["health"].(map[string]any)["status"] == nil {
		t.Fatalf("status = %v", status)
	}

	want(t, status["url"], `{"value":"`+rg.url+`","from":"env"}`)
	want(t, status["token"], `{"set":true,"from":"env"}`)

	if strings.Contains(run.stdout, "s3cret") {
		t.Fatal("status printed the token")
	}
}

// TestStatusWithoutURLIsUsage checks the required status connection settings.
// Unlike --version, status cannot succeed without an inspector.
func TestStatusWithoutURLIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "status").expectFail(t, 2, "no inspector URL")
}

// TestStatusReportsServerVersion checks remote version reporting.
// The selected URL's provenance accompanies the negotiated protocol.
func TestStatusReportsServerVersion(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	status := rg.run("-url", rg.url, "status").expectOK(t).json(t)

	if status["protocol"] != "2026-07-28" || status["app"].(map[string]any)["version"] != "1.2.3" {
		t.Fatalf("status = %v", status)
	}

	want(t, status["url"], `{"value":"`+rg.url+`","from":"flag"}`)
}

// TestVersionWithoutURLIsOffline checks native local version reporting.
// No endpoint is needed to identify the installed executable.
func TestVersionWithoutURLIsOffline(t *testing.T) {
	t.Parallel()

	result := newRig(t).runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "--version").expectOK(t)

	if result.json(t)["version"] != "(devel)" {
		t.Fatalf("version = %q", result.stdout)
	}
}

// TestVersionIgnoresUnreachableInspector checks local-only version output.
// Even an unusable endpoint must not affect the version result.
func TestVersionIgnoresUnreachableInspector(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = deadURL(t)

	if result := rg.run("--version").expectOK(t); result.json(t)["version"] != "(devel)" {
		t.Fatalf("version = %q", result.stdout)
	}
}

// TestStatusNativeProtocolFailureIsExitOne checks SDK error propagation.
// Status fails without printing a report when tools/list rejects discovery.
func TestStatusNativeProtocolFailureIsExitOne(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "broken", Version: "1"}, nil)
	server.AddReceivingMiddleware(func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			if method == "tools/list" {
				return nil, &jsonrpc.Error{Code: -32602, Message: "catalog rejected"}
			}

			return next(ctx, method, request)
		}
	})
	rg.env[remote.EnvURL] = nativeServerURL(t, server)

	run := rg.run("status")

	if run.code != 1 || run.stdout != "" {
		t.Fatalf("status: code %d\n%s", run.code, run.stdout)
	}

	contains(t, run.stderr, "catalog rejected")
}

// TestStatusRejectsArguments checks the argument-free inspector command.
// Extra words must not be mistaken for remote operation input.
func TestStatusRejectsArguments(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("status", "x").expectFail(t, 2, "takes no arguments")
}

// TestNoArgumentsPrintsGeneratedHelp checks the no-command entry point.
// Native root help is a successful, offline command inventory.
func TestNoArgumentsPrintsGeneratedHelp(t *testing.T) {
	t.Parallel()

	run := newRig(t).run()

	if run.code != 0 || !strings.Contains(run.stdout, "COMMANDS:") {
		t.Fatalf("run = %+v", run)
	}
}

// TestEnvFileProvidesURLAndToken checks paired project-file credentials.
// Status must identify the file as the source without printing its token.
func TestEnvFileProvidesURLAndToken(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token("tok")
	})

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte("ARKOREN_MCP_URL="+rg.url+"\nARKOREN_MCP_TOKEN=tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := rg.runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "status").expectOK(t).json(t)

	want(t, status["url"], `{"value":"`+rg.url+`","from":".env"}`)
	want(t, status["token"], `{"set":true,"from":".env"}`)
}

// TestEnvPrecedenceIsFlagThenEnvThenFile checks configuration precedence.
// Explicit flags and environment values must override project-file settings.
func TestEnvPrecedenceIsFlagThenEnvThenFile(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	file := "ARKOREN_MCP_URL=http://file.invalid/x\n"

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	fromEnvironment := rg.run("status").expectOK(t).json(t)

	want(t, fromEnvironment["url"], `{"value":"`+rg.url+`","from":"env"}`)

	fromFlag := rg.runWith(t.Context(), runOptions{
		env: map[string]string{remote.EnvURL: "http://env.invalid"},
	}, "-url", rg.url, "status").expectOK(t).json(t)

	want(t, fromFlag["url"], `{"value":"`+rg.url+`","from":"flag"}`)
}

// TestEnvFileFlagReadsAnotherFile checks an explicitly selected env file.
// Quoted export syntax must preserve the URL and its source name.
func TestEnvFileFlagReadsAnotherFile(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	if err := os.WriteFile(filepath.Join(rg.dir, "other.env"), []byte("export ARKOREN_MCP_URL='"+rg.url+"' # local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := rg.runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "-env-file", "other.env", "status").expectOK(t).json(t)

	want(t, status["url"], `{"value":"`+rg.url+`","from":"other.env"}`)
}

// TestEnvFileFlagEmptyDisablesTheFile checks explicit file-loading opt-out.
// A valid project URL must be ignored when the flag's value is empty.
func TestEnvFileFlagEmptyDisablesTheFile(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte("ARKOREN_MCP_URL="+rg.url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rg.runWith(t.Context(), runOptions{
		env: map[string]string{},
	}, "-env-file", "", "status").expectFail(t, 2, "no inspector URL")
}

// TestMissingExplicitEnvFileIsUsage checks errors for a requested file.
// An explicit missing path must not silently fall back to other files.
func TestMissingExplicitEnvFileIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("-env-file", "nope.env", "status").expectFail(t, 2, "read env file")
}

// TestMissingDefaultEnvFileIsFine checks optional default-file loading.
// Environment configuration remains sufficient without a project file.
func TestMissingDefaultEnvFileIsFine(t *testing.T) {
	t.Parallel()

	newRig(t).run("status").expectOK(t)
}

// follow runs the CLI in the background with -f and returns the channel of
// lines it prints, and a function that interrupts it and returns its result.
func (rig *rig) follow(args ...string) (<-chan string, func() result) {
	rig.t.Helper()

	ctx, cancel := context.WithCancel(rig.t.Context())
	sink := lineSink{
		lines: make(chan string, 16),
	}
	done := make(chan result, 1)

	go func() {
		done <- rig.runWith(ctx, runOptions{
			stdout: sink,
		}, args...)
	}()

	return sink.lines, func() result {
		cancel()

		return <-done
	}
}

// nextLine waits for the next line of a followed stream. The deadline only
// guards against a hung test; it is never reached on success.
func nextLine(t *testing.T, lines <-chan string) string {
	t.Helper()

	select {
	case line := <-lines:
		return line
	case <-time.After(10 * time.Second):
		t.Fatal("no line arrived on the stream")

		return ""
	}
}

// TestFollowPrintsOneObjectPerLineAndDroppedNotices checks streaming framing.
// Dropped notices retain their position between the surrounding payment items.
func TestFollowPrintsOneObjectPerLineAndDroppedNotices(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	lines, stop := rg.follow("billing:payments", "-f")

	rg.billing.arrivals <- Payment{
		ID:     "p3",
		Status: "paid",
		Cents:  1,
	}
	rg.billing.arrivals <- Payment{
		ID:     "gap",
		Status: "failed",
		Cents:  2,
	}

	got := []string{nextLine(t, lines), nextLine(t, lines), nextLine(t, lines)}

	want := []string{`{"cents":1,"id":"p3","status":"paid"}` + "\n", `{"dropped":3}` + "\n", `{"cents":2,"id":"gap","status":"failed"}` + "\n"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}

	if run := stop(); run.code != 0 {
		t.Fatalf("interrupt exit code = %d; stderr %s", run.code, run.stderr)
	}
}

// TestFollowAppliesTheOperationsFlags checks server-side stream filtering.
// Input flags must reach the follow handler as they do an ordinary read.
func TestFollowAppliesTheOperationsFlags(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	lines, stop := rg.follow("-f", "billing:payments", "-status", "failed")

	rg.billing.arrivals <- Payment{
		ID:     "a",
		Status: "paid",
		Cents:  1,
	}
	rg.billing.arrivals <- Payment{
		ID:     "b",
		Status: "failed",
		Cents:  2,
	}

	if line := nextLine(t, lines); line != `{"cents":2,"id":"b","status":"failed"}`+"\n" {
		t.Fatalf("line = %q", line)
	}

	stop()
}

// TestFollowIsColoredOnATerminal checks terminal rendering of streamed JSON.
// Cancellation joins the CLI run before its exact ANSI output is compared.
func TestFollowIsColoredOnATerminal(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	ctx, cancel := context.WithCancel(t.Context())
	sink := lineSink{
		lines: make(chan string, 4),
	}
	done := make(chan result, 1)

	go func() {
		done <- rg.runWith(ctx, runOptions{
			stdout:   sink,
			terminal: true,
		}, "billing:payments", "-f")
	}()

	rg.billing.arrivals <- Payment{
		ID:     "a",
		Status: "paid",
		Cents:  1,
	}
	line := nextLine(t, sink.lines)

	cancel()
	<-done

	expected := "{" + console.ColorKey + `"cents"` + console.ColorReset + ":" + console.ColorNumber + "1" + console.ColorReset + "," +
		console.ColorKey + `"id"` + console.ColorReset + ":" + console.ColorString + `"a"` + console.ColorReset + "," +
		console.ColorKey + `"status"` + console.ColorReset + ":" + console.ColorString + `"paid"` + console.ColorReset + "}\n"

	if line != expected {
		t.Fatalf("line = %q, want %q", line, expected)
	}
}

// TestFollowOnAnOperationThatCannotFollowIsUsage checks manifest capabilities.
// A local usage error must reject follow mode for an ordinary read.
func TestFollowOnAnOperationThatCannotFollowIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("public:routes", "-f").expectFail(t, 2, "public:routes cannot follow")
}

// TestFollowErrorIsReportedBeforeTheStream checks validation before streaming.
// Problem details must still map invalid input back to its supplying flag.
func TestFollowErrorIsReportedBeforeTheStream(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:payments", "-f", "-status", "bogus").expectFail(t, 1, "Invalid input", "-status")
}

// fakeInspectorToken is used only by the local configuration test servers.
const fakeInspectorToken = "fake-inspector-token"

// projectURLRig supplies a project-only URL and an inherited token. The
// server records SDK requests, with an optional malformed-response substitute.
// A nil response selects a native SDK server with an empty tool catalog.
func projectURLRig(t *testing.T, response http.Handler) (*rig, <-chan *http.Request) {
	t.Helper()

	requests := make(chan *http.Request, 16)
	native := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "config", Version: "1"}, &sdkmcp.ServerOptions{
		Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}},
	})

	if response == nil {
		response = sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return native },
			&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests <- request

		response.ServeHTTP(w, request)
	}))
	t.Cleanup(server.Close)

	rg := &rig{
		t:   t,
		url: server.URL + "/inspect",
		dir: t.TempDir(),
		env: map[string]string{remote.EnvToken: fakeInspectorToken},
	}

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte(remote.EnvURL+"="+rg.url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return rg, requests
}

// expectNoInspectorRequests checks the network boundary, not just the CLI's
// exit status. A failure reports whether the first request carried a bearer
// without printing the token.
func expectNoInspectorRequests(t *testing.T, requests <-chan *http.Request) {
	t.Helper()

	if count := len(requests); count != 0 {
		request := <-requests
		t.Fatalf("%d inspector requests; first was %s %s, carried bearer: %t", count,
			request.Method, request.URL.Path, strings.HasPrefix(request.Header.Get("Authorization"), "Bearer "))
	}
}

// expectUnapprovedProjectURL checks the failure's migration advice and that
// neither output stream exposes the token or attempts discovery.
func expectUnapprovedProjectURL(t *testing.T, run result, requests <-chan *http.Request) {
	t.Helper()

	expectNoInspectorRequests(t, requests)

	run.expectFail(t, console.ExitUsage, "implicit ./.env", "-url", remote.EnvURL, "-env-file")

	if run.stdout != "" || strings.Contains(run.stderr, fakeInspectorToken) {
		t.Fatal("unapproved configuration produced stdout or printed the token")
	}
}

// TestToolsDoesNotLeakTokenToImplicitProjectURL checks discovery trust rules.
// An unapproved project URL must receive no discovery request or bearer token.
func TestToolsDoesNotLeakTokenToImplicitProjectURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	expectUnapprovedProjectURL(t, rg.run("tools"), requests)
}

// TestRemoteHelpDoesNotLeakTokenToImplicitProjectURL checks schema-help trust.
// A help flag does not authorize sending an inherited token to a project URL.
func TestRemoteHelpDoesNotLeakTokenToImplicitProjectURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	expectUnapprovedProjectURL(t, rg.run("public:routes", "--help"), requests)
}

// TestImplicitProjectURLDoesNotReceiveEnvironmentToken checks inherited secrets.
// A project file alone cannot authorize sending an environment token.
func TestImplicitProjectURLDoesNotReceiveEnvironmentToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	expectUnapprovedProjectURL(t, rg.run("public:routes"), requests)
}

// TestHelpFlagDoesNotLeakTokenToImplicitProjectURL checks the short help route.
// Unlike remote help, generated root help never resolves the URL or token.
func TestHelpFlagDoesNotLeakTokenToImplicitProjectURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	rg.run("-h").expectOK(t)
	expectNoInspectorRequests(t, requests)
}

// TestStatusDoesNotLeakTokenToImplicitProjectURL checks status URL approval.
// Rejected configuration must fail before opening a network connection.
func TestStatusDoesNotLeakTokenToImplicitProjectURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	expectUnapprovedProjectURL(t, rg.run("status"), requests)
}

// TestVersionDoesNotLeakTokenToImplicitProjectURL checks offline version output.
// Local build information must never send inherited credentials anywhere.
func TestVersionDoesNotLeakTokenToImplicitProjectURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	rg.run("--version").expectOK(t)
	expectNoInspectorRequests(t, requests)
}

// TestOperationDoesNotLeakTokenToImplicitProjectURL checks qualified dispatch.
// The URL must be approved before either discovery or operation invocation.
func TestOperationDoesNotLeakTokenToImplicitProjectURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	expectUnapprovedProjectURL(t, rg.run("billing:payments"), requests)
}

// TestInvalidManifestCannotReceiveInheritedToken checks pre-handshake rejection.
// A malformed server response must never be needed to decide URL trust.
func TestInvalidManifestCannotReceiveInheritedToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`<html>not JSON</html>`))
	}))

	expectUnapprovedProjectURL(t, rg.run("tools", "--json"), requests)
}

// TestImplicitProjectURLDoesNotReceiveFlagToken checks explicit-token safety.
// Supplying a token does not approve a URL inherited from the project file.
func TestImplicitProjectURLDoesNotReceiveFlagToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)
	rg.env = map[string]string{}

	expectUnapprovedProjectURL(t, rg.run("-token", fakeInspectorToken, "status"), requests)
}

// TestImplicitEnvFileTokenDoesNotAuthorizeInheritedToken checks token pairing.
// A file token cannot approve sending a different environment token to its URL.
func TestImplicitEnvFileTokenDoesNotAuthorizeInheritedToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)
	file := remote.EnvURL + "=" + rg.url + "\n" + remote.EnvToken + "=other-file-token\n"

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	expectUnapprovedProjectURL(t, rg.run("status"), requests)
}

// TestNoArgumentsIgnoreImplicitProjectURLAndToken checks configuration-free help.
// The no-command route neither connects nor exposes inherited credentials.
func TestNoArgumentsIgnoreImplicitProjectURLAndToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	run := rg.run()

	expectNoInspectorRequests(t, requests)

	if run.code != console.ExitOK || run.stderr != "" {
		t.Fatalf("no-argument help: code %d, stderr %q", run.code, run.stderr)
	}

	contains(t, run.stdout, "COMMANDS:")

	if strings.Contains(run.stdout, fakeInspectorToken) {
		t.Fatal("offline help printed the token")
	}
}

// TestFlagURLReceivesInheritedToken checks explicit URL approval.
// The trusted server receives the token while the project URL remains untouched.
func TestFlagURLReceivesInheritedToken(t *testing.T) {
	t.Parallel()

	trusted := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token(fakeInspectorToken)
	})
	rg, requests := projectURLRig(t, nil)

	run := rg.run("-url", trusted.url, "status").expectOK(t)
	expectNoInspectorRequests(t, requests)

	status := run.json(t)

	want(t, status["url"], `{"value":"`+trusted.url+`","from":"flag"}`)
	want(t, status["token"], `{"set":true,"from":"env"}`)

	if strings.Contains(run.stdout+run.stderr, fakeInspectorToken) {
		t.Fatal("trusted URL diagnostics printed the token")
	}
}

// TestEnvironmentURLReceivesInheritedToken checks environment URL approval.
// Status retains provenance without printing the authenticated token.
func TestEnvironmentURLReceivesInheritedToken(t *testing.T) {
	t.Parallel()

	trusted := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token(fakeInspectorToken)
	})
	rg, requests := projectURLRig(t, nil)
	rg.env[remote.EnvURL] = trusted.url

	run := rg.run("status").expectOK(t)
	expectNoInspectorRequests(t, requests)

	status := run.json(t)

	want(t, status["url"], `{"value":"`+trusted.url+`","from":"env"}`)
	want(t, status["token"], `{"set":true,"from":"env"}`)

	if strings.Contains(run.stdout+run.stderr, fakeInspectorToken) {
		t.Fatal("trusted URL diagnostics printed the token")
	}
}

// TestFlagTokenOverridesEnvironmentTokenForTrustedURL checks token precedence.
// An approved URL uses the explicitly supplied token and reports its source.
func TestFlagTokenOverridesEnvironmentTokenForTrustedURL(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token(fakeInspectorToken)
	})
	rg.env[remote.EnvToken] = "other-fake-token"

	status := rg.run("-token", fakeInspectorToken, "status").expectOK(t).json(t)

	want(t, status["token"], `{"set":true,"from":"flag"}`)
}

// expectInspectorBearer checks that an approved configuration sends the
// intended token on the initial handshake, rather than silently dropping it.
func expectInspectorBearer(t *testing.T, requests <-chan *http.Request) {
	t.Helper()

	if count := len(requests); count < 2 {
		t.Fatalf("inspector requests = %d, want discovery and tools/list", count)
	}

	for len(requests) > 0 {
		request := <-requests

		if request.Method != http.MethodPost || request.URL.Path != "/inspect" ||
			request.Header.Get("Authorization") != "Bearer "+fakeInspectorToken {
			t.Fatal("MCP request method, path, or bearer did not match the approved configuration")
		}
	}
}

// TestExplicitEnvFileApprovesURLForInheritedToken checks explicit file trust.
// The approved URL receives an authenticated handshake without leaking output.
func TestExplicitEnvFileApprovesURLForInheritedToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	if err := os.WriteFile(filepath.Join(rg.dir, "trusted.env"), []byte(remote.EnvURL+"="+rg.url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := rg.run("-env-file", "trusted.env", "status").expectOK(t)
	expectInspectorBearer(t, requests)

	status := run.json(t)

	want(t, status["url"], `{"value":"`+rg.url+`","from":"trusted.env"}`)
	want(t, status["token"], `{"set":true,"from":"env"}`)

	if strings.Contains(run.stdout+run.stderr, fakeInspectorToken) {
		t.Fatal("explicit-file diagnostics printed the token")
	}
}

// TestExplicitDefaultEnvFileApprovesURLForFlagToken checks explicit .env approval.
// Naming the default file deliberately permits its URL to receive a flag token.
func TestExplicitDefaultEnvFileApprovesURLForFlagToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)
	rg.env = map[string]string{}

	run := rg.run("-env-file", ".env", "-token", fakeInspectorToken, "status").expectOK(t)
	expectInspectorBearer(t, requests)

	status := run.json(t)

	want(t, status["url"], `{"value":"`+rg.url+`","from":".env"}`)
	want(t, status["token"], `{"set":true,"from":"flag"}`)

	if strings.Contains(run.stdout+run.stderr, fakeInspectorToken) {
		t.Fatal("explicit-file diagnostics printed the token")
	}
}

// TestExplicitEnvFileDoesNotFallBackToImplicitProjectURL checks file isolation.
// Selecting a token-only file must not resurrect a URL from the default file.
func TestExplicitEnvFileDoesNotFallBackToImplicitProjectURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	if err := os.WriteFile(filepath.Join(rg.dir, "token.env"), []byte(remote.EnvToken+"=file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := rg.run("-env-file", "token.env", "status")
	expectNoInspectorRequests(t, requests)

	run.expectFail(t, console.ExitUsage, "no inspector URL")
}

// TestDisabledEnvFileIgnoresProjectURLWithInheritedToken checks file opt-out.
// Without a trusted URL the command fails locally and keeps the token private.
func TestDisabledEnvFileIgnoresProjectURLWithInheritedToken(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)

	run := rg.run("-env-file", "", "status")
	expectNoInspectorRequests(t, requests)

	run.expectFail(t, console.ExitUsage, "no inspector URL")

	if strings.Contains(run.stdout+run.stderr, fakeInspectorToken) {
		t.Fatal("disabled-file diagnostics printed the token")
	}
}

// TestDisabledEnvFilePreservesTrustedEnvironmentURLAndToken checks env fallback.
// A disabled malformed project file cannot interfere with trusted credentials.
func TestDisabledEnvFilePreservesTrustedEnvironmentURLAndToken(t *testing.T) {
	t.Parallel()

	trusted := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token(fakeInspectorToken)
	})
	rg, requests := projectURLRig(t, nil)
	rg.env[remote.EnvURL] = trusted.url

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte(remote.EnvURL+"='unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rg.run("-env-file", "", "status").expectOK(t)
	expectNoInspectorRequests(t, requests)
}

// TestImplicitEnvFileTokenStaysPairedWithURL checks paired file credentials.
// An empty environment token does not replace the file's authenticated pair.
func TestImplicitEnvFileTokenStaysPairedWithURL(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)
	file := remote.EnvURL + "=" + rg.url + "\n" + remote.EnvToken + "=" + fakeInspectorToken + "\n"

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	rg.env = map[string]string{remote.EnvToken: ""}

	run := rg.run("status").expectOK(t)
	expectInspectorBearer(t, requests)

	status := run.json(t)

	want(t, status["url"], `{"value":"`+rg.url+`","from":".env"}`)
	want(t, status["token"], `{"set":true,"from":".env"}`)

	if strings.Contains(run.stdout+run.stderr, fakeInspectorToken) {
		t.Fatal("paired-file diagnostics printed the token")
	}
}

// TestMCPTokenEnvironmentAuthenticates checks the public environment key.
// The literal name must authenticate discovery and remain redacted in status.
func TestMCPTokenEnvironmentAuthenticates(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token(fakeInspectorToken)
	})
	rg.env = map[string]string{
		"ARKOREN_MCP_URL":   rg.url,
		"ARKOREN_MCP_TOKEN": fakeInspectorToken,
	}

	run := rg.run("status").expectOK(t)

	want(t, run.json(t)["token"], `{"set":true,"from":"env"}`)

	if strings.Contains(run.stdout+run.stderr, fakeInspectorToken) {
		t.Fatal("status printed the bearer credential")
	}
}

// TestRetiredInspectTokenIsIgnored checks that no legacy credential alias exists.
// Neither the environment nor the project file may send the retired token;
// its file value is ignored even when malformed, like any unrelated secret.
func TestRetiredInspectTokenIsIgnored(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)
	rg.env = map[string]string{"ARKOREN_INSPECT_TOKEN": fakeInspectorToken}
	file := "ARKOREN_MCP_URL=" + rg.url + "\nARKOREN_INSPECT_TOKEN='unterminated\n"

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	rg.run("status").expectOK(t)

	if count := len(requests); count < 2 {
		t.Fatalf("inspector requests = %d, want discovery and tools/list", count)
	}

	for len(requests) > 0 {
		if request := <-requests; request.Header.Get("Authorization") != "" {
			t.Fatal("retired setting sent an authorization header")
		}
	}
}

// TestImplicitProjectURLWithoutTokenConnects checks unauthenticated discovery.
// URL approval is not required when no bearer token crosses the boundary.
func TestImplicitProjectURLWithoutTokenConnects(t *testing.T) {
	t.Parallel()

	rg, requests := projectURLRig(t, nil)
	rg.env = map[string]string{}

	rg.run("status").expectOK(t)

	if count := len(requests); count < 2 {
		t.Fatalf("inspector requests = %d, want discovery and tools/list", count)
	}

	for len(requests) > 0 {
		if request := <-requests; request.Header.Get("Authorization") != "" {
			t.Fatal("tokenless configuration sent an authorization header")
		}
	}
}

// connectionInput implements the conventional wait input on a caller extension.
type connectionInput struct {
	// Wait requests the schema-declared condition-wait convention.
	Wait bool `json:"wait"`

	// Timeout carries the remaining command budget as a duration string.
	Timeout string `json:"timeout"`
}

// connectionResult records one isolated CLI invocation without an app fixture.
type connectionResult struct {
	// code is the CLI exit status without spawning a process.
	code int

	// out captures JSON results printed to stdout.
	out string

	// err captures failure diagnostics printed to stderr, not a Go error.
	err string
}

// runConnection drives the real client with a pinned URL and optional stdin.
func runConnection(t *testing.T, ctx context.Context, url, stdin string, args ...string) connectionResult {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := run(ctx, append([]string{"--url", url}, args...), console.Environment{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader(stdin),
		Dir:    t.TempDir(),
		Env: func(string) (string, bool) {
			return "", false
		},
	})

	return connectionResult{
		code: code,
		out:  stdout.String(),
		err:  stderr.String(),
	}
}

// conditionInspector builds a manifest-discovered read without importing app.
func conditionInspector(t *testing.T, handler func(context.Context, connectionInput) (bool, error)) http.Handler {
	t.Helper()

	tool := mcp.NewTool("life.ready", func(ctx context.Context, input connectionInput) (map[string]bool, error) {
		ready, err := handler(ctx, input)

		return map[string]bool{"satisfied": ready}, err
	}, func(tool *mcp.ToolBuilder[connectionInput, map[string]bool]) {
		tool.CLI("life", "ready").ReadOnly(true)
	})

	return (&fixtureBuilder{tools: []mcp.Tool{tool}}).handler(t)
}

// mcpMethod reads and restores an SDK request for transport fault injection.
// Tests target only tools/call, never initialization or notifications.
func mcpMethod(request *http.Request) string {
	body, err := io.ReadAll(request.Body)

	if err != nil {
		return ""
	}

	request.Body = io.NopCloser(bytes.NewReader(body))

	var envelope struct {
		// Method distinguishes tools/call from SDK negotiation and notifications.
		Method string `json:"method"`
	}

	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}

	return envelope.Method
}

// satisfied checks the explicitly object-shaped condition output. MCP core
// tools deliberately reject scalar outputs, unlike the retired inspect API.
func satisfied(result connectionResult) bool {
	var value map[string]bool

	return json.Unmarshal([]byte(result.out), &value) == nil && value["satisfied"]
}

// unboundAddress reserves and releases a local port so no listener exists yet.
func unboundAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	address := listener.Addr().String()

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	return address
}

// bindInspector starts serving only when called, not when the address is chosen.
func bindInspector(t *testing.T, address string, handler http.Handler) {
	t.Helper()

	listener, err := net.Listen("tcp", address)

	if err != nil {
		t.Fatal(err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: time.Second,
	}
	done := make(chan error, 1)

	go func() {
		done <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()

		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("server: %v", err)
		}
	})
}

// TestWaitConnectsAfterPortIsBoundThenWaitsForCondition checks late startup.
// Connection retries and the condition wait must share one timeout budget.
func TestWaitConnectsAfterPortIsBoundThenWaitsForCondition(t *testing.T) {
	t.Parallel()

	address := unboundAddress(t)
	ready := make(chan struct{})
	called := make(chan connectionInput, 1)

	handler := conditionInspector(t, func(ctx context.Context, input connectionInput) (bool, error) {
		called <- input

		select {
		case <-ready:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
	done := make(chan connectionResult, 1)

	go func() {
		done <- runConnection(t, t.Context(), "http://"+address, "", "life:ready", "--wait", "--timeout", "2s")
	}()

	select {
	case result := <-done:
		t.Fatalf("returned before any listener existed: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}

	bindInspector(t, address, handler)

	input := <-called
	remaining, err := time.ParseDuration(input.Timeout)

	if err != nil || !input.Wait || remaining >= 2*time.Second || remaining <= 0 {
		t.Fatalf("input = %+v, duration error = %v", input, err)
	}

	select {
	case result := <-done:
		t.Fatalf("returned before condition was satisfied: %+v", result)
	default:
	}

	close(ready)

	result := <-done

	if result.code != 0 || !satisfied(result) || result.err != "" {
		t.Fatalf("result = %+v", result)
	}
}

// TestWaitTimesOutWhenPortNeverBinds checks connection-budget exhaustion.
// Intermediate transport failures stay hidden behind one final diagnostic.
func TestWaitTimesOutWhenPortNeverBinds(t *testing.T) {
	t.Parallel()

	address := unboundAddress(t)

	result := runConnection(t, t.Context(), "http://"+address, "", "life:ready", "--wait", "--timeout", "100ms")

	if result.code != 1 || !strings.Contains(result.err, "timed out after 100ms connecting to the inspector") {
		t.Fatalf("result = %+v", result)
	}

	if errorDocument(t, result.err)["message"] == "" || result.out != "" {
		t.Fatalf("intermediate errors leaked: %+v", result)
	}
}

// TestWaitDeadlineIncludesConnectionTime checks budget propagation to the server.
// Time spent connecting must reduce the condition handler's remaining timeout.
func TestWaitDeadlineIncludesConnectionTime(t *testing.T) {
	t.Parallel()

	address := unboundAddress(t)
	called := make(chan connectionInput, 1)

	handler := conditionInspector(t, func(ctx context.Context, input connectionInput) (bool, error) {
		called <- input
		<-ctx.Done()

		return false, ctx.Err()
	})
	done := make(chan connectionResult, 1)

	go func() {
		done <- runConnection(t, t.Context(), "http://"+address, "", "life:ready", "--wait", "--timeout", "1s")
	}()

	time.Sleep(150 * time.Millisecond)
	bindInspector(t, address, handler)

	input := <-called
	remaining, err := time.ParseDuration(input.Timeout)

	if err != nil || remaining >= 900*time.Millisecond {
		t.Fatalf("connection time was not subtracted: %+v, %v", input, err)
	}

	result := <-done

	if result.code != 1 || !strings.Contains(result.err, "timed out after 1s waiting for life:ready") {
		t.Fatalf("result = %+v", result)
	}
}

// TestWaitDoesNotRetryHTTPFailure checks terminal HTTP problem responses.
// A server-side failure must be shown immediately rather than replayed.
func TestWaitDoesNotRetryHTTPFailure(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	manifest := conditionInspector(t, func(context.Context, connectionInput) (bool, error) {
		return true, nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if mcpMethod(request) == "tools/call" {
			calls.Add(1)
			writer.Header().Set("Content-Type", "application/problem+json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"status":503,"title":"Health check failed","detail":"database: bad credentials"}`))

			return
		}

		manifest.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)

	result := runConnection(t, t.Context(), server.URL, "", "life:ready", "--wait", "--timeout", "1s")

	if result.code != 1 || calls.Load() != 1 || !strings.Contains(result.err, "Service Unavailable") {
		t.Fatalf("result = %+v, calls = %d", result, calls.Load())
	}
}

// TestWaitDoesNotRetryAuthenticationFailure checks handshake authorization.
// An unauthorized response exits with the authentication code after one call.
func TestWaitDoesNotRetryAuthenticationFailure(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	result := runConnection(t, t.Context(), server.URL, "", "life:ready", "--wait", "--timeout", "1s")

	if result.code != 4 || calls.Load() != 1 {
		t.Fatalf("result = %+v, calls = %d", result, calls.Load())
	}
}

// TestWaitFromStdinIsMaterializedOnce checks single-pass input handling.
// Preparing a wait must leave the same base input available for invocation.
func TestWaitFromStdinIsMaterializedOnce(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(conditionInspector(t, func(_ context.Context, input connectionInput) (bool, error) {
		return input.Wait && input.Timeout != "", nil
	}))
	t.Cleanup(server.Close)

	result := runConnection(t, t.Context(), server.URL, `{"wait":true,"timeout":"1s"}`, "life:ready", "--input", "-")

	if result.code != 0 || !satisfied(result) {
		t.Fatalf("result = %+v", result)
	}
}

// TestWaitRetriesReadPostAfterConnectionReset checks safe read replay.
// A discovered waiting read carries an idempotency key and retries a reset.
func TestWaitRetriesReadPostAfterConnectionReset(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	manifest := conditionInspector(t, func(context.Context, connectionInput) (bool, error) {
		return true, nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if mcpMethod(request) == "tools/call" {
			if calls.Add(1) == 1 {
				connection, _, err := writer.(http.Hijacker).Hijack()

				if err != nil {
					t.Error(err)

					return
				}

				_ = connection.Close()

				return
			}
		}

		manifest.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)

	result := runConnection(t, t.Context(), server.URL, "", "life:ready", "--wait", "--timeout", "1s")

	if result.code != 0 || !satisfied(result) || calls.Load() != 2 || result.err != "" {
		t.Fatalf("result = %+v, calls = %d", result, calls.Load())
	}
}

// TestWaitNeverReplaysAnAction checks the action replay boundary.
// Wait-shaped input cannot make a potentially effectful action retryable.
func TestWaitNeverReplaysAnAction(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	tool := mcp.NewTool("life.change", func(context.Context, connectionInput) (map[string]bool, error) {
		return map[string]bool{"satisfied": true}, nil
	}, func(tool *mcp.ToolBuilder[connectionInput, map[string]bool]) {
		tool.CLI("life", "change")
	})
	inspector := (&fixtureBuilder{tools: []mcp.Tool{tool}}).handler(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if mcpMethod(request) != "tools/call" {
			inspector.ServeHTTP(writer, request)

			return
		}

		calls.Add(1)

		if request.Header.Get("Idempotency-Key") != "" {
			t.Error("action was marked replayable")
		}

		connection, _, err := writer.(http.Hijacker).Hijack()

		if err != nil {
			t.Error(err)

			return
		}

		_ = connection.Close()
	}))
	t.Cleanup(server.Close)

	result := runConnection(t, t.Context(), server.URL, "", "life:change", "--wait", "--timeout", "1s")

	if result.code != 1 || calls.Load() != 1 {
		t.Fatalf("result = %+v, calls = %d", result, calls.Load())
	}
}

// TestWaitConnectionRetriesRespectCancellation checks caller cancellation.
// A long configured budget must not keep the retry loop alive after cancel.
func TestWaitConnectionRetriesRespectCancellation(t *testing.T) {
	t.Parallel()

	address := unboundAddress(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan connectionResult, 1)
	go func() {
		done <- runConnection(t, ctx, "http://"+address, "", "life:ready", "--wait", "--timeout", "30s")
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if result.code != 1 || !strings.Contains(result.err, "context canceled") {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("connection retry ignored cancellation")
	}
}

// TestWithoutWaitConnectionFailureIsImmediate checks nonwaiting dispatch.
// Explicit wait=false must retain the immediate connection-failure path.
func TestWithoutWaitConnectionFailureIsImmediate(t *testing.T) {
	t.Parallel()

	address := unboundAddress(t)

	before := time.Now()
	result := runConnection(t, t.Context(), "http://"+address, "", "life:ready", "--wait=false")

	if result.code != 1 || !strings.Contains(result.err, "cannot reach the inspector") || time.Since(before) > time.Second {
		t.Fatalf("result = %+v, elapsed = %s", result, time.Since(before))
	}
}

// TestExistingProjectConflictIsConcise reproduces the complete bundled-skill
// collision without allowing hundreds of paths into the ordinary diagnostic.
func TestExistingProjectConflictIsConcise(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if code := run(t.Context(), []string{"new", "."}, env); code != console.ExitOK {
		t.Fatalf("create project: exit %d; %s", code, env.Stderr)
	}

	env.Stdout.(*bytes.Buffer).Reset()
	env.Stderr.(*bytes.Buffer).Reset()

	code := run(t.Context(), []string{"new", ".", "--yes"}, env)

	text := env.Stderr.(*bytes.Buffer).String()
	diagnostic := errorDocument(t, text)

	if code != console.ExitUsage || env.Stdout.(*bytes.Buffer).Len() != 0 || len(text) > 1200 {
		t.Fatalf("exit = %d; stdout = %s; diagnostic length = %d", code, env.Stdout, len(text))
	}

	if diagnostic["conflict_count"].(float64) < 100 || diagnostic["type"] != "file_conflict" {
		t.Fatalf("diagnostic = %v", diagnostic)
	}

	if _, ok := diagnostic["conflicts"]; ok {
		t.Fatal("default diagnostic contains the complete file list")
	}

	if strings.Contains(text, "references/guides/") || !strings.Contains(text, "--verbose") {
		t.Fatalf("diagnostic = %s", text)
	}
}

// TestVerboseProjectConflictListsPaths keeps the complete conflict list as
// structured opt-in data rather than embedding it in the message.
func TestVerboseProjectConflictListsPaths(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if code := run(t.Context(), []string{"new", "."}, env); code != console.ExitOK {
		t.Fatalf("create project: exit %d; %s", code, env.Stderr)
	}

	env.Stdout.(*bytes.Buffer).Reset()
	env.Stderr.(*bytes.Buffer).Reset()

	code := run(t.Context(), []string{"new", ".", "--yes", "--verbose"}, env)

	diagnostic := errorDocument(t, env.Stderr.(*bytes.Buffer).String())
	paths := diagnostic["conflicts"].([]any)

	if code != console.ExitUsage || len(paths) < 100 || len(paths) != int(diagnostic["conflict_count"].(float64)) {
		t.Fatalf("exit = %d; conflicts = %d", code, len(paths))
	}

	if len(diagnostic["message"].(string)) > 120 || env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatalf("message = %v; stdout = %s", diagnostic["message"], env.Stdout)
	}
}

// TestProjectPreviewSummarizesFiles keeps a normal dry run small even when the
// generated project includes the entire consumer documentation bundle.
func TestProjectPreviewSummarizesFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	code := run(t.Context(), []string{"new", ".", "--dry-run"}, env)

	output := result{code: code, stdout: env.Stdout.(*bytes.Buffer).String()}
	document := output.expectOK(t).json(t)

	if document["file_count"].(float64) < 100 || document["conflict_count"] != float64(0) || len(output.stdout) > 1200 {
		t.Fatalf("preview = %s", output.stdout)
	}

	for _, name := range []string{"files", "conflicts"} {
		if _, ok := document[name]; ok {
			t.Fatalf("default preview contains %s", name)
		}
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("preview wrote files: %v; %v", entries, err)
	}
}

// TestVerboseProjectPreviewListsFiles keeps complete previews available without
// changing their read-only behavior.
func TestVerboseProjectPreviewListsFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	code := run(t.Context(), []string{"new", ".", "--dry-run", "--verbose"}, env)

	document := (result{code: code, stdout: env.Stdout.(*bytes.Buffer).String()}).expectOK(t).json(t)

	if len(document["files"].([]any)) != int(document["file_count"].(float64)) || len(document["conflicts"].([]any)) != 0 {
		t.Fatal("verbose preview omitted file data")
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("preview wrote files: %v; %v", entries, err)
	}
}

// TestConflictingPreviewPrintsOneReport avoids repeating a verbose conflict list
// on stderr after an explicit preview already reported it on stdout.
func TestConflictingPreviewPrintsOneReport(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.WriteFile(filepath.Join(env.Dir, "go.mod"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	code := run(t.Context(), []string{"new", ".", "--dry-run", "--verbose"}, env)

	document := (result{stdout: env.Stdout.(*bytes.Buffer).String()}).json(t)

	if code != console.ExitUsage || document["conflict_count"] != float64(1) || env.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatalf("exit = %d; preview = %v; stderr = %s", code, document, env.Stderr)
	}

	content, err := os.ReadFile(filepath.Join(env.Dir, "go.mod"))

	if err != nil || string(content) != "original" {
		t.Fatalf("preview changed conflicting file: %q; %v", content, err)
	}
}

// TestSkillStatusSummarizesManyEdits keeps managed-file status useful without
// dumping the entire installation when many reference pages were modified.
func TestSkillStatusSummarizesManyEdits(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	content := map[string]string{"SKILL.md": "original"}

	for index := range 99 {
		content[fmt.Sprintf("references/%03d.md", index)] = "original"
	}

	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")

	installTestSkill(t, folder, testSkillBundle(t, content))

	for path := range content {
		if err := os.WriteFile(filepath.Join(folder, path), []byte("edited"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code := run(t.Context(), []string{"skills", "status"}, env)

	output := result{code: code, stdout: env.Stdout.(*bytes.Buffer).String()}
	document := output.expectOK(t).json(t)

	if document["modified_file_count"] != float64(100) || len(output.stdout) > 1200 {
		t.Fatalf("status = %s", output.stdout)
	}

	if _, ok := document["modified_files"]; ok {
		t.Fatal("default status lists every modified file")
	}

	env.Stdout.(*bytes.Buffer).Reset()

	code = run(t.Context(), []string{"skills", "status", "--verbose"}, env)

	document = (result{code: code, stdout: env.Stdout.(*bytes.Buffer).String()}).expectOK(t).json(t)

	if len(document["modified_files"].([]any)) != 100 {
		t.Fatal("verbose status omitted modified files")
	}
}

// TestVerboseToolsExposeInvocationDetails makes verbosity useful for machines
// that need schemas, without restoring the old unrelated CLI manual bundle.
func TestVerboseToolsExposeInvocationDetails(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	document := rig.run("tools", "--verbose").expectOK(t).json(t)

	for _, name := range []string{"cli", "manifest", "global_flags", "input", "output", "exit_codes"} {
		if _, ok := document[name]; ok {
			t.Fatalf("verbose discovery contains unrelated %s", name)
		}
	}

	for _, value := range document["tools"].([]any) {
		tool := value.(map[string]any)

		if tool["command"] == "billing:payments" {
			if tool["input_schema"].(map[string]any)["type"] != "object" || tool["output_schema"] == nil {
				t.Fatalf("tool schemas missing: %v", tool)
			}

			if len(tool["examples"].([]any)) == 0 || tool["positional"].([]any)[0] != "status" {
				t.Fatalf("invocation metadata missing: %v", tool)
			}

			return
		}
	}

	t.Fatal("billing:payments missing from verbose discovery")
}

// TestVerboseStatusIncludesRuntime expands only the explicit summary command;
// it performs no extra calls and leaves successful remote payloads unchanged.
func TestVerboseStatusIncludesRuntime(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	brief := rig.run("status").expectOK(t).json(t)
	full := rig.run("status", "--verbose").expectOK(t).json(t)

	if full["app"].(map[string]any)["runtime"] == nil || full["app"].(map[string]any)["build"] == nil {
		t.Fatal("verbose status omitted full identity")
	}

	if _, ok := brief["app"].(map[string]any)["runtime"]; ok {
		t.Fatal("default status included runtime internals")
	}

	if brief["health"].(map[string]any)["check_count"] == nil || full["health"].(map[string]any)["items"] == nil {
		t.Fatal("status did not distinguish health summary from detailed checks")
	}
}

// TestVerboseFlagDoesNotReplaceRemoteInput reserves presentation control while
// preserving a tool's same-named property through the explicit JSON input path.
func TestVerboseFlagDoesNotReplaceRemoteInput(t *testing.T) {
	t.Parallel()

	flags, rest, err := parseGlobals([]string{"external", "--verbose", "--input", `{"verbose":true}`})

	if err != nil || !flags.Verbose || len(rest) != 3 || rest[2] != `{"verbose":true}` {
		t.Fatalf("flags = %+v; rest = %v; error = %v", flags, rest, err)
	}

	flags, _, err = parseGlobals([]string{"--verbose", "--verbose=false"})

	if err != nil || flags.Verbose {
		t.Fatalf("explicit false was ignored: %+v; %v", flags, err)
	}
}

// TestVerboseVersionShowsSafeBuildIdentity helps distinguish installed binaries
// without reading configuration or exposing raw compiler settings.
func TestVerboseVersionShowsSafeBuildIdentity(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	code := run(t.Context(), []string{"--version", "--verbose"}, env)

	document := (result{code: code, stdout: env.Stdout.(*bytes.Buffer).String()}).expectOK(t).json(t)

	if document["go_version"] == nil || document["version"] == nil {
		t.Fatalf("verbose version = %v", document)
	}

	for _, name := range []string{"settings", "dependencies", "replace", "compiler_flags"} {
		if _, ok := document[name]; ok {
			t.Fatalf("version exposes raw %s", name)
		}
	}

	if len(console.VersionReport(false)) != 1 {
		t.Fatal("default version no longer stays concise")
	}
}

// TestVerboseReportsKeepBearerCredentialsPrivate ensures presentation expansion
// never becomes HTTP tracing or a dump of resolved credential values.
func TestVerboseReportsKeepBearerCredentialsPrivate(t *testing.T) {
	t.Parallel()

	credential := "private-fixture-bearer-value"
	rig := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token(credential)
	})

	for _, command := range []string{"status", "tools"} {
		output := rig.run(command, "--verbose", "--token", credential).expectOK(t)

		if strings.Contains(output.stdout+output.stderr, credential) {
			t.Fatalf("%s verbose output exposed its credential", command)
		}
	}
}

// TestVerboseDoesNotBypassUsageRedaction protects invalid flag values even
// when the caller explicitly requests complete diagnostic context.
func TestVerboseDoesNotBypassUsageRedaction(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	code := run(t.Context(), []string{"new", "preview", "--verbose", "--dry-run=private-fixture-value"}, env)

	text := env.Stderr.(*bytes.Buffer).String()

	if code != console.ExitUsage || strings.Contains(text, "private-fixture-value") {
		t.Fatalf("exit = %d; diagnostic = %s", code, text)
	}

	errorDocument(t, text)
}

// discoveryIdentityServer advertises an identity role without a read-only hint.
// The supplied handler lets each test observe calls or inject safe failures;
// merely discovering this metadata must never authorize its execution.
func discoveryIdentityServer(handler sdkmcp.ToolHandler) *sdkmcp.Server {
	return externalServer(&sdkmcp.Tool{
		Name: "vendor.identify", Description: "Identify this endpoint",
		InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{
			"group": "vendor", "operation": "identify", "role": "identity",
		}},
	}, handler)
}

// TestDiscoveryEffectsToolsDoesNotInvokeIdentity keeps discovery limited to MCP
// initialization and tools/list, even when an identity action is advertised.
func TestDiscoveryEffectsToolsDoesNotInvokeIdentity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": "shop", "arkoren": "identity-version"}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "tools", "--json", "--verbose")

	if calls.Load() != 0 {
		t.Fatalf("discovery invoked identity %d times", calls.Load())
	}

	if output.code != console.ExitOK || output.err != "" {
		t.Fatalf("output = %+v", output)
	}

	var document remote.ToolsReport

	if err := json.Unmarshal([]byte(output.out), &document); err != nil {
		t.Fatal(err)
	}

	if document.Inspector.App != nil || document.Inspector.Arkoren != "1" {
		t.Fatalf("discovery included tool-derived identity: %+v", document.Inspector)
	}

	if len(document.Tools) != 1 {
		t.Fatalf("tools = %+v", document.Tools)
	}

	if document.Tools[0].Kind != "action" || document.Tools[0].Role != "identity" {
		t.Fatalf("identity metadata = %+v", document.Tools[0])
	}
}

// TestDiscoveryEffectsToolsIgnoresFailingIdentity proves discovery does not
// depend on whether the identity action would succeed if explicitly invoked.
func TestDiscoveryEffectsToolsIgnoresFailingIdentity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "identity unavailable"},
		}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "tools")

	if output.code != console.ExitOK || output.err != "" || calls.Load() != 0 {
		t.Fatalf("output = %+v; identity calls = %d", output, calls.Load())
	}

	if !strings.Contains(output.out, `"command": "vendor:identify"`) {
		t.Fatalf("catalog = %s", output.out)
	}
}

// TestDiscoveryEffectsRemoteHelpDoesNotInvokeIdentity protects help for the
// identity tool itself: rendering its schema must not execute its handler.
func TestDiscoveryEffectsRemoteHelpDoesNotInvokeIdentity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": "shop"}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "vendor.identify", "--help")

	if output.code != console.ExitOK || output.err != "" || calls.Load() != 0 {
		t.Fatalf("output = %+v; identity calls = %d", output, calls.Load())
	}

	if !strings.Contains(output.out, "Identify this endpoint") {
		t.Fatalf("help = %s", output.out)
	}
}

// TestDiscoveryEffectsGroupHelpDoesNotInvokeIdentity protects metadata-derived
// group help from hidden tool execution, independently of exact-name help.
func TestDiscoveryEffectsGroupHelpDoesNotInvokeIdentity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": "shop"}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "vendor:", "--help")

	if output.code != console.ExitOK || output.err != "" || calls.Load() != 0 {
		t.Fatalf("output = %+v; identity calls = %d", output, calls.Load())
	}

	if !strings.Contains(output.out, "vendor:identify") {
		t.Fatalf("help = %s", output.out)
	}
}

// TestDiscoveryEffectsUnknownCommandDoesNotInvokeIdentity preserves usage-error
// precedence without allowing role metadata to execute a fallback action.
func TestDiscoveryEffectsUnknownCommandDoesNotInvokeIdentity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "identity unavailable"},
		}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "missing")

	if output.code != console.ExitUsage || output.out != "" || calls.Load() != 0 {
		t.Fatalf("output = %+v; identity calls = %d", output, calls.Load())
	}

	if !strings.Contains(errorDocument(t, output.err)["message"].(string), `unknown command "missing"`) {
		t.Fatalf("diagnostic = %s", output.err)
	}
}

// TestDiscoveryEffectsInvalidInputDoesNotInvokeIdentity keeps local input
// parsing ahead of any tools/call, including calls to the identity tool itself.
func TestDiscoveryEffectsInvalidInputDoesNotInvokeIdentity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": "shop"}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "vendor:identify", "--input", "{")

	if output.code != console.ExitUsage || output.out != "" || calls.Load() != 0 {
		t.Fatalf("output = %+v; identity calls = %d", output, calls.Load())
	}

	if !strings.Contains(output.err, "input") {
		t.Fatalf("diagnostic = %s", output.err)
	}
}

// TestDiscoveryEffectsExplicitIdentityInvokesOnce ensures selecting identity
// remains supported without a hidden discovery call doubling the action.
func TestDiscoveryEffectsExplicitIdentityInvokesOnce(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": "shop"}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "vendor:identify", "--compact")

	if output.code != console.ExitOK || output.err != "" || calls.Load() != 1 {
		t.Fatalf("output = %+v; identity calls = %d", output, calls.Load())
	}

	if output.out != "{\"name\":\"shop\"}\n" {
		t.Fatalf("identity = %s", output.out)
	}
}

// TestDiscoveryEffectsOtherActionSkipsIdentity ensures ordinary remote commands
// execute only the selected action, not unrelated advertised identity actions.
func TestDiscoveryEffectsOtherActionSkipsIdentity(t *testing.T) {
	t.Parallel()

	var identityCalls, actionCalls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		identityCalls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": "shop"}}, nil
	})
	server.AddTool(&sdkmcp.Tool{Name: "vendor.action", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			actionCalls.Add(1)

			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"called": true}}, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "vendor.action", "--compact")

	if output.code != console.ExitOK || output.err != "" || output.out != "{\"called\":true}\n" {
		t.Fatalf("output = %+v", output)
	}

	if identityCalls.Load() != 0 || actionCalls.Load() != 1 {
		t.Fatalf("identity calls = %d; selected action calls = %d", identityCalls.Load(), actionCalls.Load())
	}
}

// TestDiscoveryEffectsStatusRetainsIdentityBeforeHealth preserves status's
// explicit role calls, identity version override, and non-gating health output.
func TestDiscoveryEffectsStatusRetainsIdentityBeforeHealth(t *testing.T) {
	t.Parallel()

	var identityCalls, healthCalls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		identityCalls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]any{
			"name": "shop", "arkoren": "identity-version", "instance": "process-1",
		}}, nil
	})
	server.AddTool(&sdkmcp.Tool{
		Name: "vendor.health", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "health", "role": "health"}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		if identityCalls.Load() != 1 {
			t.Errorf("health ran before identity completed: %d identity calls", identityCalls.Load())
		}

		healthCalls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"status": "failing"}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "status")

	if output.code != console.ExitOK || output.err != "" {
		t.Fatalf("output = %+v", output)
	}

	if identityCalls.Load() != 1 || healthCalls.Load() != 1 {
		t.Fatalf("identity calls = %d; health calls = %d", identityCalls.Load(), healthCalls.Load())
	}

	var document remote.StatusReport

	if err := json.Unmarshal([]byte(output.out), &document); err != nil {
		t.Fatal(err)
	}

	if document.Arkoren != "identity-version" || document.Instance != "process-1" {
		t.Fatalf("status identity = %+v", document)
	}

	if string(remote.CompactJSON(document.Health)) != `{"status":"failing"}` {
		t.Fatalf("health = %s", document.Health)
	}
}

// TestDiscoveryEffectsStatusIdentityFailurePrecedesHealthAndOutput retains the
// original early identity failure: health and result rendering do not run.
func TestDiscoveryEffectsStatusIdentityFailurePrecedesHealthAndOutput(t *testing.T) {
	t.Parallel()

	var identityCalls, healthCalls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		identityCalls.Add(1)

		return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "identity unavailable"},
		}}, nil
	})
	server.AddTool(&sdkmcp.Tool{
		Name: "vendor.health", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "health", "role": "health"}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		healthCalls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"status": "ok"}}, nil
	})
	var stderr bytes.Buffer

	code := run(t.Context(), []string{"--url", nativeServerURL(t, server), "status"}, console.Environment{
		Stdout: &failingOutput{err: syscall.EPIPE}, Stderr: &stderr,
		Stdin: strings.NewReader(""), Dir: t.TempDir(),
		Env: func(string) (string, bool) {
			return "", false
		},
	})

	if code != console.ExitFailure || identityCalls.Load() != 1 || healthCalls.Load() != 0 {
		t.Fatalf("exit = %d; identity calls = %d; health calls = %d", code, identityCalls.Load(), healthCalls.Load())
	}

	if errorDocument(t, stderr.String())["message"] != "identity unavailable" {
		t.Fatalf("diagnostic = %s", stderr.String())
	}
}

// TestDiscoveryEffectsStatusOutputFailureRetainsPrecedence checks the shared
// stdout boundary after successful identity and a non-gating health failure.
func TestDiscoveryEffectsStatusOutputFailureRetainsPrecedence(t *testing.T) {
	t.Parallel()

	var identityCalls, healthCalls atomic.Int64
	server := discoveryIdentityServer(func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		identityCalls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": "shop"}}, nil
	})
	server.AddTool(&sdkmcp.Tool{
		Name: "vendor.health", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "health", "role": "health"}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		healthCalls.Add(1)

		return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "health unavailable"},
		}}, nil
	})
	var stderr bytes.Buffer

	code := run(t.Context(), []string{"--url", nativeServerURL(t, server), "status"}, console.Environment{
		Stdout: &failingOutput{err: syscall.EPIPE}, Stderr: &stderr,
		Stdin: strings.NewReader(""), Dir: t.TempDir(),
		Env: func(string) (string, bool) {
			return "", false
		},
	})

	if code != console.ExitFailure || identityCalls.Load() != 1 || healthCalls.Load() != 1 {
		t.Fatalf("exit = %d; identity calls = %d; health calls = %d", code, identityCalls.Load(), healthCalls.Load())
	}

	if errorDocument(t, stderr.String())["message"] != "write stdout: "+syscall.EPIPE.Error() {
		t.Fatalf("diagnostic = %s", stderr.String())
	}
}

// TestEndpointCredentialsAreRejectedWithoutDisclosure checks endpoint policy
// before any HTTP request. Credentials belong to the dedicated token source,
// never URL userinfo that status or transport diagnostics could reproduce.
func TestEndpointCredentialsAreRejectedWithoutDisclosure(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)
	endpoint := strings.Replace(server.URL, "http://", "http://alice:private-password@", 1)

	output := runConnection(t, context.Background(), endpoint, "", "tools", "--verbose")

	if output.code != 2 || requests.Load() != 0 || strings.Contains(output.out+output.err, "private-password") {
		t.Fatalf("credential endpoint = %+v; requests = %d", output, requests.Load())
	}
}

// TestInvalidEndpointDiagnosticsOmitSecrets checks all invalid URL paths at
// the public CLI boundary. Both parse failures and rejected components can carry
// credentials; neither concise nor verbose diagnostics may quote those inputs.
func TestInvalidEndpointDiagnosticsOmitSecrets(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"http://localhost/?token=private-password",
		"http://localhost/#private-password",
		"http://alice:private-password@%invalid",
		"file://alice:private-password@localhost/inspect",
	} {
		output := runConnection(t, t.Context(), endpoint, "", "tools", "--verbose")

		if output.code != 2 || strings.Contains(output.out+output.err, "private-password") {
			t.Fatalf("invalid endpoint diagnostic = %+v", output)
		}
	}
}

// TestEndpointPreservesEncodedPathSeparators checks the exact approved MCP
// resource. Decoding an escaped slash before constructing HTTP requests can
// select a different endpoint even though the origin remains unchanged.
func TestEndpointPreservesEncodedPathSeparators(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
		})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	host := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.EscapedPath() != "/tenant%2Fapp/inspect" {
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(host.Close)

	output := runConnection(t, t.Context(), host.URL+"/tenant%2Fapp/inspect", "", "echo")

	if output.code != 0 || !strings.Contains(output.out, `"ok": true`) {
		t.Fatalf("encoded endpoint = %+v", output)
	}
}

// nativeServerURL serves an SDK server without any Arkoren metadata or owner
// fixture. Native interop tests choose its tools and protocol capabilities.
func nativeServerURL(t *testing.T, server *sdkmcp.Server) string {
	t.Helper()

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true})
	host := httptest.NewServer(handler)
	t.Cleanup(host.Close)

	return host.URL
}

// fixtureBuilder composes explicit owner tool declarations into an SDK server.
// It is test-only composition, not an implicit inspector or runtime dependency
// on the MCP driver. Tests opt into every owner in newRig.
type fixtureBuilder struct {
	// compose registers lifecycle work before the fixture starts. Tests can
	// install readiness checks without mutating a running application.
	compose application.ComposerFunc

	// tools are registered directly with the SDK after validation.
	tools []mcp.Tool
	// token protects the outer HTTP handler, independently of tool failures.
	token string
}

// Tools appends explicit declarations. Example: fixture.Tools(public.MCPTools("public")...).
func (fixture *fixtureBuilder) Tools(tools ...mcp.Tool) {
	fixture.tools = append(fixture.tools, tools...)
}

// Token protects the HTTP endpoint. Example: fixture.Token("test-secret").
func (fixture *fixtureBuilder) Token(token string) { fixture.token = token }

// handler exposes typed core tools through a real SDK stateless HTTP server.
// Raw SDK registration leaves validation to core, preserving field diagnostics.
func (fixture *fixtureBuilder) handler(t *testing.T) http.Handler {
	t.Helper()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fixture", Version: "test"}, nil)

	for _, tool := range fixture.tools {
		registerFixtureTool(t, server, tool)
	}

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true})

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if fixture.token != "" && request.Header.Get("Authorization") != "Bearer "+fixture.token {
			writer.WriteHeader(http.StatusUnauthorized)

			return
		}

		handler.ServeHTTP(writer, request)
	})
}

// registerFixtureTool adapts only fixture composition. Core owns schemas,
// argument parsing, output encoding, and public failure classification.
func registerFixtureTool(t *testing.T, server *sdkmcp.Server, tool mcp.Tool) {
	t.Helper()

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	info := tool.Describe()
	var input, output any

	if err := json.Unmarshal(info.InputSchema, &input); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(info.OutputSchema, &output); err != nil {
		t.Fatal(err)
	}

	native := &sdkmcp.Tool{Name: info.Name, Description: info.Description, InputSchema: input, OutputSchema: output,
		Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: info.Annotations.ReadOnly},
		Meta:        sdkmcp.Meta{"arkoren.dev/cli": fixtureRecord(info.CLI)},
	}

	server.AddTool(native, func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		body, err := tool.Call(ctx, request.Params.Arguments)

		if err != nil {
			return fixtureFailure(err), nil
		}

		var structured map[string]any

		if err := json.Unmarshal(body, &structured); err != nil {
			return nil, err
		}

		return &sdkmcp.CallToolResult{StructuredContent: structured}, nil
	})
}

// fixtureFailure exposes only explicitly public failures and schema messages.
// Internal errors are redacted, matching the production adapter's contract.
func fixtureFailure(err error) *sdkmcp.CallToolResult {
	info, ok := mcp.ToolFailureInfo(err)

	if !ok {
		info = mcp.Failure{Code: "internal", Message: "tool failed"}
	}

	if message, safe := mcp.ToolErrorMessage(err); safe {
		info.Message = message
	}

	if errors.Is(err, mcp.ErrInvalidInput) {
		info.Code, info.Message = "invalid_input", "Invalid input"

		var violations validate.Errors

		if errors.As(err, &violations) {
			info.Details = fixtureFields(violations)
		}
	}

	return &sdkmcp.CallToolResult{IsError: true,
		Meta:    sdkmcp.Meta{"arkoren.dev/failure": fixtureRecord(info)},
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: info.Message}},
	}
}

// fixtureRecord crosses the SDK's JSON v1 boundary without treating jsontext
// values as byte strings. Test declarations contain only JSON-compatible data.
func fixtureRecord(value any) any {
	encoded, err := json.Marshal(value)

	if err != nil {
		panic(err)
	}

	var record any

	if err := json.Unmarshal(encoded, &record); err != nil {
		panic(err)
	}

	return record
}

// fixtureFields serializes pointer messages without exposing invalid values.
func fixtureFields(violations validate.Errors) []byte {
	fields := make(map[string][]string)

	for pointer, entries := range violations {
		for _, entry := range entries {
			fields[pointer] = append(fields[pointer], entry.Message)
		}
	}

	encoded, err := json.Marshal(map[string]any{"fields": fields})

	if err != nil {
		panic(err)
	}

	return encoded
}

// TestHelpBoundaryNewCannotCreateDestination reproduces a global help request
// followed by --. Restoring help must not turn it into a destination operand
// that creates a project, even when a valid module flag permits provisioning.
func TestHelpBoundaryNewCannotCreateDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ARKOREN_MCP_URL='unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"new", "--module", "example.com/demo", "--help", "--"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, Dir: dir,
		Env: func(string) (string, bool) {
			t.Error("local help consulted the environment")

			return "", false
		},
	})

	if _, err := os.Stat(filepath.Join(dir, "--help")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help created its own destination: stat error = %v; stdout = %s", err, &stdout)
	}

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stdout.String(), "NAME:", "USAGE:", "arkoren new", "--module")
}

// TestHelpBoundaryNewIgnoresLiteralDestination keeps a genuine help request
// read-only even when -- is followed by a valid provisioning destination.
// Invalid endpoint settings must remain irrelevant to the local help page.
func TestHelpBoundaryNewIgnoresLiteralDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{
		"new", "--module=example.com/demo", "--url=invalid", "--env-file=missing",
		"--help", "--", "demo",
	}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, Dir: dir,
		Env: func(string) (string, bool) {
			t.Error("local help consulted the environment")

			return "", false
		},
	})

	if _, err := os.Stat(filepath.Join(dir, "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help created the destination: stat error = %v", err)
	}

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stdout.String(), "NAME:", "arkoren new", "--module")
}

// TestHelpBoundaryNewCannotConsumeHelpAsFlagValue prevents a partial repair
// that merely moves help before --. A pending string flag could consume that
// restored token and still execute project creation instead of showing help.
func TestHelpBoundaryNewCannotConsumeHelpAsFlagValue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{
		"new", "demo", "--module=example.com/demo", "--skills-dir", "--help", "--",
	}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, Dir: dir,
		Env: func(string) (string, bool) {
			t.Error("local help consulted the environment")

			return "", false
		},
	})

	if _, err := os.Stat(filepath.Join(dir, "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help created the destination: stat error = %v", err)
	}

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stdout.String(), "NAME:", "arkoren new", "--skills-dir")
}

// TestHelpBoundaryNestedFamilyKeepsTemplate verifies that discarding the
// operand boundary still selects the nested certificate family. The parent's
// renderer must retain its terminal headings rather than fall back to root help.
func TestHelpBoundaryNestedFamilyKeepsTemplate(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"certificate", "root", "--help", "--"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, Dir: t.TempDir(), IsTerminal: true,
		Env: func(name string) (string, bool) {
			if name != "NO_COLOR" {
				t.Errorf("local help consulted %s", name)
			}

			return "", false
		},
	})

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stdout.String(), "arkoren certificate root", "\x1b[1mCOMMANDS:\x1b[0m", "generate", "trust", "untrust")
}

// TestHelpBoundaryLeafNeverWritesKey keeps provisioning-leaf help offline and
// read-only with otherwise complete generation arguments. Only the selected
// leaf's options belong in the rendered page, even with a trailing --.
func TestHelpBoundaryLeafNeverWritesKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{
		"key", "generate", "signing", "--out", "private.pem", "-h", "--",
	}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, Dir: dir,
		Env: func(string) (string, bool) {
			t.Error("local help consulted the environment")

			return "", false
		},
	})

	if _, err := os.Stat(filepath.Join(dir, "private.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help generated a private key: stat error = %v", err)
	}

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stdout.String(), "NAME:", "arkoren key generate", "--out", "mode 0600")
}

// TestHelpBoundaryRootStaysOffline verifies that the boundary alone does not
// redirect global root help into command dispatch or endpoint resolution.
// The generated root page must retain its command and global-option sections.
func TestHelpBoundaryRootStaysOffline(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"--help", "--"}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, Dir: t.TempDir(),
		Env: func(string) (string, bool) {
			t.Error("root help consulted the environment")

			return "", false
		},
	})

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	contains(t, stdout.String(), "NAME:", "USAGE:", "COMMANDS:", "GLOBAL OPTIONS:")
}

// TestHelpBoundaryLiteralHelpRemainsDestination distinguishes an operand
// named --help from parsed global help. With no help flag before --, explicit
// offline creation must still write the requested destination normally.
func TestHelpBoundaryLiteralHelpRemainsDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{
		"new", "--module=example.com/demo", "--no-agent-files", "--", "--help",
	}, console.Environment{
		Stdout: &stdout, Stderr: &stderr, Dir: dir,
		Env: func(string) (string, bool) {
			t.Error("offline creation consulted the environment")

			return "", false
		},
	})

	if code != console.ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d; stdout = %s; stderr = %s", code, &stdout, &stderr)
	}

	data, err := os.ReadFile(filepath.Join(dir, "--help", "go.mod"))

	if err != nil {
		t.Fatal(err)
	}

	contains(t, string(data), "module example.com/demo")
	contains(t, stdout.String(), `"created": true`)
}

// TestInputNestedObjectFlag checks dotted paths into an optional object.
// Sibling flags must contribute to the same nested JSON value.
func TestInputNestedObjectFlag(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-address.city", "Paris", "-address.zip", "75001")

	want(t, got["address"], `{"city":"Paris","zip":"75001"}`)
}

// TestInputMapEntryFlag checks dynamic map entries in path flags.
// Separate keys must retain their original text and coexist in one map.
func TestInputMapEntryFlag(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-labels.env", "prod", "-labels.team", "pay")

	want(t, got["labels"], `{"env":"prod","team":"pay"}`)
}

// TestInputArrayElementFlags checks indexed object construction.
// Out-of-order flags must still produce a dense array in index order.
func TestInputArrayElementFlags(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-items.1.sku", "B", "-items.0.sku", "A", "-items.0.qty", "2")

	want(t, got["items"], `[{"sku":"A","qty":2},{"sku":"B"}]`)
}

// TestInputRepeatedFlagBuildsArrayOfScalars checks scalar-array accumulation.
// Repeated flags append values in command-line order.
func TestInputRepeatedFlagBuildsArrayOfScalars(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-tags", "a", "-tags", "b")

	want(t, got["tags"], `["a","b"]`)
}

// TestInputJSONArrayForArrayOfScalars checks explicit array input.
// JSON array text is decoded as a whole instead of becoming one string item.
func TestInputJSONArrayForArrayOfScalars(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-tags", `["x","y"]`)

	want(t, got["tags"], `["x","y"]`)
}

// TestInputJSONSubtreeFlag checks structured values supplied to parent flags.
// Both object and array subtrees must retain their JSON shapes.
func TestInputJSONSubtreeFlag(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-items", `[{"sku":"A"}]`, "-address", `{"city":"Rome"}`)

	want(t, got["items"], `[{"sku":"A"}]`)
	want(t, got["address"], `{"city":"Rome"}`)
}

// TestInputLastFlagWins checks command-line override order.
// Child edits preserve unrelated siblings while repeated scalar flags replace.
func TestInputLastFlagWins(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-address", `{"city":"Rome","zip":"1"}`, "-address.city", "Oslo", "-currency", "usd", "-currency", "eur")

	want(t, got["address"], `{"city":"Oslo","zip":"1"}`)
	want(t, got["currency"], `"eur"`)
}

// TestInputInlineJSONBase checks inline base-object input.
// The server applies defaults without discarding supplied nested properties.
func TestInputInlineJSONBase(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	got := rg.run("billing:record-payment", "-input", `{"invoice":"I","cents":7,"labels":{"a":"b"}}`).expectOK(t).json(t)

	want(t, got, `{"invoice":"I","cents":7,"currency":"eur","labels":{"a":"b"}}`)
}

// TestInputFlagOverridesBase checks precedence over the base JSON object.
// Later flags may replace existing values or introduce new nested entries.
func TestInputFlagOverridesBase(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	got := rg.run("billing:record-payment", "-input", `{"invoice":"I","cents":7}`, "-cents", "9", "-labels.k", "v").expectOK(t).json(t)

	want(t, got["cents"], `9`)
	want(t, got["labels"], `{"k":"v"}`)
}

// TestInputFileBase checks working-directory-relative @file input.
// The file's object must become the request base before server validation.
func TestInputFileBase(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	if err := os.WriteFile(filepath.Join(rg.dir, "order.json"), []byte(`{"invoice":"F","cents":3}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := rg.run("billing:record-payment", "-input", "@order.json").expectOK(t).json(t)

	want(t, got["invoice"], `"F"`)
}

// TestInputMissingFileIsUsage checks file-input failure classification.
// A missing local source must fail as usage rather than a remote operation error.
func TestInputMissingFileIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "-input", "@nope.json").expectFail(t, 2, "-input")
}

// TestInputStdinBase checks the special -input - source.
// The supplied reader is consumed as JSON rather than a filename or literal.
func TestInputStdinBase(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	got := rg.runWith(t.Context(), runOptions{
		env:   rg.env,
		stdin: `{"invoice":"S","cents":4}`,
	}, "billing:record-payment", "-input", "-").expectOK(t).json(t)

	want(t, got["invoice"], `"S"`)
}

// TestInputBaseMustBeAnObject checks local base-input shape validation.
// Arrays and malformed JSON must be rejected before server invocation.
func TestInputBaseMustBeAnObject(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "-input", `[1]`).expectFail(t, 2, "JSON object")
	newRig(t).run("billing:record-payment", "-input", `{`).expectFail(t, 2, "not valid JSON")
}

// TestInputPositionalArguments checks manifest-declared positional properties.
// Arguments must map to invoice and amount in the declared order.
func TestInputPositionalArguments(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	got := rg.run("billing:record-payment", "INV-9", "700").expectOK(t).json(t)

	want(t, got["invoice"], `"INV-9"`)
	want(t, got["cents"], `700`)
}

// TestInputTooManyPositionalsIsUsage checks the positional argument bound.
// The diagnostic lists the properties that can accept positional values.
func TestInputTooManyPositionalsIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "a", "1", "extra").expectFail(t, 2, "too many arguments", "takes 2", "--help for argument order")
}

// TestInputDoubleDashEndsFlags checks the explicit flag terminator.
// A following dash-prefixed value must be treated as positional text.
func TestInputDoubleDashEndsFlags(t *testing.T) {
	t.Parallel()

	got := newRig(t).run("billing:record-payment", "-cents", "3", "--", "-weird").expectOK(t).json(t)

	want(t, got["invoice"], `"-weird"`)
}

// TestInputNegativeNumberIsAnArgument checks numeric positional recognition.
// Negative values reach server validation instead of becoming unknown flags.
func TestInputNegativeNumberIsAnArgument(t *testing.T) {
	t.Parallel()

	// The server refuses the amount, but the CLI must send it as the argument.
	newRig(t).run("billing:record-payment", "INV", "-5").expectFail(t, 1, "<cents>")
}

// TestInputEqualsAndDoubleDashSpellings checks equivalent path-flag syntax.
// Single dashes, double dashes, and inline values must build the same shapes.
func TestInputEqualsAndDoubleDashSpellings(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-address.city=Paris", "--labels.a", "b", "--tags=t")

	want(t, got["address"], `{"city":"Paris"}`)
	want(t, got["labels"], `{"a":"b"}`)
	want(t, got["tags"], `["t"]`)
}

// TestInputBooleanFlags checks bare, inline, and separate boolean values.
// False remains false even when the server omits its zero-valued property.
func TestInputBooleanFlags(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	want(t, rg.record("-express")["express"], `true`)
	want(t, rg.record("-express=true")["express"], `true`)
	want(t, rg.record("-express", "true")["express"], `true`)

	if _, present := rg.record("-express=false")["express"]; present {
		t.Fatal("express=false should be omitted by the server's omitzero")
	}

	if _, present := rg.record("-express", "false")["express"]; present {
		t.Fatal("-express false should be false")
	}
}

// TestInputNumberConversion checks floating-point flag conversion.
// A numeric schema property must receive a JSON number rather than a string.
func TestInputNumberConversion(t *testing.T) {
	t.Parallel()

	got := newRig(t).record("-rate", "1.5")

	want(t, got["rate"], `1.5`)
}

// TestInputBadNumberIsUsage checks invalid scalar text at the CLI boundary.
// Conversion failures must identify the argument or flag that supplied them.
func TestInputBadNumberIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "x").expectFail(t, 2, "<cents>", `"x" is not an integer`)
	newRig(t).run("billing:record-payment", "INV", "5", "-rate", "fast").expectFail(t, 2, "-rate", "is not a number")
	newRig(t).run("billing:record-payment", "INV", "5", "-express=maybe").expectFail(t, 2, "is not true or false")
}

// TestInputBadJSONForObjectIsUsage checks structured flag-value requirements.
// Plain strings cannot stand in for schema-declared objects or object arrays.
func TestInputBadJSONForObjectIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "5", "-address", "Paris").expectFail(t, 2, "-address", "expects a JSON object")
	newRig(t).run("billing:record-payment", "INV", "5", "-items", "x").expectFail(t, 2, "-items", "expects a JSON array")
}

// TestInputSparseArrayIndexIsUsage checks dense-array construction.
// Leaving an earlier index unset must produce a local missing-index diagnostic.
func TestInputSparseArrayIndexIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "5", "-items.1.sku", "B").expectFail(t, 2, `array "items" is missing index 0`)
}

// TestInputUnknownFlagSuggestsNames checks nearby schema-path suggestions.
// A misspelled nested property should suggest the intended dotted flag.
func TestInputUnknownFlagSuggestsNames(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "5", "-adress.city", "x").expectFail(t, 2, "unknown flag -adress.city", "did you mean -address.city")
}

// TestInputUnknownFlagWithoutNearNameListsProperties checks fallback guidance.
// An unrelated flag name should list available top-level schema properties.
func TestInputUnknownFlagWithoutNearNameListsProperties(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "5", "-zzzzzzzzzz", "x").expectFail(t, 2, "arkoren billing:record-payment --help")
}

// TestInputFlagNeedsAValue checks missing nonboolean flag arguments.
// The local diagnostic names the flag before any request is sent.
func TestInputFlagNeedsAValue(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "5", "-rate").expectFail(t, 2, "-rate needs a value")
}

// TestInputGlobalNameWinsOverProperty checks reserved global names.
// A colliding input property remains settable through base JSON only.
func TestInputGlobalNameWinsOverProperty(t *testing.T) {
	t.Parallel()

	// -token is the global flag, so it is not sent as the "token" property.
	rg := newRig(t)

	got := rg.record("-token", "secret")

	if _, present := got["token"]; present {
		t.Fatalf("token property was set: %v", got)
	}

	// -input is the way to set it.
	got = rg.run("billing:record-payment", "-input", `{"invoice":"I","cents":1,"token":"in"}`).expectOK(t).json(t)

	want(t, got["token"], `"in"`)
}

// TestInputMapKeyWithDotNeedsInput checks keys that cannot use path syntax.
// Base JSON preserves literal dots instead of treating them as separators.
func TestInputMapKeyWithDotNeedsInput(t *testing.T) {
	t.Parallel()

	got := newRig(t).run("billing:record-payment", "-input", `{"invoice":"I","cents":1,"labels":{"a.b":"c"}}`).expectOK(t).json(t)

	want(t, got["labels"], `{"a.b":"c"}`)
}

// TestInputConflictWithBaseIsUsage checks incompatible intermediate values.
// A child flag cannot descend through a scalar supplied by the base object.
func TestInputConflictWithBaseIsUsage(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "-input", `{"invoice":"I","cents":1,"address":"flat"}`, "-address.city", "x").expectFail(t, 2, "-address.city", "conflicts")
}

// TestInputHugeIndexIsRefused checks the array-allocation guard.
// A likely index typo must fail locally rather than allocate a huge sparse list.
func TestInputHugeIndexIsRefused(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing:record-payment", "INV", "5", "-items.999999.sku", "x").expectFail(t, 2, "too large")
}

// TestRemoteInputCancellationDoesNotOwnStdin uses a real blocked pipe to check
// command cancellation without wall-clock waits. The CLI must return before the
// input owner releases EOF, while leaving that caller-owned pipe open.
func TestRemoteInputCancellationDoesNotOwnStdin(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		input, writer := io.Pipe()
		t.Cleanup(func() { _ = input.Close() })
		t.Cleanup(func() { _ = writer.Close() })

		env := scaffoldEnvironment(t)
		env.Stdin = input
		env.Env = func(string) (string, bool) {
			return "", false
		}
		done := make(chan struct{})

		go func() {
			run(ctx, []string{"--url", "http://127.0.0.1:1", "echo", "--input", "-"}, env)
			close(done)
		}()

		synctest.Wait()
		cancel()
		synctest.Wait()

		returned := false

		select {
		case <-done:
			returned = true
		default:
		}

		_, err := writer.Write([]byte(`{}`))
		_ = writer.Close()
		<-done

		if !returned {
			t.Fatal("cancellation waited for caller-owned stdin EOF")
		}

		if err != nil {
			t.Fatalf("CLI closed caller-owned stdin: %v", err)
		}
	})
}

// TestOnlyEffectiveInputSourceIsRead checks ordinary repeated-flag precedence
// before discovery. An overridden file source must not fail or perform I/O when
// the final input flag supplies the complete request object.
func TestOnlyEffectiveInputSourceIsRead(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
			}}, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "echo", "--compact",
		"--input", "@does-not-exist.json", "--input", `{"value":42}`)

	if output.code != 0 || !strings.Contains(output.out, `"value":42`) {
		t.Fatalf("superseded input was read: %+v", output)
	}
}

// externalServer registers one raw SDK tool without assuming any Arkoren
// conventions. Native fixtures can return scalar/text/multimodal MCP results.
func externalServer(tool *sdkmcp.Tool, handler sdkmcp.ToolHandler) *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "external", Version: "1"}, nil)
	server.AddTool(tool, handler)

	return server
}

// TestNativeToolUsesExactNameAndRawInput exercises unsupported schema forms.
// Optional CLI metadata must not be a prerequisite for tools/call invocation.
func TestNativeToolUsesExactNameAndRawInput(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "vendor.echo", InputSchema: map[string]any{
		"type": "object", "allOf": []any{map[string]any{"required": []string{"value"}}},
	}}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var value map[string]any

		if err := json.Unmarshal(request.Params.Arguments, &value); err != nil {
			return nil, err
		}

		return &sdkmcp.CallToolResult{StructuredContent: value}, nil
	})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "vendor.echo", "-input", `{"value":{"nested":42}}`, "-compact")

	if result.code != 0 || result.out != "{\"value\":{\"nested\":42}}\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestNativeToolDoesNotInventPositionalsOrFollow protects absent CLI hints.
// Familiar schema properties do not opt an arbitrary tool into conventions.
func TestNativeToolDoesNotInventPositionalsOrFollow(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "external", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{}, nil
		})
	url := nativeServerURL(t, server)

	if result := runConnection(t, t.Context(), url, "", "external", "position"); result.code != 2 {
		t.Fatalf("result = %+v", result)
	}

	if result := runConnection(t, t.Context(), url, "", "external", "-f"); result.code != 2 {
		t.Fatalf("result = %+v", result)
	}
}

// TestNativeJSONTextIsNotDoubleEncoded verifies fallback for legacy tools that
// return content text containing JSON instead of structuredContent.
func TestNativeJSONTextIsNotDoubleEncoded(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "legacy", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"answer":42}`}}}, nil
		})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "legacy", "-compact")

	if result.code != 0 || result.out != "{\"answer\":42}\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestNativePlainTextRemainsAJSONString preserves non-JSON text losslessly.
// It is JSON encoded exactly once so stdout remains machine-readable.
func TestNativePlainTextRemainsAJSONString(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "plain", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello world"}}}, nil
		})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "plain")

	if result.code != 0 || result.out != "\"hello world\"\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestNativeToolFailureIsNotAuthentication distinguishes execution errors from
// actual HTTP 401, even when an external tool labels its own failure unauthorized.
func TestNativeToolFailureIsNotAuthentication(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "denied", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "resource denied"}},
				Meta: sdkmcp.Meta{"arkoren.dev/failure": map[string]any{"code": "unauthorized", "message": "resource denied"}}}, nil
		})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "denied")

	if result.code != 1 || result.out != "" || !strings.Contains(result.err, "resource denied") {
		t.Fatalf("result = %+v", result)
	}
}

// TestNativeProtocolErrorIsNotAuthentication preserves SDK protocol failures
// without assigning HTTP status from a JSON-RPC code or message.
func TestNativeProtocolErrorIsNotAuthentication(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "broken", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return nil, &jsonrpc.Error{Code: -32602, Message: "bad external arguments"}
		})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "broken")

	if result.code != 1 || !strings.Contains(result.err, "bad external arguments") {
		t.Fatalf("result = %+v", result)
	}
}

// TestRedirectDoesNotForwardBearer protects cross-origin redirect targets: the
// CLI never follows a redirect with the configured destination's credentials.
func TestRedirectDoesNotForwardBearer(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(target.Close)

	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	result := runConnection(t, t.Context(), redirect.URL, "", "external", "-token", "secret")

	if result.code != 1 || requests.Load() != 0 || strings.Contains(result.out+result.err, "secret") {
		t.Fatalf("result = %+v, requests = %d", result, requests.Load())
	}
}

// TestHealthRoleUsesAdvertisedOperationNames protects owners whose semantic role
// differs from their CLI or native spelling. Health interpretation follows the
// explicit role, not a hard-coded group, operation, or MCP identifier.
func TestHealthRoleUsesAdvertisedOperationNames(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "vendor.probe", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "diagnostics", "operation": "probe", "role": "health"}}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"status": "failing"}}, nil
		})
	url := nativeServerURL(t, server)

	result := runConnection(t, t.Context(), url, "", "diagnostics:probe")

	if result.code != 1 || result.err != "" || !strings.Contains(result.out, "failing") {
		t.Fatalf("result = %+v", result)
	}

	if result := runConnection(t, t.Context(), url, "", "tools"); !strings.Contains(result.out, `"command": "diagnostics:probe"`) {
		t.Fatalf("help = %+v", result)
	}

	if result := runConnection(t, t.Context(), url, "", "health"); result.code != 2 || !strings.Contains(errorDocument(t, result.err)["message"].(string), `unknown command "health"`) {
		t.Fatalf("former shortcut = %+v", result)
	}
}

// TestNativeHealthNameDoesNotGrantRole ensures absent metadata is not guessed.
// An external tool named health is directly callable but has ordinary MCP output.
func TestNativeHealthNameDoesNotGrantRole(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "health", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"status": "failing"}}, nil
		})

	if result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "health"); result.code != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestFormerShortcutsRemainExactNativeNames protects external MCP servers.
// Removing built-in shortcuts must not reserve or reject their native spellings.
func TestFormerShortcutsRemainExactNativeNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"routes", "traces", "trace", "logs", "metrics", "health"} {
		server := externalServer(&sdkmcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"called": true}}, nil
			})

		result := runConnection(t, t.Context(), nativeServerURL(t, server), "", name)

		if result.code != 0 || !strings.Contains(result.out, `"called": true`) {
			t.Fatalf("native %s = %+v", name, result)
		}
	}
}

// TestUnknownShortcutNeverInvokesRoleTool protects actions from fallback calls.
// A matching metadata role cannot authorize dispatch after name resolution fails.
func TestUnknownShortcutNeverInvokesRoleTool(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := externalServer(&sdkmcp.Tool{Name: "vendor.probe", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "diagnostics", "operation": "probe", "role": "health"}}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			calls.Add(1)

			return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"status": "ok"}}, nil
		})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "health")

	if result.code != 2 || !strings.Contains(errorDocument(t, result.err)["message"].(string), `unknown command "health"`) || result.out != "" {
		t.Fatalf("result = %+v", result)
	}

	if calls.Load() != 0 {
		t.Fatal("unknown shortcut invoked the role-bearing tool")
	}
}

// TestNativeToolFailureWithoutMetadataKeepsText checks SDK-native execution
// failures without Arkoren metadata. Client GetError is intentionally not used:
// SDK GetError contains server-local state and is always nil on clients.
func TestNativeToolFailureWithoutMetadataKeepsText(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "fails", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "action rejected"}}}, nil
		})

	result := runConnection(t, t.Context(), nativeServerURL(t, server), "", "fails")

	if result.code != 1 || result.out != "" || !strings.Contains(result.err, "action rejected") {
		t.Fatalf("result = %+v", result)
	}
}

// TestMalformedCLIHintFallsBackToNativeInvocation treats CLI metadata as optional
// presentation only. An incomplete role hint cannot grant roles or positionals.
func TestMalformedCLIHintFallsBackToNativeInvocation(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "fallback", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"role": "health", "positional": []string{"value"}}}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
		})
	url := nativeServerURL(t, server)

	if result := runConnection(t, t.Context(), url, "", "fallback", "-input", `{"value":1}`); result.code != 0 {
		t.Fatalf("result = %+v", result)
	}

	if result := runConnection(t, t.Context(), url, "", "health"); result.code != 2 {
		t.Fatalf("former shortcut = %+v", result)
	}

	if result := runConnection(t, t.Context(), url, "", "fallback", "position"); result.code != 2 {
		t.Fatalf("positional = %+v", result)
	}
}

// TestNativeToolCatalogReadsEverySDKPage protects discovery beyond the first
// page. Pagination cursors belong to the SDK rather than a custom manifest.
func TestNativeToolCatalogReadsEverySDKPage(t *testing.T) {
	t.Parallel()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "paged", Version: "1"}, &sdkmcp.ServerOptions{PageSize: 1})

	for _, name := range []string{"first", "second", "third"} {
		server.AddTool(&sdkmcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				return &sdkmcp.CallToolResult{StructuredContent: map[string]string{"name": name}}, nil
			})
	}

	url := nativeServerURL(t, server)

	result := runConnection(t, t.Context(), url, "", "third", "-compact")

	if result.code != 0 || result.out != "{\"name\":\"third\"}\n" {
		t.Fatalf("result = %+v", result)
	}

	result = runConnection(t, t.Context(), url, "", "tools", "--json")

	var document struct {
		Tools []any `json:"tools"`
	}

	if err := json.Unmarshal([]byte(result.out), &document); err != nil {
		t.Fatal(err)
	}

	if len(document.Tools) != 3 {
		t.Fatalf("catalog = %+v", document)
	}
}

// TestToolsSummarizesNativeOnlyTools keeps exact callable names without
// leaking schemas or inventing read/follow capabilities from their spelling.
func TestToolsSummarizesNativeOnlyTools(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{
		Name: "vendor.health", Description: "Vendor diagnostics",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "tools")

	var document remote.ToolsReport

	if err := json.Unmarshal([]byte(output.out), &document); err != nil {
		t.Fatal(err)
	}

	if output.code != console.ExitOK || len(document.Tools) != 1 || document.Inspector.App != nil {
		t.Fatalf("catalog = %+v; output = %+v", document, output)
	}

	tool := document.Tools[0]

	if tool.Command != "vendor.health" || tool.NativeName != "" || tool.Kind != "action" || tool.Follow {
		t.Fatalf("tool = %+v", tool)
	}
}

// TestToolSummariesAvoidShadowedAliases prevents discovery from recommending
// a metadata alias that exact-name resolution would send to a different tool.
func TestToolSummariesAvoidShadowedAliases(t *testing.T) {
	t.Parallel()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "external", Version: "1"}, nil)

	for _, tool := range []*sdkmcp.Tool{
		{Name: "native.echo", InputSchema: map[string]any{"type": "object"},
			Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "echo"}}},
		{Name: "vendor:echo", InputSchema: map[string]any{"type": "object"}},
	} {
		server.AddTool(tool, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
		})
	}

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "tools")

	var document remote.ToolsReport

	if err := json.Unmarshal([]byte(output.out), &document); err != nil {
		t.Fatal(err)
	}

	if output.code != console.ExitOK || len(document.Tools) != 2 || document.Tools[0].Command != "native.echo" || document.Tools[1].Command != "vendor:echo" {
		t.Fatalf("catalog = %+v; output = %+v", document, output)
	}
}

// TestStatusHealthFailuresUseDiagnosticObjects preserves status's non-gating
// behavior while using the same error shape as stderr and discovery failures.
func TestStatusHealthFailuresUseDiagnosticObjects(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{
		Name: "vendor.health", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "health", "role": "health"}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "maintenance"}}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "status")

	var document struct {
		Health struct {
			Error console.ErrorReport `json:"error"`
		} `json:"health"`
	}

	if err := json.Unmarshal([]byte(output.out), &document); err != nil {
		t.Fatal(err)
	}

	if output.code != console.ExitOK || output.err != "" || document.Health.Error.Message != "maintenance" {
		t.Fatalf("status = %+v; output = %+v", document, output)
	}
}

// TestLegacyNativeSSEResponseUsesSDK verifies legacy streamable HTTP responses
// without any CLI event parser. Cursor follow is separate from the native MCP
// transport's SSE encoding, which remains entirely the SDK's responsibility.
func TestLegacyNativeSSEResponseUsesSDK(t *testing.T) {
	t.Parallel()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "legacy-sse", Version: "1"},
		&sdkmcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	server.AddTool(&sdkmcp.Tool{Name: "legacy.echo", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"ok": true}}, nil
		})
	host := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, &sdkmcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(host.Close)

	result := runConnection(t, t.Context(), host.URL, "", "legacy.echo", "-compact")

	if result.code != 0 || result.out != "{\"ok\":true}\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestRawInputNumbersReachMCPExactly protects values beyond binary floating
// point precision. The server echoes its actual protocol arguments as JSON text,
// so neither the fixture nor the assertion decodes numbers into float64.
func TestRawInputNumbersReachMCPExactly(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "exact", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
			}}, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "exact", "--compact", "--input",
		`{"integer":9007199254740993,"decimal":0.12345678901234567890123456789}`)

	if output.code != 0 || !strings.Contains(output.out, `"integer":9007199254740993`) ||
		!strings.Contains(output.out, `"decimal":0.12345678901234567890123456789`) {
		t.Fatalf("numeric round trip = %+v", output)
	}
}

// TestNumericFlagsReachMCPExactly checks schema-directed numeric conversion
// without imposing local machine-integer or floating-point limits. Validation
// remains the server's responsibility after the exact value reaches it.
func TestNumericFlagsReachMCPExactly(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "exact", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			"integer": map[string]any{"type": "integer"},
			"decimal": map[string]any{"type": "number"},
		},
	}}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
		}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "exact", "--compact",
		"--integer", "18446744073709551616", "--decimal", "0.12345678901234567890123456789")

	if output.code != 0 || !strings.Contains(output.out, `"integer":18446744073709551616`) ||
		!strings.Contains(output.out, `"decimal":0.12345678901234567890123456789`) {
		t.Fatalf("numeric flags = %+v", output)
	}
}

// TestWaitBudgetPreservesUnrelatedNumbers checks that rewriting the remaining
// timeout never rounds another property of the same request. The waiting-read
// convention changes only timeout; the server sees every other value verbatim.
func TestWaitBudgetPreservesUnrelatedNumbers(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "exact", Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"wait": map[string]any{"type": "boolean"}, "timeout": map[string]any{"type": "string"},
		}},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
		}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "exact", "--compact", "--input",
		`{"wait":true,"timeout":"1s","integer":9007199254740993}`)

	if output.code != 0 || !strings.Contains(output.out, `"integer":9007199254740993`) {
		t.Fatalf("wait numeric input = %+v", output)
	}
}

// TestCursorFollowPreservesNumericFilters checks the first real cursor poll.
// Adding cursor and wait must not convert a large numeric filter through float64;
// cancellation after the observed request terminates follow without a sleep.
func TestCursorFollowPreservesNumericFilters(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	observed := make(chan string, 1)
	server := externalServer(&sdkmcp.Tool{Name: "exact", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "events", "cursor": true}},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		observed <- string(request.Params.Arguments)
		cancel()

		return &sdkmcp.CallToolResult{}, nil
	})

	output := runConnection(t, ctx, nativeServerURL(t, server), "", "exact", "--follow", "--input",
		`{"integer":9007199254740993}`)

	if output.code != 0 {
		t.Fatalf("follow = %+v", output)
	}

	select {
	case arguments := <-observed:
		if !strings.Contains(arguments, `"integer":9007199254740993`) {
			t.Fatalf("cursor request rounded filter: %s", arguments)
		}
	default:
		t.Fatal("follow made no cursor request")
	}
}

// TestWaitDoesNotReplaceAnUnrelatedTimeoutType protects external tool schemas.
// Boolean wait alone does not declare a duration-valued timeout convention; an
// integer timeout belongs to the tool and must arrive with its original type.
func TestWaitDoesNotReplaceAnUnrelatedTimeoutType(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "exact", Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"wait": map[string]any{"type": "boolean"}, "timeout": map[string]any{"type": "integer"},
		}},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
		}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "exact", "--compact", "--input",
		`{"wait":true,"timeout":42}`)

	if output.code != 0 || !strings.Contains(output.out, `"timeout":42`) {
		t.Fatalf("non-duration timeout = %+v", output)
	}
}

// TestWaitDoesNotRepairInvalidTimeoutInput preserves server-side validation.
// A numeric value for a declared string timeout is invalid input, not permission
// for the CLI to silently replace it with a valid remaining-budget string.
func TestWaitDoesNotRepairInvalidTimeoutInput(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "exact", Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"wait": map[string]any{"type": "boolean"}, "timeout": map[string]any{"type": "string"},
		}},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
		}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "exact", "--compact", "--input",
		`{"wait":true,"timeout":42}`)

	if output.code != 0 || !strings.Contains(output.out, `"timeout":42`) {
		t.Fatalf("invalid timeout was repaired: %+v", output)
	}
}

// TestCursorFollowDoesNotUseConditionWaitTimeout keeps cursor polling separate
// from condition waiting. An unrelated timeout property cannot create a total
// follow deadline or be interpreted as a duration before tool discovery.
func TestCursorFollowDoesNotUseConditionWaitTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	called := make(chan struct{}, 1)
	server := externalServer(&sdkmcp.Tool{Name: "events", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "vendor", "operation": "events", "cursor": true}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		called <- struct{}{}
		cancel()

		return &sdkmcp.CallToolResult{}, nil
	})

	output := runConnection(t, ctx, nativeServerURL(t, server), "", "events", "--follow", "--input",
		`{"wait":true,"timeout":"not-a-condition-duration"}`)

	if output.code != 0 {
		t.Fatalf("cursor input became a condition wait: %+v", output)
	}

	select {
	case <-called:
	default:
		t.Fatal("follow made no cursor request")
	}
}

// TestStructuredMCPNumbersReachStdoutExactly checks the generic SDK result
// boundary independently of server-side decoding. The raw structured value is
// deliberately already encoded, so any rounding occurs in the CLI client.
func TestStructuredMCPNumbersReachStdoutExactly(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "exact", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{StructuredContent: jsontext.Value(
				`{"integer":9007199254740993,"decimal":0.12345678901234567890123456789}`)}, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "exact", "--compact")

	if output.code != 0 || !strings.Contains(output.out, `"integer":9007199254740993`) ||
		!strings.Contains(output.out, `"decimal":0.12345678901234567890123456789`) {
		t.Fatalf("structured numeric result = %+v", output)
	}
}

// TestCatalogPreservesSchemaAndExampleNumbers checks tools/list independently
// of tools/call. The server publishes raw schemas and optional CLI metadata;
// verbose discovery must not round constraints, defaults, or example inputs.
func TestCatalogPreservesSchemaAndExampleNumbers(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "exact",
		InputSchema:  jsontext.Value(`{"type":"object","properties":{"integer":{"type":"integer","maximum":9007199254740993}}}`),
		OutputSchema: jsontext.Value(`{"type":"object","properties":{"decimal":{"type":"number","default":0.12345678901234567890123456789}}}`),
		Meta: sdkmcp.Meta{"arkoren.dev/cli": jsontext.Value(
			`{"group":"vendor","operation":"exact","examples":[{"description":"Exact","input":{"integer":9007199254740993}}]}`)},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "tools", "--verbose", "--compact")

	if output.code != 0 || !strings.Contains(output.out, `"maximum":9007199254740993`) ||
		!strings.Contains(output.out, `"default":0.12345678901234567890123456789`) {
		t.Fatalf("catalog schema numbers = %+v", output)
	}

	if !strings.Contains(output.out, `"input":{"integer":9007199254740993}`) {
		t.Fatalf("catalog example number = %+v", output)
	}
}

// TestFailureDetailsPreserveExactNumbers checks the error path through the
// same generic SDK boundary. Safe metadata remains a failure on stderr, with
// no stdout document and no rounding of the server's diagnostic result.
func TestFailureDetailsPreserveExactNumbers(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "fails", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{IsError: true,
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "condition failed"}},
				Meta: sdkmcp.Meta{"arkoren.dev/failure": jsontext.Value(
					`{"code":"timeout","message":"condition failed","details":{"result":{"integer":9007199254740993,"decimal":0.12345678901234567890123456789}}}`)},
			}, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "fails", "--verbose", "--compact")

	if output.code != 1 || output.out != "" {
		t.Fatalf("failure became success: %+v", output)
	}

	if !strings.Contains(output.err, `"integer":9007199254740993`) ||
		!strings.Contains(output.err, `"decimal":0.12345678901234567890123456789`) {
		t.Fatalf("failure detail numbers = %+v", output)
	}
}

// TestWaitPrepassDoesNotDecodeUnrelatedDomainNumbers verifies that convention
// detection ignores arbitrary properties. A valid JSON exponent need not fit a
// float64 merely because the CLI checks whether another field requests waiting.
// The legacy protocol avoids the pinned SDK's separate 2026 HTTP-header decoder
// limit, allowing this test to isolate the CLI's own convention detection.
func TestWaitPrepassDoesNotDecodeUnrelatedDomainNumbers(t *testing.T) {
	t.Parallel()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "exact", Version: "1"},
		&sdkmcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	server.AddTool(&sdkmcp.Tool{Name: "exact", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
			}}, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "exact", "--compact", "--input",
		`{"number":1e10000}`)

	if output.code != 0 || !strings.Contains(output.out, `"number":1e10000`) {
		t.Fatalf("convention detection decoded a domain value: %+v", output)
	}
}

// TestToolsReturnsFocusedCatalog rejects CLI manuals and native schema dumps
// from the ordinary discovery result.
func TestToolsReturnsFocusedCatalog(t *testing.T) {
	t.Parallel()

	document := newRig(t).run("tools").expectOK(t).json(t)

	for _, key := range []string{"cli", "manifest", "commands", "global_flags", "input", "output", "exit_codes"} {
		if _, ok := document[key]; ok {
			t.Fatalf("catalog contains unrelated %s", key)
		}
	}

	tools, ok := document["tools"].([]any)

	if !ok || len(tools) == 0 {
		t.Fatalf("catalog = %v", document)
	}

	previous := ""
	found := false

	for _, item := range tools {
		tool := item.(map[string]any)
		command := tool["command"].(string)

		if command < previous {
			t.Fatal("catalog is not sorted by command")
		}

		previous = command

		for _, key := range []string{"inputSchema", "outputSchema", "input_schema", "output_schema", "_meta", "annotations"} {
			if _, ok := tool[key]; ok {
				t.Fatalf("tool contains protocol details %s", key)
			}
		}

		if tool["command"] == "billing:payments" {
			if tool["kind"] != "read" || tool["follow"] != true || tool["description"] == "" || tool["native_name"] != "billing.payments" {
				t.Fatalf("tool = %v", tool)
			}

			found = true
		}
	}

	if !found {
		t.Fatal("catalog omitted billing:payments")
	}
}

// TestStatusSummarizesIdentity keeps volatile runtime/build data in the
// explicitly invoked identity tool rather than the connection summary.
func TestStatusSummarizesIdentity(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	document := rig.run("status").expectOK(t).json(t)

	summary := document["app"].(map[string]any)

	if summary["name"] != "shop" || summary["version"] != "1.2.3" {
		t.Fatalf("app = %v", summary)
	}

	for _, key := range []string{"runtime", "build"} {
		if _, ok := summary[key]; ok {
			t.Fatalf("status leaked full identity %s", key)
		}
	}

	// The shared rig deliberately pauses startup. Use a running application to
	// verify uptime instead of waiting on that intentionally blocked fixture.
	app := application.New()
	definition := &fixtureBuilder{}

	if err := app.Start(t.Context(), func(composition *application.Composer) {
		definition.Tools(composition.Must(composition.MCPTools("app"))...)
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	server := httptest.NewServer(definition.handler(t))
	t.Cleanup(server.Close)

	response := runConnection(t, t.Context(), server.URL+"/inspect", "", "app:info")

	identity := (result{code: response.code, stdout: response.out, stderr: response.err}).expectOK(t).json(t)

	if identity["runtime"] == nil || identity["build"] == nil {
		t.Fatalf("explicit identity lost full diagnostics: %v", identity)
	}

	uptime, ok := identity["uptime"].(string)

	if !ok {
		t.Fatalf("explicit identity lost uptime: %v", identity)
	}

	duration, err := time.ParseDuration(uptime)

	if err != nil || identity["uptime_seconds"] != float64(int64(duration/time.Second)) {
		t.Fatalf("inconsistent identity uptime: %v, err=%v", identity, err)
	}
}

// TestLocalReportsHonorCompactNoColor exercises every local JSON-producing
// family through actual command dispatch, without exporting raw artifacts.
func TestLocalReportsHonorCompactNoColor(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.IsTerminal, env.ErrorTerminal = true, true
	env.Env = func(string) (string, bool) {
		return "", false
	}

	for _, args := range [][]string{
		{"new", "preview", "--dry-run", "--no-agent-files"},
		{"skills", "status"},
		{"key", "generate", "shared", "--out", "fixture.key"},
		{"key", "check", "shared", "fixture.key"},
		{"certificate", "root", "generate", "--name", "Fixture", "--cert", "fixture.pem", "--key", "fixture-key.pem"},
		{"certificate", "check", "--cert", "fixture.pem", "--key", "fixture-key.pem"},
		{"tools"},
		{"--version"},
	} {
		env.Stdout.(*bytes.Buffer).Reset()
		env.Stderr.(*bytes.Buffer).Reset()

		code := run(t.Context(), append([]string{"--compact", "--no-color"}, args...), env)

		output := result{code: code, stdout: env.Stdout.(*bytes.Buffer).String(), stderr: env.Stderr.(*bytes.Buffer).String()}
		output.expectOK(t).json(t)

		if strings.Count(output.stdout, "\n") != 1 || output.stderr != "" {
			t.Fatalf("%v: stdout = %q; stderr = %q", args, output.stdout, output.stderr)
		}
	}
}

// TestLocalReportsUseTerminalStyle covers indentation and color across local
// report producers, not merely the shared renderer in isolation.
func TestLocalReportsUseTerminalStyle(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.IsTerminal = true
	env.Env = func(string) (string, bool) {
		return "", false
	}

	for _, args := range [][]string{
		{"new", "preview", "--dry-run", "--no-agent-files"},
		{"skills", "status"},
		{"key", "generate", "shared", "--out", "fixture.key"},
		{"key", "check", "shared", "fixture.key"},
		{"certificate", "root", "generate", "--name", "Fixture", "--cert", "fixture.pem", "--key", "fixture-key.pem"},
		{"certificate", "check", "--cert", "fixture.pem", "--key", "fixture-key.pem"},
		{"tools"},
		{"--version"},
	} {
		env.Stdout.(*bytes.Buffer).Reset()

		code := run(t.Context(), args, env)

		output := env.Stdout.(*bytes.Buffer).String()

		if code != console.ExitOK || !strings.Contains(output, "\x1b[") || !strings.Contains(output, "\n  ") {
			t.Fatalf("%v: exit = %d; stdout = %q; stderr = %s", args, code, output, env.Stderr)
		}
	}
}

// TestRemoteSummaryReportsUseTerminalStyle protects both discovery and status
// after replacing raw protocol documents with tailored reports.
func TestRemoteSummaryReportsUseTerminalStyle(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	for _, command := range []string{"tools", "status"} {
		output := rig.runWith(t.Context(), runOptions{terminal: true}, command).expectOK(t)

		if !strings.Contains(output.stdout, "\x1b[") || !strings.Contains(output.stdout, "\n  ") {
			t.Fatalf("%s: stdout = %q", command, output.stdout)
		}
	}
}

// TestToolsDefaultMatchesJSONCompatibility keeps discovery format independent
// of the old opt-in flag, including when no endpoint is configured.
func TestToolsDefaultMatchesJSONCompatibility(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	options := runOptions{env: map[string]string{}}

	plain := rig.runWith(t.Context(), options, "tools", "--compact").expectOK(t)
	explicit := rig.runWith(t.Context(), options, "tools", "--json", "--compact").expectOK(t)

	if plain.stdout != explicit.stdout {
		t.Fatal("tools and tools --json differ")
	}

	if plain.json(t)["inspector"].(map[string]any)["state"] != remote.StateNotConfigured {
		t.Fatalf("catalog = %s", plain.stdout)
	}
}

// TestProjectCreationPrintsOneJSONResult checks successful local mutation does
// not mix its receipt with progress or completion prose.
func TestProjectCreationPrintsOneJSONResult(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	code := run(t.Context(), []string{"new", "billing", "--no-agent-files", "--compact"}, env)

	output := result{code: code, stdout: env.Stdout.(*bytes.Buffer).String(), stderr: env.Stderr.(*bytes.Buffer).String()}
	document := output.expectOK(t).json(t)

	if document["created"] != true || document["module"] != "billing" || strings.Count(output.stdout, "\n") != 1 {
		t.Fatalf("project result = %s", output.stdout)
	}
}

// TestSkillMutationsPrintOneJSONResult checks both mutations report their final
// state in a single document rather than separate status and plan documents.
func TestSkillMutationsPrintOneJSONResult(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	for _, operation := range []string{"install", "update"} {
		env.Stdout.(*bytes.Buffer).Reset()

		code := run(t.Context(), []string{"skills", operation, "--compact"}, env)

		output := result{code: code, stdout: env.Stdout.(*bytes.Buffer).String(), stderr: env.Stderr.(*bytes.Buffer).String()}
		document := output.expectOK(t).json(t)

		if document["completed"] != true || document["installed_revision"] != document["bundled_revision"] {
			t.Fatalf("skill result = %s", output.stdout)
		}

		if strings.Count(output.stdout, "\n") != 1 || document["modified_file_count"] != float64(0) {
			t.Fatalf("skill result = %s", output.stdout)
		}

		if _, ok := document["changes"]; ok {
			t.Fatal("completed mutation dumped its per-file plan")
		}

		if _, ok := document["written_files"]; !ok {
			t.Fatal("completed mutation omitted file counts")
		}
	}
}

// TestLocalJSONUsesTerminalColor checks local reports share remote formatting.
func TestLocalJSONUsesTerminalColor(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.IsTerminal = true
	env.Env = func(string) (string, bool) {
		return "", false
	}

	if code := run(t.Context(), []string{"--version", "--compact"}, env); code != console.ExitOK {
		t.Fatalf("exit = %d", code)
	}

	output := env.Stdout.(*bytes.Buffer).String()

	if !strings.Contains(output, "\x1b[") || strings.Count(output, "\n") != 1 {
		t.Fatalf("version = %q", output)
	}
}

// TestLocalJSONNoColorIsParseable checks explicit presentation overrides work
// for local commands without consulting inspector configuration.
func TestLocalJSONNoColorIsParseable(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.IsTerminal = true

	code := run(t.Context(), []string{"--version", "--compact", "--no-color"}, env)

	output := result{code: code, stdout: env.Stdout.(*bytes.Buffer).String()}

	if output.expectOK(t).json(t)["version"] != "(devel)" || strings.Count(output.stdout, "\n") != 1 {
		t.Fatalf("version = %q", output.stdout)
	}
}

// TestJSONErrorRetainsValidationContext checks machine-readable field errors,
// argument origins, and help guidance survive the diagnostic conversion.
func TestJSONErrorRetainsValidationContext(t *testing.T) {
	t.Parallel()

	output := newRig(t).run("billing:record-payment", "INV", "5", "--address", `{"zip":"1"}`)

	output.expectFail(t, console.ExitFailure)

	diagnostic := errorDocument(t, output.stderr)

	if diagnostic["type"] != "invalid_input" || diagnostic["sources"].(map[string]any)["/address/city"] != "-address (at /city)" {
		t.Fatalf("diagnostic = %v", diagnostic)
	}

	if len(diagnostic["fields"].(map[string]any)["/address/city"].([]any)) == 0 || diagnostic["hint"] == "" {
		t.Fatalf("diagnostic = %v", diagnostic)
	}
}

// TestJSONErrorEscapesMultilineMessages protects stderr's one-record-per-line
// contract even when a safe backend message contains control characters.
func TestJSONErrorEscapesMultilineMessages(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	message := "first\nsecond\t\"quoted\""

	printError(&output, errors.New(message), console.Style{}, false)

	if errorDocument(t, output.String())["message"] != message || strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("diagnostic = %q", output.String())
	}
}

// TestStatusErrorUsesStderrTerminalStyle reproduces a failed status call when
// stdout is redirected but stderr is still an interactive terminal.
func TestStatusErrorUsesStderrTerminalStyle(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.ErrorTerminal = true
	env.Env = func(string) (string, bool) {
		return "", false
	}

	code := run(t.Context(), []string{"status", "--url", deadURL(t)}, env)

	output := env.Stderr.(*bytes.Buffer).String()

	if code != console.ExitFailure || !strings.Contains(output, "\x1b[") || !strings.Contains(output, "\n  ") {
		t.Fatalf("exit = %d; stderr = %q", code, output)
	}
}

// TestRedirectedErrorsStayUncolored checks stdout's terminal status cannot add
// ANSI escapes to a redirected diagnostic document.
func TestRedirectedErrorsStayUncolored(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Env = func(string) (string, bool) {
		return "", false
	}
	env.IsTerminal = true

	code := run(t.Context(), []string{"status", "--url", deadURL(t)}, env)

	output := env.Stderr.(*bytes.Buffer).String()

	if code != console.ExitFailure || !strings.Contains(output, "\n  ") {
		t.Fatalf("exit = %d; stderr = %q", code, output)
	}

	if errorDocument(t, output)["message"] == "" {
		t.Fatal("missing error message")
	}
}

// TestTerminalErrorsHonorNoColor preserves indentation when color is disabled.
func TestTerminalErrorsHonorNoColor(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Env = func(string) (string, bool) {
		return "", false
	}
	env.ErrorTerminal = true

	code := run(t.Context(), []string{"status", "--url", deadURL(t), "--no-color"}, env)

	output := env.Stderr.(*bytes.Buffer).String()

	if code != console.ExitFailure || !strings.Contains(output, "\n  ") {
		t.Fatalf("exit = %d; stderr = %q", code, output)
	}

	errorDocument(t, output)
}

// TestTerminalErrorsHonorNoColorEnvironment applies NO_COLOR to stderr too.
func TestTerminalErrorsHonorNoColorEnvironment(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.ErrorTerminal = true
	env.Env = func(name string) (string, bool) {
		if name == "NO_COLOR" {
			return "1", true
		}

		return "", false
	}

	code := run(t.Context(), []string{"status", "--url", deadURL(t)}, env)

	output := env.Stderr.(*bytes.Buffer).String()

	if code != console.ExitFailure || !strings.Contains(output, "\n  ") {
		t.Fatalf("exit = %d; stderr = %q", code, output)
	}

	errorDocument(t, output)
}

// TestTerminalErrorsHonorCompact keeps machine-oriented overrides available
// even when stderr itself is an interactive terminal.
func TestTerminalErrorsHonorCompact(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Env = func(string) (string, bool) {
		return "", false
	}
	env.ErrorTerminal = true

	code := run(t.Context(), []string{"status", "--url", deadURL(t), "--compact", "--no-color"}, env)

	output := env.Stderr.(*bytes.Buffer).String()

	if code != console.ExitFailure || strings.Count(output, "\n") != 1 {
		t.Fatalf("exit = %d; stderr = %q", code, output)
	}

	errorDocument(t, output)
}

// TestGlobalParseErrorsRetainPresentation uses already parsed overrides when
// a later global flag is invalid, before any command or configuration is read.
func TestGlobalParseErrorsRetainPresentation(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.ErrorTerminal = true

	code := run(t.Context(), []string{"--compact", "--no-color", "--request-timeout=invalid"}, env)

	output := env.Stderr.(*bytes.Buffer).String()

	if code != console.ExitUsage || strings.Count(output, "\n") != 1 {
		t.Fatalf("exit = %d; stderr = %q", code, output)
	}

	errorDocument(t, output)
}

// TestErrorReportsOmitDuplicateProtocolFields keeps validation diagnostics
// concise without discarding additional safe tool context or exact numbers.
func TestErrorReportsOmitDuplicateProtocolFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	failure := &remote.APIError{
		Type: "invalid_input", Title: "Invalid input",
		Fields:  map[string][]string{"/amount": {"too large"}},
		Details: []byte(`{"fields":{"/amount":["too large"]},"limit":9007199254740993}`),
	}

	printError(&output, failure, console.Style{}, true)

	diagnostic := errorDocument(t, output.String())

	for _, key := range []string{"title", "detail", "status"} {
		if _, ok := diagnostic[key]; ok {
			t.Fatalf("diagnostic contains redundant %s", key)
		}
	}

	if _, ok := diagnostic["details"].(map[string]any)["fields"]; ok {
		t.Fatal("validation fields repeated in details")
	}

	if !strings.Contains(output.String(), "9007199254740993") {
		t.Fatalf("safe numeric context changed: %s", output.String())
	}
}

// failingOutput accepts a fixed number of writes before failing. It can also
// simulate an invalid short write without an error from an injected writer.
type failingOutput struct {
	// remaining is the number of successful writes before failure.
	remaining int
	// err is the failure returned after remaining reaches zero; nil short-writes.
	err error
}

// Write preserves complete writes until the configured failure point. A nil
// failure deliberately violates io.Writer's contract to exercise normalization.
func (writer *failingOutput) Write(data []byte) (int, error) {
	if writer.remaining > 0 {
		writer.remaining--

		return len(data), nil
	}

	return 0, writer.err
}

// TestJSONOutputFailureExitsNonzero catches the formerly discarded writes in
// Session.print through the complete command boundary and injected environment.
func TestJSONOutputFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{err: syscall.EPIPE},
	}, "status")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, syscall.EPIPE.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestDirectHelpOutputFailureExitsNonzero exercises offline creation help,
// which writes directly rather than through JSON or a tabwriter.
func TestDirectHelpOutputFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	cause := errors.New("help output failed")

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{err: cause},
	}, "new", "--help")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, cause.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestHelpTableFlushFailureExitsNonzero allows the extension headings through,
// then fails the underlying write made by the remote operation table's Flush.
func TestHelpTableFlushFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	cause := errors.New("table output failed")

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{remaining: 2, err: cause},
	}, "billing:", "--help")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, cause.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestJSONShortWriteExitsNonzero checks malformed injected writers cannot
// silently truncate a result and still produce a successful command status.
func TestJSONShortWriteExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{},
	}, "status")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, io.ErrShortWrite.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestJSONNewlineFailureExitsNonzero ensures the trailing delimiter is part
// of the result contract, even when the whole JSON document was accepted.
func TestJSONNewlineFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	cause := errors.New("newline output failed")

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{remaining: 1, err: cause},
	}, "status")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, cause.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestFollowOutputFailureExitsNonzero checks the callback stops a real inspector
// follow stream on a write failure rather than treating it as cancellation.
func TestFollowOutputFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.billing.arrivals = make(chan Payment, 1)
	rig.billing.arrivals <- Payment{ID: "p3"}
	cause := errors.New("follow output failed")

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{err: cause},
	}, "billing:payments", "-f")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, cause.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestFollowShortWriteExitsNonzero covers the streaming callback's formerly
// unchecked byte count, independently of an ordinary io.Writer error.
func TestFollowShortWriteExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.billing.arrivals = make(chan Payment, 1)
	rig.billing.arrivals <- Payment{ID: "p3"}

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{},
	}, "billing:payments", "-f")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, io.ErrShortWrite.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestStderrFailureDoesNotHideStdoutFailure checks the final diagnostic is
// best effort: a second broken sink cannot recurse or turn failure into success.
func TestStderrFailureDoesNotHideStdoutFailure(t *testing.T) {
	t.Parallel()

	code := run(context.Background(), []string{"new", "--help"}, console.Environment{
		Stdout: &failingOutput{err: errors.New("stdout failed")},
		Stderr: &failingOutput{err: errors.New("stderr failed")},
	})

	if code != console.ExitFailure {
		t.Fatalf("code=%d, want %d", code, console.ExitFailure)
	}
}

// TestRootHelpOutputFailureExitsNonzero checks generated root help cannot
// hide an output failure behind its otherwise successful command status.
func TestRootHelpOutputFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	code := run(context.Background(), nil, console.Environment{
		Stdout: &failingOutput{err: syscall.EPIPE},
		Stderr: io.Discard,
	})

	if code != console.ExitFailure {
		t.Fatalf("code=%d, want %d", code, console.ExitFailure)
	}
}

// TestProjectPlanOutputFailureExitsNonzero covers local text outside help.
// Dry-run must retain its no-write behavior while reporting its failed output.
func TestProjectPlanOutputFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	result := rig.runWith(context.Background(), runOptions{
		stdout: &failingOutput{err: syscall.EPIPE},
	}, "new", "example", "--dry-run")

	if result.code != console.ExitFailure || !strings.Contains(result.stderr, syscall.EPIPE.Error()) {
		t.Fatalf("code=%d stderr=%q", result.code, result.stderr)
	}
}

// TestMainBrokenPipeUsesFailureExit exercises real descriptor 1, where Go's
// default SIGPIPE handling otherwise bypasses the command's output policy.
func TestMainBrokenPipeUsesFailureExit(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = writer.Close()
	})

	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMainBrokenPipeHelper$")
	command.Env = append(os.Environ(), "ARKOREN_TEST_BROKEN_PIPE=1")
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr

	err = command.Run()

	failure, ok := errors.AsType[*exec.ExitError](err)

	if !ok {
		t.Fatalf("process error = %v, want exit failure", err)
	}

	if failure.ExitCode() != console.ExitFailure {
		t.Fatalf("exit = %d, want %d; stderr=%q", failure.ExitCode(), console.ExitFailure, stderr.String())
	}
}

// TestMainBrokenPipeHelper runs main only in the isolated subprocess above.
// It cannot run in parallel because main mutates process signal handling and
// exits the process; the ordinary test invocation skips this helper.
func TestMainBrokenPipeHelper(t *testing.T) {
	if os.Getenv("ARKOREN_TEST_BROKEN_PIPE") != "1" {
		t.Skip("subprocess helper")
	}

	os.Args = []string{"arkoren", "new", "--help"}

	main()
}

// TestInputRequiredIsNotSuccessfulOutput checks the MCP multi-round-trip
// contract through the CLI. An unfulfilled result must never become JSON null
// with exit zero, and the CLI must not replay an action without user input.
func TestInputRequiredIsNotSuccessfulOutput(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := externalServer(&sdkmcp.Tool{Name: "requires-input", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			calls.Add(1)
			result := &sdkmcp.CallToolResult{}

			if err := result.UnmarshalJSON([]byte(`{"resultType":"input_required","content":[],"inputRequests":{}}`)); err != nil {
				return nil, err
			}

			return result, nil
		})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "requires-input")

	if calls.Load() != 1 {
		t.Fatalf("action replayed %d times; output = %+v", calls.Load(), output)
	}

	if output.code != 1 || output.out != "" || !strings.Contains(output.err, "requires additional input") {
		t.Fatalf("input-required result = %+v", output)
	}
}

// TestMalformedSchemaCannotCrashRemoteHelp treats unreadable schema fragments
// as untyped hints. The native tool still accepts raw object input, and remote
// help must remain a read-only diagnostic rather than panic on a null child.
func TestMalformedSchemaCannotCrashRemoteHelp(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "malformed", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"broken": nil},
	}}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"called": true}}, nil
	})
	url := nativeServerURL(t, server)

	output := runConnection(t, t.Context(), url, "", "malformed", "--help")

	if output.code != 0 || !strings.Contains(output.out, "-broken") || !strings.Contains(output.out, "any") {
		t.Fatalf("malformed schema help = %+v", output)
	}

	if raw := runConnection(t, t.Context(), url, "", "malformed", "--input", `{"broken":42}`); raw.code != 0 {
		t.Fatalf("raw invocation = %+v", raw)
	}
}

// TestOutputIsIndentedWithoutColorWhenPiped checks nonterminal defaults.
// Ordinary JSON output is readable but contains no ANSI escape sequences.
func TestOutputIsIndentedWithoutColorWhenPiped(t *testing.T) {
	t.Parallel()

	out := newRig(t).run("billing:payments").expectOK(t).stdout

	if !strings.Contains(out, "\n  \"items\": [\n") || strings.Contains(out, "\x1b") {
		t.Fatalf("out = %q", out)
	}
}

// TestOutputIsColoredOnATerminal checks terminal output defaults.
// Pretty indentation and syntax coloring must both be enabled.
func TestOutputIsColoredOnATerminal(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	out := rg.runWith(t.Context(), runOptions{
		env:      rg.env,
		terminal: true,
	}, "billing:payments").expectOK(t).stdout

	if !strings.Contains(out, console.ColorKey+`"items"`+console.ColorReset) || !strings.Contains(out, "\n  ") {
		t.Fatalf("out = %q", out)
	}
}

// TestColorIsOffWithNoColorFlagOrEnvironment checks color opt-out precedence.
// Either the flag or NO_COLOR disables escapes even for terminal output.
func TestColorIsOffWithNoColorFlagOrEnvironment(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	flagged := rg.runWith(t.Context(), runOptions{
		terminal: true,
	}, "-no-color", "billing:payments").expectOK(t).stdout

	env := map[string]string{remote.EnvURL: rg.url, "NO_COLOR": "1"}

	fromEnv := rg.runWith(t.Context(), runOptions{
		env:      env,
		terminal: true,
	}, "billing:payments").expectOK(t).stdout

	if strings.Contains(flagged, "\x1b") || strings.Contains(fromEnv, "\x1b") {
		t.Fatal("output is colored")
	}
}

// TestCompactOutputIsOneLine checks the CLI's compact-output switch.
// Exactly one terminating newline remains after the result document.
func TestCompactOutputIsOneLine(t *testing.T) {
	t.Parallel()

	out := newRig(t).run("-compact", "billing:payments").expectOK(t).stdout

	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `"items":[{"cents":500,"id":"p1"`) {
		t.Fatalf("out = %q", out)
	}
}

// TestStructuredOutputHasDeterministicKeyOrder checks the native SDK map result.
// The SDK decodes structuredContent into maps, so the CLI sorts keys for stable
// output rather than claiming to retain wire ordering it cannot observe.
func TestStructuredOutputHasDeterministicKeyOrder(t *testing.T) {
	t.Parallel()

	out := newRig(t).run("-compact", "billing:payments").expectOK(t).stdout

	if !strings.Contains(out, `{"cents":500,"id":"p1","status":"paid"}`) {
		t.Fatalf("out = %q", out)
	}
}

// Payment is one payment of the billing extension.
type Payment struct {
	// ID identifies a fixture payment in read and follow responses.
	ID string `json:"id"`

	// Status selects the paid or failed stream-filtering scenario.
	Status string `json:"status"`

	// Cents is the payment amount in integer minor currency units.
	Cents int `json:"cents"`
}

// paymentsInput is the input of billing:payments.
type paymentsInput struct {
	// Cursor selects cursor polling; tail establishes a position only.
	Cursor string `json:"cursor,omitzero"`
	// Wait bounds one cursor observation as an encoded duration.
	Wait string `json:"wait,omitzero"`
	// Status optionally restricts results to one fixture status.
	Status string `json:"status,omitzero"`

	// Limit exercises integer constraints and the server's default of ten.
	Limit int `json:"limit,omitzero"`
}

// Address is a nested object of the order input.
type Address struct {
	// City is required when the optional address object is supplied.
	City string `json:"city,omitzero"`

	// Zip exercises an optional sibling of the required city property.
	Zip string `json:"zip,omitzero"`
}

// Item is an element of the order's array of objects.
type Item struct {
	// SKU is the required product identifier in each array element.
	SKU string `json:"sku"`

	// Qty exercises optional integer input within an array of objects.
	Qty int `json:"qty,omitzero"`
}

// orderInput is the input and output of billing:record-payment: every kind
// of property a flag can set. Token has the name of a global flag.
type orderInput struct {
	// Invoice is the required first positional payment argument.
	Invoice string `json:"invoice"`

	// Cents is the required positive amount and second positional argument.
	Cents int `json:"cents"`

	// Rate exercises floating-point conversion from flag text.
	Rate float64 `json:"rate,omitzero"`

	// Express exercises bare and explicit boolean flag forms.
	Express bool `json:"express,omitzero"`

	// Currency exercises enum help and a server-applied default.
	Currency string `json:"currency,omitzero"`

	// Address exercises an optional nested object and child validation errors.
	Address *Address `json:"address,omitzero"`

	// Labels exercises literal map-key paths in flags.
	Labels map[string]string `json:"labels,omitzero"`

	// Items exercises dense indexed paths into an array of objects.
	Items []Item `json:"items,omitzero"`

	// Tags exercises repeated scalar flags and JSON-array replacement.
	Tags []string `json:"tags,omitzero"`

	// Token collides with a global flag, so only base JSON can supply it.
	Token string `json:"token,omitzero"`
}

// orderSchema requires the invoice, a positive amount, and a city when an
// address is given, and defaults the currency.
var orderSchema = validate.Struct(func(object *validate.StructBuilder[orderInput]) {
	object.Field("Invoice", func(invoice *validate.StringBuilder[string]) {
		invoice.Required()
		invoice.Describe("The invoice to pay")
	})
	object.Field("Cents", func(cents *validate.IntBuilder[int]) {
		cents.Required()
		cents.Min(1)
		cents.Describe("Amount in cents")
	})
	object.Field("Rate", func(rate *validate.FloatBuilder[float64]) {
	})
	object.Field("Express", func(express *validate.BoolBuilder[bool]) {
		express.Describe("Settle immediately")
	})
	object.Field("Currency", func(currency *validate.StringBuilder[string]) {
		currency.OneOf("eur", "usd")
		currency.Default("eur")
		currency.Describe("Settlement currency")
	})
	object.Field("Address", func(address *validate.PtrBuilder[Address]) {
		address.Value(func(object *validate.StructBuilder[Address]) {
			object.Field("City", func(city *validate.StringBuilder[string]) {
				city.Required()
			})
			object.Field("Zip", func(zip *validate.StringBuilder[string]) {
			})
		})
	})
	object.Field("Labels", func(labels *validate.MapBuilder[string, string]) {
		labels.Keys(func(key *validate.StringBuilder[string]) {
		})
		labels.Values(func(value *validate.StringBuilder[string]) {
		})
	})
	object.Field("Items", func(items *validate.SliceBuilder[Item]) {
		items.Values(func(object *validate.StructBuilder[Item]) {
			object.Field("SKU", func(sku *validate.StringBuilder[string]) {
				sku.Required()
			})
			object.Field("Qty", func(quantity *validate.IntBuilder[int]) {
			})
		})
	})
	object.Field("Tags", func(tags *validate.SliceBuilder[string]) {
		tags.Values(func(tag *validate.StringBuilder[string]) {
		})
	})
	object.Field("Token", func(token *validate.StringBuilder[string]) {
	})
})

// paymentsSchema rejects an unknown status and defaults the limit.
var paymentsSchema = validate.Struct(func(object *validate.StructBuilder[paymentsInput]) {
	object.Field("Cursor", func(cursor *validate.StringBuilder[string]) {})
	object.Field("Wait", func(wait *validate.StringBuilder[string]) {})
	object.Field("Status", func(status *validate.StringBuilder[string]) {
		status.OneOf("failed", "paid")
		status.Describe("Only payments with this status")
	})
	object.Field("Limit", func(limit *validate.IntBuilder[int]) {
		limit.Min(1)
		limit.Max(100)
		limit.Default(10)
	})
})

// billing is the third-party extension: a read that follows, an action that
// echoes its input, and a read that fails.
type billing struct {
	// mu protects the fixture ledger during concurrent inspector reads.
	mu sync.Mutex

	// payments is the initial ledger returned by the read operation.
	payments []Payment

	// arrivals feeds the payments follow; a test sends on it.
	arrivals chan Payment
}

// extension builds the billing extension over the ledger's live data.
func (billing *billing) tools() []mcp.Tool {
	payments := mcp.NewTool("billing.payments", billing.list, func(read *mcp.ToolBuilder[paymentsInput, paymentPage]) {
		read.CLI("billing", "payments").GroupSummary("Payments and invoices")
		read.ReadOnly(true).Cursor(true)
		read.Description("List recent payments")
		read.Input(paymentsSchema)
		read.Positional("status")
		read.Example("Failed payments", `{"status":"failed"}`)
	})

	record := mcp.NewTool("billing.record-payment", func(_ context.Context, input orderInput) (orderInput, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[orderInput, orderInput]) {
		action.CLI("billing", "record-payment")
		action.Description("Record a payment against an invoice")
		action.Input(orderSchema)
		action.Positional("invoice", "cents")
		action.Example("A payment with a nested address, a label, an item, and tags",
			`{"invoice":"INV-1","cents":500,"address":{"city":"Paris"},"items":[{"sku":"A","qty":2}],"labels":{"env":"prod"},"tags":["a","b"]}`)
	})

	boom := mcp.NewTool("billing.boom", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, mcp.ToolError("ledger unreachable")
	}, func(read *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
		read.CLI("billing", "boom").ReadOnly(true)
		read.Description("Always fails")
	})

	return []mcp.Tool{payments, record, boom}
}

// paymentPage is the object-shaped result shared by list and cursor polls.
type paymentPage struct {
	// Items contains matching fixture payments in source order.
	Items []Payment `json:"items"`
	// Cursor is an opaque fixture position, never interpreted by the CLI.
	Cursor string `json:"cursor"`
	// Dropped exercises explicit loss notices before streamed items.
	Dropped int `json:"dropped"`
}

// list answers billing:payments.
func (billing *billing) list(ctx context.Context, input paymentsInput) (paymentPage, error) {
	if input.Cursor == "tail" {
		return paymentPage{Items: []Payment{}, Cursor: "live"}, nil
	}

	if input.Cursor != "" {
		return billing.follow(ctx, input)
	}

	billing.mu.Lock()
	defer billing.mu.Unlock()

	items := []Payment{}

	for _, payment := range billing.payments {
		if input.Status == "" || payment.Status == input.Status {
			items = append(items, payment)
		}
	}

	return paymentPage{
		Items: items, Cursor: "live",
	}, nil
}

// follow streams the payments sent on arrivals; the one with the ID "gap"
// is preceded by a notice that three items were lost.
func (billing *billing) follow(ctx context.Context, input paymentsInput) (paymentPage, error) {
	for {
		select {
		case <-ctx.Done():
			return paymentPage{}, ctx.Err()
		case payment := <-billing.arrivals:
			if input.Status == "" || payment.Status == input.Status {
				page := paymentPage{Items: []Payment{payment}, Cursor: "live"}

				if payment.ID == "gap" {
					page.Dropped = 3
				}

				return page, nil
			}
		}
	}
}

// rig is a real inspector served over HTTP and the process settings the CLI
// runs with.
type rig struct {
	// t owns fixture cleanup and attributes helper failures to the test.
	t *testing.T

	// url points to this fixture's served inspection root.
	url string

	// application exposes the composed fixture's runtime state.
	application *application.Application

	// telemetry records traces without starting the whole application.
	telemetry *telemetry.Telemetry

	// billing exposes ledger arrivals for deterministic stream assertions.
	billing *billing

	// env provides the default environment values for CLI runs.
	env map[string]string

	// dir is the isolated working directory for env files and input paths.
	dir string
}

// showUser is a handler with a stable name for the routes output.
func showUser(http.ResponseWriter, *http.Request) error {
	return nil
}

// newRig serves the application's inspector with a "public" router, a "jobs"
// scheduler, and the billing tools, and points ARKOREN_MCP_URL at the endpoint.
func newRig(t *testing.T, opts ...func(*fixtureBuilder)) *rig {
	t.Helper()

	rg := &rig{
		t:   t,
		dir: t.TempDir(),
		application: application.New(func(app *application.Builder) {
			app.Name("shop")
			app.Version("1.2.3")
		}),
	}
	rg.telemetry = rg.application.Telemetry()
	rg.billing = &billing{
		payments: []Payment{
			{
				ID:     "p1",
				Status: "paid",
				Cents:  500,
			},
			{
				ID:     "p2",
				Status: "failed",
				Cents:  900,
			},
		},
		arrivals: make(chan Payment),
	}

	public := router.New()
	public.Get("/users/{id}", showUser)
	public.Post("/users", showUser)

	scheduler := schedule.New(memoryschedule.New())
	nightly := schedule.NewEntry("nightly-report", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Cron("0 3 * * *"))
		entry.Description("builds the report")
	})

	if err := scheduler.Add(nightly); err != nil {
		t.Fatal(err)
	}

	definition := &fixtureBuilder{}
	definition.Tools(public.MCPTools("public")...)
	definition.Tools(scheduler.MCPTools("jobs")...)
	definition.Tools(rg.billing.tools()...)

	for _, configure := range opts {
		configure(definition)
	}

	rg.startFixture(t, definition)

	server := httptest.NewServer(definition.handler(t))
	t.Cleanup(server.Close)

	rg.url = server.URL + "/inspect"
	rg.env = map[string]string{remote.EnvURL: rg.url}

	return rg
}

// startFixture completes composition and pauses startup until cleanup. This
// exposes real capture and health tools while keeping readiness waits pending.
// The startup signal publishes the completed tool slice before HTTP serving.
func (rig *rig) startFixture(t *testing.T, definition *fixtureBuilder) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	starting := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- rig.application.Run(ctx, func(composition *application.Composer) {
			definition.Tools(composition.Must(composition.MCPTools("app"))...)

			if definition.compose != nil {
				definition.compose(composition)
			}

			composition.Startup(func(ctx context.Context) error {
				close(starting)
				<-ctx.Done()

				return nil
			})
		})
	}()
	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("application shutdown: %v", err)
		}
	})

	select {
	case <-starting:
	case err := <-done:
		done <- err
		t.Fatalf("application startup: %v", err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

// failedTrace records a server trace that ends in a 500 with a failing child
// span and a log record, and returns its trace ID.
func (rig *rig) failedTrace(ctx context.Context) string {
	logger := slog.New(rig.telemetry.Handler(slog.NewTextHandler(new(bytes.Buffer), nil)))
	tracer := rig.telemetry.Tracer("orders")

	ctx, root := tracer.Start(ctx, "GET /orders/{id}", telemetry.Kind(telemetry.Server), telemetry.Attrs(
		telemetry.Int("http.response.status_code", 500),
		telemetry.String("http.route", "/orders/{id}"),
	))

	ctx, child := tracer.Start(ctx, "payments charge", telemetry.Kind(telemetry.Client), telemetry.Attrs(
		telemetry.String("error.type", "payment.Declined"),
	))

	logger.ErrorContext(ctx, "payment declined", "order_id", 42)
	child.SetStatus(telemetry.Error, "declined")
	child.End()
	root.End()

	return root.Context().TraceID.String()
}

// result is what one CLI run produced.
type result struct {
	// code is the CLI exit status, without spawning a process.
	code int

	// stdout contains the complete result unless a custom stream writer is used.
	stdout string

	// stderr contains diagnostics and hints emitted by this run.
	stderr string
}

// runOptions varies one run from the defaults.
type runOptions struct {
	// env replaces the rig's environment when not nil.
	env map[string]string

	// stdin is the text "-input -" reads.
	stdin string

	// terminal makes stdout a terminal.
	terminal bool

	// inputTerminal enables local interactive prompts independently of stdout.
	inputTerminal bool

	// stdout receives the output instead of the result, for tests that read
	// a stream while it runs.
	stdout io.Writer
}

// lineSink is a writer that hands every Write, which is one line of a follow
// stream, to a channel.
type lineSink struct {
	// lines receives complete follow writes so tests can consume them in order.
	lines chan string
}

// Write sends p to the channel.
func (sink lineSink) Write(p []byte) (int, error) {
	sink.lines <- string(p)

	return len(p), nil
}

// run executes the CLI with the rig's environment and returns its result.
func (rig *rig) run(args ...string) result {
	rig.t.Helper()

	return rig.runWith(context.Background(), runOptions{}, args...)
}

// runWith executes the CLI with options and a context the test controls.
func (rig *rig) runWith(ctx context.Context, options runOptions, args ...string) result {
	rig.t.Helper()

	env := rig.env

	if options.env != nil {
		env = options.env
	}

	var stdout, stderr bytes.Buffer
	var out io.Writer = &stdout

	if options.stdout != nil {
		out = options.stdout
	}

	code := run(ctx, args, console.Environment{
		Stdin:  strings.NewReader(options.stdin),
		Stdout: out,
		Stderr: &stderr,
		Env: func(key string) (string, bool) {
			value, ok := env[key]

			return value, ok
		},
		Dir:           rig.dir,
		IsTerminal:    options.terminal,
		InputTerminal: options.inputTerminal,
	})

	return result{
		code:   code,
		stdout: stdout.String(),
		stderr: stderr.String(),
	}
}

// json decodes the stdout of a result into a generic value, failing the
// test when it is not JSON.
func (result result) json(t *testing.T) map[string]any {
	t.Helper()

	var value map[string]any

	if err := json.Unmarshal([]byte(result.stdout), &value); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s\nstderr: %s", err, result.stdout, result.stderr)
	}

	return value
}

// expectOK fails the test unless the run exited 0 and returns the result.
func (result result) expectOK(t *testing.T) result {
	t.Helper()

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout: %s\nstderr: %s", result.code, result.stdout, result.stderr)
	}

	return result
}

// expectFail checks the exit status and decoded JSON diagnostic messages.
func (result result) expectFail(t *testing.T, code int, fragments ...string) {
	t.Helper()

	if result.code != code {
		t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", result.code, code, result.stdout, result.stderr)
	}

	diagnostic := errorDocument(t, result.stderr)
	text := fmt.Sprint(diagnostic)

	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Fatalf("stderr lacks %q:\n%s", fragment, result.stderr)
		}
	}
}

// errorDocument requires exactly one JSON error document on stderr.
func errorDocument(t *testing.T, text string) map[string]any {
	t.Helper()

	var document struct {
		Error map[string]any `json:"error"`
	}

	if err := json.Unmarshal([]byte(text), &document); err != nil || document.Error == nil {
		t.Fatalf("stderr is not one JSON error object: %v\n%s", err, text)
	}

	return document.Error
}

// record runs billing record-payment with the invoice and amount as
// arguments, then args, and returns the echoed input.
func (rig *rig) record(args ...string) map[string]any {
	rig.t.Helper()

	full := append([]string{"billing:record-payment", "INV-1", "5"}, args...)

	return rig.run(full...).expectOK(rig.t).json(rig.t)
}

// commandExtension supplies a no-input preview operation under a name that
// may collide with local commands. Its empty result makes a remote
// invocation distinguishable from local help and router output.
func commandExtension(name string) mcp.Tool {
	return mcp.NewTool(name+".preview", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, nil
	}, func(tool *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
		tool.CLI(name, "preview").ReadOnly(true)
	})
}

// TestQualifiedLocalCommandNamesRemainReachable protects the boundary between
// local dispatch and server discovery, including the new project command.
func TestQualifiedLocalCommandNamesRemainReachable(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"new", "help", "tools", "status", "version", "doctor", "key", "certificate", "skill", "upgrade", "make", "dev", "vet"} {
		rg := newRig(t, func(inspector *fixtureBuilder) {
			inspector.Tools(commandExtension(name))
		})

		out := rg.run(name + ":preview").expectOK(t).stdout

		if strings.TrimSpace(out) != "{}" {
			t.Fatalf("%s: output = %q", name, out)
		}
	}
}

// TestQualifiedCommandsRequireExactlyOneColon rejects empty segments and
// multiple separators rather than treating them as operation arguments.
func TestQualifiedCommandsRequireExactlyOneColon(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	for _, command := range []string{":", ":payments", "billing:", "billing:payments:extra", "billing::payments", "billing:payments:"} {
		rg.run(command).expectFail(t, console.ExitUsage, "invalid command", "one colon and nonempty names")
	}
}

// TestOldTwoWordInvocationIsRejected ensures no compatibility fallback
// accidentally consumes an extension and operation as separate words.
func TestOldTwoWordInvocationIsRejected(t *testing.T) {
	t.Parallel()

	newRig(t).run("billing", "payments").expectFail(t, console.ExitUsage, "unknown command", "<extension>:<operation>")
}

// TestToolsRejectsTargets keeps catalog discovery separate from command help.
// Positional targets must fail before configuration or discovery is attempted.
func TestToolsRejectsTargets(t *testing.T) {
	t.Parallel()

	newRig(t).run("tools", "billing").expectFail(t, console.ExitUsage, "tools takes no arguments")
}

// TestExplicitExtensionHelpUsesTrailingColon covers the unambiguous spelling
// for extension discovery while preserving the ordinary bare-name shorthand.
func TestExplicitExtensionHelpUsesTrailingColon(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	if rg.run("billing:", "--help").expectOK(t).stdout != rg.run("billing", "--help").expectOK(t).stdout {
		t.Fatal("explicit extension help differs from bare extension help")
	}
}

// TestExplicitExtensionHelpReachesLocalNames checks that a colon selects a
// server extension even when its bare name belongs to the local dispatcher.
func TestExplicitExtensionHelpReachesLocalNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"tools", "new", "certificate", "status"} {
		rg := newRig(t, func(inspector *fixtureBuilder) {
			inspector.Tools(commandExtension(name))
		})

		contains(t, rg.run(name+":", "--help").expectOK(t).stdout, "arkoren "+name+":<operation>", name+":preview")

		if strings.Contains(rg.run(name, "--help").expectOK(t).stdout, name+":preview") {
			t.Fatalf("help %s describes the remote extension instead of the local command", name)
		}
	}
}

// TestFormerShortcutMatchingGroupIsNotInvocable checks a bare group name.
// Matching a former diagnostic shortcut does not grant invocation semantics.
func TestFormerShortcutMatchingGroupIsNotInvocable(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Tools(commandExtension("routes"))
	})

	rg.run("routes").expectFail(t, 2, `unknown command "routes"`)
	rg.run("routes:preview").expectOK(t)
}

// TestQualifiedHealthKeepsFailingExitCode protects the one remote result the
// CLI interprets. Qualification must not change the extension/operation names
// passed to the health verdict or convert this failure into a usage error.
func TestQualifiedHealthKeepsFailingExitCode(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.compose = func(composition *application.Composer) {
			composition.Check("cache", func(context.Context) error {
				return os.ErrDeadlineExceeded
			})
		}
	})

	result := rg.run("app:health", "-fresh")

	if result.code != console.ExitFailure || result.json(t)["status"] != "failing" {
		t.Fatalf("health: code %d, %s", result.code, result.stdout)
	}

	if result.stderr != "" {
		t.Fatalf("stderr = %q", result.stderr)
	}
}

// TestNewHelpBypassesInspectorSettings checks both local help spellings with
// broken project configuration and a missing explicitly selected env file.
func TestNewHelpBypassesInspectorSettings(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = "not a URL"

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte("ARKOREN_MCP_URL=\"unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}

	flagHelp := rg.run("new", "--help").expectOK(t).stdout
	commandHelp := rg.run("new", "-h").expectOK(t).stdout
	missingFile := rg.run("-env-file", "missing.env", "new", "--help").expectOK(t).stdout

	if flagHelp != commandHelp || flagHelp != missingFile {
		t.Fatal("local new help depends on inspector configuration")
	}

	contains(t, flagHelp, "arkoren new")
}

// TestNewBypassesInspectorSettings exercises project planning, not just help,
// while all inspector configuration sources would fail remote dispatch.
func TestNewBypassesInspectorSettings(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = "not a URL"

	if err := os.WriteFile(filepath.Join(rg.dir, ".env"), []byte("ARKOREN_MCP_URL=\"unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}

	rg.run("-env-file", "missing.env", "new", "preview", "--dry-run").expectOK(t)

	if _, err := os.Stat(filepath.Join(rg.dir, "preview")); !os.IsNotExist(err) {
		t.Fatalf("dry run created destination or could not inspect it: %v", err)
	}
}

// TestWaitTimeoutIsOperationInputNotGlobal checks manifest-driven wait flags.
// The operation timeout is independent of global request-timeout parsing.
func TestWaitTimeoutIsOperationInputNotGlobal(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("app:ready", "--wait", "--timeout", "20ms").expectFail(t, 1,
		"timed out")
}

// TestConditionReadsHaveDistinctOutputs checks the application's condition reads.
// Started, healthy, and ready must retain their separate response schemas.
func TestConditionReadsHaveDistinctOutputs(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	started := rg.run("app:started").expectOK(t).json(t)

	if started["started"] != false || started["state"] != "starting" {
		t.Fatalf("started = %v", started)
	}

	healthy := rg.run("app:healthy").expectOK(t).json(t)

	if healthy["status"] != "ok" || healthy["checks"] == nil {
		t.Fatalf("healthy = %v", healthy)
	}

	ready := rg.run("app:ready").expectOK(t).json(t)

	if ready["ready"] != false || ready["reason"] != "not live" {
		t.Fatalf("ready = %v", ready)
	}
}

// TestWaitRequestDeadlineCanCancelServerWait checks explicit request deadlines.
// They may end a server wait before the operation's larger timeout budget.
func TestWaitRequestDeadlineCanCancelServerWait(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	rg.run("app:ready", "--wait", "--timeout", "1s", "--request-timeout", "20ms").expectFail(t, 1,
		"did not answer within 20ms", "request-timeout")
}

// TestWaitHelpComesFromManifest checks condition-read help discovery.
// Wait flags and defaults must come from the operation's published input.
func TestWaitHelpComesFromManifest(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	result := rg.run("app:ready", "--help").expectOK(t)

	contains(t, result.stdout, "-wait", "-timeout", "30s", "ready")
}

// TestWaitConventionWorksForCallerExtension checks extension-neutral waiting.
// A caller-defined boolean wait input needs no CLI-specific operation mapping.
func TestWaitConventionWorksForCallerExtension(t *testing.T) {
	t.Parallel()

	type input struct {
		// Wait enables the caller-defined handler's condition-wait path.
		Wait bool `json:"wait"`
	}

	read := mcp.NewTool("custom.settled", func(_ context.Context, in input) (map[string]bool, error) {
		return map[string]bool{"settled": in.Wait}, nil
	}, func(tool *mcp.ToolBuilder[input, map[string]bool]) {
		tool.CLI("custom", "settled").ReadOnly(true)
	})
	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Tools(read)
	})

	result := rg.run("custom:settled", "--wait").expectOK(t)

	if result.json(t)["settled"] != true {
		t.Fatalf("output = %q", result.stdout)
	}
}

// TestWaitFlagAndRequestTimeoutRemainIndependent checks global flag extraction.
// Operation wait and timeout arguments remain intact for schema conversion.
func TestWaitFlagAndRequestTimeoutRemainIndependent(t *testing.T) {
	t.Parallel()

	flags, rest, err := parseGlobals([]string{"app:ready", "--wait", "--timeout", "1m", "--request-timeout", "2m"})

	if err != nil {
		t.Fatal(err)
	}

	if !flags.TimeoutSet || flags.Timeout != 2*time.Minute {
		t.Fatalf("globals = %+v", flags)
	}

	encoded, err := json.Marshal(rest)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `["app:ready","--wait","--timeout","1m"]` {
		t.Fatalf("operation args = %s", encoded)
	}
}

// TestFailFastFlagUsesManifestInput checks the kebab-case fail-fast input alias.
// The CLI forwards the condition-read policy rather than treating it as global.
func TestFailFastFlagUsesManifestInput(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.compose = func(composition *application.Composer) {
			composition.Check("database", func(context.Context) error {
				return nil
			})
		}
	})

	rg.run("app:ready", "--wait", "--fail-fast", "--timeout", "20ms").expectFail(t, 1,
		"timed out")
}

// TestHealthWaitHelpListsFailFastAlias checks schema-specific alias help.
// Fail-fast belongs to health conditions and must remain invalid for started.
func TestHealthWaitHelpListsFailFastAlias(t *testing.T) {
	t.Parallel()

	rg := newRig(t)

	contains(t, rg.run("app:ready", "--help").expectOK(t).stdout, "-fail_fast", "-fail-fast")
	contains(t, rg.run("app:healthy", "--help").expectOK(t).stdout, "-fail-fast")

	rg.run("app:started", "--wait", "--fail-fast").expectFail(t, 2, "unknown flag -fail-fast")
}

// TestWaitDiscoveryDoesNotStealInputFlagValues verifies token ownership through
// the advertised schema. A string flag may consume the text --input; it is not
// a request to read a file or stdin and must reach tools/call unchanged.
func TestWaitDiscoveryDoesNotStealInputFlagValues(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string"}},
	}}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
		}}, nil
	})
	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "echo", "--compact", "--label", "--input")

	if output.code != 0 || output.out != "{\"label\":\"--input\"}\n" {
		t.Fatalf("input token was stolen from its schema-owned value: %+v", output)
	}
}

// TestWaitDiscoveryDoesNotStealWaitFlagValues keeps a value spelled --wait
// distinct from a boolean wait input. The unrelated timeout property belongs to
// this tool and is not a duration to validate before discovery.
func TestWaitDiscoveryDoesNotStealWaitFlagValues(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			"label": map[string]any{"type": "string"}, "timeout": map[string]any{"type": "string"},
		},
	}}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
		}}, nil
	})
	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "echo", "--compact",
		"--label", "--wait", "--timeout", "not-a-duration")

	if output.code != 0 || output.out != "{\"label\":\"--wait\",\"timeout\":\"not-a-duration\"}\n" {
		t.Fatalf("wait token was stolen from its schema-owned value: %+v", output)
	}
}

// TestWaitDiscoveryDoesNotAssumeBooleanWaitArity covers a conventional name
// with a different advertised type. Its --input-looking string value must not
// become a missing input flag or an attempt to materialize a source.
func TestWaitDiscoveryDoesNotAssumeBooleanWaitArity(t *testing.T) {
	t.Parallel()

	server := externalServer(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"wait": map[string]any{"type": "string"}},
	}}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(request.Params.Arguments)},
		}}, nil
	})
	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "echo", "--compact", "--wait", "--input")

	if output.code != 0 || output.out != "{\"wait\":\"--input\"}\n" {
		t.Fatalf("wait arity was assumed before reading its schema: %+v", output)
	}
}

// TestSchemaOwnedWaitAfterOtherFlagsRetainsDiscoveryBudget verifies that
// provisional connection hints still support interspersed operation flags.
// Actual token ownership and the remaining timeout come from the advertised
// Schema; fifty milliseconds of failed discovery consume that same budget.
func TestSchemaOwnedWaitAfterOtherFlagsRetainsDiscoveryBudget(t *testing.T) {
	// The injected public HTTP transport is process-wide, so this stays serial.
	synctest.Test(t, func(t *testing.T) {
		handler := cleanupProtocolHandler(&sdkmcp.Tool{Name: "echo", Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"label": map[string]any{"type": "string"}, "wait": map[string]any{"type": "boolean"},
				"timeout": map[string]any{"type": "string"},
			}},
		}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			var input map[string]any

			if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
				return nil, err
			}

			return &sdkmcp.CallToolResult{StructuredContent: input}, nil
		})
		startedAt := time.Now()
		original := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = original })
		http.DefaultTransport = cliRoundTripper(func(request *http.Request) (*http.Response, error) {
			if time.Since(startedAt) < 50*time.Millisecond {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			}

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			return response.Result(), nil
		})

		output := runConnection(t, t.Context(), "http://fixture.invalid/mcp", "", "echo", "--compact",
			"--label", "value", "--wait", "--timeout=500ms")

		if output.code != 0 || output.out != "{\"label\":\"value\",\"timeout\":\"450ms\",\"wait\":true}\n" {
			t.Fatalf("interspersed wait lost its discovery budget: %+v", output)
		}
	})
}

// TestMaterializedInputCannotBecomeAnotherSource verifies that file contents
// remain data after normal schema parsing owns the input flag. A malformed JSON
// file beginning with @ must fail, not select and read a second input file.
func TestMaterializedInputCannotBecomeAnotherSource(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	second := filepath.Join(directory, "second.json")

	if err := os.WriteFile(second, []byte(`{"value":42}`), 0o600); err != nil {
		t.Fatal(err)
	}

	first := filepath.Join(directory, "first.json")

	if err := os.WriteFile(first, []byte("@"+second), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int64
	server := externalServer(&sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string"}},
	}}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{StructuredContent: map[string]bool{"called": true}}, nil
	})

	output := runConnection(t, t.Context(), nativeServerURL(t, server), "", "echo", "--label", "value", "--input", "@"+first)

	if output.code != 2 || calls.Load() != 0 || !strings.Contains(output.err, "not valid JSON") {
		t.Fatalf("materialized data was reinterpreted as a source: %+v; calls = %d", output, calls.Load())
	}
}

// want compares a decoded value with the JSON text it should equal.
func want(t *testing.T, got any, expected string) {
	t.Helper()

	var parsed any

	if err := json.Unmarshal([]byte(expected), &parsed); err != nil {
		t.Fatalf("bad expectation %s: %v", expected, err)
	}

	if !reflect.DeepEqual(got, parsed) {
		encoded, _ := json.Marshal(got)
		t.Fatalf("got %s, want %s", encoded, expected)
	}
}
