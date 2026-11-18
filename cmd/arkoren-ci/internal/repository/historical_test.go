package repository_test

import (
	"context"
	"slices"
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

// TestHistoricalAccessDevelopmentRoot preserves the former development boundary
// without approving it for current policy.
func TestHistoricalAccessDevelopmentRoot(t *testing.T) {
	root := fixture(t)
	write(t, root, "ark/access/go.mod", "module arkoren.dev/ark/access\n")
	data := `{"schema":1,"public":["core","cmd/arkoren","driver/example","foundation/example"],"development":["ark/access","cmd/arkoren-ci"]}`
	write(t, root, ".github/release-modules.json", data)
	sha := snapshot(t, root)
	result, err := repository.ReleaseInventory(context.Background(), root, sha)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, module := range result.Modules {
		if module.Directory == "ark/access" {
			found = true
			if module.Classification != "development" {
				t.Fatalf("historical access was public: %+v", module)
			}
		}
	}
	if !found {
		t.Fatal("historical access missing from inventory")
	}
	if _, err := repository.ParsePolicy([]byte(data)); err == nil {
		t.Fatal("current parser accepted historical access")
	}
	if _, err := repository.Inspect(context.Background(), root, sha); err == nil {
		t.Fatal("current inspection accepted historical access")
	}
}

// TestHistoricalIAMDevelopmentRoot preserves the held-back ark/iam boundary of
// commits before its first release for replay, without approving it for
// current policy.
func TestHistoricalIAMDevelopmentRoot(t *testing.T) {
	root := fixture(t)
	write(t, root, "ark/iam/go.mod", "module arkoren.dev/ark/iam\n")
	data := `{"schema":1,"public":["core","cmd/arkoren","driver/example","foundation/example"],"development":["ark/iam","cmd/arkoren-ci"]}`
	write(t, root, ".github/release-modules.json", data)
	sha := snapshot(t, root)
	result, err := repository.ReleaseInventory(context.Background(), root, sha)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(result.Modules, func(module repository.Module) bool {
		return module.Directory == "ark/iam" && module.Classification == "development"
	}) {
		t.Fatalf("historical iam is not a development module: %+v", result.Modules)
	}
	if _, err := repository.ParsePolicy([]byte(data)); err == nil {
		t.Fatal("current parser accepted held-back iam")
	}
}

// TestHistoricalPolicyRejectsUnknownDevelopmentRoot keeps historical
// compatibility limited to reviewed roots.
func TestHistoricalPolicyRejectsUnknownDevelopmentRoot(t *testing.T) {
	root := fixture(t)
	write(t, root, ".github/release-modules.json", `{"schema":1,"public":["core","cmd/arkoren"],"development":["ark/access","cmd/unknown"]}`)
	sha := snapshot(t, root)
	if _, err := repository.ReleaseInventory(context.Background(), root, sha); err == nil {
		t.Fatal("accepted unknown historical development root")
	}
}

// TestHistoricalPolicyRejectsDuplicateDevelopmentRoot retains duplicate checks.
func TestHistoricalPolicyRejectsDuplicateDevelopmentRoot(t *testing.T) {
	root := fixture(t)
	write(t, root, ".github/release-modules.json", `{"schema":1,"public":["core","cmd/arkoren"],"development":["ark/access","ark/access"]}`)
	sha := snapshot(t, root)
	if _, err := repository.ReleaseInventory(context.Background(), root, sha); err == nil {
		t.Fatal("accepted duplicate historical development root")
	}
}

// TestHistoricalPolicyRejectsOverlappingRoots retains classification checks.
func TestHistoricalPolicyRejectsOverlappingRoots(t *testing.T) {
	root := fixture(t)
	write(t, root, ".github/release-modules.json", `{"schema":1,"public":["core","cmd/arkoren","ark/access"],"development":["ark/access"]}`)
	sha := snapshot(t, root)
	if _, err := repository.ReleaseInventory(context.Background(), root, sha); err == nil {
		t.Fatal("accepted overlapping historical classifications")
	}
}
