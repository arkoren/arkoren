package repository_test

import (
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

func TestPolicyRejectsOverlappingClassifications(t *testing.T) {
	_, err := repository.ParsePolicy([]byte(`{"schema":1,"public":["core","cmd/arkoren"],"development":["cmd/arkoren"]}`))
	if err == nil {
		t.Fatal("overlapping classification accepted")
	}
}

func TestPolicyRejectsDuplicatePublicRoots(t *testing.T) {
	_, err := repository.ParsePolicy([]byte(`{"schema":1,"public":["core","cmd/arkoren","core"],"development":[]}`))
	if err == nil {
		t.Fatal("duplicate public root accepted")
	}
}

func TestPolicyRejectsTraversal(t *testing.T) {
	_, err := repository.ParsePolicy([]byte(`{"schema":1,"public":["core","cmd/arkoren","driver/../outside"],"development":[]}`))
	if err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestPolicyRejectsUnknownSchema(t *testing.T) {
	_, err := repository.ParsePolicy([]byte(`{"schema":2,"public":["core","cmd/arkoren"],"development":[]}`))
	if err == nil {
		t.Fatal("unknown schema accepted")
	}
}

func TestPolicyRejectsUnreviewedDevelopmentRoot(t *testing.T) {
	_, err := repository.ParsePolicy([]byte(`{"schema":1,"public":["core","cmd/arkoren"],"development":["cmd/new"]}`))
	if err == nil {
		t.Fatal("unreviewed development root accepted")
	}
}

func TestPolicyAcceptsRetiredDirectory(t *testing.T) {
	policy, err := repository.ParsePolicy([]byte(`{"schema":1,"public":["core","cmd/arkoren"],"development":[],"retired":["driver/oauth"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Retired) != 1 || policy.Retired[0] != "driver/oauth" {
		t.Fatalf("retired list not parsed: %v", policy.Retired)
	}
}

func TestPolicyRejectsPublicAndRetiredDirectory(t *testing.T) {
	_, err := repository.ParsePolicy([]byte(`{"schema":1,"public":["core","cmd/arkoren","driver/oauth"],"development":[],"retired":["driver/oauth"]}`))
	if err == nil {
		t.Fatal("directory both public and retired accepted")
	}
}

func TestPolicyRejectsDuplicateOrNoncanonicalRetiredDirectory(t *testing.T) {
	for _, retired := range []string{`["driver/oauth","driver/oauth"]`, `["driver/../outside"]`} {
		_, err := repository.ParsePolicy([]byte(`{"schema":1,"public":["core","cmd/arkoren"],"development":[],"retired":` + retired + `}`))
		if err == nil {
			t.Fatalf("invalid retired list accepted: %s", retired)
		}
	}
}
