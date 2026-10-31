package temporal_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/mock"
	commandpb "go.temporal.io/api/command/v1"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	sdklog "go.temporal.io/sdk/log"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/temporal"
	"arkoren.dev/driver/temporal/temporalschedule"
	"arkoren.dev/driver/temporal/temporalworkflow"
)

// pendingChildren substitutes the native child acknowledgement/result boundary
// while leaving workflow replay, timers, and cancellation in the real SDK.
type pendingChildren struct {
	// WorkerInterceptorBase forwards unrelated native worker operations.
	interceptor.WorkerInterceptorBase
}

// InterceptWorkflow installs a native outbound child interceptor for each run.
func (*pendingChildren) InterceptWorkflow(
	_ sdkworkflow.Context,
	next interceptor.WorkflowInboundInterceptor,
) interceptor.WorkflowInboundInterceptor {
	return &pendingChildInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// pendingChildInbound preserves SDK inbound execution and replaces only the
// outbound child-start response, representing an engine acknowledgement delay.
type pendingChildInbound struct {
	// WorkflowInboundInterceptorBase forwards actual workflow invocation.
	interceptor.WorkflowInboundInterceptorBase
}

// Init forwards the wrapped outbound boundary to the native workflow runner.
func (inbound *pendingChildInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return inbound.Next.Init(&pendingChildOutbound{
		WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next},
	})
}

// pendingChildOutbound leaves the child future unready; parent cancellation
// must still end the runtime's wait without canceling detached child ownership.
type pendingChildOutbound struct {
	// WorkflowOutboundInterceptorBase forwards all other deterministic SDK calls.
	interceptor.WorkflowOutboundInterceptorBase
}

// ExecuteChildWorkflow returns a real native SDK future with no acknowledgement.
// The test deliberately does not emulate forced termination of child callbacks.
func (*pendingChildOutbound) ExecuteChildWorkflow(
	ctx sdkworkflow.Context,
	_ string,
	_ ...any,
) sdkworkflow.ChildWorkflowFuture {
	future, _ := sdkworkflow.NewFuture(ctx)

	return &pendingChildFuture{future: future}
}

// pendingChildFuture supplies one unready SDK future for child results and
// acknowledgement. No private Arkoren runtime implementation is invoked.
type pendingChildFuture struct {
	// ChildWorkflowFuture leaves unused native child methods outside this seam.
	sdkworkflow.ChildWorkflowFuture
	// future is native SDK scheduling state, not a Go channel or wall clock.
	future sdkworkflow.Future
}

// Get delegates the result wait to the native SDK future.
func (future *pendingChildFuture) Get(ctx sdkworkflow.Context, value any) error {
	return future.future.Get(ctx, value)
}

// IsReady reports the underlying native future's deterministic state.
func (future *pendingChildFuture) IsReady() bool {
	return future.future.IsReady()
}

// GetChildWorkflowExecution delegates acknowledgement waiting to that same
// deliberately unready native future.
func (future *pendingChildFuture) GetChildWorkflowExecution() sdkworkflow.Future {
	return future.future
}

// requireNativeWorkflowTimeout inspects the shipped envelope using only public
// SDK application-error details and the core error decoder.
func requireNativeWorkflowTimeout(t *testing.T, environment *testsuite.TestWorkflowEnvironment) {
	t.Helper()

	var app *sdktemporal.ApplicationError

	if err := environment.GetWorkflowError(); !errors.As(err, &app) {
		t.Fatalf("workflow error = %v; want application envelope", err)
	}

	var envelope jsontext.Value

	if err := app.Details(&envelope); err != nil {
		t.Fatal(err)
	}

	if err := workflow.DecodeError(envelope); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("workflow failure = %v; want ErrTimeout", err)
	}
}

// TestSpawnAcknowledgementWaitRespectsParentMaxDuration verifies detached child
// ownership does not detach the caller's wait from its own cancellation budget.
func TestSpawnAcknowledgementWaitRespectsParentMaxDuration(t *testing.T) {
	t.Parallel()

	child := workflow.New("child", func(_ *workflow.Context, in struct{}) (struct{}, error) {
		return in, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	parent := workflow.New("parent", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		_, err := ctx.Spawn(child, "detached", in)

		return struct{}{}, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Second)
	})
	registry, err := workflow.Handlers(parent, child)

	if err != nil {
		t.Fatal(err)
	}

	environment := publicWorkflow(t, registry, &testsuite.WorkflowTestSuite{})
	environment.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&pendingChildren{}}})
	environment.SetTestTimeout(time.Second)

	environment.ExecuteWorkflow("arkoren-wf-parent", jsontext.Value(`{"v":{}}`))

	requireNativeWorkflowTimeout(t, environment)
}

// TestChildResultWaitRespectsParentMaxDuration verifies a pending native child
// result cannot keep the parent's blocking runtime call alive after its limit.
func TestChildResultWaitRespectsParentMaxDuration(t *testing.T) {
	t.Parallel()

	child := workflow.New("child", func(_ *workflow.Context, in struct{}) (struct{}, error) {
		return in, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	parent := workflow.New("parent", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		return ctx.Child(child, "joined", in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Second)
	})
	registry, err := workflow.Handlers(parent, child)

	if err != nil {
		t.Fatal(err)
	}

	environment := publicWorkflow(t, registry, &testsuite.WorkflowTestSuite{})
	environment.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&pendingChildren{}}})
	environment.SetTestTimeout(time.Second)

	environment.ExecuteWorkflow("arkoren-wf-parent", jsontext.Value(`{"v":{}}`))

	requireNativeWorkflowTimeout(t, environment)
}

// TestExistingChildJoinWaitRespectsParentMaxDuration verifies a duplicate native
// child ID takes the actual join-activity path and ends the parent's observation
// at its limit. Observing an existing run does not acquire cancellation ownership.
func TestExistingChildJoinWaitRespectsParentMaxDuration(t *testing.T) {
	t.Parallel()

	child := workflow.New("child", func(_ *workflow.Context, in struct{}) (struct{}, error) {
		return in, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	parent := workflow.New("parent", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		return ctx.Child(child, "existing", in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Second)
	})
	registry, err := workflow.Handlers(parent, child)

	if err != nil {
		t.Fatal(err)
	}

	environment := publicWorkflow(t, registry, &testsuite.WorkflowTestSuite{})
	conflict := &sdktemporal.ChildWorkflowExecutionAlreadyStartedError{}
	environment.OnWorkflow("arkoren-wf-child", mock.Anything, mock.Anything).Return(nil, conflict)
	environment.OnActivity("arkoren-join", mock.Anything, "existing").After(time.Hour).Return(nil, nil)
	joined := make(chan struct{}, 1)
	environment.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		if info.ActivityType.Name == "arkoren-join" {
			joined <- struct{}{}
		}
	})

	environment.ExecuteWorkflow("arkoren-wf-parent", jsontext.Value(`{"v":{}}`))

	requireNativeWorkflowTimeout(t, environment)

	select {
	case <-joined:
	default:
		t.Fatal("duplicate child did not take the join-activity path")
	}
}

