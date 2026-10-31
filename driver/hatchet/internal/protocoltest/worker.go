package protocoltest

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/driver/hatchet"
)

// Worker models native registration and one assigned action, not engine
// scheduling or durable history. Construct the public adapter on Host before
// starting it. Tests observe actual SDK lifecycle events through Outcomes.
//
//	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "task", `{"input":{}}`)
//	// Register a native task on worker.Host, then start the host.
//	if err := worker.Host.Start(t.Context()); err != nil {
//		t.Fatal(err)
//	}
type Worker struct {
	// Host is the unstarted host connected only to this fixture's local servers.
	//
	//	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "task", `{"input":{}}`)
	//	_ = worker.Host.Register(registration)
	Host *hatchet.Host
	// Version supplies native version-probe responses. Set it before Host.Start;
	// tests can distinguish the host's probe from the SDK's independent probe.
	//
	//	worker.Version = func() (string, error) {
	//		return "v0.114.11", nil
	//	}
	Version func() (string, error)
	// Outcomes receives terminal native action events as protobuf JSON. STARTED
	// events are omitted. Reading it observes the actual worker protocol result.
	//
	//	select {
	//	case event := <-worker.Outcomes:
	//		_ = event
	//	case <-ctx.Done():
	//		return ctx.Err()
	//	}
	Outcomes <-chan string
	// Started closes when the SDK reports its first STARTED event, before the
	// application callback. It orders public readiness tests without sleeps.
	//
	//	select {
	//	case <-worker.Started:
	//		// The native action has reached the worker.
	//	case <-ctx.Done():
	//		return ctx.Err()
	//	}
	Started <-chan struct{}
	// RetryCount supplies the native assignment's zero-based retry count. Set it
	// before Start to observe adapter behavior at a finite retry boundary.
	//
	//	worker.RetryCount = 2 // Third native attempt.
	RetryCount int32
	// Deliver optionally gates the first server assignment. Set it before Start
	// to order delivery against a public lifecycle operation, without sleeps.
	//
	//	deliver := make(chan struct{})
	//	worker.Deliver = deliver
	//	// Finish the lifecycle operation, then release native assignment.
	//	close(deliver)
	Deliver <-chan struct{}
	// workflow selects the exact registered native action assigned by ListenV2.
	workflow string
	// payload is the native action payload, including its input object.
	payload string
	// mutex protects action identities learned through native registrations.
	mutex sync.Mutex
	// actions maps workflow names to the SDK's actual registered action IDs.
	actions map[string]string
	// outcomes is the writable side of Outcomes, used only by RPC handlers.
	outcomes chan string
	// started is the writable event notification; this fixture assigns one task.
	started chan struct{}
}

// NewWorker creates local REST/gRPC endpoints and an unstarted worker host.
// It assigns one action when the SDK subscribes; no real engine, credentials,
// retries, timers, or engine persistence are simulated. Cleanup stops the host
// before canceling and joining the protocol servers.
//
//	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "task", `{"input":{}}`)
//	if err := worker.Host.Start(t.Context()); err != nil {
//		t.Fatal(err)
//	}
func NewWorker(t *testing.T, handler http.Handler, workflow, payload string) *Worker {
	t.Helper()

	outcomes := make(chan string, 1)
	started := make(chan struct{})

	worker := &Worker{
		workflow: workflow, payload: payload, actions: map[string]string{},
		outcomes: outcomes, Outcomes: outcomes,
		started: started, Started: started,
		Version: func() (string, error) {
			return "v0.114.11", nil
		},
	}

	address := GRPC(t, worker.rpc)
	worker.Host = Host(t, handler, func(host *hatchet.Builder) {
		host.NoWorker(false)
		host.HostPort(address)
	})

	return worker
}

