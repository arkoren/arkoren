package awsqueue_test

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/driver/aws/awsqueue"
)

// disposableSQSClient requires an explicitly provisioned loopback emulator.
// Short mode skips before any SDK client or connection. The default endpoint
// belongs to this module's Compose queue service;
// fixed synthetic credentials cannot access a production AWS account.
func disposableSQSClient(t *testing.T) *sqs.Client {
	t.Helper()

	endpoint := os.Getenv("ARKOREN_DISPOSABLE_SQS_ENDPOINT")

	if testing.Short() {
		t.Skip("integration test")
	}

	if endpoint == "" {
		endpoint = "http://127.0.0.1:14568"
	}

	parsed, err := url.Parse(endpoint)

	if err != nil {
		t.Fatal(err)
	}

	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" {
		t.Fatal("disposable SQS endpoint must be loopback")
	}

	return sqs.NewFromConfig(aws.Config{
		Region: "us-east-1", BaseEndpoint: aws.String(endpoint),
		Credentials:      credentials.NewStaticCredentialsProvider("000000000000", "test", ""),
		RetryMaxAttempts: 1,
	})
}

// disposableFIFOQueue acquires one unique queue and immediately registers its
// deletion. Each test owns its native client and queues; no shared fixtures run.
func disposableFIFOQueue(t *testing.T, native *sqs.Client) string {
	t.Helper()

	created, err := native.CreateQueue(t.Context(), &sqs.CreateQueueInput{
		QueueName:  aws.String("arkoren-" + rand.Text() + ".fifo"),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, err := native.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl}); err != nil {
			t.Error(err)
		}
	})

	return aws.ToString(created.QueueUrl)
}