// nativeStart captures one public Driver.Start request after SDK serialization.
// It mutates the original containers after submission to expose retained aliases
// and uses NewFrom's existing client without a separate host dial or worker.
func nativeStart(t *testing.T) *workflowservice.StartWorkflowExecutionRequest {
	t.Helper()

	started := make(chan *workflowservice.StartWorkflowExecutionRequest, 1)
	options := protocolOptions(func(
		_ context.Context,
		_ string,
		request, response any,
		_ *grpc.ClientConn,
		_ grpc.UnaryInvoker,
		_ ...grpc.CallOption,
	) error {
		if message, ok := request.(*workflowservice.StartWorkflowExecutionRequest); ok {
			started <- message
			response.(*workflowservice.StartWorkflowExecutionResponse).RunId = "native-run"
		}

		return nil
	})

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	definition := workflow.New("onboard", func(_ *workflow.Context, input string) (string, error) {
		return input, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	driver, err := temporalworkflow.NewDriver(temporal.NewFrom(connection), func(driver *temporalworkflow.DriverBuilder) {
		driver.Registry(registry)
	})

	if err != nil {
		t.Fatal(err)
	}

	metadata := map[string]string{"source": "signup"}
	trace := map[string]string{"traceparent": "parent"}
	input := []byte(`{"v":"input"}`)

	if err := driver.Start(t.Context(), "onboard", "user-42", input, workflow.StartOptions{
		Delay: 2 * time.Hour, Metadata: metadata, Trace: trace,
	}); err != nil {
		t.Fatal(err)
	}

	metadata["source"] = "changed"
	trace["traceparent"] = "changed"
	clear(input)

	return <-started
}

// TestNativeStartPreservesWorkflowIdentity verifies persisted IDs and wire type
// names stay unchanged, with both running and finished duplicate IDs rejected.
func TestNativeStartPreservesWorkflowIdentity(t *testing.T) {
	t.Parallel()

	request := nativeStart(t)

	if request.WorkflowId != "user-42" || request.WorkflowType.Name != "arkoren-wf-onboard" {
		t.Fatalf("native workflow identity = %q %q", request.WorkflowId, request.WorkflowType.Name)
	}

	if request.WorkflowIdReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE {
		t.Fatalf("reuse policy = %v", request.WorkflowIdReusePolicy)
	}

	if request.WorkflowIdConflictPolicy != enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL {
		t.Fatalf("conflict policy = %v", request.WorkflowIdConflictPolicy)
	}
}

// TestNativeStartExecutionTimeoutIncludesDelay verifies the server's backstop
// includes the start delay, separate from the runner's execution-time timer.
func TestNativeStartExecutionTimeoutIncludesDelay(t *testing.T) {
	t.Parallel()

	request := nativeStart(t)
	executionTimeout := request.WorkflowExecutionTimeout.AsDuration()
	startDelay := request.WorkflowStartDelay.AsDuration()

	if executionTimeout != 3*time.Hour || startDelay != 2*time.Hour {
		t.Fatalf("native timeout/delay = %s/%s", request.WorkflowExecutionTimeout, request.WorkflowStartDelay)
	}
}

// TestNativeStartDoesNotEnableWorkflowRetries keeps whole-workflow execution
// retries distinct from the finite native activity retries used by steps.
func TestNativeStartDoesNotEnableWorkflowRetries(t *testing.T) {
	t.Parallel()

	if request := nativeStart(t); request.RetryPolicy != nil {
		t.Fatalf("whole-workflow retry policy = %v; want none", request.RetryPolicy)
	}
}

// TestNativeStartDetachesMetadataAndTrace verifies application memo and trace
// carriers stay independent and retain their original serialized values after
// caller-owned maps and input bytes are mutated following Start.
func TestNativeStartDetachesMetadataAndTrace(t *testing.T) {
	t.Parallel()

	request := nativeStart(t)

	var source string

	if err := converter.GetDefaultDataConverter().FromPayload(request.Memo.Fields["source"], &source); err != nil {
		t.Fatal(err)
	}

	if source != "signup" || len(request.Memo.Fields) != 1 {
		t.Fatalf("memo = %v; want only original source", request.Memo)
	}

	want := `{"arkoren_trace":{"traceparent":"parent"},"input":{"v":"input"}}`

	if got := string(request.Input.Payloads[0].Data); got != want {
		t.Fatalf("serialized traced input = %s", got)
	}
}

// sliceFailure is a legitimate error with a non-comparable dynamic value.
// Error handling must not compare it through interface equality.
type sliceFailure []string

// Error supplies a deterministic native failure message without making the
// underlying slice comparable.
func (sliceFailure) Error() string {
	return "native failure"
}

// failureClient supplies SDK descriptions and result failures at the public
// native-client boundary. Unused SDK methods fail through the embedded interface.
type failureClient struct {
	// Client keeps unrelated SDK operations outside this test's seam.
	client.Client
	// describeErr scripts an arbitrary native observation failure.
	describeErr error
	// resultErr scripts the native workflow outcome, including nested failures.
	resultErr error
}

// DescribeWorkflow returns a known correctly typed execution unless a native
// failure was explicitly supplied for status/error-boundary tests.
func (connection *failureClient) DescribeWorkflow(
	context.Context,
	string,
	string,
) (*client.WorkflowExecutionDescription, error) {
	description := &client.WorkflowExecutionDescription{}
	description.WorkflowType.Name = "arkoren-wf-order"

	return description, connection.describeErr
}

// GetWorkflow returns the scripted native run without performing I/O.
func (connection *failureClient) GetWorkflow(context.Context, string, string) client.WorkflowRun {
	return &failureRun{err: connection.resultErr}
}

// failureRun models only the SDK's result wait; its failure is returned intact.
type failureRun struct {
	// WorkflowRun keeps unused native run methods outside the test seam.
	client.WorkflowRun
	// err is the native failure surfaced by Get.
	err error
}

// Get returns the native outcome unchanged, as the Temporal client would after
// decoding its server failure chain.
func (run *failureRun) Get(context.Context, any) error {
	return run.err
}

// TestNativeTimeoutOutranksItsLastApplicationFailure verifies a failed attempt
// carried as the cause of a native timeout cannot mask the terminal timeout.
func TestNativeTimeoutOutranksItsLastApplicationFailure(t *testing.T) {
	t.Parallel()

	last := sdktemporal.NewApplicationError("previous attempt", "application.failure")
	connection := &failureClient{resultErr: sdktemporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, last)}
	driver, err := temporalworkflow.NewDriver(temporal.NewFrom(connection))

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.Result(t.Context(), "order", "run")

	if !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("Result = %v; want ErrTimeout", err)
	}
}

