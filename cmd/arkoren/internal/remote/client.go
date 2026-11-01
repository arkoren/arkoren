package remote

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/cmd/arkoren/internal/console"
	coremcp "arkoren.dev/core/mcp"
)

// Client owns one SDK session. Discovery establishes the session before
// any tool is called, so reconnecting discovery can never replay an action.
type Client struct {
	// Base is the approved MCP endpoint, with no operation path appended.
	Base *url.URL
	// token is attached only to requests to the approved origin.
	token string
	// requestTimeout publishes the current discovery or invocation deadline to
	// concurrent SDK HTTP requests without mutating http.Client configuration.
	requestTimeout atomic.Int64
	// cleanupContext retains the command owner separately from the SDK's detached
	// session context. Immutable interface snapshots are published atomically so
	// a DELETE can observe cancellation without racing phase transitions.
	cleanupContext atomic.Pointer[context.Context]
	// Session is established by FetchManifest and closed by the command.
	Session *mcp.ClientSession
	// wire preserves exact generic JSON at the SDK connection boundary. Commands
	// perform sequential catalog/call operations; raw snapshots are consumed once.
	wire *wireTransport
}

// APIError preserves safe tool failures and HTTP authentication failures. Native
// MCP protocol errors remain SDK errors rather than masquerading as HTTP 401.
type APIError struct {
	// Status is populated only for an actual HTTP failure.
	Status int
	// Type is the optional Arkoren failure code.
	Type string
	// Title and Detail form the human-readable diagnostic.
	Title, Detail string
	// Fields maps input JSON pointers to all validation messages.
	Fields map[string][]string
	// Details retains safe structured failure context such as app wait results.
	Details jsontext.Value
	// origins maps validation pointers back to the arguments the user supplied.
	origins []origin
	// operation supplies the help target for a validation diagnostic.
	operation string
}

// Error renders the public failure message without encoding it as JSON text.
func (failure *APIError) Error() string {
	if failure.Detail == "" {
		return failure.Title
	}

	return failure.Title + ": " + failure.Detail
}

// unreachableError distinguishes dialing failures from tool and protocol errors.
// Help and version can report an unreachable server without failing themselves.
type unreachableError struct {
	// url identifies the configured endpoint without credentials.
	url string
	// err retains the underlying network failure for errors.Is and errors.As.
	err error
}

// Error names the endpoint so the CLI can suggest port forwarding.
func (failure *unreachableError) Error() string {
	if cause, ok := errors.AsType[*net.OpError](failure.err); ok {
		return fmt.Sprintf("cannot reach the inspector at %s: %v", failure.url, cause.Err)
	}

	return "cannot reach the inspector at " + failure.url + ": " + failure.err.Error()
}

// Unwrap exposes the transport failure without losing its classification.
func (failure *unreachableError) Unwrap() error { return failure.err }

// authenticatedTransport adds credentials at the HTTP boundary and classifies
// authentication before the SDK decodes a protocol response. It never retries.
type authenticatedTransport struct {
	// client supplies the single approved origin, token, and request deadline.
	client *Client
	// denied stops SDK legacy negotiation from repeating an HTTP 401. Protocol
	// negotiation cannot repair rejected credentials; a fresh CLI run can.
	denied atomic.Pointer[APIError]
}

// RoundTrip sends one request. Redirects are disabled by the enclosing client;
// a cloned request keeps authorization out of SDK-owned request state.
func (transport *authenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if failure := transport.denied.Load(); failure != nil {
		return nil, failure
	}

	ctx, cancel := transport.client.requestContext(request)
	request = request.Clone(ctx)

	if transport.client.token != "" {
		request.Header.Set("Authorization", "Bearer "+transport.client.token)
	}

	response, err := http.DefaultTransport.RoundTrip(request)

	if err != nil {
		cancel()

		return nil, err
	}

	response.Body = &requestBody{ReadCloser: response.Body, context: ctx, cancel: cancel}

	if response.StatusCode == http.StatusUnauthorized {
		// The authentication verdict takes precedence over body-close failures.
		_ = response.Body.Close()
		failure := &APIError{Status: response.StatusCode, Title: "unauthorized"}
		transport.denied.Store(failure)

		return nil, failure
	}

	return response, nil
}

