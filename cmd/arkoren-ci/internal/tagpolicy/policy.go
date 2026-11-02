// Package tagpolicy checks existing GitHub rules without granting the App admin.
package tagpolicy

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

// mainID identifies the existing strict main protection ruleset.
const mainID = 26273253

// immutableID identifies the non-bypassable version-tag protection ruleset.
const immutableID = 26273278

// Actor is the exact reviewed Integration bypass for creation-only policy.
type Actor struct {
	// ID identifies the approved release Integration, never a human or role.
	ID int `json:"actor_id"`
	// Type must remain Integration for the creation-only bypass.
	Type string `json:"actor_type"`
	// Mode describes GitHub's bypass scope within this one creation ruleset.
	Mode string `json:"bypass_mode"`
}

// Rule preserves parameters when existing rules are inspected or rewritten.
type Rule struct {
	// Type selects the GitHub protection operation.
	Type string `json:"type"`
	// Parameters retains every server-supplied policy parameter.
	Parameters jsontext.Value `json:"parameters,omitempty"`
}

// Ruleset is the writable shape; server-only metadata is never sent in updates.
type Ruleset struct {
	// Name is the reviewed human-readable ruleset identity.
	Name string `json:"name"`
	// Target distinguishes branch protection from tag protection.
	Target string `json:"target"`
	// Enforcement must be active before the global creation lock is opened.
	Enforcement string `json:"enforcement"`
	// Conditions confines this ruleset to its exact reviewed ref selectors.
	Conditions struct {
		// Ref selects full refs, not unqualified tag names.
		Ref struct {
			// Include lists protected full-ref patterns.
			Include []string `json:"include"`
			// Exclude lists explicit exceptions within this ruleset only.
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	// Rules lists every protected operation in this ruleset.
	Rules []Rule `json:"rules"`
	// Actors lists every bypass; immutable and main rulesets permit none.
	Actors []Actor `json:"bypass_actors"`
}

// creationRules derives exact allowlists from the current schema-1 policy only.
func creationRules(modules []string) []Ruleset {
	modules = slices.Clone(modules)
	slices.Sort(modules)
	patterns := make([]string, 0, len(modules))
	for _, module := range modules {
		patterns = append(patterns, "refs/tags/"+module+"/v0.*")
	}
	locked := Ruleset{Name: "Lock unapproved version tag creation", Target: "tag", Enforcement: "active", Rules: []Rule{{Type: "creation"}}, Actors: []Actor{}}
	locked.Conditions.Ref.Include = []string{"refs/tags/v*", "refs/tags/**/v*"}
	locked.Conditions.Ref.Exclude = patterns
	app := Ruleset{Name: "Arkoren Releases canonical v0 tag creation", Target: "tag", Enforcement: "active", Rules: []Rule{{Type: "creation"}},
		Actors: []Actor{{ID: 5540931, Type: "Integration", Mode: "always"}}}
	app.Conditions.Ref.Include = patterns
	app.Conditions.Ref.Exclude = []string{}

	return []Ruleset{locked, app}
}

// protectedMain requires strict checks and a PR, without any bypass actor.
func protectedMain(record Ruleset) error {
	if record.Target != "branch" || record.Enforcement != "active" || len(record.Actors) != 0 ||
		!slices.Equal(record.Conditions.Ref.Include, []string{"~DEFAULT_BRANCH"}) || len(record.Conditions.Ref.Exclude) != 0 {
		return fmt.Errorf("main protection differs from approved policy")
	}
	checks, pr := false, false
	for _, rule := range record.Rules {
		switch rule.Type {
		case "required_status_checks":
			var parameters struct {
				Strict bool `json:"strict_required_status_checks_policy"`
				Checks []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			}
			if err := json.Unmarshal(rule.Parameters, &parameters); err != nil {
				return err
			}
			names := []string{}
			for _, item := range parameters.Checks {
				names = append(names, item.Context)
			}
			slices.Sort(names)
			checks = parameters.Strict && slices.Equal(names, []string{"CI gate", "website"})
		case "pull_request":
			var parameters struct {
				Count int `json:"required_approving_review_count"`
			}
			if err := json.Unmarshal(rule.Parameters, &parameters); err != nil {
				return err
			}
			pr = parameters.Count == 0
		}
	}
	if !checks || !pr {
		return fmt.Errorf("main must retain strict CI gate, website and zero-approval PR policy")
	}

	return nil
}

// immutable rejects extra rules, parameters, bypasses or namespace exceptions.
func immutable(record Ruleset, locked bool) error {
	types := []string{}
	for _, rule := range record.Rules {
		if len(rule.Parameters) != 0 {
			return fmt.Errorf("unexpected immutable rule parameters")
		}
		types = append(types, rule.Type)
	}
	slices.Sort(types)
	expected := []string{"deletion", "non_fast_forward", "update"}
	if locked {
		expected = append([]string{"creation"}, expected...)
	}
	if record.Target != "tag" || record.Enforcement != "active" || len(record.Actors) != 0 ||
		!slices.Equal(record.Conditions.Ref.Include, []string{"refs/tags/v*", "refs/tags/**/v*"}) ||
		len(record.Conditions.Ref.Exclude) != 0 || !slices.Equal(types, expected) {
		return fmt.Errorf("immutable version-tag protection differs from approved policy")
	}

	return nil
}

// Snapshot contains both full server records and the validated creation rules.
type Snapshot struct {
	// Main preserves the complete original server record for unchanged readback.
	Main jsontext.Value `json:"main"`
	// Tags preserves the global version-tag lock, including metadata.
	Tags jsontext.Value `json:"tags"`
	// Creation preserves both validated creation-only server records.
	Creation []jsontext.Value `json:"creation"`
}

// Check validates the existing App allowlist and global/main protection read-only.
func Check(ctx context.Context, client github.Client, locked bool) (Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(client.Root, ".github/release-modules.json"))
	if err != nil {
		return Snapshot{}, err
	}
	policy, err := repository.ParsePolicy(data)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Creation: []jsontext.Value{}}
	for identifier, destination := range map[int]*jsontext.Value{mainID: &snapshot.Main, immutableID: &snapshot.Tags} {
		if err := client.Call(ctx, "GET", github.API+"/rulesets/"+strconv.Itoa(identifier), nil, destination); err != nil {
			return Snapshot{}, err
		}
	}
	main, tags := Ruleset{}, Ruleset{}
	if err := json.Unmarshal(snapshot.Main, &main); err != nil {
		return Snapshot{}, err
	}
	if err := json.Unmarshal(snapshot.Tags, &tags); err != nil {
		return Snapshot{}, err
	}
	if err := protectedMain(main); err != nil {
		return Snapshot{}, err
	}
	if err := immutable(tags, locked); err != nil {
		return Snapshot{}, err
	}
	if !matchesShape(snapshot.Tags, tags) {
		return Snapshot{}, fmt.Errorf("unrecognized nested immutable tag policy field")
	}
	records, err := github.Pages[struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}](ctx, client, github.API+"/rulesets")
	if err != nil {
		return Snapshot{}, err
	}
	for _, expected := range creationRules(policy.Public) {
		matches := 0
		for _, record := range records {
			if record.Name != expected.Name {
				continue
			}
			matches++
			var raw jsontext.Value
			if err := client.Call(ctx, "GET", github.API+"/rulesets/"+strconv.Itoa(record.ID), nil, &raw); err != nil {
				return Snapshot{}, err
			}
			actual := Ruleset{}
			if err := json.Unmarshal(raw, &actual); err != nil {
				return Snapshot{}, err
			}
			if !matchesShape(raw, expected) {
				return Snapshot{}, fmt.Errorf("creation policy differs from exact approved App/public allowlist")
			}
			snapshot.Creation = append(snapshot.Creation, raw)
		}
		if matches != 1 {
			return Snapshot{}, fmt.Errorf("missing or duplicate approved creation ruleset")
		}
	}

	return snapshot, nil
}