// TestFIFOFailureReportingOnDisposableSQS verifies FIFO dispatch, receipt
// handling, failure publication, and source deletion against disposable native
// infrastructure. Short checks do not run it.
func TestFIFOFailureReportingOnDisposableSQS(t *testing.T) {
	native := disposableSQSClient(t)

	t.Parallel()

	source := disposableFIFOQueue(t, native)
	failures := disposableFIFOQueue(t, native)

	driver, err := awsqueue.NewFrom(native, map[string]string{"work": source}, func(consumer *awsqueue.Builder) {
		consumer.FailureQueueURL(failures)
		consumer.WaitTimeSeconds(1)
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	if err := driver.Dispatch(ctx, queue.Message{ID: "job-1", Name: "work", Queue: "work", Payload: []byte(`{"ready":true}`)}); err != nil {
		t.Fatal(err)
	}

	err = driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
		if err := received.Fail(ctx, errors.New("synthetic failure")); err != nil {
			t.Error(err)
		}

		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	readContext, stop := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(stop)

	report, err := native.ReceiveMessage(readContext, &sqs.ReceiveMessageInput{QueueUrl: aws.String(failures), WaitTimeSeconds: 1})

	if err != nil || len(report.Messages) != 1 {
		t.Fatalf("failure receive error = %v, response = %+v", err, report)
	}

	var body map[string]jsontext.Value

	if err := json.Unmarshal([]byte(aws.ToString(report.Messages[0].Body)), &body); err != nil {
		t.Fatal(err)
	}

	if string(body["error"]) != `"synthetic failure"` {
		t.Fatal("failure archive did not preserve its existing JSON envelope")
	}
}

// sqsProtocol exercises the real SDK's JSON protocol without opening sockets.
// It supplies one delivery, records native request values, and lets tests stall
// visibility changes while honoring the HTTP request's cancellation context.
type sqsProtocol struct {
	// mu protects recorded requests, which can arrive from the heartbeat worker.
	mu sync.Mutex
	// receives records polling settings sent through the native SDK.
	receives []sqs.ReceiveMessageInput
	// changes records heartbeat and retry visibility settings in request order.
	changes []sqs.ChangeMessageVisibilityInput
	// sends records ordinary dispatches and terminal failure report envelopes.
	sends []sqs.SendMessageInput
	// onVisibility substitutes slow or failed visibility I/O for individual tests.
	// It is set before construction and is never changed while Consume runs.
	onVisibility func(context.Context, int32) error
}

// sqsHTTPFunc substitutes an HTTP response at the public native SDK transport
// seam. It never replaces a driver helper or bypasses request serialization.
type sqsHTTPFunc func(*http.Request) (*http.Response, error)

// Do delegates one serialized request to the test's transport implementation.
// The function must honor request cancellation for any blocking operation.
func (sqsHTTPFunc sqsHTTPFunc) Do(request *http.Request) (*http.Response, error) {
	return sqsHTTPFunc(request)
}

// Do implements the SDK's HTTP client boundary. Receive returns one message;
// later polls block until canceled. Successful settlement returns empty JSON.
// Unexpected operations fail rather than contacting an actual AWS endpoint.
func (protocol *sqsProtocol) Do(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}

	switch request.Header.Get("X-Amz-Target") {
	case "AmazonSQS.ReceiveMessage":
		return protocol.receive(request)
	case "AmazonSQS.ChangeMessageVisibility":
		return protocol.changeVisibility(request)
	case "AmazonSQS.DeleteMessage":
		return sqsResponse("{}"), nil
	case "AmazonSQS.SendMessage":
		return protocol.send(request)
	default:
		return nil, fmt.Errorf("unexpected SQS operation %q", request.Header.Get("X-Amz-Target"))
	}
}

// send captures the real SDK's FIFO fields and body without interpreting the
// application's failure-report format or contacting an external queue.
func (protocol *sqsProtocol) send(request *http.Request) (*http.Response, error) {
	var input sqs.SendMessageInput

	if err := json.UnmarshalRead(request.Body, &input); err != nil {
		return nil, err
	}

	protocol.mu.Lock()
	protocol.sends = append(protocol.sends, input)
	protocol.mu.Unlock()

	return sqsResponse("{}"), nil
}

// receive decodes the actual SDK payload before returning the fixture envelope.
// The second poll remains cancellable, so Consume can be joined in each test.
func (protocol *sqsProtocol) receive(request *http.Request) (*http.Response, error) {
	var input sqs.ReceiveMessageInput

	if err := json.UnmarshalRead(request.Body, &input); err != nil {
		return nil, err
	}

	protocol.mu.Lock()
	protocol.receives = append(protocol.receives, input)
	first := len(protocol.receives) == 1
	protocol.mu.Unlock()

	if first {
		return sqsResponse(`{"Messages":[{"MessageId":"message-1","ReceiptHandle":"receipt-1","Body":"{\"name\":\"email\"}"}]}`), nil
	}

	<-request.Context().Done()

	return nil, request.Context().Err()
}

// changeVisibility records the wire value before running the test's I/O hook.
// Returning its error exercises native SDK cancellation/error mapping too.
func (protocol *sqsProtocol) changeVisibility(request *http.Request) (*http.Response, error) {
	var input sqs.ChangeMessageVisibilityInput

	if err := json.UnmarshalRead(request.Body, &input); err != nil {
		return nil, err
	}

	protocol.mu.Lock()
	protocol.changes = append(protocol.changes, input)
	protocol.mu.Unlock()

	if protocol.onVisibility != nil {
		if err := protocol.onVisibility(request.Context(), input.VisibilityTimeout); err != nil {
			return nil, err
		}
	}

	return sqsResponse("{}"), nil
}

// receiveInputs copies polling records so assertions never race native calls.
// SDK input pointers are not retained; the copied request fields are immutable.
func (protocol *sqsProtocol) receiveInputs() []sqs.ReceiveMessageInput {
	protocol.mu.Lock()
	defer protocol.mu.Unlock()

	return slices.Clone(protocol.receives)
}

// visibilityInputs copies visibility records in observed HTTP request order.
// It includes both automatic renewal and explicit Delivery.Retry requests.
func (protocol *sqsProtocol) visibilityInputs() []sqs.ChangeMessageVisibilityInput {
	protocol.mu.Lock()
	defer protocol.mu.Unlock()

	return slices.Clone(protocol.changes)
}

// sqsResponse constructs the successful JSON response expected by the pinned
// SDK. The in-memory body lets tests run entirely inside a synctest bubble.
func sqsResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newProtocolDriver constructs through the public native-client conversion.
// Static dummy credentials and an in-memory HTTP client avoid ambient AWS
// configuration. One SDK attempt keeps simulated transport failures immediate.
func newProtocolDriver(t *testing.T, protocol *sqsProtocol, configure ...awsqueue.BuilderFunc) *awsqueue.Driver {
	t.Helper()

	client := sqs.NewFromConfig(aws.Config{
		Region:           "eu-west-1",
		Credentials:      credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:       protocol,
		RetryMaxAttempts: 1,
	})

	driver, err := awsqueue.NewFrom(client, map[string]string{"work": "https://sqs.example/work"}, configure...)

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// pollProtocol acknowledges the fixture delivery and cancels consumption,
// returning the first native polling request for caller-side wire assertions.
// It uses no private driver methods or settings and joins renewal on return.
func pollProtocol(t *testing.T, driver *awsqueue.Driver, protocol *sqsProtocol) sqs.ReceiveMessageInput {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	err := driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
		if err := received.Acknowledge(ctx); err != nil {
			t.Error(err)
		}

		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume error = %v, want context.Canceled", err)
	}

	return protocol.receiveInputs()[0]
}

// TestQueueNewFromUsesZeroSettingDefaults observes documented zero settings in
// the native protocol rather than treating zero as an immediate visibility.
func TestQueueNewFromUsesZeroSettingDefaults(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
		consumer.WaitTimeSeconds(0)
		consumer.MaxMessages(0)
		consumer.VisibilityTimeout(0)
	})

	input := pollProtocol(t, driver, protocol)

	if input.WaitTimeSeconds != 20 || input.MaxNumberOfMessages != 1 || input.VisibilityTimeout != 30 {
		t.Fatalf("zero-setting polling request = %+v, want wait=20, batch=1, visibility=30", input)
	}
}