// requestBody retains a request deadline through body consumption rather than
// canceling it as soon as response headers arrive. Closing always releases the
// timer, including SDK protocol failures that stop before EOF.
type requestBody struct {
	// ReadCloser is the owned HTTP response stream; Close releases it before
	// releasing the deadline. The body retains cancellation until that close.
	//
	// Example:
	//
	//	defer response.Body.Close()
	io.ReadCloser
	// context preserves deadline/cancellation identity when the SDK reads a body.
	context context.Context
	// cancel releases this request's timer and parent cancellation registration.
	cancel context.CancelFunc
}

// Read preserves context error identity when a body read outlives its request.
// Other read errors remain unchanged so protocol/transport classification stays
// with the SDK; callers must still handle partial data alongside an error.
//
// Example:
//
//	n, err := response.Body.Read(buffer)
//	consume(buffer[:n]) // Process partial data even when the read also fails.
//	if err != nil && err != io.EOF {
//		return err
//	}
func (body *requestBody) Read(data []byte) (int, error) {
	n, err := body.ReadCloser.Read(data)

	if err != nil && body.context.Err() != nil {
		return n, body.context.Err()
	}

	return n, err
}

// Close releases the HTTP stream and its deadline on every path. The underlying
// close error is retained; cancellation itself adds no new failure.
//
// Example:
//
//	if err := response.Body.Close(); err != nil {
//		return err
//	}
func (body *requestBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()

	return err
}

// cleanupTimeout bounds best-effort native session termination independently of
// potentially long tool calls. The command's cancellation/deadline can end cleanup
// sooner; the standard HTTP transport cooperatively releases the request itself.
const cleanupTimeout = time.Second

// configureRequests publishes request deadlines and cleanup ownership for one
// command phase. The CLI changes phases sequentially; SDK HTTP bookkeeping reads
// immutable context snapshots and an atomic duration rather than changing an
// in-use http.Client. A zero request timeout does not remove the cleanup bound.
func (client *Client) configureRequests(ctx context.Context, timeout time.Duration) {
	client.requestTimeout.Store(int64(timeout))
	client.cleanupContext.Store(&ctx)
}

// requestContext gives each SDK request its current deadline, including body
// reads. Zero leaves duration unbounded while retaining caller cancellation;
// the returned release belongs to the response body or a failed RoundTrip.
func (client *Client) requestContext(request *http.Request) (context.Context, context.CancelFunc) {
	if request.Method == http.MethodDelete {
		return client.cleanupRequestContext(request.Context())
	}

	ctx := request.Context()
	timeout := time.Duration(client.requestTimeout.Load())

	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}

	return context.WithCancel(ctx)
}

// cleanupRequestContext preserves the SDK's own cancellation while adding the
// command owner's cancellation and a short local upper bound. AfterFunc invokes
// only the cooperative cancel function; its registration is removed when the
// response closes or the RoundTrip fails. No blocking cleanup worker is abandoned.
func (client *Client) cleanupRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	stop := context.AfterFunc(*client.cleanupContext.Load(), cancel)

	return ctx, func() {
		stop()
		cancel()
	}
}

