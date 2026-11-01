// Package repository validates the maintenance tool's source and module boundary.
package repository

import (
	"encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
)

// Policy classifies reviewed module roots, not publication records or versions.
type Policy struct {
	// Schema identifies this policy format independently of signed plan formats.
	Schema int `json:"schema"`

	// Public lists the approved release namespaces.
	Public []string `json:"public"`

	// Development lists explicit development-only modules, excluded from releases.
	Development []string `json:"development"`
}

// canonical matches public module roots, never arbitrary command directories.
var canonical = regexp.MustCompile(`^(core|cmd/arkoren|(driver|foundation|ark)/[a-z0-9][a-z0-9-]*)$`)

// ParsePolicy rejects unknown fields, duplicates and overlapping classifications.
func ParsePolicy(data []byte) (Policy, error) {
	policy := Policy{}
	err := json.Unmarshal(data, &policy, json.RejectUnknownMembers(true))

	if err != nil {
		return Policy{}, fmt.Errorf("decode module policy: %w", err)
	}
	if policy.Schema != 1 || len(policy.Public) == 0 || policy.Development == nil {
		return Policy{}, fmt.Errorf("unsupported or empty module policy")
	}
	seen := make(map[string]bool)
	for _, directory := range policy.Public {
		if !canonical.MatchString(directory) || seen[directory] {
			return Policy{}, fmt.Errorf("invalid or duplicate public module: %s", directory)
		}
		seen[directory] = true
	}
	for _, directory := range policy.Development {
		if seen[directory] || directory != "cmd/arkoren-ci" {
			return Policy{}, fmt.Errorf("unreviewed or duplicate development module: %s", directory)
		}
		seen[directory] = true
	}
	if !slices.Contains(policy.Public, "core") || !slices.Contains(policy.Public, "cmd/arkoren") {
		return Policy{}, fmt.Errorf("module policy requires core and consumer CLI")
	}

	return policy, nil
}
