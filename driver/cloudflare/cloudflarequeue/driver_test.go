package cloudflarequeue_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/queues"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
	"arkoren.dev/driver/cloudflare/cloudflarequeue"
)

const (
	// testAccountID is the account ID the fake API serves.
	testAccountID = "account-1"
	// testToken is the API token the fake API requires.
	testToken = "test-token"
	// mainQueueID identifies the fake API queue used by ordinary deliveries.
	mainQueueID = "queue-main"
	// otherQueueID provides an independent route for conformance isolation checks.
	otherQueueID = "queue-other"
)

// request records one call the fake API received.
type request struct {
	// Path is the URL path below the account, such as "queues/q/messages/ack".
	Path string
	// Authorization is the Authorization header.
	Authorization string
	// Body is the decoded JSON request body.
	Body map[string]any
}

// storedMessage is a message held by the fake API.
type storedMessage struct {
	// id identifies the message across deliveries.
	id string
	// body is the text body.
	body string
	// attempts counts the deliveries so far.
	attempts int
	// availableAt is when the message may be pulled.
	availableAt time.Time
	// leaseID is the current lease, empty when not leased.
	leaseID string
	// leaseUntil is when the current lease ends.
	leaseUntil time.Time
}

// fakeAPI is an httptest stand-in for Cloudflare's push, pull, and ack endpoints.
type fakeAPI struct {
	// server serves the fake API.
	server *httptest.Server
	// mu guards the fields below.
	mu sync.Mutex
	// queues holds the messages of each queue ID.
	queues map[string][]*storedMessage
	// requests lists every call in order.
	requests []request
	// nextID numbers messages and leases.
	nextID int
	// pullStatus overrides every pull's status when nonzero.
	pullStatus int
	// ackWarnings is the warning returned for each acknowledged lease.
	ackWarnings string
}

// newFakeAPI starts a fake API with two empty queues.
func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	api := &fakeAPI{
		queues: map[string][]*storedMessage{mainQueueID: nil, otherQueueID: nil},
	}

	api.server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.server.Close)

	return api
}

