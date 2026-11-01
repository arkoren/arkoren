package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

func TestNoArgumentsShowsSafeHelp(t *testing.T) {
	output := &bytes.Buffer{}
	if err := run(context.Background(), nil, output); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("missing help")
	}
}

func TestInventoryHelpDoesNotReadRepository(t *testing.T) {
	output := &bytes.Buffer{}
	if err := run(context.Background(), []string{"inventory", "--repo", "/missing", "--help"}, output); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryReturnsCleanJSON(t *testing.T) {
	output := &bytes.Buffer{}
	if err := run(context.Background(), []string{"inventory", "--repo", "../.."}, output); err != nil {
		t.Fatal(err)
	}
	result := repository.Inventory{}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not one JSON document: %v", err)
	}
	if len(result.Modules) == 0 {
		t.Fatalf("unexpected module count: %d", len(result.Modules))
	}
}

func TestInvalidInventoryEmitsNoPartialJSON(t *testing.T) {
	output := &bytes.Buffer{}
	if err := run(context.Background(), []string{"inventory", "--repo", t.TempDir()}, output); err == nil {
		t.Fatal("invalid repository accepted")
	}
	if output.Len() != 0 {
		t.Fatal("partial inventory published")
	}
}

func TestPublicationCommandUnavailable(t *testing.T) {
	output := &bytes.Buffer{}
	if err := run(context.Background(), []string{"publish"}, output); err == nil {
		t.Fatal("publisher unexpectedly available")
	}
}
