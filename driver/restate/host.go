package restate

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
)

// ErrStarted reports a [Host.Bind] or [Host.Start] after [Host.Start].
// A host has one startup lifetime; construct another host rather than rebinding.
//
// Example:
//
//	if err := host.Start(ctx); errors.Is(err, ErrStarted) {
//		return err
//	}
var ErrStarted = errors.New("restate host is started")

// ErrNoIdentityKeys reports a [Host.Start] that would open a service endpoint
// with neither [Builder.IdentityKeys] nor [Builder.AllowInsecure]. Without keys
// anyone who reaches the listener can invoke the bound handlers.
//
// Example:
//
//	if err := host.Start(ctx); errors.Is(err, ErrNoIdentityKeys) {
//		return err // Reconstruct with the server's request-identity public keys.
//	}
var ErrNoIdentityKeys = errors.New("restate endpoint has no identity keys")

// ErrClientOnly reports a [Host.Bind] on a host with [Builder.ClientOnly].
// Such a host deliberately has no endpoint; use it for ingress/admin calls.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.ClientOnly(true)
//	})
//	if err := host.Bind(restatesdk.NewService("Example")); !errors.Is(err, ErrClientOnly) {
//		panic(err)
//	}
var ErrClientOnly = errors.New("restate host is client only")

const (
	// readHeaderTimeout bounds slow request headers without limiting invocation
	// bodies, which may stream for the lifetime of a Restate attempt.
	readHeaderTimeout = 10 * time.Second

	// idleTimeout retires unused keep-alive connections, not active invocations.
	idleTimeout = 120 * time.Second
)

// Host serves one Restate service endpoint and reaches the Restate ingress
// and admin API. Drivers bind service definitions with [Host.Bind] before
// [Host.Start].
// The host owns its endpoint listener, not the shared HTTP client. Register Stop
// immediately after construction so startup rollback also releases the listener.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.ClientOnly(true)
//	})
//	if err := host.Start(context.Background()); err != nil {
//		panic(err)
//	}
//	_ = host.Ingress()
type Host struct {
	// config is the configuration with defaults applied.
	config Config

	// client is used for admin calls.
	client *http.Client

	// ingress is the ingress client.
	ingress *ingress.Client

	// mu guards the fields below.
	mu sync.Mutex

	// definitions are the bound service definitions.
	definitions []restatesdk.ServiceDefinition

	// listener is the endpoint listener, set by Start.
	listener net.Listener

	// server is the endpoint server, set by Start. Nil when the host serves
	// nothing.
	server *http.Server

	// started is set by Start.
	started bool
}

// New returns a Host without performing I/O. Construction callbacks run once,
// synchronously in order on a fresh [Builder]; nil entries panic. Identity keys
// are copied at capture and publication; the HTTP client and logger stay shared.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.ClientOnly(true)
//	})
//	_ = host.Addr() // Nil until a serving host starts.
func New(configure ...BuilderFunc) *Host {
	config := buildSettings(configure...)
	config.Addr = cmp.Or(config.Addr, ":9080")
	config.IngressURL = cmp.Or(config.IngressURL, "http://localhost:8080")
	config.AdminURL = cmp.Or(config.AdminURL, "http://localhost:9070")
	config.AdvertisedURL = cmp.Or(config.AdvertisedURL, "http://localhost"+config.Addr)
	config.HTTPClient = cmp.Or(config.HTTPClient, &http.Client{})
	config.Logger = cmp.Or(config.Logger, slog.Default())

	ingressOptions := []ingress.ClientOption{ingress.WithHttpClient(config.HTTPClient)}

	if config.AuthKey != "" {
		ingressOptions = append(ingressOptions, ingress.WithAuthKey(config.AuthKey))
	}

	return &Host{
		config:  config,
		client:  config.HTTPClient,
		ingress: ingress.NewClient(config.IngressURL, ingressOptions...),
	}
}

// Bind adds a service definition to the endpoint. It returns [ErrStarted]
// once [Host.Start] has run and [ErrClientOnly] on a client-only host.
// Native definitions remain shared; finish editing them before startup.
//
// Example:
//
//	host := New()
//	if err := host.Bind(restatesdk.NewService("Example")); err != nil {
//		panic(err)
//	}
func (host *Host) Bind(definition restatesdk.ServiceDefinition) error {
	host.mu.Lock()
	defer host.mu.Unlock()

	if host.started {
		return fmt.Errorf("bind: %w", ErrStarted)
	}

	if host.config.ClientOnly {
		return fmt.Errorf("bind: %w", ErrClientOnly)
	}

	host.definitions = append(host.definitions, definition)

	return nil
}

// Ping checks that the Restate admin API is healthy.
// It does not prove ingress reachability or that this endpoint is registered.
// The caller bounds readiness I/O with ctx; requests without a deadline use
// the host's 30-second administrative timeout.
//
// Example:
//
//	if err := host.Ping(ctx); err != nil {
//		return err
//	}
func (host *Host) Ping(ctx context.Context) error {
	response, err := host.Admin(ctx, http.MethodGet, "/health", nil)

	if err != nil {
		return fmt.Errorf("ping restate: %w", err)
	}

	// Health is determined by the status; closing its response is best-effort cleanup.
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ping restate: status %d", response.StatusCode)
	}

	return nil
}

// Ingress returns the Restate ingress client.
// It is available before Start. Configure the shared HTTP client before
// constructing the host; endpoint Stop does not close that client.
//
// Example:
//
//	host := New()
//	_ = host.Ingress() // Pass to SDK ingress.ServiceSend/ObjectSend/WorkflowSend.
func (host *Host) Ingress() *ingress.Client {
	return host.ingress
}