// newDriver returns a driver for the fake API with two quickly polled routes.
func newDriver(t *testing.T, api *fakeAPI, opts ...func(*cloudflarequeue.Builder)) *cloudflarequeue.Driver {
	t.Helper()

	client := cloudflare.NewClient(option.WithAPIToken(testToken), option.WithBaseURL(api.server.URL), option.WithMaxRetries(0))
	opts = append([]func(*cloudflarequeue.Builder){
		func(consumer *cloudflarequeue.Builder) {
			consumer.PollInterval(10 * time.Millisecond)
			consumer.MaxPollInterval(50 * time.Millisecond)
		},
	}, opts...)

	driver, err := cloudflarequeue.NewFrom(client, testAccountID, map[string]string{
		"conformance":       mainQueueID,
		"conformance-other": otherQueueID,
	}, opts...)

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// recorded returns the calls whose path ends in suffix.
func (api *fakeAPI) recorded(suffix string) []request {
	api.mu.Lock()
	defer api.mu.Unlock()

	var matched []request

	for _, recorded := range api.requests {
		if strings.HasSuffix(recorded.Path, suffix) {
			matched = append(matched, recorded)
		}
	}

	return matched
}

// stored returns the number of messages left in queueID.
func (api *fakeAPI) stored(queueID string) int {
	api.mu.Lock()
	defer api.mu.Unlock()

	return len(api.queues[queueID])
}

// serve routes a request to the push, pull, or ack handler.
func (api *fakeAPI) serve(writer http.ResponseWriter, httpRequest *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()

	path, ok := strings.CutPrefix(httpRequest.URL.Path, "/accounts/"+testAccountID+"/")
	recorded := request{
		Path:          path,
		Authorization: httpRequest.Header.Get("Authorization"),
	}

	if err := json.UnmarshalRead(httpRequest.Body, &recorded.Body); err != nil {
		recorded.Body = nil
	}

	api.requests = append(api.requests, recorded)

	parts := strings.Split(path, "/")

	if !ok || recorded.Authorization != "Bearer "+testToken || len(parts) < 3 || parts[0] != "queues" {
		respond(writer, http.StatusUnauthorized, nil, "unauthorized")

		return
	}

	if _, found := api.queues[parts[1]]; !found {
		respond(writer, http.StatusNotFound, nil, "queue not found")

		return
	}

	queueID := parts[1]

	switch strings.Join(parts[2:], "/") {
	case "messages":
		api.push(writer, queueID, recorded.Body)
	case "messages/pull":
		api.pull(writer, queueID, recorded.Body)
	case "messages/ack":
		api.ack(writer, queueID, recorded.Body)
	default:
		respond(writer, http.StatusNotFound, nil, "no route")
	}
}

// push stores a text message.
func (api *fakeAPI) push(writer http.ResponseWriter, queueID string, body map[string]any) {
	text, _ := body["body"].(string)
	delay, _ := body["delay_seconds"].(float64)

	api.nextID++
	api.queues[queueID] = append(api.queues[queueID], &storedMessage{
		id:          "message-" + strconv.Itoa(api.nextID),
		body:        text,
		availableAt: time.Now().Add(time.Duration(delay) * time.Second),
	})

	respond(writer, http.StatusOK, map[string]any{}, "")
}

// pull leases up to batch_size available messages.
func (api *fakeAPI) pull(writer http.ResponseWriter, queueID string, body map[string]any) {
	if api.pullStatus != 0 {
		respond(writer, api.pullStatus, nil, "pull failed")

		return
	}

	batch, ok := body["batch_size"].(float64)

	if !ok {
		batch = 5
	}

	visibility, ok := body["visibility_timeout_ms"].(float64)

	if !ok {
		visibility = 30000
	}

	now := time.Now()
	pulled := []map[string]any{}

	for _, message := range api.queues[queueID] {
		if len(pulled) >= int(batch) {
			break
		}

		if message.availableAt.After(now) || message.leaseUntil.After(now) {
			continue
		}

		api.nextID++
		message.attempts++
		message.leaseID = "lease-" + strconv.Itoa(api.nextID)
		message.leaseUntil = now.Add(time.Duration(visibility) * time.Millisecond)
		pulled = append(pulled, map[string]any{
			"id":           message.id,
			"body":         message.body,
			"attempts":     message.attempts,
			"lease_id":     message.leaseID,
			"timestamp_ms": now.UnixMilli(),
			"metadata":     map[string]any{"CF-sourceMessageSource": "api"},
		})
	}

	respond(writer, http.StatusOK, map[string]any{"message_backlog_count": len(api.queues[queueID]), "messages": pulled}, "")
}

// ack deletes acked messages and releases retried ones after their delay.
func (api *fakeAPI) ack(writer http.ResponseWriter, queueID string, body map[string]any) {
	var acked, retried float64
	warnings := map[string]string{}
	leased := func(leaseID string) *storedMessage {
		for _, message := range api.queues[queueID] {
			if message.leaseID == leaseID {
				return message
			}
		}

		return nil
	}

	acks, _ := body["acks"].([]any)

	for _, entry := range acks {
		leaseID, _ := entry.(map[string]any)["lease_id"].(string)
		message := leased(leaseID)

		if message == nil {
			warnings[leaseID] = "lease not found"

			continue
		}

		api.queues[queueID] = slices.DeleteFunc(api.queues[queueID], func(other *storedMessage) bool {
			return other == message
		})
		acked++

		if api.ackWarnings != "" {
			warnings[leaseID] = api.ackWarnings
		}
	}

	retries, _ := body["retries"].([]any)

	for _, entry := range retries {
		fields, _ := entry.(map[string]any)
		leaseID, _ := fields["lease_id"].(string)
		delay, _ := fields["delay_seconds"].(float64)
		message := leased(leaseID)

		if message == nil {
			warnings[leaseID] = "lease not found"

			continue
		}

		message.leaseID = ""
		message.leaseUntil = time.Time{}
		message.availableAt = time.Now().Add(time.Duration(delay) * time.Second)
		retried++
	}

	respond(writer, http.StatusOK, map[string]any{"ackCount": acked, "retryCount": retried, "warnings": warnings}, "")
}

// respond writes a Cloudflare API envelope.
func respond(writer http.ResponseWriter, status int, result any, message string) {
	envelope := map[string]any{"success": status == http.StatusOK, "errors": []any{}, "messages": []any{}, "result": result}

	if message != "" {
		envelope["errors"] = []any{map[string]any{"code": 10000, "message": message}}
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.MarshalWrite(writer, envelope)
}

// mustJSON returns the JSON encoding of value.
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	encoded, err := json.Marshal(value)

	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

// TestConformanceAgainstFakeAPI runs the shared suite against the local fake.
func TestConformanceAgainstFakeAPI(t *testing.T) {
	queuetest.Run(t, func(t *testing.T) queuetest.Driver {
		driver := newDriver(t, newFakeAPI(t))

		return queuetest.Driver{
			Dispatcher: driver,
			Consumer:   driver,
		}
	})
}

// integrationLease is short enough for successive integration subtests to drain.
const integrationLease = 2 * time.Second

// TestConformanceAgainstCloudflare runs against two real queues only with
// explicit credentials and outside short mode. Every message in both queues
// is deleted before each subtest; this is a destructive cloud integration test.
func TestConformanceAgainstCloudflare(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	token, accountID := os.Getenv("CLOUDFLARE_API_TOKEN"), os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	queueID, otherID := os.Getenv("CLOUDFLARE_QUEUE_ID"), os.Getenv("CLOUDFLARE_OTHER_QUEUE_ID")

	if token == "" || accountID == "" || queueID == "" || otherID == "" {
		t.Skip("CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_QUEUE_ID and CLOUDFLARE_OTHER_QUEUE_ID are required")
	}

	queuetest.Run(t, func(t *testing.T) queuetest.Driver {
		driver, err := cloudflarequeue.New(token, accountID,
			map[string]string{"conformance": queueID, "conformance-other": otherID},
			func(consumer *cloudflarequeue.Builder) {
				consumer.VisibilityTimeout(integrationLease)
				consumer.PollInterval(500 * time.Millisecond)
				consumer.MaxPollInterval(2 * time.Second)
			},
		)

		if err != nil {
			t.Fatal(err)
		}

		drain(t, driver.Client(), accountID, queueID)
		drain(t, driver.Client(), accountID, otherID)

		return queuetest.Driver{
			Dispatcher: driver,
			Consumer:   driver,
		}
	})
}

// drain deletes every message, including still-leased integration fixtures.
func drain(t *testing.T, client *cloudflare.Client, accountID, queueID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	for quietSince := time.Now(); time.Since(quietSince) < 2*integrationLease; {
		pulled, err := client.Queues.Messages.Pull(ctx, queueID, queues.MessagePullParams{
			AccountID:           cloudflare.F(accountID),
			BatchSize:           cloudflare.F(100.0),
			VisibilityTimeoutMs: cloudflare.F(float64(integrationLease.Milliseconds())),
		}, option.WithMaxRetries(2))

		if err != nil {
			t.Fatalf("drain %s: %v", queueID, err)
		}

		if len(pulled.Messages) == 0 {
			time.Sleep(500 * time.Millisecond)

			continue
		}

		acks := make([]queues.MessageAckParamsAck, len(pulled.Messages))

		for i, message := range pulled.Messages {
			acks[i] = queues.MessageAckParamsAck{
				LeaseID: cloudflare.F(message.LeaseID),
			}
		}

		if _, err := client.Queues.Messages.Ack(ctx, queueID, queues.MessageAckParams{
			AccountID: cloudflare.F(accountID),
			Acks:      cloudflare.F(acks),
		}); err != nil {
			t.Fatalf("drain %s: %v", queueID, err)
		}

		quietSince = time.Now()
	}
}

// testTimeout bounds every wait for an asynchronous event.
const testTimeout = 10 * time.Second

// TestNewCapturesRoutingBeforeCallbacks observes dispatch's native HTTP route
// after a callback mutates the caller's original map during construction.
func TestNewCapturesRoutingBeforeCallbacks(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	routes := map[string]string{"conformance": mainQueueID}

	driver, err := cloudflarequeue.New(testToken, testAccountID, routes, func(consumer *cloudflarequeue.Builder) {
		consumer.Endpoint(api.server.URL)
		routes["conformance"] = otherQueueID
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Dispatch(t.Context(), queue.Message{Queue: "conformance", Name: "work"}); err != nil {
		t.Fatal(err)
	}

	requests := api.recorded("/messages")

	if len(requests) != 1 || requests[0].Path != "queues/"+mainQueueID+"/messages" {
		t.Fatalf("dispatch used callback-mutated routing: %+v", requests)
	}
}

// TestNewFromCapturesRoutingBeforeCallbacks preserves the same input-capture
// boundary while borrowing an application-owned native client unchanged.
func TestNewFromCapturesRoutingBeforeCallbacks(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	native := cloudflare.NewClient(option.WithAPIToken(testToken), option.WithBaseURL(api.server.URL))
	routes := map[string]string{"conformance": mainQueueID}

	driver, err := cloudflarequeue.NewFrom(native, testAccountID, routes, func(consumer *cloudflarequeue.Builder) {
		routes["conformance"] = otherQueueID
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Dispatch(t.Context(), queue.Message{Queue: "conformance", Name: "work"}); err != nil {
		t.Fatal(err)
	}

	requests := api.recorded("/messages")

	if len(requests) != 1 || requests[0].Path != "queues/"+mainQueueID+"/messages" {
		t.Fatalf("dispatch used callback-mutated routing: %+v", requests)
	}

	if driver.Client() != native {
		t.Fatal("NewFrom changed native client identity")
	}
}

// TestSettlementRequiresConfirmedCount rejects a native warning with zero
// acknowledgments, without marking the delivery settled before a later success.
func TestSettlementRequiresConfirmedCount(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	first := true
	transport := queueTransport(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/ack") && first {
			first = false

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"success":true,"result":{"ackCount":0,"retryCount":0,"warnings":{"lease-2":"expired"}}}`)),
			}, nil
		}

		return http.DefaultTransport.RoundTrip(request)
	})
	native := cloudflare.NewClient(option.WithAPIToken(testToken), option.WithBaseURL(api.server.URL),
		option.WithHTTPClient(&http.Client{Transport: transport}), option.WithMaxRetries(0))
	driver, err := cloudflarequeue.NewFrom(native, testAccountID, map[string]string{"conformance": mainQueueID})

	if err != nil {
		t.Fatal(err)
	}

	dispatchTestMessage(t, driver)

	err = consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		if err := delivery.Acknowledge(ctx); err == nil {
			return errors.New("zero acknowledged leases reported success")
		}

		return delivery.Acknowledge(ctx)
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestRetryRequiresMatchingCount ensures an acknowledgment count cannot confirm
// a retry, and an unconfirmed retry leaves the lease open for a later attempt.
func TestRetryRequiresMatchingCount(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	first := true
	transport := queueTransport(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/ack") && first {
			first = false

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"success":true,"result":{"ackCount":1,"retryCount":0}}`)),
			}, nil
		}

		return http.DefaultTransport.RoundTrip(request)
	})
	native := cloudflare.NewClient(option.WithAPIToken(testToken), option.WithBaseURL(api.server.URL),
		option.WithHTTPClient(&http.Client{Transport: transport}), option.WithMaxRetries(0))
	driver, err := cloudflarequeue.NewFrom(native, testAccountID, map[string]string{"conformance": mainQueueID})

	if err != nil {
		t.Fatal(err)
	}

	dispatchTestMessage(t, driver)

	err = consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		if err := delivery.Retry(ctx, time.Minute); err == nil {
			return errors.New("acknowledgment count confirmed a retry")
		}

		return delivery.Retry(ctx, time.Minute)
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestConsumeStopsBeforeRemainingBatch ensures cancellation after one handler
// cannot start another already-pulled job during shutdown.
func TestConsumeStopsBeforeRemainingBatch(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, newFakeAPI(t), func(consumer *cloudflarequeue.Builder) {
		consumer.BatchSize(2)
	})

	dispatchTestMessage(t, driver)
	dispatchTestMessage(t, driver)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	calls := 0

	err := driver.Consume(ctx, "conformance", func(context.Context, queue.Delivery) error {
		calls++
		cancel()

		return nil
	})

	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, handlers = %d; want canceled after one", err, calls)
	}
}

