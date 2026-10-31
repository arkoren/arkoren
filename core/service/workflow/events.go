package workflow

import "arkoren.dev/core/event"

// RunEvent is the payload of [RunStarted] and [RunCanceled]. It carries no
// input, output, or metadata of the run.
// RunID is application-supplied operational data: restrict subscriptions when
// those identities are sensitive. These events describe local accepted actions.
//
//	unsubscribe, err := bus.Listen(workflow.RunStarted, func(_ context.Context, notice workflow.RunEvent) error {
//		fmt.Println(notice.Workflow)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type RunEvent struct {
	// Workflow is the workflow name.
	Workflow string

	// RunID is the run identifier.
	RunID string
}

// RunSignaledEvent is the payload of [RunSignaled]. It omits the signal
// value.
// The event reports accepted delivery, not workflow consumption or completion.
//
//	unsubscribe, err := bus.Listen(workflow.RunSignaled, func(_ context.Context, notice workflow.RunSignaledEvent) error {
//		fmt.Println(notice.Signal)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type RunSignaledEvent struct {
	// Workflow is the workflow name.
	Workflow string

	// RunID is the run identifier.
	RunID string

	// Signal is the signal name.
	Signal string
}

// RunStarted is published when [Client.Start] has started a run. A start that
// fails, including one that finds the run already started, publishes nothing.
// Configure ClientBuilder.Events with the same bus before observing commands.
//
//	unsubscribe, err := bus.Listen(workflow.RunStarted, func(_ context.Context, notice workflow.RunEvent) error {
//		fmt.Println(notice.Workflow)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var RunStarted = event.NewTopic[RunEvent]("workflow.run.started")

// RunSignaled is published when [Client.Signal] has delivered a signal to a
// run. A failed signal publishes nothing.
// It never includes the signal payload or replaces a run's trace parent.
//
//	unsubscribe, err := bus.Listen(workflow.RunSignaled, func(_ context.Context, notice workflow.RunSignaledEvent) error {
//		fmt.Println(notice.Signal)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var RunSignaled = event.NewTopic[RunSignaledEvent]("workflow.run.signaled")

// RunCanceled is published when [Run.Cancel] has asked the driver to cancel
// a run. A failed cancel publishes nothing. Canceling a finished run is not
// an error and publishes the event as well.
// The observation is not a distributed subscription to engine terminal states.
//
//	unsubscribe, err := bus.Listen(workflow.RunCanceled, func(_ context.Context, notice workflow.RunEvent) error {
//		fmt.Println(notice.Workflow)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var RunCanceled = event.NewTopic[RunEvent]("workflow.run.canceled")