// TestQueueNewFromAcceptsMinimumSettings verifies both lower construction bounds
// and exact whole-second visibility through a real SDK ReceiveMessage request.
func TestQueueNewFromAcceptsMinimumSettings(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
		consumer.WaitTimeSeconds(1)
		consumer.MaxMessages(1)
		consumer.VisibilityTimeout(time.Second)
	})

	input := pollProtocol(t, driver, protocol)

	if input.WaitTimeSeconds != 1 || input.MaxNumberOfMessages != 1 || input.VisibilityTimeout != 1 {
		t.Fatalf("minimum polling request = %+v, want wait=1, batch=1, visibility=1", input)
	}
}

// TestQueueNewFromAcceptsMaximumSettings checks that inclusive native maxima
// survive construction and conversion without wrapping or adding a second.
func TestQueueNewFromAcceptsMaximumSettings(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
		consumer.WaitTimeSeconds(20)
		consumer.MaxMessages(10)
		consumer.VisibilityTimeout(12 * time.Hour)
	})

	input := pollProtocol(t, driver, protocol)

	if input.WaitTimeSeconds != 20 || input.MaxNumberOfMessages != 10 || input.VisibilityTimeout != 43200 {
		t.Fatalf("maximum polling request = %+v, want wait=20, batch=10, visibility=43200", input)
	}
}

// TestConsumeRoundsVisibilityAboveWholeSecond protects ceiling conversion at
// nanosecond precision through the public constructor and receive protocol.
func TestConsumeRoundsVisibilityAboveWholeSecond(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
		consumer.VisibilityTimeout(time.Second + time.Nanosecond)
	})

	if input := pollProtocol(t, driver, protocol); input.VisibilityTimeout != 2 {
		t.Fatalf("receive visibility = %d, want 2", input.VisibilityTimeout)
	}
}

