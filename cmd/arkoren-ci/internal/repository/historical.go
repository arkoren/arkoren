package repository

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

// ReleaseInventory reads historical signed-v1 inventory through a compatibility
// adapter. Inspect and ParsePolicy remain strictly schema-1-only.
func ReleaseInventory(ctx context.Context, root, revision string) (Inventory, error) {
	source, err := committed(ctx, root, revision)

	if err != nil {
		return Inventory{}, err
	}
	policy := Policy{Schema: 1, Development: []string{}}
	mode, found := source.modes[".github/release-modules.json"]
	if found {
		if !mode.IsRegular() || mode.Perm() != 0 {
			return Inventory{}, fmt.Errorf("invalid module policy mode")
		}
		data, err := source.read(".github/release-modules.json")

		if err != nil {
			return Inventory{}, err
		}
		if strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
			if err := json.Unmarshal(data, &policy.Public); err != nil {
				return Inventory{}, err
			}
			data, err = json.Marshal(policy)

			if err != nil {
				return Inventory{}, err
			}
		}
		policy, err = ParsePolicy(data)

		if err != nil {
			return Inventory{}, err
		}
	} else {
		for name := range source.modes {
			if directory, ok := strings.CutSuffix(name, "/go.mod"); ok && canonical.MatchString(directory) {
				policy.Public = append(policy.Public, directory)
			}
		}
		slices.Sort(policy.Public)
		// The shared structural validator needs this marker, not policy bytes.
		source.modes[".github/release-modules.json"] = 0
	}

	return source.inventory(ctx, policy, true)
}