// queueTransport substitutes deterministic native HTTP responses while allowing
// other requests to reach the test's local Cloudflare protocol fixture.
type queueTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements the SDK transport seam without interpreting driver state.
// Each fixture is responsible for keeping requests local or fully synthetic.
func (transport queueTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// consumeOne runs Consume on the main queue until the handler has been called
// once, and returns the error the handler returned.
func consumeOne(t *testing.T, driver *cloudflarequeue.Driver, handler queue.DeliveryHandler) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()

	result := make(chan error, 1)

	err := driver.Consume(ctx, "conformance", func(ctx context.Context, delivery queue.Delivery) error {
		result <- handler(ctx, delivery)
		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, want context.Canceled after the handler ran", err)
	}

	select {
	case err := <-result:
		return err
	default:
		t.Fatal("the handler was not called")

		return nil
	}
}

// dispatchTestMessage dispatches a valid message to the main queue.
func dispatchTestMessage(t *testing.T, driver *cloudflarequeue.Driver) queue.Message {
	t.Helper()

	message := queue.Message{
		ID:      "job-1",
		Name:    "send-welcome",
		Queue:   "conformance",
		Payload: []byte(`{"n":1}`),
	}

	if err := driver.Dispatch(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	return message
}

// TestNewRequiresAPIToken checks that missing authentication is rejected
// during construction, before the driver can make requests.
func TestNewRequiresAPIToken(t *testing.T) {
	t.Parallel()

	_, err := cloudflarequeue.New("", testAccountID, map[string]string{"emails": "queue"})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Fatalf("New without a token = %v, want ErrInvalidConfig", err)
	}
}

// TestNewFromRejectsNilClient checks the native-client guard returns the
// package configuration sentinel instead of publishing an unusable driver.
func TestNewFromRejectsNilClient(t *testing.T) {
	t.Parallel()

	_, err := cloudflarequeue.NewFrom(nil, testAccountID, map[string]string{"emails": "queue"})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Fatalf("NewFrom without a client = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsInvalidSettings checks a blank account ID is rejected at
// construction rather than becoming an invalid native request later.
func TestNewRejectsInvalidSettings(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"emails": "queue"}

	_, err := cloudflarequeue.New("token", " ", queues)

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "blank account ID", err)
	}
}

