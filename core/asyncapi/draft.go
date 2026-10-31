package asyncapi

import (
	"fmt"
	"regexp"
)

// validateID accepts only nonempty, JSON-reference-safe component identifiers.
// This deliberately excludes slashes, escapes, whitespace and template syntax.
func validateID(id string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(id) {
		return fmt.Errorf("invalid id %q: use letters, digits, dots, underscores or hyphens", id)
	}

	return nil
}

// binding captures one actual per-level binding object. Repeated protocol keys
// are errors, so later declarations cannot silently change an earlier contract.
func (draft *draft) binding(protocol string, object RawObject) {
	value, err := captureObject(object)
	draft.fail(err)
	draft.fail(validateID(protocol))

	bindings := draft.object["bindings"].(map[string]any)

	if _, exists := bindings[protocol]; exists {
		draft.fail(fmt.Errorf("duplicate binding protocol %q", protocol))

		return
	}

	bindings[protocol] = value
}

// references appends local reference objects while detecting duplicate IDs
// across repeated calls. Inputs are converted immediately, retaining no slices.
func (draft *draft) references(member, prefix string, ids []string) {
	items, _ := draft.object[member].([]any)
	seen := make(map[string]bool, len(items))

	for _, item := range items {
		seen[item.(map[string]any)["$ref"].(string)] = true
	}

	for _, id := range ids {
		draft.fail(validateID(id))

		reference := prefix + id

		if seen[reference] {
			draft.fail(fmt.Errorf("duplicate %s reference %q", member, id))
		}

		seen[reference] = true
		items = append(items, map[string]any{"$ref": reference})
	}

	draft.object[member] = items
}
