package natsqueue_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/driver/nats/natsqueue"
)

// TestStartCancellationReachesStreamLookup observes the native request boundary:
// an API server that never answers must not keep Start alive after cancellation.
func TestStartCancellationReachesStreamLookup(t *testing.T) {
	t.Parallel()

	requested := make(chan struct{})
	endpoint := newAPIServer(t, func(subject string) []byte {
		if strings.Contains(subject, "STREAM.INFO") {
			close(requested)
		}

		return nil
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var group sync.WaitGroup
	result := make(chan error, 1)
	group.Go(func() {
		result <- driver.Start(ctx)
	})
	t.Cleanup(group.Wait)

	<-requested
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want canceled", err)
	}
}

// TestStartRejectsHeadersOnlyConsumer protects queued bodies from being mistaken
// for malformed envelopes and terminally acknowledged after a successful Start.
func TestStartRejectsHeadersOnlyConsumer(t *testing.T) {
	t.Parallel()

	endpoint := newAPIServer(t, func(subject string) []byte {
		if strings.Contains(subject, "STREAM.INFO") {
			return []byte(`{"config":{"name":"JOBS","subjects":["jobs.work"]}}`)
		}

		return []byte(`{"config":{"durable_name":"work","filter_subject":"jobs.work","ack_policy":"explicit","ack_wait":30000000000,"max_ack_pending":1,"max_batch":1,"headers_only":true}}`)
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); !errors.Is(err, natsqueue.ErrResourcesNotProvisioned) {
		t.Fatalf("Start = %v, want incompatible resources", err)
	}
}

// TestStartRejectsInsufficientPullExpiry checks the server-side limit against
// the driver's bounded pull request, independently of acknowledgement timing.
func TestStartRejectsInsufficientPullExpiry(t *testing.T) {
	t.Parallel()

	endpoint := newAPIServer(t, func(subject string) []byte {
		if strings.Contains(subject, "STREAM.INFO") {
			return []byte(`{"config":{"name":"JOBS","subjects":["jobs.work"]}}`)
		}

		return []byte(`{"config":{"durable_name":"work","filter_subject":"jobs.work","ack_policy":"explicit","ack_wait":30000000000,"max_ack_pending":1,"max_batch":1,"max_expires":1000000000}}`)
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); !errors.Is(err, natsqueue.ErrResourcesNotProvisioned) {
		t.Fatalf("Start = %v, want insufficient pull expiry", err)
	}
}

// TestStopExpiredContextStillClosesOwnedConnection checks the public lifecycle
// guarantee without assuming native Drain completes synchronously.
func TestStopExpiredContextStillClosesOwnedConnection(t *testing.T) {
	t.Parallel()

	endpoint := newAPIServer(t, func(subject string) []byte {
		if strings.Contains(subject, "STREAM.INFO") {
			return []byte(`{"config":{"name":"JOBS","subjects":["jobs.work"]}}`)
		}

		return []byte(`{"config":{"durable_name":"work","filter_subject":"jobs.work","ack_policy":"explicit","ack_wait":30000000000,"max_ack_pending":1,"max_batch":1}}`)
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop = %v, want canceled", err)
	}

	if !driver.Connection().IsClosed() {
		t.Fatal("Stop retained owned connection")
	}
}

// TestStartPreservesStreamLookupFailureClass distinguishes denied lookup from
// absent topology while retaining the native API error for errors.As callers.
func TestStartPreservesStreamLookupFailureClass(t *testing.T) {
	t.Parallel()

	endpoint := newAPIServer(t, func(string) []byte {
		return []byte(`{"error":{"code":403,"err_code":10000,"description":"denied"}}`)
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Start(t.Context())

	if errors.Is(err, natsqueue.ErrResourcesNotProvisioned) {
		t.Fatal("denied lookup classified as missing topology")
	}

	if err, ok := errors.AsType[*nats.APIError](err); !ok || err.Code != 403 {
		t.Fatalf("Start = %v, want native denied error", err)
	}
}

// TestStartCancellationReachesConsumerLookup covers a successful stream lookup
// followed by a silent consumer endpoint, without a shared server or real waits.
func TestStartCancellationReachesConsumerLookup(t *testing.T) {
	t.Parallel()

	requested := make(chan struct{})
	endpoint := newAPIServer(t, func(subject string) []byte {
		if strings.Contains(subject, "STREAM.INFO") {
			return []byte(`{"config":{"name":"JOBS","subjects":["jobs.work"]}}`)
		}

		close(requested)

		return nil
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		result <- driver.Start(ctx)
	})
	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	<-requested
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want canceled", err)
	}
}

// TestStartCancellationReachesStreamProvisioning checks the write path as well
// as lookup: a canceled Start must not wait for a missing provisioning response.
func TestStartCancellationReachesStreamProvisioning(t *testing.T) {
	t.Parallel()

	requested := make(chan struct{})
	endpoint := newAPIServer(t, func(subject string) []byte {
		if strings.Contains(subject, "STREAM.INFO") {
			return []byte(`{"error":{"code":404,"err_code":10059,"description":"stream not found"}}`)
		}

		close(requested)

		return nil
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
		consumer.Provision(true)
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		result <- driver.Start(ctx)
	})
	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	<-requested
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want canceled", err)
	}
}

// TestConsumeCancellationReachesConsumerBinding checks the SDK lookup repeated
// during PullSubscribe, after startup has already validated the same durable.
func TestConsumeCancellationReachesConsumerBinding(t *testing.T) {
	t.Parallel()

	requested := make(chan struct{})
	lookups := 0
	endpoint := newAPIServer(t, func(subject string) []byte {
		if strings.Contains(subject, "STREAM.INFO") {
			return []byte(`{"config":{"name":"JOBS","subjects":["jobs.work"]}}`)
		}

		lookups++

		if lookups == 1 {
			return []byte(`{"stream_name":"JOBS","name":"work","config":{"durable_name":"work","filter_subject":"jobs.work","ack_policy":"explicit","ack_wait":30000000000,"max_ack_pending":1,"max_batch":1}}`)
		}

		close(requested)

		return nil
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		result <- driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
			t.Error("binding unexpectedly succeeded")

			return nil
		})
	})
	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	<-requested
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, want canceled", err)
	}
}

// newAPIServer speaks only the NATS protocol needed for request/reply tests. It
// uses a private loopback listener, never a shared broker, and joins its worker
// during cleanup. Returning nil deliberately leaves an API request unanswered.
func newAPIServer(t *testing.T, respond func(string) []byte) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	var mutex sync.Mutex
	var connection net.Conn
	group.Go(func() {
		client, err := listener.Accept()

		if err != nil {
			return
		}

		mutex.Lock()
		connection = client
		mutex.Unlock()
		defer client.Close()

		_, _ = fmt.Fprint(client, "INFO {\"server_id\":\"test\",\"proto\":1,\"max_payload\":1048576}\r\n")

		peer := protocolPeer{
			connection:    client,
			reader:        bufio.NewReader(client),
			subscriptions: make(map[string]string),
			respond: func(request *nats.Msg) *nats.Msg {
				body := respond(request.Subject)

				if body == nil {
					return nil
				}

				return &nats.Msg{Subject: request.Reply, Data: body}
			},
		}

		peer.serve()
	})
	t.Cleanup(func() {
		_ = listener.Close()
		mutex.Lock()

		if connection != nil {
			_ = connection.Close()
		}

		mutex.Unlock()
		group.Wait()
	})

	return "nats://" + listener.Addr().String()
}

