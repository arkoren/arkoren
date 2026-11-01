package publication

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

type fixture struct {
	root   string
	remote string
	trust  string
	secret string
}

func command(t *testing.T, root, binary string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v %s", binary, args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func writeTest(t *testing.T, root, name, data string) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fixture fixture) commit(t *testing.T, message string) string {
	t.Helper()
	command(t, fixture.root, "git", "add", ".")
	command(t, fixture.root, "git", "commit", "-m", message)
	return command(t, fixture.root, "git", "rev-parse", "HEAD")
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if os.Getenv("ARKOREN_CLIFF_TEST") != "1" {
		t.Skip("real Git/cliff/SSH fixtures run with make maintenance-check")
	}
	directory := t.TempDir()
	root := filepath.Join(directory, "source")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	command(t, root, "git", "init", "-b", "main")
	command(t, root, "git", "config", "user.name", "Fixture")
	command(t, root, "git", "config", "user.email", "fixture@example.invalid")
	command(t, root, "git", "config", "commit.gpgsign", "false")
	command(t, root, "git", "config", "tag.gpgsign", "false")
	modules := []string{"core", "cmd/arkoren", "driver/example", "foundation/example"}
	for _, module := range modules {
		writeTest(t, root, module+"/go.mod", "module arkoren.dev/"+module+"\n")
	}
	policy, err := json.Marshal(map[string]any{"schema": 1, "public": modules, "development": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, root, ".github/release-modules.json", string(policy))
	fixture := fixture{root: root, remote: filepath.Join(directory, "remote.git"), trust: filepath.Join(root, ".github/release-signing.allowed-signers")}
	fixture.commit(t, "chore: baseline")
	for _, module := range modules {
		command(t, root, "git", "tag", module+"/v0.1.0")
	}
	command(t, root, "git", "clone", "--bare", root, fixture.remote)
	command(t, root, "git", "remote", "add", "origin", fixture.remote)
	key := filepath.Join(directory, "key")
	command(t, root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	secret, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	fixture.secret = string(secret)
	public := command(t, root, "ssh-keygen", "-y", "-f", key)
	writeTest(t, root, ".github/release-signing.allowed-signers", principal+" "+strings.Join(strings.Fields(public)[:2], " ")+"\n")
	fixture.commit(t, "ci: dedicated fixture signer")
	return fixture
}

func (fixture fixture) batch(t *testing.T) Batch {
	t.Helper()
	writeTest(t, fixture.root, "core/payload.go", "package core\n")
	writeTest(t, fixture.root, "driver/example/payload.go", "package example\n")
	sha := fixture.commit(t, "fix: Unicode é 😀 and -----BEGIN SSH SIGNATURE----- text")
	plan, err := release.Prepare(context.Background(), fixture.root, sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := NewBatch(plan)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func (fixture fixture) push(t *testing.T, batch Batch) {
	t.Helper()
	if err := PushTags(context.Background(), fixture.root, fixture.trust, batch, fixture.secret, "fixture-app-token"); err != nil {
		t.Fatal(err)
	}
}

func TestSignedAtomicBatchPreservesOriginalPlanAfterTags(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	fixture.push(t, batch)
	fixture.push(t, batch)
	history, err := Recorded(context.Background(), fixture.root, fixture.trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Digest != batch.Digest || len(history[0].Proposals()) != 2 {
		t.Fatalf("wrong durable batch: %+v", history)
	}
	if err := Recompute(context.Background(), fixture.root, history[0]); err != nil {
		t.Fatal(err)
	}
	plan, err := release.Prepare(context.Background(), fixture.root, batch.Plan.SHA, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range plan.Modules {
		if entry.NextVersion != nil {
			t.Fatal("already signed version bumped again")
		}
	}
}

func TestAtomicReceiveHookRejectionCreatesNoRemoteTag(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	hook := filepath.Join(fixture.remote, "hooks/pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PushTags(context.Background(), fixture.root, fixture.trust, batch, fixture.secret, "fixture"); err == nil {
		t.Fatal("atomic rejection accepted")
	}
	if output := command(t, fixture.root, "git", "ls-remote", "--refs", "origin", "refs/tags/core/v0.1.1", "refs/tags/driver/example/v0.1.1"); output != "" {
		t.Fatal("rejected batch created remote refs")
	}
}

func TestRemoteConflictComparesTagObjectNotPeeledCommit(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	fixture.push(t, batch)
	name := TagName(batch.Proposals()[0])
	command(t, fixture.root, "git", "tag", "-d", name)
	command(t, fixture.root, "git", "tag", name, batch.Plan.SHA)
	if _, err := Reconcile(context.Background(), fixture.root, fixture.trust, batch); err == nil {
		t.Fatal("same-commit different-object conflict accepted")
	}
}

func TestSignedWrongTargetIsRejected(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	annotation, err := batch.Annotation()
	if err != nil {
		t.Fatal(err)
	}
	name := TagName(batch.Proposals()[0])
	baseline := command(t, fixture.root, "git", "rev-parse", "core/v0.1.0^{commit}")
	err = withSigner(context.Background(), fixture.trust, fixture.secret, func(env []string) error {
		_, err := execute(context.Background(), fixture.root, env, "git", annotation, "tag", "--cleanup=verbatim", "-s", name, baseline, "-F", "-")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTag(context.Background(), fixture.root, fixture.trust, name, &batch); err == nil {
		t.Fatal("signed wrong commit accepted")
	}
}

func TestUnsignedAndWrongKeyTagsAreRejected(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	fixture.push(t, batch)
	wrong := filepath.Join(t.TempDir(), "empty-trust")
	if err := os.WriteFile(wrong, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	name := TagName(batch.Proposals()[0])
	if _, err := VerifyTag(context.Background(), fixture.root, wrong, name, &batch); err == nil {
		t.Fatal("wrong key accepted")
	}
	command(t, fixture.root, "git", "tag", "-d", name)
	annotation, err := batch.Annotation()
	if err != nil {
		t.Fatal(err)
	}
	command(t, fixture.root, "git", "tag", "-a", name, "-m", annotation)
	if _, err := VerifyTag(context.Background(), fixture.root, fixture.trust, name, &batch); err == nil {
		t.Fatal("unsigned annotation accepted")
	}
}

func TestTamperedPlanFailsSourceRecomputation(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	batch.Plan.Modules[0].Notes = "foreign notes"
	altered, err := NewBatch(batch.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := Recompute(context.Background(), fixture.root, altered); err == nil {
		t.Fatal("self-consistent tampered plan accepted")
	}
}

func TestStaleUnsignedBatchRefusesBeforeTagCreation(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	writeTest(t, fixture.root, "scripts/maintenance.sh", "# later main")
	main := fixture.commit(t, "ci: later main")
	if err := Validate(context.Background(), fixture.root, fixture.trust, main, batch); err == nil {
		t.Fatal("stale unsigned batch accepted")
	}
	if actual := command(t, fixture.root, "git", "tag", "--list", "core/v0.1.1"); actual != "" {
		t.Fatal("validation wrote tag")
	}
}

func TestFullySignedOlderBatchCanResumeAfterMainAdvances(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	fixture.push(t, batch)
	writeTest(t, fixture.root, "scripts/maintenance.sh", "# later main")
	main := fixture.commit(t, "ci: later main")
	if err := Validate(context.Background(), fixture.root, fixture.trust, main, batch); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalArrayPolicyBatchSurvivesSchemaClassification(t *testing.T) {
	fixture := newFixture(t)
	writeTest(t, fixture.root, ".github/release-modules.json", `["core","cmd/arkoren","driver/example","foundation/example"]`)
	fixture.commit(t, "ci: historical array policy")
	writeTest(t, fixture.root, "core/payload.go", "package core\n")
	sha := fixture.commit(t, "fix: historical Unicode é 😀")
	plan, err := release.PrepareHistorical(context.Background(), fixture.root, sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := NewBatch(plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture.push(t, batch)
	writeTest(t, fixture.root, ".github/release-modules.json", `{"schema":1,"public":["core","cmd/arkoren","driver/example","foundation/example"],"development":["cmd/arkoren-ci"]}`)
	writeTest(t, fixture.root, "cmd/arkoren-ci/go.mod", "module arkoren.dev/cmd/arkoren-ci\n")
	fixture.commit(t, "ci: explicit development classification")
	if err := Recompute(context.Background(), fixture.root, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := Recorded(context.Background(), fixture.root, fixture.trust); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalNoPolicyBatchSurvivesSchemaClassification(t *testing.T) {
	fixture := newFixture(t)
	command(t, fixture.root, "git", "rm", ".github/release-modules.json")
	fixture.commit(t, "ci: historical no policy")
	writeTest(t, fixture.root, "core/payload.go", "package core\n")
	sha := fixture.commit(t, "fix: historical repair")
	plan, err := release.PrepareHistorical(context.Background(), fixture.root, sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := NewBatch(plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture.push(t, batch)
	writeTest(t, fixture.root, ".github/release-modules.json", `{"schema":1,"public":["core","cmd/arkoren","driver/example","foundation/example"],"development":[]}`)
	fixture.commit(t, "ci: explicit classification")
	if err := Recompute(context.Background(), fixture.root, batch); err != nil {
		t.Fatal(err)
	}
}

func TestSignerTemporaryKeyPermissionsAndCleanupOnError(t *testing.T) {
	fixture := newFixture(t)
	key := ""
	err := withSigner(context.Background(), fixture.trust, fixture.secret, func(env []string) error {
		for _, value := range env {
			if strings.HasPrefix(value, "GIT_CONFIG_VALUE_1=") {
				key = strings.TrimPrefix(value, "GIT_CONFIG_VALUE_1=")
			}
			if strings.HasPrefix(value, "APP_TOKEN=") || strings.HasPrefix(value, "GH_TOKEN=") || strings.HasPrefix(value, "SIGNING_KEY=") {
				t.Fatal("credential leaked into signing process")
			}
		}
		info, err := os.Stat(key)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("key is not private")
		}
		info, err = os.Stat(filepath.Dir(key))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("key directory is not private")
		}
		return fmt.Errorf("fixture failure")
	})
	if err == nil || key == "" {
		t.Fatal("fixture did not fail")
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Fatal("temporary key survived failure")
	}
}

func TestPreflightCreatesNoSourceOrRemoteRef(t *testing.T) {
	fixture := newFixture(t)
	before := command(t, fixture.root, "git", "tag", "--list")
	if err := Preflight(context.Background(), fixture.trust, fixture.secret); err != nil {
		t.Fatal(err)
	}
	if before != command(t, fixture.root, "git", "tag", "--list") {
		t.Fatal("preflight changed source refs")
	}
}

func TestPythonV1GoldenAnnotationIsSignedVerifiedAndRecomputedWithoutPython(t *testing.T) {
	if os.Getenv("ARKOREN_CLIFF_TEST") != "1" {
		t.Skip("real Git/cliff/SSH fixtures run with make maintenance-check")
	}
	t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:00+00:00")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00+00:00")
	root := t.TempDir()
	command(t, root, "git", "init", "-b", "main")
	command(t, root, "git", "config", "user.name", "Legacy Fixture")
	command(t, root, "git", "config", "user.email", "legacy@example.invalid")
	command(t, root, "git", "config", "commit.gpgsign", "false")
	modules := []string{"core", "cmd/arkoren", "driver/example", "foundation/example"}
	for _, module := range modules {
		writeTest(t, root, module+"/go.mod", "module arkoren.dev/"+module+"\n")
	}
	fixture := fixture{root: root, trust: filepath.Join(root, ".github/release-signing.allowed-signers")}
	fixture.commit(t, "chore: baseline")
	for _, module := range modules {
		command(t, root, "git", "tag", module+"/v0.1.0")
	}
	writeTest(t, root, "core/payload.go", "package core\n")
	sha := fixture.commit(t, "fix: Unicode é 😀 and -----BEGIN SSH SIGNATURE----- text")
	if sha != "9d5f29ca80c4bfadd350cad788562d888603cc65" {
		t.Fatalf("historical source fixture drifted: %s", sha)
	}
	key := filepath.Join(t.TempDir(), "key")
	command(t, root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	secret, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	public := strings.Fields(command(t, root, "ssh-keygen", "-y", "-f", key))
	writeTest(t, root, ".github/release-signing.allowed-signers", principal+" "+strings.Join(public[:2], " ")+"\n")
	writeTest(t, root, ".github/release-modules.json", `{"schema":1,"public":["core","cmd/arkoren","driver/example","foundation/example"],"development":[]}`)
	// This file was generated independently by the removed Python planner and
	// json.dumps at 373ba86. Test execution requires no Python interpreter.
	annotation, err := os.ReadFile("testdata/python-v1.txt")
	if err != nil {
		t.Fatal(err)
	}
	err = withSigner(context.Background(), fixture.trust, string(secret), func(env []string) error {
		_, err := execute(context.Background(), root, env, "git", string(annotation), "tag", "--cleanup=verbatim", "-s", "core/v0.1.1", sha, "-F", "-")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := VerifyTag(context.Background(), root, fixture.trust, "core/v0.1.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Digest != "eb52c8a371e53e8a80a223eaed0a3c16be0b9d75244e9068475c08bb4b6d8c34" {
		t.Fatal("original Python digest changed")
	}
	if err := Recompute(context.Background(), root, batch); err != nil {
		t.Fatal(err)
	}
}