// TestNativeNonComparableErrorDoesNotPanic verifies arbitrary SDK errors remain
// inspectable without an equality panic in the driver's contextual wrapper.
func TestNativeNonComparableErrorDoesNotPanic(t *testing.T) {
	t.Parallel()

	connection := &failureClient{describeErr: sliceFailure{"unavailable"}}
	driver, err := temporalworkflow.NewDriver(temporal.NewFrom(connection))

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.Status(t.Context(), "order", "run")

	var failure sliceFailure

	if !errors.As(err, &failure) {
		t.Fatalf("Status = %v; want preserved native sliceFailure", err)
	}
}

// TestNativeApplicationFailureOutranksItsTimeoutCause verifies the inverse
// nesting case still preserves the actual terminal application failure. A
// timeout recorded only as its cause must not replace that outer outcome.
func TestNativeApplicationFailureOutranksItsTimeoutCause(t *testing.T) {
	t.Parallel()

	cause := sdktemporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil)
	connection := &failureClient{
		resultErr: sdktemporal.NewApplicationErrorWithCause("application stopped", "application.failure", cause),
	}
	driver, err := temporalworkflow.NewDriver(temporal.NewFrom(connection))

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.Result(t.Context(), "order", "run")

	if !errors.Is(err, workflow.NewError("application.failure", "")) {
		t.Fatalf("Result = %v; want terminal application.failure", err)
	}
}

// differentWorkflowClient describes a real ID belonging to another workflow.
// commands records any read/control that wrongly passes this identity boundary.
type differentWorkflowClient struct {
	// Client leaves unrelated native methods outside the scripted seam.
	client.Client
	// commands counts result, cancel, and signal calls against the wrong type.
	commands int
}

// DescribeWorkflow exposes the native type needed to validate a typed handle.
func (*differentWorkflowClient) DescribeWorkflow(
	context.Context,
	string,
	string,
) (*client.WorkflowExecutionDescription, error) {
	description := &client.WorkflowExecutionDescription{}
	description.WorkflowType.Name = "arkoren-wf-other"

	return description, nil
}

// GetWorkflow records an unintended read and returns a successful empty result.
func (connection *differentWorkflowClient) GetWorkflow(context.Context, string, string) client.WorkflowRun {
	connection.commands++

	return &failureRun{}
}

// CancelWorkflow records an unintended mutation of another workflow's run.
func (connection *differentWorkflowClient) CancelWorkflow(context.Context, string, string) error {
	connection.commands++

	return nil
}

// SignalWorkflow records an unintended delivery to another workflow's run.
func (connection *differentWorkflowClient) SignalWorkflow(context.Context, string, string, string, any) error {
	connection.commands++

	return nil
}

// differentWorkflow constructs the public driver over a mismatching native
// execution description, keeping each typed-handle scenario independent.
func differentWorkflow(t *testing.T) (*temporalworkflow.Driver, *differentWorkflowClient) {
	t.Helper()

	connection := &differentWorkflowClient{}
	driver, err := temporalworkflow.NewDriver(temporal.NewFrom(connection))

	if err != nil {
		t.Fatal(err)
	}

	return driver, connection
}

// TestResultRejectsWrongWorkflowName verifies ID uniqueness does not allow a
// handle for one workflow to read another workflow's result.
func TestResultRejectsWrongWorkflowName(t *testing.T) {
	t.Parallel()

	driver, connection := differentWorkflow(t)

	_, err := driver.Result(t.Context(), "order", "shared-id")

	if !errors.Is(err, workflow.ErrNotFound) || connection.commands != 0 {
		t.Fatalf("Result = %v, native reads = %d; want NotFound without reading", err, connection.commands)
	}
}

// TestStatusRejectsWrongWorkflowName verifies the native execution type is
// checked before reporting a status for a typed workflow handle.
func TestStatusRejectsWrongWorkflowName(t *testing.T) {
	t.Parallel()

	driver, _ := differentWorkflow(t)

	_, err := driver.Status(t.Context(), "order", "shared-id")

	if !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status = %v; want NotFound", err)
	}
}

// TestCancelRejectsWrongWorkflowName verifies a wrong typed handle cannot
// cancel an unrelated workflow that happens to use the supplied namespace ID.
func TestCancelRejectsWrongWorkflowName(t *testing.T) {
	t.Parallel()

	driver, connection := differentWorkflow(t)

	err := driver.Cancel(t.Context(), "order", "shared-id")

	if !errors.Is(err, workflow.ErrNotFound) || connection.commands != 0 {
		t.Fatalf("Cancel = %v, native commands = %d; want NotFound without mutation", err, connection.commands)
	}
}

// TestSignalRejectsWrongWorkflowName verifies a wrong typed handle cannot
// deliver payloads to an unrelated workflow sharing the requested namespace ID.
func TestSignalRejectsWrongWorkflowName(t *testing.T) {
	t.Parallel()

	driver, connection := differentWorkflow(t)

	err := driver.Signal(t.Context(), "order", "shared-id", "approved", []byte(`{"v":true}`))

	if !errors.Is(err, workflow.ErrNotFound) || connection.commands != 0 {
		t.Fatalf("Signal = %v, native commands = %d; want NotFound without delivery", err, connection.commands)
	}
}

// policyRegistry separates the registered handler's policy from the immutable
// definition used by Context.Step. This exposes per-call Derive behavior through
// exported constructors rather than private runtime or retry-policy assertions.
func policyRegistry(
	t *testing.T,
	registered *workflow.Step[struct{}, struct{}],
	invoked *workflow.Step[struct{}, struct{}],
) *workflow.Registry {
	t.Helper()

	definition := workflow.New("policy", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		return ctx.Step(invoked, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	registry, err := workflow.Handlers(definition, registered)

	if err != nil {
		t.Fatal(err)
	}

	return registry
}

// decodedWorkflowFailure reads the existing native application-error envelope
// using public SDK details and core decoding. Unexpected success or a changed
// wire type fails the caller before its stable-error identity assertions.
func decodedWorkflowFailure(t *testing.T, err error) error {
	t.Helper()

	var app *sdktemporal.ApplicationError

	if !errors.As(err, &app) {
		t.Fatalf("workflow error = %v; want native application failure", err)
	}

	if app.Type() != "arkoren.error" {
		t.Fatalf("native failure type = %q; want existing arkoren.error envelope", app.Type())
	}

	var envelope jsontext.Value

	if err := app.Details(&envelope); err != nil {
		t.Fatal(err)
	}

	return workflow.DecodeError(envelope)
}

// TestImmediateStepRetryExecutesNothingAndPreservesUnsupported verifies the
// rejected policy cannot execute even a handler that would succeed immediately,
// and the existing error kind survives native conversion and core decoding.
func TestImmediateStepRetryExecutesNothingAndPreservesUnsupported(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		executions.Add(1)

		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 0)
	})
	environment := publicWorkflow(t, policyRegistry(t, step, step), &testsuite.WorkflowTestSuite{})

	environment.ExecuteWorkflow("arkoren-wf-policy", jsontext.Value(`{"v":{}}`))

	if got := executions.Load(); got != 0 {
		t.Fatalf("step executions = %d; want zero", got)
	}

	if err := decodedWorkflowFailure(t, environment.GetWorkflowError()); !errors.Is(err, workflow.ErrUnsupported) {
		t.Fatalf("workflow failure = %v; want ErrUnsupported", err)
	}
}

