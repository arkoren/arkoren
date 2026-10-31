package hatchet

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// checkEngine fails when a workflow holds a durable task and the engine is
// older than the first version with durable eviction.
func (host *Host) checkEngine(ctx context.Context, client *hatchet.Client, workflows []hatchet.WorkflowBase) error {
	if !hasDurableTask(workflows) {
		return nil
	}

	version, err := client.GetEngineVersion(ctx)

	if err != nil {
		return fmt.Errorf("check hatchet engine: %w", err)
	}

	release, parsed := numericRelease(version)

	if !parsed {
		host.logger.WarnContext(ctx, "hatchet engine version is not semver; continuing", "version", version)

		return nil
	}

	return checkDurableVersion(release)
}

// numericRelease reads the numeric release core accepted by the SDK's version
// comparison. Explicit parsing is necessary because its support predicate never
// returns a parsing error; build/prerelease suffixes do not affect this gate.
func numericRelease(version string) (string, bool) {
	release, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), "-")
	release, _, _ = strings.Cut(release, "+")
	parts := strings.Split(release, ".")

	if len(parts) != 3 {
		return "", false
	}

	for _, part := range parts {
		value, err := strconv.Atoi(part)

		if err != nil || value < 0 {
			return "", false
		}
	}

	return release, true
}

// checkDurableVersion returns [ErrEngineUnsupported] when version lacks
// durable eviction.
func checkDurableVersion(version string) error {
	supported, err := hatchet.SupportsDurableEviction(version)

	if err != nil {
		return fmt.Errorf("check hatchet engine %q: %w", version, err)
	}

	if !supported {
		return fmt.Errorf("%w: durable tasks need engine %s or newer, found %s",
			ErrEngineUnsupported, hatchet.MinEngineVersion.DurableEviction, version)
	}

	return nil
}

// hasDurableTask reports whether any workflow declares a durable task.
func hasDurableTask(workflows []hatchet.WorkflowBase) bool {
	for _, workflow := range workflows {
		request, _, _, _ := workflow.Dump()

		if request == nil {
			continue
		}

		for _, task := range request.GetTasks() {
			if task.GetIsDurable() {
				return true
			}
		}
	}

	return false
}