// protocolPeer retains the real native protocol's buffered input and registered
// inbox subscriptions. Its callback supplies API responses or fetched deliveries;
// driver behavior is always observed through public constructors and Consume.
type protocolPeer struct {
	// connection is the private loopback or in-process native connection.
	connection net.Conn
	// reader retains buffered protocol bytes between frames.
	reader *bufio.Reader
	// subscriptions maps native inbox filters to SDK subscription IDs.
	subscriptions map[string]string
	// respond supplies one native reply, or nil to leave the request unanswered.
	respond func(*nats.Msg) *nats.Msg
}

// serve reads native handshake and publication frames until the socket closes.
// Payload parsing and native delivery framing live in publish, not driver mocks.
func (peer *protocolPeer) serve() {
	for {
		line, err := peer.reader.ReadString('\n')

		if err != nil {
			return
		}

		parts := strings.Fields(line)

		switch parts[0] {
		case "PING":
			_, err = fmt.Fprint(peer.connection, "PONG\r\n")
		case "SUB":
			peer.subscriptions[parts[1]] = parts[len(parts)-1]
		case "PUB":
			err = peer.publish(parts)
		}

		if err != nil {
			return
		}
	}
}

// publish decodes a PUB frame, invokes the test's native server behavior, and
// sends a MSG response with optional JetStream acknowledgement metadata.
func (peer *protocolPeer) publish(parts []string) error {
	size, err := strconv.Atoi(parts[len(parts)-1])

	if err != nil {
		return err
	}

	body := make([]byte, size+2)

	if _, err := io.ReadFull(peer.reader, body); err != nil {
		return err
	}

	request := &nats.Msg{Subject: parts[1], Data: body[:size]}

	if len(parts) == 4 {
		request.Reply = parts[2]
	}

	response := peer.respond(request)

	if response == nil {
		return nil
	}

	_, err = fmt.Fprintf(peer.connection, "MSG %s %s %s%d\r\n%s\r\n",
		response.Subject, peer.subscriptionID(response.Subject), nativeReply(response.Reply), len(response.Data), response.Data)

	return err
}