// TestDerivedImmediateRetryOverridesRegisteredPositivePolicy verifies the
// completed invocation policy, not the registry's positive default, is rejected.
func TestDerivedImmediateRetryOverridesRegisteredPositivePolicy(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	base := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		executions.Add(1)

		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
	})
	derived := base.Derive(func(step *workflow.StepBuilder) {
		step.Backoff(0, 2, time.Second)
	})
	environment := publicWorkflow(t, policyRegistry(t, base, derived), &testsuite.WorkflowTestSuite{})

	environment.ExecuteWorkflow("arkoren-wf-policy", jsontext.Value(`{"v":{}}`))

	if got := executions.Load(); got != 0 {
		t.Fatalf("derived step executions = %d; want zero", got)
	}

	if err := decodedWorkflowFailure(t, environment.GetWorkflowError()); !errors.Is(err, workflow.ErrUnsupported) {
		t.Fatalf("derived workflow failure = %v; want ErrUnsupported", err)
	}
}

// TestDerivedSingleAttemptAcceptsRegisteredImmediatePolicy verifies registration
// remains valid and a per-call single-attempt override can execute. Retry(0)
// normalizes to one, never the native SDK's unlimited-attempt meaning.
func TestDerivedSingleAttemptAcceptsRegisteredImmediatePolicy(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	base := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		executions.Add(1)

		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 100*time.Millisecond)
	})
	derived := base.Derive(func(step *workflow.StepBuilder) {
		step.Retry(0)
	})

	registry := policyRegistry(t, base, derived)

	command := scheduledActivity(t, registry)

	if command.RetryPolicy.MaximumAttempts != 1 {
		t.Fatalf("native attempts = %d; want one", command.RetryPolicy.MaximumAttempts)
	}

	environment := publicWorkflow(t, registry, &testsuite.WorkflowTestSuite{})

	environment.ExecuteWorkflow("arkoren-wf-policy", jsontext.Value(`{"v":{}}`))

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}

	if got := executions.Load(); got != 1 {
		t.Fatalf("derived step executions = %d; want one", got)
	}
}

// TestDerivedPositiveBackoffAcceptsRegisteredImmediatePolicy verifies a local
// positive delay restores finite native retries even when registration retained
// zero initial delay. The SDK advances retry time without a wall-clock sleep.
func TestDerivedPositiveBackoffAcceptsRegisteredImmediatePolicy(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	base := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		if executions.Add(1) == 1 {
			return struct{}{}, errors.New("retry once")
		}

		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 0)
	})
	derived := base.Derive(func(step *workflow.StepBuilder) {
		step.Backoff(time.Millisecond, 1, time.Millisecond)
	})
	environment := publicWorkflow(t, policyRegistry(t, base, derived), &testsuite.WorkflowTestSuite{})

	environment.ExecuteWorkflow("arkoren-wf-policy", jsontext.Value(`{"v":{}}`))

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}

	if got := executions.Load(); got != 2 {
		t.Fatalf("derived step executions = %d; want two", got)
	}
}

// TestPositiveDefaultStepRetriesRetainFiniteNativePolicy verifies Retry alone
// preserves the core's positive defaults, rather than enabling SDK defaults or
// unbounded activity retry behavior.
func TestPositiveDefaultStepRetriesRetainFiniteNativePolicy(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
	})

	command := scheduledActivity(t, policyRegistry(t, step, step))

	if command.RetryPolicy.MaximumAttempts != 3 || command.RetryPolicy.BackoffCoefficient != 2 {
		t.Fatalf("native retry policy = %v; want three attempts and factor two", command.RetryPolicy)
	}

	if command.RetryPolicy.InitialInterval.AsDuration() != time.Second {
		t.Fatalf("native initial interval = %s; want one second", command.RetryPolicy.InitialInterval)
	}

	if command.RetryPolicy.MaximumInterval.AsDuration() != 30*time.Second {
		t.Fatalf("native maximum interval = %s; want thirty seconds", command.RetryPolicy.MaximumInterval)
	}
}

// TestUnknownStepPrecedesImmediateRetryRejection preserves existing name
// validation precedence when an unsupported policy also names an absent step.
func TestUnknownStepPrecedesImmediateRetryRejection(t *testing.T) {
	t.Parallel()

	registered := workflow.NewStep("registered", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	})
	missing := workflow.NewStep("missing", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 0)
	})
	environment := publicWorkflow(t, policyRegistry(t, registered, missing), &testsuite.WorkflowTestSuite{})

	environment.ExecuteWorkflow("arkoren-wf-policy", jsontext.Value(`{"v":{}}`))

	if err := decodedWorkflowFailure(t, environment.GetWorkflowError()); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("workflow failure = %v; want ErrNotFound", err)
	}
}

// TestRunTimeoutPrecedesImmediateRetryRejection preserves the run cancellation
// guard before policy validation. The SDK advances durable time without sleeps.
func TestRunTimeoutPrecedesImmediateRetryRejection(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 0)
	})
	definition := workflow.New("policy", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		if err := ctx.Sleep(2 * time.Hour); !errors.Is(err, workflow.ErrTimeout) {
			return struct{}{}, err
		}

		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	environment := publicWorkflow(t, registry, &testsuite.WorkflowTestSuite{})

	environment.ExecuteWorkflow("arkoren-wf-policy", jsontext.Value(`{"v":{}}`))

	if err := decodedWorkflowFailure(t, environment.GetWorkflowError()); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("workflow failure = %v; want ErrTimeout", err)
	}
}

// TestRunCancellationPrecedesImmediateRetryRejection verifies explicit native
// workflow cancellation keeps its cancellation outcome instead of being masked
// by a policy error when the handler observes a step after cancellation.
func TestRunCancellationPrecedesImmediateRetryRejection(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 0)
	})
	definition := workflow.New("policy", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		if err := ctx.Sleep(2 * time.Hour); !errors.Is(err, workflow.ErrCanceled) {
			return struct{}{}, err
		}

		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	environment := publicWorkflow(t, registry, &testsuite.WorkflowTestSuite{})
	environment.RegisterDelayedCallback(environment.CancelWorkflow, time.Second)

	environment.ExecuteWorkflow("arkoren-wf-policy", jsontext.Value(`{"v":{}}`))

	if err := environment.GetWorkflowError(); !sdktemporal.IsCanceledError(err) {
		t.Fatalf("native workflow failure = %v; want cancellation", err)
	}
}

// replayProbe captures exported Runner registrations through the native SDK
// plugin boundary. Replay never executes or substitutes an Arkoren private helper.
type replayProbe struct {
	// activityProbe supplies native plugin identity and no-poll startup.
	*activityProbe
	// target receives the actual workflow functions registered by Host.Start.
	target worker.WorkflowReplayer
}

