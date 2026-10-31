// Package workflowtest helps test code that starts workflows and checks
// drivers of the workflow package. It offers two tools.
//
// [Fake] is a [workflow.Driver] for unit tests: it records the runs that code
// starts, signals and cancels, and tests script their outcome with
// [Fake.Complete] and [Fake.Fail]. It never executes workflow bodies.
//
//	fake := workflowtest.New()
//	client := workflow.NewClient(fake)
//
//	run, _ := client.Start(ctx, Onboard, "user-42", user)
//	fake.AssertStarted(t, Onboard, func(in User) bool {
//		return in.ID == 42
//	})
//
//	fake.Complete(Onboard.Name(), "user-42", "welcome")
//	out, err := run.Result(ctx)
//
// [Run] is the conformance suite that every driver must pass. A driver test
// builds the registry with [Registry] and hands it to the driver:
//
//	func TestConformance(t *testing.T) {
//		workflowtest.Run(t, func(t *testing.T) workflow.Driver {
//			driver := memoryworkflow.New(workflowtest.Registry(t))
//			t.Cleanup(func() {
//				_ = driver.Stop(context.Background())
//			})
//
//			return driver
//		}, workflowtest.WithSynctest())
//	}
//
// Drivers backed by an engine run the suite from a test gated like their other
// integration tests, without WithSynctest. A driver that cannot run Child or
// Spawn declares [WithoutChild] or [WithoutSpawn]; these require ErrUnsupported
// and absence of submitted child work rather than silently skipping the cases.
// All other operations keep the same contract assertions.
// Child ownership cases independently prestart a signal-blocked target, then
// require it to survive the joining parent's cancellation and MaxDuration and
// finish with its original input. Newly created children must instead stop with
// their parent. Observable work lives in steps with atomic per-run counters,
// never in replayed orchestration. Native driver tests can additionally observe
// their engine's join acknowledgement before requesting cancellation.
// [WithoutImmediateStepRetries] declares only that completed multi-attempt step
// policies with zero initial backoff cannot be expressed. The case still runs:
// it requires ErrUnsupported and zero affected-handler executions. Single-attempt
// zero-backoff and positive-backoff controls remain required. The workflow entry
// control also checks that step retries were not replaced by whole-workflow retries.
// Registry starts an independent recording telemetry service for worker-side
// extraction. The suite checks serialized start carriers, retained-map ownership,
// and forwarding to actual child/spawn steps where those operations are supported.
// Fake records a detached Trace map but never fabricates worker observations.
package workflowtest