// TestConsumeRoundsVisibilityUpToMaximum covers the fractional upper edge
// without calling private duration conversion helpers.
func TestConsumeRoundsVisibilityUpToMaximum(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
		consumer.VisibilityTimeout(12*time.Hour - time.Nanosecond)
	})

	if input := pollProtocol(t, driver, protocol); input.VisibilityTimeout != 43200 {
		t.Fatalf("receive visibility = %d, want 43200", input.VisibilityTimeout)
	}
}

// TestConsumePublishesDetachedVisibilityPolicy checks final-value validation
// and retained-draft isolation by observing the resulting native request.
func TestConsumePublishesDetachedVisibilityPolicy(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	var retained *awsqueue.Builder
	driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
		retained = consumer
		consumer.VisibilityTimeout(time.Nanosecond)
	}, func(consumer *awsqueue.Builder) {
		consumer.VisibilityTimeout(1500 * time.Millisecond)
	})

	retained.VisibilityTimeout(time.Hour)

	if input := pollProtocol(t, driver, protocol); input.VisibilityTimeout != 2 {
		t.Fatalf("published receive visibility = %d, want 2", input.VisibilityTimeout)
	}
}

// TestConsumeRoundsVisibilityAndPreservesHeartbeatInterval checks the receive
// and renewal payloads while virtual time verifies the unrounded half interval.
// Acknowledging then waiting also verifies that settled deliveries stop renewing.
func TestConsumeRoundsVisibilityAndPreservesHeartbeatInterval(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		renewed := make(chan time.Time, 1)
		protocol := &sqsProtocol{onVisibility: func(context.Context, int32) error {
			renewed <- time.Now()

			return nil
		}}
		driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
			consumer.VisibilityTimeout(1500 * time.Millisecond)
		})
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		startedAt := time.Now()

		err := driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
			if elapsed := (<-renewed).Sub(startedAt); elapsed != 750*time.Millisecond {
				t.Errorf("first heartbeat after %s, want 750ms", elapsed)
			}

			if err := received.Acknowledge(ctx); err != nil {
				t.Error(err)
			}

			time.Sleep(2 * time.Second)
			cancel()

			return nil
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}

		if got := protocol.receiveInputs()[0].VisibilityTimeout; got != 2 {
			t.Errorf("receive visibility = %d, want 2", got)
		}

		changes := protocol.visibilityInputs()

		if len(changes) != 1 || changes[0].VisibilityTimeout != 2 {
			t.Fatalf("visibility changes = %+v, want only one 2s renewal", changes)
		}
	})
}

// TestConsumeHeartbeatTimeoutFitsShortLease verifies that a stalled SDK call
// exhausts at most half the one-second minimum visibility, not the full lease.
func TestConsumeHeartbeatTimeoutFitsShortLease(t *testing.T) {
	t.Parallel()

	assertHeartbeatTimeout(t, time.Second, 500*time.Millisecond)
}

// TestConsumeHeartbeatTimeoutHasFiniteCap verifies that long leases do not
// permit hours-long blocked renewal operations or unbounded SDK retry loops.
func TestConsumeHeartbeatTimeoutHasFiniteCap(t *testing.T) {
	t.Parallel()

	assertHeartbeatTimeout(t, 30*time.Second, 10*time.Second)
}

// assertHeartbeatTimeout observes request deadlines and cancellation through
// Consume and the SDK's HTTP client. releaseRPC guarantees cleanup even when
// testing an implementation that supplies no deadline to its owned RPC.
func assertHeartbeatTimeout(t *testing.T, visibility, budget time.Duration) {
	t.Helper()

	synctest.Test(t, func(t *testing.T) {
		started := make(chan context.Context, 1)
		completed := make(chan error, 2)
		releaseRPC := make(chan struct{})
		protocol := &sqsProtocol{onVisibility: func(ctx context.Context, _ int32) error {
			started <- ctx

			select {
			case <-ctx.Done():
				completed <- ctx.Err()

				return ctx.Err()
			case <-releaseRPC:
				return nil
			}
		}}
		driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
			consumer.VisibilityTimeout(visibility)
		})
		ctx, cancel := context.WithCancel(t.Context())
		returnHandler := make(chan struct{})
		finished := make(chan error, 1)
		var group sync.WaitGroup

		group.Go(func() {
			finished <- driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
				<-returnHandler

				return nil
			})
		})

		heartbeat := <-started
		deadline, ok := heartbeat.Deadline()

		if !ok || time.Until(deadline) != budget {
			t.Errorf("heartbeat deadline = %v, present=%t; want budget %s", deadline, ok, budget)
		}

		time.Sleep(budget)
		synctest.Wait()

		select {
		case err := <-completed:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("heartbeat error = %v, want context.DeadlineExceeded", err)
			}
		default:
			t.Error("stalled heartbeat did not finish within its owned RPC budget")
		}

		cancel()
		close(returnHandler)
		close(releaseRPC)
		group.Wait()

		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Errorf("Consume error = %v, want context.Canceled", err)
		}
	})
}

