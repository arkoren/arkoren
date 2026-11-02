package release_test

import (
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

// TestTitleCore keeps the toolkit root distinct from module categories.
func TestTitleCore(t *testing.T) {
	if actual := release.Title("core", "v0.1.1"); actual != "Core v0.1.1" {
		t.Fatal(actual)
	}
}

// TestTitleCLI names the consumer tool without its command-directory prefix.
func TestTitleCLI(t *testing.T) {
	if actual := release.Title("cmd/arkoren", "v0.1.3"); actual != "CLI v0.1.3" {
		t.Fatal(actual)
	}
}

// TestTitleDriver preserves the RabbitMQ brand within its category.
func TestTitleDriver(t *testing.T) {
	if actual := release.Title("driver/rabbitmq", "v0.1.1"); actual != "Driver: RabbitMQ v0.1.1" {
		t.Fatal(actual)
	}
}

// TestTitleFoundation retains the module identity rather than a capability name.
func TestTitleFoundation(t *testing.T) {
	if actual := release.Title("foundation/validate", "v0.1.0"); actual != "Foundation: Validate v0.1.0" {
		t.Fatal(actual)
	}
}

// TestTitleNewModule has a deterministic fallback for future approved modules.
func TestTitleNewModule(t *testing.T) {
	if actual := release.Title("ark/billing", "v0.1.0"); actual != "Ark: Billing v0.1.0" {
		t.Fatal(actual)
	}
}