// TestNewRejectsMissingQueues checks that routing is required independently
// of authentication and the account identifier.
func TestNewRejectsMissingQueues(t *testing.T) {
	t.Parallel()

	_, err := cloudflarequeue.New("token", "account", nil)

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "no queues", err)
	}
}

// TestNewRejectsBlankQueueName checks whitespace-only routing keys fail
// construction even when the native queue ID is present.
func TestNewRejectsBlankQueueName(t *testing.T) {
	t.Parallel()

	_, err := cloudflarequeue.New("token", "account", map[string]string{" ": "queue"})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "blank queue name", err)
	}
}

// TestNewRejectsBlankQueueID checks a named route must identify a native queue
// before the driver can publish or consume it.
func TestNewRejectsBlankQueueID(t *testing.T) {
	t.Parallel()

	_, err := cloudflarequeue.New("token", "account", map[string]string{"emails": ""})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "blank queue ID", err)
	}
}

// TestNewRejectsBatchSizeAboveLimit checks native batch limits are validated
// during construction rather than deferred to a pull request.
func TestNewRejectsBatchSizeAboveLimit(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"emails": "queue"}

	_, err := cloudflarequeue.New("token", "account", queues, func(consumer *cloudflarequeue.Builder) {
		consumer.BatchSize(101)
	})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "batch size above 100", err)
	}
}