// ConfigureWorker forwards native workflow registrations to the public replayer.
// Activities need no replayer registration because their results come from history.
func (probe *replayProbe) ConfigureWorker(_ context.Context, options worker.PluginConfigureWorkerOptions) error {
	options.WorkerRegistryOptions.OnRegisterWorkflow = probe.target.RegisterWorkflowWithOptions

	return nil
}

// publicReplayer constructs a real exported Runner and captures its workflows
// at the SDK registry seam. The resulting SDK replayer performs actual command
// matching against historical events without a service or private registration.
func publicReplayer(t *testing.T, registry *workflow.Registry) worker.WorkflowReplayer {
	t.Helper()

	replayer := worker.NewWorkflowReplayer()
	options := protocolOptions(emptyProtocolResponse)
	options.Plugins = []client.Plugin{&replayProbe{activityProbe: &activityProbe{}, target: replayer}}

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	host := temporal.NewFrom(connection)

	if _, err := temporalworkflow.NewRunner(host, registry); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return replayer
}

// legacyPolicyHistory records a completed one-step run made by the old zero
// initial fallback: its native interval was min(one second, one hundred ms).
// The same history is valid for an explicitly positive core policy that maps to
// those native settings. Event IDs and wire names match the real SDK protocol.
func legacyPolicyHistory() *historypb.History {
	history := firstTask("legacy-policy").History
	value := &commonpb.Payloads{Payloads: []*commonpb.Payload{{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte(`{"v":{}}`),
	}}}
	history.Events = append(history.Events,
		&historypb.HistoryEvent{EventId: 4, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{
				WorkflowTaskCompletedEventAttributes: &historypb.WorkflowTaskCompletedEventAttributes{
					ScheduledEventId: 2, StartedEventId: 3,
				},
			}},
		&historypb.HistoryEvent{EventId: 5, EventType: enumspb.EVENT_TYPE_TIMER_STARTED,
			Attributes: &historypb.HistoryEvent_TimerStartedEventAttributes{
				TimerStartedEventAttributes: &historypb.TimerStartedEventAttributes{
					TimerId: "5", StartToFireTimeout: durationpb.New(time.Hour), WorkflowTaskCompletedEventId: 4,
				},
			}},
		&historypb.HistoryEvent{EventId: 6, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
			Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
				ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
					ActivityId: "6", ActivityType: &commonpb.ActivityType{Name: "arkoren-step-step"},
					TaskQueue: &taskqueuepb.TaskQueue{Name: "arkoren"}, Input: value,
					StartToCloseTimeout: durationpb.New(time.Minute), WorkflowTaskCompletedEventId: 4,
					RetryPolicy: &commonpb.RetryPolicy{
						InitialInterval: durationpb.New(100 * time.Millisecond), BackoffCoefficient: 2,
						MaximumInterval: durationpb.New(100 * time.Millisecond), MaximumAttempts: 3,
					},
				},
			}},
		&historypb.HistoryEvent{EventId: 7, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED,
			Attributes: &historypb.HistoryEvent_ActivityTaskStartedEventAttributes{
				ActivityTaskStartedEventAttributes: &historypb.ActivityTaskStartedEventAttributes{ScheduledEventId: 6},
			}},
		&historypb.HistoryEvent{EventId: 8, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED,
			Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{
				ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{
					ScheduledEventId: 6, StartedEventId: 7, Result: value,
				},
			}},
		&historypb.HistoryEvent{EventId: 9, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{
				WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{
					TaskQueue: &taskqueuepb.TaskQueue{Name: "arkoren"},
				},
			}},
		&historypb.HistoryEvent{EventId: 10, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskStartedEventAttributes{
				WorkflowTaskStartedEventAttributes: &historypb.WorkflowTaskStartedEventAttributes{ScheduledEventId: 9},
			}},
		&historypb.HistoryEvent{EventId: 11, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{
				WorkflowTaskCompletedEventAttributes: &historypb.WorkflowTaskCompletedEventAttributes{
					ScheduledEventId: 9, StartedEventId: 10,
				},
			}},
		&historypb.HistoryEvent{EventId: 12, EventType: enumspb.EVENT_TYPE_TIMER_CANCELED,
			Attributes: &historypb.HistoryEvent_TimerCanceledEventAttributes{
				TimerCanceledEventAttributes: &historypb.TimerCanceledEventAttributes{
					TimerId: "5", StartedEventId: 5, WorkflowTaskCompletedEventId: 11,
				},
			}},
		&historypb.HistoryEvent{EventId: 13, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{
				WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{
					Result: value, WorkflowTaskCompletedEventId: 11,
				},
			}},
	)

	return history
}

// TestLegacyImmediateRetryHistoryRequiresExplicitWorkerRollout demonstrates that
// rejecting a formerly scheduled step changes command selection. This driver
// does not add a version marker or pretend that upgrading old histories is safe.
func TestLegacyImmediateRetryHistoryRequiresExplicitWorkerRollout(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 100*time.Millisecond)
	})
	replayer := publicReplayer(t, policyRegistry(t, step, step))
	logger := sdklog.NewStructuredLogger(slog.New(slog.DiscardHandler))

	err := replayer.ReplayWorkflowHistory(logger, legacyPolicyHistory())

	if err == nil {
		t.Fatal("legacy zero-policy history unexpectedly replayed; rollout requirement is no longer demonstrated")
	}

	t.Logf("legacy zero-policy replay requires migration: %v", err)
}

// TestExplicitPositivePolicyReplaysLegacyNativeFallback verifies opting into
// the old observed positive delay preserves this representative history's wire
// names and command sequence, without a driver-owned native version marker.
func TestExplicitPositivePolicyReplaysLegacyNativeFallback(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(time.Second, 2, 100*time.Millisecond)
	})
	replayer := publicReplayer(t, policyRegistry(t, step, step))
	logger := sdklog.NewStructuredLogger(slog.New(slog.DiscardHandler))

	if err := replayer.ReplayWorkflowHistory(logger, legacyPolicyHistory()); err != nil {
		t.Fatalf("explicit positive policy changed legacy native commands: %v", err)
	}
}

// firstTask supplies the minimal native history needed to execute one initial
// workflow task. Facade input is empty, so policy-only handlers accept struct{}.
func firstTask(id string) *workflowservice.PollWorkflowTaskQueueResponse {
	return &workflowservice.PollWorkflowTaskQueueResponse{
		TaskToken:         []byte("task"),
		WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: id, RunId: id},
		WorkflowType:      &commonpb.WorkflowType{Name: "arkoren-wf-policy"},
		StartedEventId:    3,
		Attempt:           1,
		History: &historypb.History{Events: []*historypb.HistoryEvent{
			{
				EventId: 1, EventTime: timestamppb.Now(), EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
				Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
					WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
						WorkflowType: &commonpb.WorkflowType{Name: "arkoren-wf-policy"},
						TaskQueue:    &taskqueuepb.TaskQueue{Name: "arkoren"},
						Input: &commonpb.Payloads{Payloads: []*commonpb.Payload{{
							Metadata: map[string][]byte{"encoding": []byte("json/plain")},
							Data:     []byte(`{"v":{}}`),
						}}},
					},
				},
			},
			{EventId: 2, EventTime: timestamppb.Now(), EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
				Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{
					WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{
						TaskQueue: &taskqueuepb.TaskQueue{Name: "arkoren"},
					},
				}},
			{EventId: 3, EventTime: timestamppb.Now(), EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED,
				Attributes: &historypb.HistoryEvent_WorkflowTaskStartedEventAttributes{
					WorkflowTaskStartedEventAttributes: &historypb.WorkflowTaskStartedEventAttributes{ScheduledEventId: 2},
				}},
		}},
	}
}

