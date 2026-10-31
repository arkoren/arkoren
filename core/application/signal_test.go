package application_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"

	"arkoren.dev/core/application"
)

// TestNewSignalContextStopCancels checks that early startup failure can release
// signal handling without waiting for a signal. Repeated stop calls are safe.
func TestNewSignalContextStopCancels(t *testing.T) {
	t.Parallel()

	ctx, stop := application.NewSignalContext(t.Context())
	t.Cleanup(stop)

	stop()
	stop()

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v", ctx.Err())
	}
}

// TestNewSignalContextParentCancellation checks synchronous parent cancellation
// and releases the registration even when no process signal arrives. Process-wide
// signal registration stays outside synctest's isolated goroutine bubbles.
func TestNewSignalContextParentCancellation(t *testing.T) {
	t.Parallel()

	parent, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	ctx, stop := application.NewSignalContext(parent)
	t.Cleanup(stop)

	cancel()

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v", ctx.Err())
	}

	stop()
}

// TestNewSignalContextInterrupt isolates actual SIGINT delivery in a child so
// neither the test runner nor unrelated parallel tests can receive the signal.
func TestNewSignalContextInterrupt(t *testing.T) {
	t.Parallel()

	runSignalProcess(t, "interrupt")
}

// TestNewSignalContextTerminate exercises actual SIGTERM delivery independently
// of SIGINT, with the registration confined to a disposable child process.
func TestNewSignalContextTerminate(t *testing.T) {
	t.Parallel()

	runSignalProcess(t, "terminate")
}

// runSignalProcess runs only the signal probe in the current test executable.
// CommandContext bounds a broken probe by the enclosing test's cancellation.
func runSignalProcess(t *testing.T, name string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support sending these signals with Process.Signal")
	}

	executable, err := os.Executable()

	if err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestSignalContextProcess$")
	command.Env = append(os.Environ(), "ARKOREN_TEST_SIGNAL="+name)

	output, err := command.CombinedOutput()

	if err != nil {
		t.Fatalf("signal probe: %v\n%s", err, output)
	}
}

// TestSignalContextProcess is inert in the main runner. A child registers the
// helper before signaling itself; successful cancellation must let it exit.
func TestSignalContextProcess(t *testing.T) {
	name := os.Getenv("ARKOREN_TEST_SIGNAL")

	if name == "" {
		return
	}

	ctx, stop := application.NewSignalContext(t.Context())
	t.Cleanup(stop)

	process, err := os.FindProcess(os.Getpid())

	if err != nil {
		t.Fatal(err)
	}

	received := os.Interrupt

	if name == "terminate" {
		received = syscall.SIGTERM
	}

	err = process.Signal(received)

	if err != nil {
		t.Fatal(err)
	}

	<-ctx.Done()

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v", ctx.Err())
	}
}