// TestConsumeRenewsDuringCanceledHandlerDrain preserves the lease while a
// canceled handler finishes. Explicit settlement gets its own caller budget;
// canceling Consume alone does not release a message still being processed.
func TestConsumeRenewsDuringCanceledHandlerDrain(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		protocol := &sqsProtocol{}
		driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
			consumer.VisibilityTimeout(time.Second)
		})
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		err := driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
			cancel()
			time.Sleep(750 * time.Millisecond)
			settleContext, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			err := received.Retry(settleContext, 2*time.Second)
			stop()

			if err != nil {
				t.Error(err)
			}

			return nil
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}

		changes := protocol.visibilityInputs()

		if len(changes) != 2 {
			t.Fatalf("visibility changes = %+v, want renewal followed by retry", changes)
		}

		if changes[0].VisibilityTimeout != 1 || changes[1].VisibilityTimeout != 2 {
			t.Fatalf("visibility changes = %+v, want 1s renewal then 2s retry", changes)
		}
	})
}

// TestDeliverySerializesRenewalBeforeSuccessfulRetry keeps a pending heartbeat
// ahead of Retry and prevents further renewal from overwriting its settled delay.
func TestDeliverySerializesRenewalBeforeSuccessfulRetry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{}, 1)
		releaseRPC := make(chan struct{})
		var retries atomic.Int32
		protocol := &sqsProtocol{onVisibility: func(ctx context.Context, seconds int32) error {
			if seconds == 2 {
				retries.Add(1)

				return nil
			}
			// Extra renewals must fail the request-count assertion, not block
			// cleanup on a notification nobody receives after the first RPC.
			select {
			case started <- struct{}{}:
			default:
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseRPC:
				return nil
			}
		}}
		driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
			consumer.VisibilityTimeout(time.Second)
		})
		ctx, cancel := context.WithCancel(t.Context())
		retryNow := make(chan struct{})
		returnHandler := make(chan struct{})
		settled := make(chan error, 1)
		finished := make(chan error, 1)
		var group sync.WaitGroup

		group.Go(func() {
			finished <- driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
				<-retryNow
				settled <- received.Retry(ctx, 2*time.Second)
				<-returnHandler

				return nil
			})
		})

		<-started
		close(retryNow)
		synctest.Wait()

		if got := retries.Load(); got != 0 {
			t.Errorf("retry requests during pending heartbeat = %d, want 0", got)
		}

		close(releaseRPC)

		if err := <-settled; err != nil {
			t.Errorf("Retry error = %v, want success", err)
		}

		time.Sleep(2 * time.Second)
		synctest.Wait()

		if got := len(protocol.visibilityInputs()); got != 2 {
			t.Errorf("visibility requests = %d, want pending renewal then successful retry", got)
		}

		cancel()
		close(returnHandler)
		group.Wait()

		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Errorf("Consume error = %v, want context.Canceled", err)
		}
	})
}

