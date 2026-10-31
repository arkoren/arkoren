package hatchet

import (
	"testing"

	sdk "github.com/hatchet-dev/hatchet/sdks/go"
)

// TestRunRegistrationRecoversPanic verifies that a native registration panic is
// converted to the startup error boundary.
func TestRunRegistrationRecoversPanic(t *testing.T) {
	t.Parallel()

	_, err := runRegistration(func(*sdk.Client) (sdk.WorkflowBase, error) {
		panic("boom")
	}, nil)

	if err == nil {
		t.Fatal("runRegistration() error = nil")
	}
}

// TestRunRegistrationRejectsNilWorkflow verifies that registration callbacks
// cannot publish a nil native workflow as a successful declaration.
func TestRunRegistrationRejectsNilWorkflow(t *testing.T) {
	t.Parallel()

	_, err := runRegistration(func(*sdk.Client) (sdk.WorkflowBase, error) {
		return nil, nil
	}, nil)

	if err == nil {
		t.Fatal("runRegistration() error = nil")
	}
}

// TestRegisterRejectsAfterStart verifies that host registration remains closed
// once startup has completed.
func TestRegisterRejectsAfterStart(t *testing.T) {
	t.Parallel()

	host := New()
	host.started = true

	if err := host.Register(func(*sdk.Client) (sdk.WorkflowBase, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("Register() error = nil")
	}
}
