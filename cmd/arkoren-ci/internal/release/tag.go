package release

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

// tag holds a validated plain version and peeled commit, not signing authority.
type tag struct {
	// name is the exact module-prefixed Git tag.
	name string
	// target is the peeled commit object ID.
	target string
	// version is the plain v0.X.Y suffix.
	version string
	// numbers permits ordering without implementing version bump policy.
	numbers [3]uint64
}

// versionPattern rejects prereleases, leading zeroes and nonplain versions.
var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// versionNumbers validates the accepted v0 namespace and extracts ordering data.
func versionNumbers(value string) ([3]uint64, error) {
	result := [3]uint64{}
	match := versionPattern.FindStringSubmatch(value)
	if match == nil || match[1] != "0" {
		return result, fmt.Errorf("invalid or v1+ version: %s", value)
	}
	for index := range result {
		number, err := strconv.ParseUint(match[index+1], 10, 64)

		if err != nil {
			return result, fmt.Errorf("version component out of range")
		}
		result[index] = number
	}

	return result, nil
}

// compareVersions orders validated versions; git-cliff alone owns increments.
func compareVersions(left, right [3]uint64) int {
	for index := range left {
		if order := cmp.Compare(left[index], right[index]); order != 0 {
			return order
		}
	}

	return 0
}

// ancestor rejects incomplete ancestry rather than interpreting Git errors as no.
func ancestor(ctx context.Context, root, old, next string) (bool, error) {
	_, err := git(ctx, root, "merge-base", "--is-ancestor", old, next)

	if err != nil {
		var status *exec.ExitError
		if errors.As(err, &status) && status.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("cannot establish complete release ancestry: %w", err)
	}

	return true, nil
}

// selectTags validates every namespace tag before choosing reachable baselines.
func selectTags(ctx context.Context, root, sha string, modules []repository.Module) (map[string]*tag, []string, error) {
	data, err := git(ctx, root, "tag", "--list")

	if err != nil {
		return nil, nil, err
	}
	selected := make(map[string]*tag, len(modules))
	reachable := []string{}
	for _, module := range modules {
		versions := []tag{}
		for _, name := range strings.Fields(data) {
			version, ok := strings.CutPrefix(name, module.Directory+"/")
			if !ok {
				continue
			}
			numbers, err := versionNumbers(version)

			if err != nil {
				return nil, nil, fmt.Errorf("malformed release tag %s: %w", name, err)
			}
			peeled, err := git(ctx, root, "rev-parse", "refs/tags/"+name+"^{commit}")

			if err != nil {
				return nil, nil, err
			}
			target := strings.TrimSpace(peeled)
			included, err := ancestor(ctx, root, target, sha)

			if err != nil {
				return nil, nil, err
			}
			if included {
				versions = append(versions, tag{name: name, target: target, version: version, numbers: numbers})
				if !slices.Contains(reachable, target) {
					reachable = append(reachable, target)
				}
			}
		}
		slices.SortFunc(versions, func(left, right tag) int {
			return compareVersions(left.numbers, right.numbers)
		})
		for index := 1; index < len(versions); index++ {
			older, newer := versions[index-1], versions[index]
			ordered, err := ancestor(ctx, root, older.target, newer.target)

			if err != nil {
				return nil, nil, err
			}
			if older.target == newer.target || !ordered {
				return nil, nil, fmt.Errorf("ambiguous release history: %s and %s", older.name, newer.name)
			}
		}
		if len(versions) > 0 {
			selected[module.Directory] = &versions[len(versions)-1]
		}
	}

	return selected, reachable, nil
}