// workflowCommands starts the exported Runner against a scripted native SDK
// protocol and returns the commands emitted by its first workflow task.
// No private runner registration or runtime helper is invoked by the test.
func workflowCommands(t *testing.T, registry *workflow.Registry) []*commandpb.Command {
	t.Helper()

	completed := make(chan *workflowservice.RespondWorkflowTaskCompletedRequest, 1)
	failed := make(chan string, 1)
	var delivered sync.Once
	options := protocolOptions(func(
		ctx context.Context,
		_ string,
		request, response any,
		_ *grpc.ClientConn,
		_ grpc.UnaryInvoker,
		_ ...grpc.CallOption,
	) error {
		switch message := request.(type) {
		case *workflowservice.PollWorkflowTaskQueueRequest:
			first := false
			delivered.Do(func() {
				first = true
			})

			if first {
				proto.Merge(response.(*workflowservice.PollWorkflowTaskQueueResponse), firstTask(t.Name()))

				return nil
			}

			<-ctx.Done()

			return ctx.Err()
		case *workflowservice.PollActivityTaskQueueRequest:
			<-ctx.Done()

			return ctx.Err()
		case *workflowservice.RespondWorkflowTaskCompletedRequest:
			completed <- message
		case *workflowservice.RespondWorkflowTaskFailedRequest:
			failed <- fmt.Sprint(message.Failure)
		}

		return nil
	})

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	host := temporal.NewFrom(connection, func(host *temporal.Builder) {
		host.WorkerStopTimeout(-time.Second)
	})

	if _, err := temporalworkflow.NewRunner(host, registry); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	var request *workflowservice.RespondWorkflowTaskCompletedRequest

	select {
	case request = <-completed:
	case err := <-failed:
		t.Fatalf("native workflow task failed: %s", err)
	case <-time.After(5 * time.Second):
		t.Fatal("native worker did not complete the initial task")
	}

	return request.Commands
}

// scheduledActivity selects the activity command from an exported Runner's
// initial native workflow task, failing if orchestration did not schedule it.
func scheduledActivity(t *testing.T, registry *workflow.Registry) *commandpb.ScheduleActivityTaskCommandAttributes {
	t.Helper()

	commands := workflowCommands(t, registry)

	for _, command := range commands {
		if activity := command.GetScheduleActivityTaskCommandAttributes(); activity != nil {
			return activity
		}
	}

	t.Fatalf("no scheduled activity in commands: %v", commands)

	return nil
}

// TestStepZeroTimeoutUsesWorkflowBound verifies a published unlimited step does
// not acquire an undocumented one-minute attempt timeout at the native boundary.
func TestStepZeroTimeoutUsesWorkflowBound(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Timeout(0)
	})
	definition := workflow.New("policy", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(2 * time.Hour)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	command := scheduledActivity(t, registry)

	if command.StartToCloseTimeout.AsDuration() != 0 {
		t.Fatalf("start-to-close timeout = %s; want no attempt limit", command.StartToCloseTimeout.AsDuration())
	}

	if command.ScheduleToCloseTimeout.AsDuration() != 2*time.Hour {
		t.Fatalf("schedule-to-close timeout = %s; want workflow bound", command.ScheduleToCloseTimeout.AsDuration())
	}
}

// TestStepBackoffCapsFirstRetry verifies Max applies to the initial retry as
// the core StepOptions.Backoff contract specifies, not only to later retries.
func TestStepBackoffCapsFirstRetry(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(time.Second, 2, 100*time.Millisecond)
	})
	definition := workflow.New("policy", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	command := scheduledActivity(t, registry)

	if command.RetryPolicy.InitialInterval.AsDuration() != 100*time.Millisecond {
		t.Fatalf("initial retry interval = %s; want 100ms", command.RetryPolicy.InitialInterval.AsDuration())
	}

	if command.RetryPolicy.MaximumInterval.AsDuration() != 100*time.Millisecond {
		t.Fatalf("maximum retry interval = %s; want 100ms", command.RetryPolicy.MaximumInterval.AsDuration())
	}

	if command.RetryPolicy.MaximumAttempts != 3 || command.RetryPolicy.BackoffCoefficient != 2 {
		t.Fatalf("retry policy = %v; want three attempts and factor two", command.RetryPolicy)
	}
}

// TestStepUncappedBackoffAvoidsNativeDefault verifies core's zero maximum does
// not become Temporal's implicit cap of one hundred initial intervals.
func TestStepUncappedBackoffAvoidsNativeDefault(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(10)
		step.Backoff(time.Second, 2, 0)
	})
	definition := workflow.New("policy", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	command := scheduledActivity(t, registry)

	if command.RetryPolicy.MaximumInterval.AsDuration() != time.Duration(math.MaxInt64) {
		t.Fatalf("maximum retry interval = %s; want largest Go duration", command.RetryPolicy.MaximumInterval.AsDuration())
	}
}