// TestNewRejectsNegativeBatchSize checks a negative batch setting is invalid,
// unlike zero which selects the existing default.
func TestNewRejectsNegativeBatchSize(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"emails": "queue"}

	_, err := cloudflarequeue.New("token", "account", queues, func(consumer *cloudflarequeue.Builder) {
		consumer.BatchSize(-1)
	})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "negative batch size", err)
	}
}

// TestNewRejectsVisibilityAboveLimit checks a lease longer than the native
// twelve-hour maximum fails before publication.
func TestNewRejectsVisibilityAboveLimit(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"emails": "queue"}

	_, err := cloudflarequeue.New("token", "account", queues, func(consumer *cloudflarequeue.Builder) {
		consumer.VisibilityTimeout(13 * time.Hour)
	})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "visibility above 12 hours", err)
	}
}

// TestNewRejectsSubMillisecondVisibility checks positive lease durations too
// small for the native millisecond representation are rejected.
func TestNewRejectsSubMillisecondVisibility(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"emails": "queue"}

	_, err := cloudflarequeue.New("token", "account", queues, func(consumer *cloudflarequeue.Builder) {
		consumer.VisibilityTimeout(time.Microsecond)
	})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "visibility below 1ms", err)
	}
}

// TestNewRejectsNegativePollInterval checks a negative polling delay cannot
// publish a driver with invalid backoff settings.
func TestNewRejectsNegativePollInterval(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"emails": "queue"}

	_, err := cloudflarequeue.New("token", "account", queues, func(consumer *cloudflarequeue.Builder) {
		consumer.PollInterval(-time.Second)
	})

	if !errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
		t.Errorf("%s: err = %v, want ErrInvalidConfig", "negative poll interval", err)
	}
}