// subscriptionID resolves the SDK's exact or final-wildcard inbox filter.
// Tests exercise only native request and pull inboxes, not broker subject ACLs.
func (peer *protocolPeer) subscriptionID(subject string) string {
	for filter, id := range peer.subscriptions {
		if filter == subject {
			return id
		}

		prefix := strings.TrimRight(filter, "*>")

		if prefix != filter && strings.HasPrefix(subject, prefix) {
			return id
		}
	}

	return "missing-subscription"
}

// nativeReply includes the optional MSG reply token and separator without
// changing empty-reply framing used by JetStream API responses.
func nativeReply(reply string) string {
	if reply == "" {
		return ""
	}

	return reply + " "
}

// pipeDialer implements the SDK's native dial hook with an in-process connection
// whose channel-based I/O is controlled by testing/synctest.
type pipeDialer struct {
	// connection is the client end of the native protocol pipe.
	connection net.Conn
}

// Dial supplies the already-created private pipe; it performs no network I/O.
func (pipeDialer *pipeDialer) Dial(string, string) (net.Conn, error) {
	return pipeDialer.connection, nil
}

// newPipeServer supplies the same native protocol seam without wall-clock I/O.
// Call it inside a synctest bubble to control acknowledgement and heartbeat time.
// DNS lookup is disabled because the custom dialer ignores hostnames; native
// resolver state must not cross between virtual-clock bubbles and real tests.
func newPipeServer(t *testing.T, respond func(*nats.Msg) *nats.Msg) nats.Option {
	t.Helper()

	client, server := net.Pipe()
	var group sync.WaitGroup
	group.Go(func() {
		defer server.Close()

		_, _ = fmt.Fprint(server, "INFO {\"server_id\":\"test\",\"proto\":1,\"max_payload\":1048576}\r\n")

		peer := protocolPeer{
			connection: server, reader: bufio.NewReader(server),
			subscriptions: make(map[string]string), respond: respond,
		}

		peer.serve()
	})
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		group.Wait()
	})

	return func(options *nats.Options) error {
		options.SkipHostLookup = true

		return nats.SetCustomDialer(&pipeDialer{connection: client})(options)
	}
}