// TestDeliveryRenewsAfterSettlementFailure resumes the heartbeat after a failed
// Retry, admits another Retry, and stops renewal after that settlement succeeds.
func TestDeliveryRenewsAfterSettlementFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var retries atomic.Int32
		retryFailure := errors.New("test retry transport failure")
		protocol := &sqsProtocol{onVisibility: func(_ context.Context, seconds int32) error {
			if seconds == 2 && retries.Add(1) == 1 {
				return retryFailure
			}

			return nil
		}}
		driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
			consumer.VisibilityTimeout(time.Second)
		})
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		err := driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
			if err := received.Retry(ctx, 2*time.Second); !errors.Is(err, retryFailure) {
				t.Errorf("first Retry error = %v, want test transport failure", err)
			}

			time.Sleep(750 * time.Millisecond)

			if err := received.Retry(ctx, 2*time.Second); err != nil {
				t.Errorf("second Retry error = %v, want success", err)
			}

			time.Sleep(2 * time.Second)
			cancel()

			return nil
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}

		changes := protocol.visibilityInputs()

		if len(changes) != 3 {
			t.Fatalf("visibility requests = %d, want failed retry, renewal, successful retry", len(changes))
		}

		if changes[1].VisibilityTimeout != 1 {
			t.Fatal("failed Retry did not resume renewal")
		}
	})
}

// TestConsumeStopsStalledHeartbeatBeforeJoining reproduces a handler returning
// while a renewal RPC is stalled. Cleanup releases the fake even on the old
// implementation, so the regression reports a failure instead of hanging.
func TestConsumeStopsStalledHeartbeatBeforeJoining(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		started := make(chan context.Context, 1)
		releaseRPC := make(chan struct{})
		protocol := &sqsProtocol{onVisibility: func(ctx context.Context, _ int32) error {
			started <- ctx

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseRPC:
				return nil
			}
		}}
		driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
			consumer.VisibilityTimeout(time.Second)
		})
		ctx, cancel := context.WithCancel(t.Context())
		returnHandler := make(chan struct{})
		finished := make(chan error, 1)
		var group sync.WaitGroup

		group.Go(func() {
			finished <- driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
				<-returnHandler

				return nil
			})
		})

		heartbeat := <-started
		cancel()
		close(returnHandler)
		synctest.Wait()

		if heartbeat.Err() == nil {
			t.Error("handler return did not cancel the stalled heartbeat before joining it")
		}

		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Consume error = %v, want context.Canceled", err)
			}
		default:
			t.Error("Consume remains blocked joining the stalled heartbeat")
		}

		close(releaseRPC)
		group.Wait()
	})
}

// TestDeliverySettlementDeadlineIncludesHeartbeatWait exercises the exported
// delivery while a native heartbeat owns serialization. A short settlement
// budget must expire without waiting for the stalled RPC or issuing Retry.
func TestDeliverySettlementDeadlineIncludesHeartbeatWait(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{}, 1)
		releaseRPC := make(chan struct{})
		protocol := &sqsProtocol{onVisibility: func(ctx context.Context, _ int32) error {
			started <- struct{}{}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseRPC:
				return nil
			}
		}}
		driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
			consumer.VisibilityTimeout(time.Second)
		})
		ctx, cancel := context.WithCancel(t.Context())
		settleNow := make(chan struct{})
		returnHandler := make(chan struct{})
		settled := make(chan error, 1)
		finished := make(chan error, 1)
		var group sync.WaitGroup

		group.Go(func() {
			finished <- driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
				<-settleNow
				settleContext, stop := context.WithTimeout(ctx, 100*time.Millisecond)
				settled <- received.Retry(settleContext, 2*time.Second)
				stop()
				<-returnHandler

				return nil
			})
		})

		<-started
		close(settleNow)
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()

		select {
		case err := <-settled:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Retry error = %v, want context.DeadlineExceeded", err)
			}
		default:
			t.Error("Retry remains blocked behind heartbeat after its settlement deadline")
		}

		if got := len(protocol.visibilityInputs()); got != 1 {
			t.Errorf("visibility requests = %d, want only the pending heartbeat", got)
		}

		close(releaseRPC)
		cancel()
		close(returnHandler)
		group.Wait()

		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Errorf("Consume error = %v, want context.Canceled", err)
		}
	})
}