// TestNewAcceptsValidSettings exercises upper polling bounds and verifies
// native-client access and the stable telemetry system name without I/O.
func TestNewAcceptsValidSettings(t *testing.T) {
	t.Parallel()

	driver, err := cloudflarequeue.New("token", "account", map[string]string{"emails": "queue"},
		func(consumer *cloudflarequeue.Builder) {
			consumer.BatchSize(100)
			consumer.VisibilityTimeout(12 * time.Hour)
			consumer.Endpoint("http://127.0.0.1:1/")
			consumer.Logger(slog.New(slog.DiscardHandler))
		},
	)

	if err != nil {
		t.Fatal(err)
	}

	if driver.Client() == nil {
		t.Error("Client() = nil")
	}

	if got := driver.MessagingSystem(); got != "cloudflare_queues" {
		t.Errorf("MessagingSystem() = %q, want cloudflare_queues", got)
	}
}

// TestCallbacksReplaceSettingsInOrder verifies validation observes the final
// draft after both callbacks rather than rejecting an intermediate setting.
func TestCallbacksReplaceSettingsInOrder(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := cloudflarequeue.New("token", "account", map[string]string{"emails": "queue"},
		func(consumer *cloudflarequeue.Builder) {
			calls++
			consumer.BatchSize(-1)
		}, func(consumer *cloudflarequeue.Builder) {
			calls++
			consumer.BatchSize(3)
		})

	if err != nil || calls != 2 {
		t.Fatalf("construction = %v, callbacks = %d", err, calls)
	}
}

// TestDispatchPushesJSONEnvelopeAsText checks authentication and the native
// text body while preserving the queue envelope's name and payload.
func TestDispatchPushesJSONEnvelopeAsText(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)

	dispatched := dispatchTestMessage(t, driver)

	pushes := api.recorded("queues/" + mainQueueID + "/messages")

	if len(pushes) != 1 {
		t.Fatalf("got %d push requests, want 1", len(pushes))
	}

	if pushes[0].Authorization != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want the bearer token", pushes[0].Authorization)
	}

	if pushes[0].Body["content_type"] != "text" {
		t.Errorf("content_type = %v, want text", pushes[0].Body["content_type"])
	}

	var envelope queue.Message

	if err := json.Unmarshal([]byte(pushes[0].Body["body"].(string)), &envelope); err != nil {
		t.Fatal(err)
	}

	if envelope.Name != dispatched.Name || !bytes.Equal(envelope.Payload, dispatched.Payload) {
		t.Errorf("pushed envelope %+v, want %+v", envelope, dispatched)
	}
}

// TestDispatchUnknownQueueFails verifies unmapped queues fail locally with
// ErrUnknownQueue and do not produce a native API request.
func TestDispatchUnknownQueueFails(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)

	err := newDriver(t, api).Dispatch(t.Context(), queue.Message{
		Name:  "send-welcome",
		Queue: "missing",
	})

	if !errors.Is(err, cloudflarequeue.ErrUnknownQueue) {
		t.Fatalf("Dispatch = %v, want ErrUnknownQueue", err)
	}

	if got := len(api.recorded("/messages")); got != 0 {
		t.Errorf("%d requests sent, want none", got)
	}
}

// TestDispatchReportsAPIError checks that a rejected API token surfaces as
// a dispatch failure rather than a successful enqueue.
func TestDispatchReportsAPIError(t *testing.T) {
	t.Parallel()

	client := cloudflare.NewClient(option.WithAPIToken("wrong"), option.WithBaseURL(newFakeAPI(t).server.URL), option.WithMaxRetries(0))

	driver, err := cloudflarequeue.NewFrom(client, testAccountID, map[string]string{"conformance": mainQueueID})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Dispatch(t.Context(), queue.Message{
		Name:  "send-welcome",
		Queue: "conformance",
	}); err == nil {
		t.Fatal("Dispatch with a rejected token succeeded")
	}
}