// Addr returns the address the endpoint listens on, or nil before
// [Host.Start]. It reports the real port when [Builder.Addr] uses port 0.
// Client-only hosts also return nil; after Stop the address remains diagnostic
// information, not proof that the endpoint still accepts connections.
//
// Example:
//
//	host := New()
//	if address := host.Addr(); address != nil {
//		fmt.Println(address.String())
//	}
func (host *Host) Addr() net.Addr {
	host.mu.Lock()
	defer host.mu.Unlock()

	if host.listener == nil {
		return nil
	}

	return host.listener.Addr()
}

// Admin sends a request to the admin API, with the bearer token of
// [Builder.AdminAuthKey] when set. The path starts with a slash. A non-nil body
// is sent as JSON. The caller closes the response body.
// Non-2xx responses are returned unchanged for the caller to interpret. Without
// a context deadline, a 30-second timeout lasts until the response body closes.
//
// Example:
//
//	response, err := host.Admin(ctx, http.MethodGet, "/health", nil)
//	if err != nil {
//		return err
//	}
//	defer response.Body.Close()
//	if response.StatusCode != http.StatusOK {
//		return fmt.Errorf("admin health: status %d", response.StatusCode)
//	}
func (host *Host) Admin(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	return host.admin(ctx, method, path, body, nil)
}

// admin is [Host.Admin] with extra request headers.
func (host *Host) admin(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	var reader io.Reader

	if body != nil {
		reader = bytes.NewReader(body)
	}

	cancel := func() {
		// The request already has a deadline, so no derived context needs cleanup.
	}

	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, adminTimeout)
	}

	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(host.config.AdminURL, "/")+path, reader)

	if err != nil {
		cancel()

		return nil, fmt.Errorf("build admin request: %w", err)
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	for key, value := range headers {
		request.Header.Set(key, value)
	}

	if host.config.AdminAuthKey != "" {
		request.Header.Set("Authorization", "Bearer "+host.config.AdminAuthKey)
	}

	response, err := host.client.Do(request)

	if err != nil {
		cancel()

		return nil, fmt.Errorf("admin %s %s: %w", method, path, err)
	}

	response.Body = cancelBody{
		ReadCloser: response.Body,
		cancel:     cancel,
	}

	return response, nil
}

// adminTimeout bounds an admin request whose context has no deadline.
const adminTimeout = 30 * time.Second

// maxQueryResponse caps the size of a query response.
const maxQueryResponse = 16 << 20

// cancelBody releases the request context when the body closes.
type cancelBody struct {
	// ReadCloser is the response body whose close operation also releases the
	// request context.
	io.ReadCloser

	// cancel releases the derived context when the owning operation ends.
	cancel context.CancelFunc
}

// Close closes the body and releases the request context.
func (cancelbody cancelBody) Close() error {
	err := cancelbody.ReadCloser.Close()
	cancelbody.cancel()

	return err
}

// Query runs a SQL statement against the introspection API of the admin
// service and returns the rows, each keyed by column name. The statement
// reads the sys_* tables, for example sys_invocation.
// Responses are limited to 16 MiB; transport, status, and decoding failures are
// errors, not empty query results. Restrict admin credentials and never expose
// arbitrary query text to untrusted callers.
//
// Example:
//
//	rows, err := host.Query(ctx, "SELECT id FROM sys_invocation LIMIT 10")
//	if err != nil {
//		return err
//	}
//	_ = rows
func (host *Host) Query(ctx context.Context, sql string) ([]map[string]any, error) {
	body, err := json.Marshal(map[string]string{"query": sql})

	if err != nil {
		return nil, fmt.Errorf("encode query: %w", err)
	}

	response, err := host.admin(ctx, http.MethodPost, "/query", body, map[string]string{"Accept": "application/json"})

	if err != nil {
		return nil, err
	}

	// Status and body reads determine the result; closing the response is best-effort cleanup.
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, maxQueryResponse))

	if err != nil {
		return nil, fmt.Errorf("read query response: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("restate query: status %d: %s", response.StatusCode, truncate(data, 512))
	}

	var result struct {
		// Rows contains the rows decoded from the server response.
		Rows []map[string]any `json:"rows"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode query response: %w", err)
	}

	return result.Rows, nil
}

// truncate returns at most limit bytes of data as a string.
func truncate(data []byte, limit int) string {
	return string(data[:min(len(data), limit)])
}

// register registers the advertised URL as a deployment. Unless
// [Builder.RegisterForce] is set it sends force=false, so a conflicting
// deployment is an error instead of an overwrite.
func (host *Host) register(ctx context.Context) error {
	payload := map[string]any{"uri": host.config.AdvertisedURL, "force": host.config.RegisterForce}

	if host.config.RegisterBreaking {
		payload["breaking"] = true
	}

	body, err := json.Marshal(payload)

	if err != nil {
		return fmt.Errorf("encode registration: %w", err)
	}

	response, err := host.Admin(ctx, http.MethodPost, "/deployments", body)

	if err != nil {
		return fmt.Errorf("register restate deployment: %w", err)
	}

	// Registration is decided by the status; closing its response is best-effort cleanup.
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512)) //nolint:errcheck // best-effort error detail

		return fmt.Errorf("register restate deployment: status %d: %s", response.StatusCode, detail)
	}

	host.config.Logger.InfoContext(ctx, "restate deployment registered", slog.String("uri", host.config.AdvertisedURL))

	return nil
}
