package encoded

import (
	"math/big"

	"arkoren.dev/foundation/validate/internal/fields"
	"arkoren.dev/foundation/validate/internal/numbers"
)

// encodedContainer binds mutable successful coverage to one instance pointer.
// Object and array evaluators extend it, then apply unevaluated assertions.
type encodedContainer struct {
	// pointer addresses this container within the instance document.
	pointer string
	// coverage includes annotations already collected by same-location refs.
	coverage encodedCoverage
}

// object checks object-wide rules and member applicators before the final
// unevaluatedProperties pass. Schema dependencies can add same-location
// coverage, but propertyNames and child schemas cannot.
func (evaluation *encodedEvaluation) object(
	node *Node, object map[string]any, location encodedContainer,
) (encodedCoverage, error) {
	length := new(big.Rat).SetInt64(int64(len(object)))

	if err := checkEncodedLimits(node, length, numbers.Objects, location.pointer); err != nil {
		return encodedCoverage{}, err
	}

	if err := evaluation.required(node.doc["required"], object, location.pointer); err != nil {
		return encodedCoverage{}, err
	}

	dependencies, err := evaluation.dependencies(node, object, location.pointer)

	if err != nil {
		return encodedCoverage{}, err
	}

	location.coverage.merge(dependencies)

	names, err := evaluation.objectKeys(object)

	if err != nil {
		return encodedCoverage{}, err
	}

	for _, name := range names {
		if err := evaluation.property(node, object[name], encodedProperty{
			name: name, container: location,
		}); err != nil {
			return encodedCoverage{}, err
		}
	}

	if err := evaluation.unevaluatedProperties(node, object, location); err != nil {
		return encodedCoverage{}, err
	}

	return location.coverage, nil
}

// required checks presence alone: explicit null and zero satisfy the presence
// rule and are independently checked by their child schema.
func (evaluation *encodedEvaluation) required(raw any, object map[string]any, pointer string) error {
	names, _ := raw.([]any)

	for _, rawName := range names {
		if err := evaluation.budget.tick(); err != nil {
			return err
		}

		name := rawName.(string)

		if _, present := object[name]; !present {
			return encodedMismatch(encodedChildPointer(pointer, name), "required")
		}
	}

	return nil
}

// encodedProperty identifies a member and its containing annotation scope.
// The member value is passed separately so property-name validation cannot
// accidentally substitute a normalized value or treat null as absent.
type encodedProperty struct {
	// name is the literal decoded JSON object name.
	name string
	// container supplies this object's pointer and shared successful coverage.
	container encodedContainer
}

// property validates the property name, its explicit schema, and all matching
// patterns. additionalProperties depends on this node's own properties/patterns,
// not the broader annotation set used later by unevaluatedProperties.
func (evaluation *encodedEvaluation) property(node *Node, value any, property encodedProperty) error {
	if err := evaluation.budget.tick(); err != nil {
		return err
	}

	pointer := encodedChildPointer(property.container.pointer, property.name)

	if _, err := evaluation.run(node.children["propertyNames"], property.name, pointer); err != nil {
		return err
	}

	child := node.children["properties/"+fields.EscapePointer(property.name)]
	matched := child != nil

	if _, err := evaluation.run(child, value, pointer); err != nil {
		return err
	}

	patterns, err := evaluation.propertyPatterns(node, value, property)

	if err != nil {
		return err
	}

	matched = matched || patterns

	if matched {
		property.container.coverage.properties[property.name] = true

		return nil
	}

	if additional := node.children["additionalProperties"]; additional != nil {
		if _, err := evaluation.run(additional, value, pointer); err != nil {
			return err
		}

		property.container.coverage.properties[property.name] = true
	}

	return nil
}

// propertyPatterns applies every matching pattern, not only the first. The
// returned boolean indicates local matching independently of other coverage.
func (evaluation *encodedEvaluation) propertyPatterns(
	node *Node, value any, property encodedProperty,
) (bool, error) {
	matched := false

	entries, _ := node.doc["patternProperties"].(map[string]any)
	patterns, err := evaluation.objectKeys(entries)

	if err != nil {
		return false, err
	}

	for _, pattern := range patterns {
		if err := evaluation.budget.tick(); err != nil {
			return false, err
		}

		if err := evaluation.budget.text(property.name); err != nil {
			return false, err
		}

		key := "patternProperties/" + fields.EscapePointer(pattern)

		if !node.patterns[key].MatchString(property.name) {
			continue
		}

		pointer := encodedChildPointer(property.container.pointer, property.name)

		if _, err := evaluation.run(node.children[key], value, pointer); err != nil {
			return false, err
		}

		matched = true
	}

	return matched, nil
}

// dependencies evaluates triggered presence and schema dependencies against the
// entire object. Draft-07 dependencies and modern dependent keywords coexist
// without interpreting an array dependency as an array instance schema.
func (evaluation *encodedEvaluation) dependencies(
	node *Node, object map[string]any, pointer string,
) (encodedCoverage, error) {
	coverage := newEncodedCoverage()

	for _, keyword := range []string{"dependentRequired", "dependentSchemas", "dependencies"} {
		result, err := evaluation.dependencyGroup(node, object, encodedBranchLocation{
			keyword: keyword, pointer: pointer,
		})

		if err != nil {
			return encodedCoverage{}, err
		}

		coverage.merge(result)
	}

	return coverage, nil
}

// dependencyGroup handles one checked dependency map. Each triggered entry is
// either a presence list or a compiled same-location schema, never both.
func (evaluation *encodedEvaluation) dependencyGroup(
	node *Node, object map[string]any, location encodedBranchLocation,
) (encodedCoverage, error) {
	coverage := newEncodedCoverage()

	entries, _ := node.doc[location.keyword].(map[string]any)
	names, err := evaluation.objectKeys(entries)

	if err != nil {
		return encodedCoverage{}, err
	}

	for _, name := range names {
		if err := evaluation.budget.tick(); err != nil {
			return encodedCoverage{}, err
		}

		if _, present := object[name]; !present {
			continue
		}

		result, err := evaluation.dependencyEntry(node, object, location, name)

		if err != nil {
			return encodedCoverage{}, err
		}

		coverage.merge(result)
	}

	return coverage, nil
}

// dependencyEntry executes one triggered schema or presence dependency. The
// caller has already checked the trigger's presence, including explicit null.
func (evaluation *encodedEvaluation) dependencyEntry(
	node *Node, object map[string]any, location encodedBranchLocation, name string,
) (encodedCoverage, error) {
	child := node.children[location.keyword+"/"+fields.EscapePointer(name)]

	if child != nil {
		return evaluation.run(child, object, location.pointer)
	}

	entries := node.doc[location.keyword].(map[string]any)

	if err := evaluation.required(entries[name], object, location.pointer); err != nil {
		return encodedCoverage{}, err
	}

	return newEncodedCoverage(), nil
}

// unevaluatedProperties applies its schema only after all other successful
// same-location applicators have contributed their evaluated member names.
func (evaluation *encodedEvaluation) unevaluatedProperties(
	node *Node, object map[string]any, location encodedContainer,
) error {
	child := node.children["unevaluatedProperties"]

	if child == nil {
		return nil
	}

	names, err := evaluation.objectKeys(object)

	if err != nil {
		return err
	}

	for _, name := range names {
		if err := evaluation.budget.tick(); err != nil {
			return err
		}

		if location.coverage.properties[name] {
			continue
		}

		if _, err := evaluation.run(child, object[name], encodedChildPointer(location.pointer, name)); err != nil {
			return err
		}

		location.coverage.properties[name] = true
	}

	return nil
}