// FetchManifest negotiates MCP with the official SDK and lists every tool page.
// The resulting catalog is a CLI view, not an alternative wire protocol.
// Multi-round-trip retries are disabled: an input-required response is not
// permission to replay an action or fulfill a request without user interaction.
func (client *Client) FetchManifest(ctx context.Context) (*Catalog, error) {
	client.configureRequests(ctx, time.Duration(client.requestTimeout.Load()))

	transport := &mcp.StreamableClientTransport{
		Endpoint: client.Base.String(), MaxRetries: -1, DisableStandaloneSSE: true,
		HTTPClient: &http.Client{
			Transport:     &authenticatedTransport{client: client},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}

	native := mcp.NewClient(&mcp.Implementation{Name: "arkoren", Version: console.VersionInfo()}, &mcp.ClientOptions{
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	client.wire = &wireTransport{transport: transport}

	connected, err := native.Connect(ctx, client.wire, nil)

	if err != nil {
		return nil, client.transportError(ctx, err)
	}

	client.Session = connected

	manifest, err := client.catalog(ctx)

	if err != nil {
		// Discovery's failure is more useful than a secondary shutdown failure.
		_ = connected.Close()

		return nil, client.transportError(ctx, err)
	}

	return manifest, nil
}

// callTool invokes an exact discovered MCP name once. Raw arguments are already
// encoded JSON, not a JSON string. The SDK's JSON v1 boundary receives raw Value
// rather than decoded float64 values, preserving arbitrary numeric precision.
// Neither the SDK nor HTTP transport replays the call.
func (client *Client) callTool(ctx context.Context, name string, body []byte) ([]byte, error) {
	result, err := client.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: jsontext.Value(body)})

	if err != nil {
		return nil, client.transportError(ctx, err)
	}

	if err := client.wire.restoreToolResult(result); err != nil {
		return nil, err
	}

	if result.NeedsInput() {
		return nil, errors.New("tool requires additional input; automatic continuation is not supported")
	}

	if result.IsError {
		return nil, toolFailure(result)
	}

	return toolOutput(result)
}

// toolOutput prefers structured content. A single JSON text block is emitted as
// JSON rather than double encoded; plain text remains a JSON string. Multimodal
// or multiple blocks retain their native content records.
func toolOutput(result *mcp.CallToolResult) ([]byte, error) {
	if result.StructuredContent != nil {
		return json.Marshal(result.StructuredContent, json.Deterministic(true))
	}

	if len(result.Content) == 1 {
		if text, ok := result.Content[0].(*mcp.TextContent); ok {
			if value := jsontext.Value(text.Text); value.IsValid() {
				return []byte(text.Text), nil
			}

			return json.Marshal(text.Text)
		}
	}

	return json.Marshal(result.Content)
}

// toolFailure decodes optional safe Arkoren metadata, retaining the SDK's native
// message for external tools. Failure codes do not imply HTTP authentication.
func toolFailure(result *mcp.CallToolResult) error {
	failure := &APIError{Title: "tool failed"}
	var messages []string

	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			messages = append(messages, text.Text)
		}
	}

	if len(messages) > 0 {
		failure.Title = strings.Join(messages, "\n")
	}

	metadata, ok := result.Meta["arkoren.dev/failure"]

	if !ok {
		return failure
	}

	encoded, err := json.Marshal(metadata)

	if err != nil {
		return fmt.Errorf("decode tool failure: %w", err)
	}

	var record coremcp.Failure

	if err := json.Unmarshal(encoded, &record); err != nil {
		return fmt.Errorf("decode tool failure: %w", err)
	}

	failure.Type, failure.Title, failure.Details = record.Code, cmp.Or(record.Message, failure.Title), record.Details

	var details struct {
		// Fields contains messages keyed by input JSON pointer.
		Fields map[string][]string `json:"fields"`
	}

	if len(record.Details) > 0 {
		if err := json.Unmarshal(record.Details, &details); err != nil {
			return fmt.Errorf("decode failure details: %w", err)
		}
	}

	failure.Fields = details.Fields

	return failure
}

// transportError preserves cancellation and actual HTTP failures while marking
// dial failures for the offline help/version policy.
func (client *Client) transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if failure, ok := errors.AsType[*APIError](err); ok {
		return failure
	}

	if errors.Is(err, context.DeadlineExceeded) {
		timeout := time.Duration(client.requestTimeout.Load())

		return fmt.Errorf("the inspector at %s did not answer within %s (raise -request-timeout): %w", client.Base, timeout, err)
	}

	if failure, ok := errors.AsType[*net.OpError](err); ok && failure.Op == "dial" {
		return &unreachableError{url: client.Base.String(), err: err}
	}

	return err
}

// CompactJSON removes insignificant whitespace without changing invalid input.
func CompactJSON(data []byte) []byte {
	value := jsontext.Value(bytes.Clone(data))

	if err := value.Compact(); err != nil {
		return data
	}

	return value
}