// TestConsumeRejectsNilHandler checks callback validation before polling,
// keeping missing handlers on the documented ErrInvalidHandler path.
func TestConsumeRejectsNilHandler(t *testing.T) {
	t.Parallel()

	err := newDriver(t, newFakeAPI(t)).Consume(t.Context(), "conformance", nil)

	if !errors.Is(err, cloudflarequeue.ErrInvalidHandler) {
		t.Fatalf("Consume = %v, want ErrInvalidHandler", err)
	}
}

// TestConsumeUnknownQueueFails verifies queue routing is checked before
// native polling, even when a valid delivery handler is supplied.
func TestConsumeUnknownQueueFails(t *testing.T) {
	t.Parallel()

	err := newDriver(t, newFakeAPI(t)).Consume(t.Context(), "missing", func(context.Context, queue.Delivery) error {
		return nil
	})

	if !errors.Is(err, cloudflarequeue.ErrUnknownQueue) {
		t.Fatalf("Consume = %v, want ErrUnknownQueue", err)
	}
}

// TestConsumePullsWithConfiguredBatchAndLease checks conversion of the
// published polling settings into native batch and millisecond lease values.
func TestConsumePullsWithConfiguredBatchAndLease(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api, func(consumer *cloudflarequeue.Builder) {
		consumer.BatchSize(7)
		consumer.VisibilityTimeout(90 * time.Second)
	})

	dispatchTestMessage(t, driver)

	consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Acknowledge(ctx)
	})

	pulls := api.recorded("/messages/pull")

	if len(pulls) == 0 {
		t.Fatal("no pull request")
	}

	if pulls[0].Body["batch_size"] != float64(7) || pulls[0].Body["visibility_timeout_ms"] != float64(90000) {
		t.Errorf("pull body %v, want batch_size 7 and visibility_timeout_ms 90000", pulls[0].Body)
	}
}

// TestConsumeDeliversCloudflareIdentity verifies broker identity and attempt
// metadata replace untrusted publisher values in the delivered envelope.
func TestConsumeDeliversCloudflareIdentity(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, newFakeAPI(t))
	dispatched := dispatchTestMessage(t, driver)
	var got queue.Message

	consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		got = delivery.Message()

		return delivery.Acknowledge(ctx)
	})

	if got.ID != "message-1" || got.ID == dispatched.ID {
		t.Errorf("ID = %q, want Cloudflare's message-1", got.ID)
	}

	if got.Attempts != 1 || got.Queue != "conformance" {
		t.Errorf("Attempts = %d, Queue = %q, want 1 and conformance", got.Attempts, got.Queue)
	}
}

// TestConsumeReturnsPullError checks native polling failures terminate
// consumption with an error distinct from context cancellation.
func TestConsumeReturnsPullError(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.pullStatus = 500

	err := newDriver(t, api).Consume(t.Context(), "conformance", func(context.Context, queue.Delivery) error {
		return nil
	})

	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, want the pull error", err)
	}
}

// TestAcknowledgeSendsLease verifies acknowledgment sends the broker lease
// identity and removes the corresponding message from the fake backend.
func TestAcknowledgeSendsLease(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)
	dispatchTestMessage(t, driver)

	consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Acknowledge(ctx)
	})

	acks := api.recorded("/messages/ack")

	if len(acks) != 1 || !strings.Contains(string(mustJSON(t, acks[0].Body["acks"])), `"lease_id":"lease-`) {
		t.Fatalf("ack requests %+v, want one carrying a lease ID", acks)
	}

	if api.stored(mainQueueID) != 0 {
		t.Error("the acknowledged message is still stored")
	}
}

// TestRetryRoundsDelayUpToSeconds checks a fractional retry delay rounds up,
// preventing redelivery before the caller's requested interval.
func TestRetryRoundsDelayUpToSeconds(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)
	dispatchTestMessage(t, driver)

	consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Retry(ctx, 1500*time.Millisecond)
	})

	acks := api.recorded("/messages/ack")

	if len(acks) != 1 || !strings.Contains(string(mustJSON(t, acks[0].Body["retries"])), `"delay_seconds":2`) {
		t.Fatalf("ack requests %+v, want one retry with delay_seconds 2", acks)
	}
}

