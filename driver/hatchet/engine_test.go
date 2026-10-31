package hatchet

import (
	"errors"
	"testing"
)

// requireDurableVersion verifies checkDurableVersion rejects version with
// [ErrEngineUnsupported] when wantErr is set and accepts it otherwise.
func requireDurableVersion(t *testing.T, version string, wantErr bool) {
	t.Helper()

	err := checkDurableVersion(version)

	if (err != nil) != wantErr {
		t.Errorf("checkDurableVersion(%q) error = %v; want error %v", version, err, wantErr)
	}

	if wantErr && !errors.Is(err, ErrEngineUnsupported) {
		t.Errorf("checkDurableVersion(%q) error = %v; want ErrEngineUnsupported", version, err)
	}
}

// TestCheckDurableVersionOld79 verifies the durable-engine version check for v0.79.9.
func TestCheckDurableVersionOld79(t *testing.T) {
	t.Parallel()

	requireDurableVersion(t, "v0.79.9", true)
}

// TestCheckDurableVersionMinimumSupported verifies the durable-engine version check for v0.80.0.
func TestCheckDurableVersionMinimumSupported(t *testing.T) {
	t.Parallel()

	requireDurableVersion(t, "v0.80.0", false)
}

// TestCheckDurableVersionNewerMinor verifies the durable-engine version check for v0.109.20.
func TestCheckDurableVersionNewerMinor(t *testing.T) {
	t.Parallel()

	requireDurableVersion(t, "v0.109.20", false)
}

// TestCheckDurableVersionNextMajor verifies the durable-engine version check for v1.0.0.
func TestCheckDurableVersionNextMajor(t *testing.T) {
	t.Parallel()

	requireDurableVersion(t, "v1.0.0", false)
}

// TestCheckDurableVersionMissingPrefix verifies the durable-engine version check for 0.60.1.
func TestCheckDurableVersionMissingPrefix(t *testing.T) {
	t.Parallel()

	requireDurableVersion(t, "0.60.1", true)
}

// TestCheckDurableVersionDev verifies the durable-engine version check for dev.
func TestCheckDurableVersionDev(t *testing.T) {
	t.Parallel()

	requireDurableVersion(t, "dev", false)
}

// TestCheckDurableVersionNightly verifies the durable-engine version check for nightly-build.
func TestCheckDurableVersionNightly(t *testing.T) {
	t.Parallel()

	requireDurableVersion(t, "nightly-build", false)
}
