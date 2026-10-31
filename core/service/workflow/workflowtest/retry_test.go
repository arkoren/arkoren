package workflowtest_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

// immediateRetryProbe selects the deliberately failing public-suite invocation
// in a child test process, keeping that expected failure out of the parent test.
const immediateRetryProbe = "ARKOREN_TEST_IMMEDIATE_RETRY_REJECTION"

// TestImmediateStepRetryFixtureUsesPerCallZeroBackoff exercises the published
// conformance registry through a real driver. Virtual time proves the completed
// local policy does not inherit a positive registered/default retry delay.
func TestImmediateStepRetryFixtureUsesPerCallZeroBackoff(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryworkflow.New(workflowtest.Registry(t))
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		input, err := workflow.EncodeValue(nil, "immediate-fixture")

		if err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()

		if err := driver.Start(t.Context(), "conf-immediate-retry", "fixture-run", input, workflow.StartOptions{}); err != nil {
			t.Fatal(err)
		}

		output, err := driver.Result(t.Context(), "conf-immediate-retry", "fixture-run")

		if err != nil {
			t.Fatal(err)
		}

		var result string

		if err := workflow.DecodeValue(nil, output, &result); err != nil {
			t.Fatal(err)
		}

		if result != "immediate-fixture-ok" || time.Since(startedAt) != 0 {
			t.Fatalf("immediate retry result %q after %s", result, time.Since(startedAt))
		}
	})
}

// TestImmediateSingleAttemptFixtureRemainsSupported exercises a completed
// single-attempt zero policy even when the registered step allows more attempts.
func TestImmediateSingleAttemptFixtureRemainsSupported(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryworkflow.New(workflowtest.Registry(t))
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		input, err := workflow.EncodeValue(nil, "single-fixture")

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Start(t.Context(), "conf-immediate-once", "single-run", input, workflow.StartOptions{}); err != nil {
			t.Fatal(err)
		}

		output, err := driver.Result(t.Context(), "conf-immediate-once", "single-run")

		if err != nil {
			t.Fatal(err)
		}

		var result string

		if err := workflow.DecodeValue(nil, output, &result); err != nil {
			t.Fatal(err)
		}

		if result != "single-fixture-once" {
			t.Fatalf("single-attempt result: %q", result)
		}
	})
}

// TestImmediateRetryOptionRejectsSupportingDriver verifies that an unsupported
// declaration runs assertions instead of skipping the policy. A real supporting
// driver must fail both the ErrUnsupported and zero-handler-execution checks.
func TestImmediateRetryOptionRejectsSupportingDriver(t *testing.T) {
	t.Parallel()

	if os.Getenv(immediateRetryProbe) == "1" {
		workflowtest.Run(t, func(t *testing.T) workflow.Driver {
			driver := memoryworkflow.New(workflowtest.Registry(t))
			t.Cleanup(func() {
				if err := driver.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			})

			return driver
		}, workflowtest.WithSynctest(), workflowtest.WithoutImmediateStepRetries())

		return
	}

	executable, err := os.Executable()

	if err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), executable,
		"-test.run=^TestImmediateRetryOptionRejectsSupportingDriver$/^step_immediate_retries$")
	command.Env = append(os.Environ(), immediateRetryProbe+"=1")

	output, err := command.CombinedOutput()
	var exit *exec.ExitError

	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("supporting driver was not rejected by the option: %v\n%s", err, output)
	}

	if !bytes.Contains(output, []byte("want ErrUnsupported")) {
		t.Fatalf("missing unsupported-policy assertion:\n%s", output)
	}

	if !bytes.Contains(output, []byte("affected step ran 3 times; want 0")) {
		t.Fatalf("missing zero-execution assertion:\n%s", output)
	}
}

// TestImmediateRetryOptionKeepsSingleAttempt runs the public control case with
// the opt-out enabled. Successful case output proves it executed rather than
// being silently skipped along with the unsupported multi-attempt policy.
func TestImmediateRetryOptionKeepsSingleAttempt(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()

	if err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), executable, "-test.v",
		"-test.run=^TestImmediateRetryOptionRejectsSupportingDriver$/^step_immediate_single_attempt$")
	command.Env = append(os.Environ(), immediateRetryProbe+"=1")

	output, err := command.CombinedOutput()

	if err != nil {
		t.Fatalf("single-attempt policy rejected by opt-out: %v\n%s", err, output)
	}

	if !bytes.Contains(output, []byte("--- PASS: TestImmediateRetryOptionRejectsSupportingDriver/step_immediate_single_attempt")) {
		t.Fatalf("single-attempt control did not execute:\n%s", output)
	}
}