// TestDeliveryRejectsRetryBeyondNativeMaximum prevents successful settlement
// from shortening a requested lower bound. Rejection must leave acknowledgement
// available and must not issue a visibility request.
func TestDeliveryRejectsRetryBeyondNativeMaximum(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	driver := newProtocolDriver(t, protocol)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	err := driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
		if err := received.Retry(ctx, 13*time.Hour); !errors.Is(err, awsqueue.ErrUnsupportedRetryDelay) {
			t.Errorf("Retry error = %v, want ErrUnsupportedRetryDelay", err)
		}

		if err := received.Acknowledge(ctx); err != nil {
			t.Error(err)
		}

		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if got := len(protocol.visibilityInputs()); got != 0 {
		t.Fatalf("visibility requests = %d, want none for an unsupported delay", got)
	}
}

// TestDeliveryFailSuppliesFIFOFields observes terminal failure publication
// through the SDK, preserving the envelope while satisfying FIFO requirements.
func TestDeliveryFailSuppliesFIFOFields(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	driver := newProtocolDriver(t, protocol, func(consumer *awsqueue.Builder) {
		consumer.FailureQueueURL("https://sqs.example/failures.fifo")
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	err := driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
		if err := received.Fail(ctx, errors.New("invalid job")); err != nil {
			t.Error(err)
		}

		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if len(protocol.sends) != 1 {
		t.Fatalf("failure publications = %d, want 1", len(protocol.sends))
	}

	input := protocol.sends[0]

	if aws.ToString(input.MessageGroupId) != "work" || aws.ToString(input.MessageDeduplicationId) != "message-1" {
		t.Fatalf("FIFO group/identity = %q/%q", aws.ToString(input.MessageGroupId), aws.ToString(input.MessageDeduplicationId))
	}

	var report struct {
		// Message is the original public delivery envelope, not a new wire type.
		Message queue.Message `json:"message"`
		// Error remains the explicitly configured failure archive's diagnostic.
		Error string `json:"error"`
	}

	if err := json.Unmarshal([]byte(aws.ToString(input.MessageBody)), &report); err != nil {
		t.Fatal(err)
	}

	if report.Message.ID != "message-1" || report.Error != "invalid job" {
		t.Fatal("FIFO failure reporting changed the existing envelope")
	}
}

// TestDeliveryRetainsNativeRemainingVisibilityError establishes the limit of
// local duration validation: SQS can reject a nominally valid 12-hour request
// because time has elapsed since receipt. Failure must not settle the delivery.
func TestDeliveryRetainsNativeRemainingVisibilityError(t *testing.T) {
	t.Parallel()

	protocol := &sqsProtocol{}
	native := sqs.NewFromConfig(aws.Config{
		Region:           "eu-west-1",
		Credentials:      credentials.NewStaticCredentialsProvider("test", "test", ""),
		RetryMaxAttempts: 1,
		HTTPClient: sqsHTTPFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("X-Amz-Target") == "AmazonSQS.ChangeMessageVisibility" {
				var input sqs.ChangeMessageVisibilityInput

				if err := json.UnmarshalRead(request.Body, &input); err != nil {
					return nil, err
				}

				if input.VisibilityTimeout != 43200 {
					t.Errorf("retry visibility = %d, want the unshortened 12-hour request", input.VisibilityTimeout)
				}

				response := sqsResponse(`{"__type":"InvalidParameterValue","message":"visibility exceeds remaining time"}`)
				response.StatusCode = http.StatusBadRequest

				return response, nil
			}

			return protocol.Do(request)
		}),
	})

	driver, err := awsqueue.NewFrom(native, map[string]string{"work": "https://sqs.example/work"})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	err = driver.Consume(ctx, "work", func(ctx context.Context, received queue.Delivery) error {
		err := received.Retry(ctx, 12*time.Hour)
		var native smithy.APIError

		if !errors.As(err, &native) || native.ErrorCode() != "InvalidParameterValue" {
			t.Errorf("Retry error = %v, want native remaining-time failure", err)
		}

		if err := received.Acknowledge(ctx); err != nil {
			t.Error(err)
		}

		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