// TestRetryRejectsUnsupportedDelay preserves the contract's positive delay
// lower bound and leaves the delivery available for a different settlement.
func TestRetryRejectsUnsupportedDelay(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)
	dispatchTestMessage(t, driver)

	err := consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		if err := delivery.Retry(ctx, 48*time.Hour); err == nil {
			return errors.New("retry silently shortened the requested delay")
		}

		return delivery.Acknowledge(ctx)
	})

	if err != nil {
		t.Fatal(err)
	}

	acks := api.recorded("/messages/ack")

	if len(acks) != 1 || acks[0].Body["retries"] != nil {
		t.Fatalf("ack requests %+v, want only the later acknowledgment", acks)
	}
}

// TestRejectSendsNothingAndLeavesLease checks rejection leaves the native
// lease to expire without sending an acknowledgment or removing the message.
func TestRejectSendsNothingAndLeavesLease(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)
	dispatchTestMessage(t, driver)

	consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Reject(ctx)
	})

	if got := len(api.recorded("/messages/ack")); got != 0 {
		t.Errorf("%d ack requests, want none", got)
	}

	if api.stored(mainQueueID) != 1 {
		t.Error("the rejected message was removed, want it left to its lease")
	}
}

// TestFailAcknowledges checks terminal failure settles the native delivery
// instead of allowing an unrecoverable message to be redelivered.
func TestFailAcknowledges(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)
	dispatchTestMessage(t, driver)

	consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Fail(ctx, errors.New("boom"))
	})

	if api.stored(mainQueueID) != 0 {
		t.Error("the failed message is still stored")
	}
}

// TestSettleTwiceReturnsErrDeliverySettled verifies successful settlement
// prevents a later operation from settling the same lease again.
func TestSettleTwiceReturnsErrDeliverySettled(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, newFakeAPI(t))
	dispatchTestMessage(t, driver)

	err := consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		if err := delivery.Reject(ctx); err != nil {
			return err
		}

		return delivery.Acknowledge(ctx)
	})

	if !errors.Is(err, cloudflarequeue.ErrDeliverySettled) {
		t.Fatalf("second settlement = %v, want ErrDeliverySettled", err)
	}
}

// TestFailedSettlementCanBeRepeated checks a canceled acknowledgment does
// not mark the lease settled and a fresh attempt can still remove it.
func TestFailedSettlementCanBeRepeated(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)
	dispatchTestMessage(t, driver)

	err := consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		canceled, cancel := context.WithCancel(ctx)
		cancel()

		if err := delivery.Acknowledge(canceled); !errors.Is(err, context.Canceled) {
			return errors.New("settlement with a canceled context did not fail")
		}

		return delivery.Acknowledge(ctx)
	})

	if err != nil {
		t.Fatal(err)
	}

	if api.stored(mainQueueID) != 0 {
		t.Error("the message is still stored after the second attempt")
	}
}

// TestSettlementWarningDoesNotFail verifies successful native responses
// with warnings remain successful delivery settlements.
func TestSettlementWarningDoesNotFail(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.ackWarnings = "lease expired"
	driver := newDriver(t, api)
	dispatchTestMessage(t, driver)

	err := consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Acknowledge(ctx)
	})

	if err != nil {
		t.Fatalf("Acknowledge with a warning = %v, want success", err)
	}
}

// TestUndecodableBodyIsFailedWithoutReachingHandler checks malformed bodies
// are dropped while the next valid envelope still reaches the application.
func TestUndecodableBodyIsFailedWithoutReachingHandler(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	driver := newDriver(t, api)
	api.queues[mainQueueID] = append(api.queues[mainQueueID], &storedMessage{
		id:   "broken",
		body: "not json",
	})
	dispatchTestMessage(t, driver)

	var names []string
	consumeOne(t, driver, func(ctx context.Context, delivery queue.Delivery) error {
		names = append(names, delivery.Message().Name)

		return delivery.Acknowledge(ctx)
	})

	if len(names) != 1 || names[0] != "send-welcome" {
		t.Errorf("handler saw %v, want only the valid message", names)
	}

	if api.stored(mainQueueID) != 0 {
		t.Error("the undecodable message was not removed")
	}
}
