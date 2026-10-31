package workflowtest

import (
	"context"
	"errors"
	"sync"
	"time"

	"arkoren.dev/core/service/workflow"
)

// ownershipCalls counts actual entry-step work by run ID. Orchestration never
// reads or mutates this map, so replay cannot change the scenario's decisions.
var ownershipCalls sync.Map

// ownershipEntry makes execution observable outside replayed orchestration.
// Unique run IDs isolate parallel scenarios and the atomic counters avoid races.
var ownershipEntry = workflow.NewStep("conf-ownership-entry", func(_ context.Context, id string) (string, error) {
	counter(&ownershipCalls, id).Add(1)

	return id, nil
})

// ownershipRelease holds an independent child until its joining parent ends.
// Its first value lets the test prove that the original input remains intact.
var ownershipRelease = workflow.NewSignal[string]("ownership-release")

// ownershipChild performs one observable step before waiting for release. The
// step count detects duplicate handler work without side effects during replay.
var ownershipChild = workflow.New("conf-ownership-child", func(ctx *workflow.Context, input string) (string, error) {
	if _, err := ctx.Step(ownershipEntry, ctx.ID()); err != nil {
		return "", err
	}

	value, err := ctx.Wait(ownershipRelease)

	return input + value, err
}, func(definition *workflow.Builder) {
	definition.MaxDuration(maxDuration)
})

// ownershipParent reaches Child after an observable entry step. The target ID
// is supplied by the test, which decides whether it prestarts the child.
var ownershipParent = workflow.New("conf-ownership-parent", ownershipJoin, func(definition *workflow.Builder) {
	definition.MaxDuration(maxDuration)
})

// ownershipTimeoutParent has less execution time than its target child. Two
// seconds permits native scheduling while keeping engine integration bounded.
var ownershipTimeoutParent = workflow.New("conf-ownership-timeout", ownershipJoin, func(definition *workflow.Builder) {
	definition.MaxDuration(2 * time.Second)
})

// ownershipJoin records parent entry through a step, then creates or joins the
// same child definition. Its replacement input must never reach an existing run.
func ownershipJoin(ctx *workflow.Context, id string) (string, error) {
	if _, err := ctx.Step(ownershipEntry, ctx.ID()); err != nil {
		return "", err
	}

	return ctx.Child(ownershipChild, id, "replacement")
}

// startOwnershipParent starts a parent targeting the scenario's child ID. Entry
// is observed at a public step boundary, not by reading driver runtime internals.
// Native integration tests additionally observe the engine's join acknowledgement.
func startOwnershipParent(s *suite, definition *workflow.Workflow[string, string]) *workflow.Run[string] {
	s.t.Helper()

	parent, err := s.client.Start(s.ctx, definition, s.id, s.id+"-child")

	if err != nil {
		s.t.Fatal(err)
	}

	waitFor(s.t, func() bool {
		return counter(&ownershipCalls, s.id).Load() == 1
	})

	return parent
}

// startIndependentChild reserves the target before the parent starts. Observing
// the child's actual entry step proves it is executing, not merely submitted.
func startIndependentChild(s *suite) {
	s.t.Helper()

	if _, err := s.client.Start(s.ctx, ownershipChild, s.id+"-child", "original"); err != nil {
		s.t.Fatal(err)
	}

	waitFor(s.t, func() bool {
		return counter(&ownershipCalls, s.id+"-child").Load() == 1
	})
}

// finishIndependentChild requires the original run to remain running after the
// parent ends, then releases it through Signal and checks its input and work count.
func finishIndependentChild(s *suite) {
	s.t.Helper()

	id := s.id + "-child"
	child := s.client.Run(ownershipChild, id)

	if status, err := child.Status(s.ctx); err != nil || status != workflow.Running {
		s.t.Fatalf("independent child Status = %v, %v; want running", status, err)
	}

	if err := s.client.Signal(s.ctx, ownershipChild, id, ownershipRelease, "!"); err != nil {
		s.t.Fatal(err)
	}

	if output, err := child.Result(s.ctx); err != nil || output != "original!" {
		s.t.Fatalf("independent child Result = %q, %v; want original!", output, err)
	}

	if calls := counter(&ownershipCalls, id).Load(); calls != 1 {
		s.t.Fatalf("child entry step ran %d times; want one", calls)
	}
}

