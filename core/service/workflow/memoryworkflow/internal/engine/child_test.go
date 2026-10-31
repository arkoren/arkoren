package engine_test

import (
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// TestSpawn checks that the parent returns the spawned run's identity and
// that the child result remains available through a separate Run handle.
func TestSpawn(t *testing.T) {
	child := workflow.New("spawned", func(_ *workflow.Context, in string) (string, error) {
		return in + "!", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	parent := workflow.New("spawner", func(c *workflow.Context, in string) (string, error) {
		id, err := c.Spawn(child, "child-1", in)

		if err != nil {
			return "", err
		}

		return id, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	client := setup(t, parent, child)

	run, err := client.Start(t.Context(), parent, "r", "hi")

	if err != nil {
		t.Fatal(err)
	}

	if got, err := run.Result(t.Context()); err != nil || got != "child-1" {
		t.Fatalf("Result = %q, %v; want child-1", got, err)
	}

	if got, err := client.Run(child, "child-1").Result(t.Context()); err != nil || got != "hi!" {
		t.Fatalf("child Result = %q, %v; want hi!", got, err)
	}
}