// TestImmediateStepRetryEmitsNoActivityCommand verifies an unsupported completed
// policy fails closed instead of becoming a positive native retry delay.
func TestImmediateStepRetryEmitsNoActivityCommand(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("step", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, 100*time.Millisecond)
	})
	definition := workflow.New("policy", func(ctx *workflow.Context, in struct{}) (struct{}, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	commands := workflowCommands(t, registry)

	for _, command := range commands {
		if command.GetScheduleActivityTaskCommandAttributes() != nil {
			t.Fatalf("unsupported immediate retries scheduled an activity: %v", command)
		}
	}
}

// clientPluginBase disambiguates the two native PluginBase embedding names;
// their sealed methods retain SDK identity through this alias.
type clientPluginBase = client.PluginBase

// activityProbe routes native worker registrations into the SDK activity test
// environment. It substitutes only the native worker's external polling loop;
// exported constructors and native SDK registration/conversion still run.
type activityProbe struct {
	// clientPluginBase supplies the native client's default plugin callbacks.
	clientPluginBase
	// PluginBase supplies forwarding defaults for unused worker callbacks.
	worker.PluginBase
	// environment executes activities captured at the public SDK registry seam.
	environment *testsuite.TestActivityEnvironment
	// options captures the completed native record received by worker.New.
	options worker.Options
}

// Name identifies the native test plugin without affecting persisted names.
func (*activityProbe) Name() string {
	return "activity-probe"
}

// ConfigureWorker installs the SDK's registration callback before Arkoren's
// constructor-supplied registration runs. No private driver function is called.
func (probe *activityProbe) ConfigureWorker(_ context.Context, options worker.PluginConfigureWorkerOptions) error {
	probe.options = *options.WorkerOptions
	options.WorkerRegistryOptions.OnRegisterActivity = probe.environment.RegisterActivityWithOptions

	return nil
}

// StartWorker avoids native server polling; activities execute in the SDK test
// environment after being registered through the actual Host.Start path.
func (*activityProbe) StartWorker(
	context.Context,
	worker.PluginStartWorkerOptions,
	func(context.Context, worker.PluginStartWorkerOptions) error,
) error {
	return nil
}

// scheduleActivity constructs and starts a real exported schedule driver, with
// a native SDK registration probe and an in-process schedule-list response.
func scheduleActivity(t *testing.T) (*temporalschedule.Driver, *testsuite.TestActivityEnvironment) {
	t.Helper()

	environment := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	options := protocolOptions(emptyProtocolResponse)
	options.Plugins = []client.Plugin{&activityProbe{environment: environment}}

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	host := temporal.NewFrom(connection)
	driver, err := temporalschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return driver, environment
}

// TestScheduleActivityWaitsForRun verifies a deferred firing delivered before
// Run is not lost as a single-attempt ErrNotStarted failure. The native activity
// waits for its local dispatcher while its activity/worker context remains live.
func TestScheduleActivityWaitsForRun(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, environment := scheduleActivity(t)
		finished := make(chan error, 1)

		go func() {
			_, err := environment.ExecuteActivity("arkoren-schedule-run", map[string]any{"Name": "later"})
			finished <- err
		}()

		synctest.Wait()

		select {
		case err := <-finished:
			t.Fatalf("activity ended before Run: %v", err)
		default:
		}

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		ran := make(chan error, 1)

		go func() {
			ran <- driver.Run(ctx, nil, func(_ context.Context, name string, _ []byte) error {
				if name != "later" {
					t.Errorf("dispatch name = %q; want later", name)
				}

				return nil
			})
		}()

		if err := <-finished; err != nil {
			t.Fatal(err)
		}

		cancel()

		if err := <-ran; err != nil {
			t.Fatal(err)
		}
	})
}

// TestScheduleRunCancellationReachesActiveDispatch verifies the dispatch's
// cancellation is owned by Run as well as the native activity/worker lifetime.
func TestScheduleRunCancellationReachesActiveDispatch(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, environment := scheduleActivity(t)
		workerStop := make(chan struct{})
		environment.SetWorkerStopChannel(workerStop)
		t.Cleanup(func() {
			close(workerStop)
		})
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		started := make(chan struct{})
		stopped := make(chan struct{})
		ran := make(chan error, 1)

		go func() {
			ran <- driver.Run(ctx, nil, func(ctx context.Context, _ string, _ []byte) error {
				close(started)
				<-ctx.Done()
				close(stopped)

				return ctx.Err()
			})
		}()

		synctest.Wait()

		finished := make(chan error, 1)

		go func() {
			_, err := environment.ExecuteActivity("arkoren-schedule-run", map[string]any{"Name": "later"})
			finished <- err
		}()

		<-started
		cancel()

		if err := <-ran; err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		select {
		case <-stopped:
		default:
			t.Fatal("Run cancellation did not cancel active dispatch")
		}

		<-finished
	})
}

// TestScheduleWorkerStopSignalDoesNotPreemptNativeGrace verifies the SDK's
// immediate stop notification does not cancel an already running callback.
// Native activity-context cancellation, or Run cancellation, ends its grace.
func TestScheduleWorkerStopSignalDoesNotPreemptNativeGrace(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, environment := scheduleActivity(t)
		workerStop := make(chan struct{})
		environment.SetWorkerStopChannel(workerStop)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		started := make(chan struct{})
		stopped := make(chan struct{})
		ran := make(chan error, 1)

		go func() {
			ran <- driver.Run(ctx, nil, func(ctx context.Context, _ string, _ []byte) error {
				close(started)
				<-ctx.Done()
				close(stopped)

				return ctx.Err()
			})
		}()

		synctest.Wait()

		finished := make(chan error, 1)

		go func() {
			_, err := environment.ExecuteActivity("arkoren-schedule-run", map[string]any{"Name": "later"})
			finished <- err
		}()

		<-started
		close(workerStop)
		synctest.Wait()

		select {
		case <-stopped:
			t.Error("worker stop notification preempted the SDK's activity grace period")
		default:
		}

		cancel()
		<-finished

		if err := <-ran; err != nil {
			t.Fatal(err)
		}
	})
}

// TestScheduleRestartDoesNotRetainStoppedDispatch verifies a later native
// delivery waits for the next local Run and never invokes the exited Run.
func TestScheduleRestartDoesNotRetainStoppedDispatch(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, environment := scheduleActivity(t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		oldCalls := make(chan struct{}, 1)
		ran := make(chan error, 1)

		go func() {
			ran <- driver.Run(ctx, nil, func(context.Context, string, []byte) error {
				oldCalls <- struct{}{}

				return nil
			})
		}()

		synctest.Wait()
		cancel()

		if err := <-ran; err != nil {
			t.Fatal(err)
		}

		finished := make(chan error, 1)

		go func() {
			_, err := environment.ExecuteActivity("arkoren-schedule-run", map[string]any{"Name": "later"})
			finished <- err
		}()

		synctest.Wait()

		select {
		case <-oldCalls:
			t.Fatal("stopped Run received a later delivery")
		default:
		}

		next, stop := context.WithCancel(t.Context())
		t.Cleanup(stop)

		go func() {
			ran <- driver.Run(next, nil, func(context.Context, string, []byte) error {
				return nil
			})
		}()

		if err := <-finished; err != nil {
			t.Fatal(err)
		}

		stop()

		if err := <-ran; err != nil {
			t.Fatal(err)
		}
	})
}

// TestScheduleRejectsConcurrentLocalRun verifies that a second Run cannot
// replace the first dispatcher's cancellation owner or reconcile its schedules.
func TestScheduleRejectsConcurrentLocalRun(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, _ := scheduleActivity(t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		dispatch := func(context.Context, string, []byte) error {
			return nil
		}
		ran := make(chan error, 1)

		go func() {
			ran <- driver.Run(ctx, nil, dispatch)
		}()

		synctest.Wait()

		if err := driver.Run(t.Context(), nil, dispatch); !errors.Is(err, schedule.ErrRunning) {
			t.Fatalf("second Run = %v; want ErrRunning", err)
		}

		cancel()

		if err := <-ran; err != nil {
			t.Fatal(err)
		}
	})
}