// requireOwnedChildStopped waits for native cancellation or parent-close
// termination to settle. Terminal error kinds differ by engine; continuing to
// run, accepting a signal, or repeating actual entry work is never permitted.
func requireOwnedChildStopped(s *suite) {
	s.t.Helper()

	id := s.id + "-child"
	child := s.client.Run(ownershipChild, id)
	waitFor(s.t, func() bool {
		status, err := child.Status(s.ctx)

		return err == nil && status != workflow.Running
	})

	if _, err := child.Result(s.ctx); err == nil {
		s.t.Fatal("owned child completed successfully after parent stopped")
	}

	if err := s.client.Signal(s.ctx, ownershipChild, id, ownershipRelease, "!"); !errors.Is(err, workflow.ErrNotFound) {
		s.t.Fatalf("stopped child Signal = %v; want ErrNotFound", err)
	}

	if calls := counter(&ownershipCalls, id).Load(); calls != 1 {
		s.t.Fatalf("child entry step ran %d times; want one", calls)
	}
}

// testChildJoinCancel checks that a joining parent never takes cancellation
// ownership. Unsupported engines must reject without affecting the original run.
func testChildJoinCancel(s *suite) {
	startIndependentChild(s)

	parent := startOwnershipParent(s, ownershipParent)

	if s.opts.withoutChild {
		if _, err := parent.Result(s.ctx); !errors.Is(err, workflow.ErrUnsupported) {
			s.t.Fatalf("parent Result = %v; want ErrUnsupported", err)
		}

		finishIndependentChild(s)

		return
	}

	if err := parent.Cancel(s.ctx); err != nil {
		s.t.Fatal(err)
	}

	if _, err := parent.Result(s.ctx); !errors.Is(err, workflow.ErrCanceled) {
		s.t.Fatalf("parent Result = %v; want ErrCanceled", err)
	}

	finishIndependentChild(s)
}

// testChildJoinTimeout requires parent MaxDuration to end only the joining
// wait. Rejection remains explicit for engines without atomic child ownership.
func testChildJoinTimeout(s *suite) {
	startIndependentChild(s)

	parent := startOwnershipParent(s, ownershipTimeoutParent)
	want := workflow.ErrTimeout

	if s.opts.withoutChild {
		want = workflow.ErrUnsupported
	}

	if _, err := parent.Result(s.ctx); !errors.Is(err, want) {
		s.t.Fatalf("parent Result = %v; want %v", err, want)
	}

	finishIndependentChild(s)
}

// testChildCreatedCancel establishes actual child execution before canceling
// its creator. Rejecting unsupported Child must instead leave no child run.
func testChildCreatedCancel(s *suite) {
	parent := startOwnershipParent(s, ownershipParent)

	if s.opts.withoutChild {
		_, err := parent.Result(s.ctx)

		if err := unsupportedResult(s.ctx, s.client.Driver(), ownershipChild.Name(), s.id+"-child", err); err != nil {
			s.t.Fatal(err)
		}

		return
	}

	waitFor(s.t, func() bool {
		return counter(&ownershipCalls, s.id+"-child").Load() == 1
	})

	if err := parent.Cancel(s.ctx); err != nil {
		s.t.Fatal(err)
	}

	if _, err := parent.Result(s.ctx); !errors.Is(err, workflow.ErrCanceled) {
		s.t.Fatalf("parent Result = %v; want ErrCanceled", err)
	}

	requireOwnedChildStopped(s)
}

// testChildCreatedTimeout requires a newly owned child to stop with its parent's
// shorter duration, rather than escaping as detached work. A rejecting engine
// must leave the requested child ID absent instead of submitting such work.
func testChildCreatedTimeout(s *suite) {
	parent := startOwnershipParent(s, ownershipTimeoutParent)

	_, err := parent.Result(s.ctx)

	if s.opts.withoutChild {
		if err := unsupportedResult(s.ctx, s.client.Driver(), ownershipChild.Name(), s.id+"-child", err); err != nil {
			s.t.Fatal(err)
		}

		return
	}

	if !errors.Is(err, workflow.ErrTimeout) {
		s.t.Fatalf("parent Result = %v; want ErrTimeout", err)
	}

	requireOwnedChildStopped(s)
}
