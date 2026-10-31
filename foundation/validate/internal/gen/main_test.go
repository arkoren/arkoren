package main

import (
	"bytes"
	"os"
	"testing"
)

// TestWrappersUpToDate compares checked-in wrappers with formatted generator
// output. A mismatch asks the maintainer to regenerate the owned artifact.
//
// Example:
//
//	go test -run "^TestWrappersUpToDate$" .
func TestWrappersUpToDate(t *testing.T) {
	want, err := generate()

	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile("../../wrappers.go")

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Fatal("wrappers.go is stale; run go generate ./... in foundation/validate")
	}
}