// matchesShape retains every nested policy field while ignoring server metadata.
func matchesShape(raw jsontext.Value, expected Ruleset) bool {
	fields := map[string]jsontext.Value{}
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	shape := map[string]jsontext.Value{}
	for _, name := range []string{"name", "target", "enforcement", "conditions", "rules", "bypass_actors"} {
		value, ok := fields[name]
		if !ok {
			return false
		}
		shape[name] = value
	}
	actual, err := json.Marshal(shape)
	if err != nil {
		return false
	}
	wanted, err := json.Marshal(expected)
	if err != nil {
		return false
	}

	return sameRecord(actual, wanted)
}

// Activate restores only the approved creation posture after external green proof.
// Existing admin gh credentials perform this one-time operation, never the App.
// A failed readback restores the global creation lock before returning an error.
func Activate(ctx context.Context, client github.Client, output string) error {
	if err := os.Mkdir(output, 0o700); err != nil {
		return err
	}
	before, err := Check(ctx, client, true)
	if err != nil {
		return err
	}
	if err := save(output, "before.json", before); err != nil {
		return err
	}
	updated := Ruleset{}
	if err := json.Unmarshal(before.Tags, &updated); err != nil {
		return err
	}
	updated.Rules = slices.DeleteFunc(updated.Rules, func(rule Rule) bool { return rule.Type == "creation" })
	endpoint := github.API + "/rulesets/" + strconv.Itoa(immutableID)
	err = client.Call(ctx, "PUT", endpoint, updated, nil)
	if err == nil {
		var after Snapshot
		after, err = Check(ctx, client, false)
		if err == nil && !sameRecord(before.Main, after.Main) {
			err = fmt.Errorf("main protection changed during activation")
		}
		if err == nil {
			err = save(output, "after.json", after)
		}
	}
	if err != nil {
		// A lost response may have opened creation. Restore only the original lock.
		original := Ruleset{}
		if decodeErr := json.Unmarshal(before.Tags, &original); decodeErr != nil {
			return fmt.Errorf("activation failed; original lock snapshot cannot decode")
		}
		if restoreErr := client.Call(context.WithoutCancel(ctx), "PUT", endpoint, original, nil); restoreErr != nil {
			return fmt.Errorf("activation failed and global creation lock restoration unconfirmed")
		}
		var restored Ruleset
		if restoreErr := client.Call(context.WithoutCancel(ctx), "GET", endpoint, nil, &restored); restoreErr != nil {
			return fmt.Errorf("activation failed; global creation lock readback unconfirmed")
		}
		if restoreErr := immutable(restored, true); restoreErr != nil {
			return fmt.Errorf("activation failed; global creation lock differs on readback")
		}
		return fmt.Errorf("activation blocked; original global creation lock restored: %w", err)
	}

	return nil
}

// sameRecord compares full server records independently of object key order.
func sameRecord(left, right jsontext.Value) bool {
	left, right = left.Clone(), right.Clone()
	if left.Canonicalize(jsontext.CanonicalizeRawInts(false)) != nil || right.Canonicalize(jsontext.CanonicalizeRawInts(false)) != nil {
		return false
	}

	return bytes.Equal(left, right)
}

// save writes private administrative evidence without overwriting existing work.
func save(directory, name string, value any) error {
	data, err := json.Marshal(value, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(directory, name), append(data, '\n'), 0o600)
}