// rpc supplies only the public worker protocol needed for action invocation.
// Unused acknowledgements are empty messages; version failures stay native gRPC
// errors so tests exercise the SDK's real capability decisions.
func (worker *Worker) rpc(ctx context.Context, method, input string) (string, error) {
	switch {
	case strings.HasSuffix(method, "/GetVersion"):
		version, err := worker.Version()

		return fmt.Sprintf(`{"version":%q}`, version), err
	case strings.HasSuffix(method, "/PutWorkflow"):
		return `{}`, worker.register(input)
	case strings.HasSuffix(method, "/Register"):
		return `{"workerId":"77777777-7777-7777-7777-777777777777","tenantId":"44444444-4444-4444-4444-444444444444"}`, nil
	case strings.HasSuffix(method, "/ListenV2"):
		return worker.assignment(ctx)
	case strings.HasSuffix(method, "/DurableTask"):
		return memoReply(input)
	case strings.HasSuffix(method, "/SendStepActionEvent"):
		if strings.Contains(input, "STEP_EVENT_TYPE_STARTED") {
			close(worker.started)

			return `{}`, nil
		}

		worker.outcomes <- input
	}

	return `{}`, nil
}

// memoReply acknowledges fresh memo nodes so the SDK computes their actual
// values. It provides no replay or persistence simulation: these tests establish
// native context boundaries, not engine durability or durable child joining.
func memoReply(input string) (string, error) {
	var request struct {
		// RegisterWorker acknowledges initial durable stream ownership.
		RegisterWorker jsontext.Value `json:"registerWorker"`
		// Memo supplies the native identity echoed in its acknowledgement.
		Memo *struct {
			// DurableTaskExternalID identifies the native task, not a logical run.
			DurableTaskExternalID string `json:"durableTaskExternalId"`
			// InvocationCount identifies this invocation on the engine.
			InvocationCount int `json:"invocationCount"`
		} `json:"memo"`
	}

	if err := json.Unmarshal([]byte(input), &request); err != nil {
		return "", err
	}

	if len(request.RegisterWorker) != 0 {
		return `{"registerWorker":` + string(request.RegisterWorker) + `}`, nil
	}

	if request.Memo == nil {
		return "", nil
	}

	return fmt.Sprintf(`{"memoAck":{"ref":{"durableTaskExternalId":%q,"invocationCount":%d,"nodeId":"1","branchId":"1"}}}`,
		request.Memo.DurableTaskExternalID, request.Memo.InvocationCount), nil
}

// assignment waits for any explicit delivery barrier, then addresses the SDK's
// registered action. Stream cancellation releases a blocked barrier on cleanup.
func (worker *Worker) assignment(ctx context.Context) (string, error) {
	if worker.Deliver != nil {
		select {
		case <-worker.Deliver:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	worker.mutex.Lock()
	action := worker.actions[worker.workflow]
	worker.mutex.Unlock()

	return fmt.Sprintf(`{"actionId":%q,"actionPayload":%q,"workflowRunId":"33333333-3333-3333-3333-333333333333","taskRunExternalId":"66666666-6666-6666-6666-666666666666","retryCount":%d}`, action, worker.payload, worker.RetryCount), nil
}

// register captures public native action identities, avoiding assumptions about
// how the SDK names callbacks. Missing single-task registrations are errors.
func (worker *Worker) register(input string) error {
	var definition struct {
		// Name is the fully namespaced engine workflow identity.
		Name string `json:"name"`
		// Tasks supplies the single action registered for this standalone task.
		Tasks []struct {
			// Action is the native dispatch identity, not a facade run ID.
			Action string `json:"action"`
		} `json:"tasks"`
	}

	if err := json.Unmarshal([]byte(input), &definition); err != nil {
		return err
	}

	if len(definition.Tasks) != 1 {
		return fmt.Errorf("expected one native task for %s", definition.Name)
	}

	worker.mutex.Lock()
	worker.actions[definition.Name] = definition.Tasks[0].Action
	worker.mutex.Unlock()

	return nil
}
