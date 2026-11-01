package repository_test

import (
	"context"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

func TestHistoricalNoPolicyAdapterPreservesStrictCurrentInspection(t *testing.T) {
	root := t.TempDir()
	for _, module := range []string{"core", "cmd/arkoren", "driver/example", "foundation/example"} {
		write(t, root, module+"/go.mod", "module arkoren.dev/"+module+"\n")
	}
	sha := snapshot(t, root)
	result, err := repository.ReleaseInventory(context.Background(), root, sha)
	if err != nil || len(result.Modules) != 4 {
		t.Fatalf("historical adapter: %+v %v", result, err)
	}
	if _, err := repository.Inspect(context.Background(), root, sha); err == nil {
		t.Fatal("historical fallback weakened current approval")
	}
}

func TestHistoricalArrayAdapterPreservesStrictCurrentInspection(t *testing.T) {
	root := t.TempDir()
	for _, module := range []string{"core", "cmd/arkoren", "driver/example", "foundation/example"} {
		write(t, root, module+"/go.mod", "module arkoren.dev/"+module+"\n")
	}
	write(t, root, ".github/release-modules.json", `["core","cmd/arkoren","driver/example","foundation/example"]`)
	sha := snapshot(t, root)
	result, err := repository.ReleaseInventory(context.Background(), root, sha)
	if err != nil || len(result.Modules) != 4 {
		t.Fatalf("historical array adapter: %+v %v", result, err)
	}
	if _, err := repository.Inspect(context.Background(), root, sha); err == nil {
		t.Fatal("historical array accepted as current approval")
	}
}