// nativeWorkerOptions observes Host's completed worker configuration through the
// SDK's public plugin boundary, not a private host option-normalization helper.
func nativeWorkerOptions(t *testing.T, configure ...temporal.BuilderFunc) worker.Options {
	t.Helper()

	probe := &activityProbe{environment: (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()}
	options := protocolOptions(emptyProtocolResponse)
	options.Plugins = []client.Plugin{probe}

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	host := temporal.NewFrom(connection, configure...)

	if err := host.Register(func(worker.Registry) {
		// Registration presence alone requests worker construction.
	}); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	return probe.options
}

// TestBorrowedClientHonorsHostWorkerStopTimeout validates NewFrom applies the
// host's graceful-stop fallback when the native worker record leaves it zero.
func TestBorrowedClientHonorsHostWorkerStopTimeout(t *testing.T) {
	t.Parallel()

	options := nativeWorkerOptions(t, func(host *temporal.Builder) {
		host.WorkerStopTimeout(3 * time.Second)
	})

	if options.WorkerStopTimeout != 3*time.Second {
		t.Fatalf("native stop timeout = %s; want 3s", options.WorkerStopTimeout)
	}
}

// TestBorrowedClientNativeWorkerTimeoutWins validates native nonzero policy
// takes precedence over the host fallback on the actual worker.New boundary.
func TestBorrowedClientNativeWorkerTimeoutWins(t *testing.T) {
	t.Parallel()

	options := nativeWorkerOptions(t, func(host *temporal.Builder) {
		host.WorkerStopTimeout(time.Second)
		host.WorkerOptions(worker.Options{WorkerStopTimeout: 7 * time.Second})
	})

	if options.WorkerStopTimeout != 7*time.Second {
		t.Fatalf("native stop timeout = %s; want 7s", options.WorkerStopTimeout)
	}
}

// TestBorrowedClientNegativeHostTimeoutStopsWithoutGrace verifies the host's
// negative disable convention is normalized to native zero before worker.New.
func TestBorrowedClientNegativeHostTimeoutStopsWithoutGrace(t *testing.T) {
	t.Parallel()

	options := nativeWorkerOptions(t, func(host *temporal.Builder) {
		host.WorkerStopTimeout(-time.Second)
	})

	if options.WorkerStopTimeout != 0 {
		t.Fatalf("native stop timeout = %s; want zero", options.WorkerStopTimeout)
	}
}

// TestScheduleDeferBeforeRunUsesDistinctNativeIDs verifies submission needs a
// connected native client, not a local Run, and preserves the deferred workflow
// namespace while returning distinct opaque IDs for otherwise identical jobs.
func TestScheduleDeferBeforeRunUsesDistinctNativeIDs(t *testing.T) {
	t.Parallel()

	driver, _ := scheduleActivity(t)
	job := schedule.Job{Name: "later", At: time.Now().Add(time.Hour), Payload: []byte("input")}

	first, err := driver.Defer(t.Context(), job)

	if err != nil {
		t.Fatal(err)
	}

	second, err := driver.Defer(t.Context(), job)

	if err != nil {
		t.Fatal(err)
	}

	if first == second || first == "" {
		t.Fatalf("deferred identifiers = %q, %q; want distinct nonempty IDs", first, second)
	}

	if !strings.HasPrefix(first, "arkoren-deferred-") {
		t.Fatalf("deferred namespace changed: %q", first)
	}
}

// workflowProbe forwards actual Host registrations into the public SDK replay
// environment. Its embedded native plugin substitutes only server polling.
type workflowProbe struct {
	// activityProbe supplies native plugin identity and no-poll startup.
	*activityProbe
	// environment receives both workflow and activity registrations.
	environment *testsuite.TestWorkflowEnvironment
}

// ConfigureWorker captures native registry callbacks before Arkoren's runner
// registers handlers. The test never calls private Runner registration helpers.
func (probe *workflowProbe) ConfigureWorker(_ context.Context, options worker.PluginConfigureWorkerOptions) error {
	options.WorkerRegistryOptions.OnRegisterWorkflow = probe.environment.RegisterWorkflowWithOptions
	options.WorkerRegistryOptions.OnRegisterActivity = probe.environment.RegisterActivityWithOptions

	return nil
}

// publicWorkflow starts an exported Runner through SDK registration hooks, then
// returns the public replay environment configured by the caller's native suite.
func publicWorkflow(
	t *testing.T,
	registry *workflow.Registry,
	suite *testsuite.WorkflowTestSuite,
) *testsuite.TestWorkflowEnvironment {
	t.Helper()

	environment := suite.NewTestWorkflowEnvironment()
	probe := &workflowProbe{activityProbe: &activityProbe{}, environment: environment}
	options := protocolOptions(emptyProtocolResponse)
	options.Plugins = []client.Plugin{probe}

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	host := temporal.NewFrom(connection)

	if _, err := temporalworkflow.NewRunner(host, registry); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return environment
}

// secretLogValue intentionally has unsafe default formatting; only LogValue is
// safe to forward to a native logger. This models credential redaction types.
type secretLogValue string

// LogValue supplies the safe representation expected at every slog boundary.
func (secretLogValue) LogValue() slog.Value {
	return slog.StringValue("[redacted]")
}

// String exposes the secret if the adapter incorrectly bypasses LogValue.
func (value secretLogValue) String() string {
	return string(value)
}

// nativeLogCapture models a native logger that formats values without slog's
// secondary resolution, exposing any unresolved secret forwarded by Arkoren.
type nativeLogCapture struct {
	// Logger silently accepts unrelated SDK diagnostics.
	sdklog.Logger
	// message selects the specific workflow or worker record under observation.
	message string
	// records receives the one workflow record under test.
	records chan string
}

// Info records the workflow's test message without applying slog redaction a
// second time, which would hide a broken adapter.
func (logger *nativeLogCapture) Info(message string, values ...any) {
	if message == logger.message {
		logger.records <- fmt.Sprint(values...)
	}
}

// TestWorkflowLoggerResolvesRedactionBeforeNativeBoundary verifies direct,
// pre-bound, and grouped LogValuers cannot expose their unsafe native formatting.
func TestWorkflowLoggerResolvesRedactionBeforeNativeBoundary(t *testing.T) {
	t.Parallel()

	definition := workflow.New("redaction", func(ctx *workflow.Context, _ struct{}) (struct{}, error) {
		logger := ctx.Logger().With("bound", secretLogValue("raw-bound")).WithGroup("nested")

		logger.InfoContext(context.Background(), "redaction",
			"direct", secretLogValue("raw-direct"),
			slog.Group("group", "leaf", secretLogValue("raw-group")))

		return struct{}{}, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	logger := &nativeLogCapture{
		Logger:  sdklog.NewStructuredLogger(slog.New(slog.DiscardHandler)),
		message: "redaction",
		records: make(chan string, 1),
	}
	suite := &testsuite.WorkflowTestSuite{}
	suite.SetLogger(logger)
	environment := publicWorkflow(t, registry, suite)

	environment.ExecuteWorkflow("arkoren-wf-redaction", jsontext.Value(`{"v":{}}`))

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}

	record := <-logger.records

	if strings.Contains(record, "raw-") {
		t.Fatalf("native logger received unresolved secrets: %s", record)
	}

	if strings.Count(record, "[redacted]") != 3 {
		t.Fatalf("native log record = %q; want three redacted values", record)
	}
}
