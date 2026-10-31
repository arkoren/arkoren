package encoded

import (
	"math/big"
	"strconv"

	"arkoren.dev/foundation/validate/internal/numbers"
)

// array checks lengths, uniqueness, positional/items schemas, and contains
// before unevaluatedItems. Successful contains matches annotate only matching
// positions, not unrelated elements or their child-location annotations.
func (evaluation *encodedEvaluation) array(
	node *Node, array []any, location encodedContainer,
) (encodedCoverage, error) {
	if err := checkEncodedCountRange(node, len(array), "Items", location.pointer); err != nil {
		return encodedCoverage{}, err
	}

	if unique, _ := node.doc["uniqueItems"].(bool); unique {
		if err := evaluation.unique(array, location.pointer); err != nil {
			return encodedCoverage{}, err
		}
	}

	if err := evaluation.arrayItems(node, array, location); err != nil {
		return encodedCoverage{}, err
	}

	if err := evaluation.contains(node, array, location); err != nil {
		return encodedCoverage{}, err
	}

	if err := evaluation.unevaluatedItems(node, array, location); err != nil {
		return encodedCoverage{}, err
	}

	return location.coverage, nil
}

// checkEncodedCountRange compares container/match counts with exact schema
// integers. Missing bounds have no effect, even if they exceed machine int.
func checkEncodedCountRange(node *Node, count int, kind, pointer string) error {
	value := new(big.Rat).SetInt64(int64(count))
	minimum, maximum := "min"+kind, "max"+kind

	if bound, present := node.doc[minimum]; present {
		number, _ := numbers.Exact(bound)

		if value.Cmp(number) < 0 {
			return encodedMismatch(pointer, minimum)
		}
	}

	if bound, present := node.doc[maximum]; present {
		number, _ := numbers.Exact(bound)

		if value.Cmp(number) > 0 {
			return encodedMismatch(pointer, maximum)
		}
	}

	return nil
}

// arrayItems applies tuple/prefix schemas and the dialect-specific remainder
// schema. Omitted items/additionalItems does not mark unconstrained positions
// as evaluated; explicitly true schemas do mark them.
func (evaluation *encodedEvaluation) arrayItems(node *Node, array []any, location encodedContainer) error {
	prefix, _ := node.doc["prefixItems"].([]any)
	keyword := "prefixItems"
	remainder := node.children["items"]

	if node.draft07 {
		prefix = nil
	}

	if tuple, ok := node.doc["items"].([]any); ok {
		prefix = tuple
		keyword = "items"
		remainder = node.children["additionalItems"]
	}

	for index, value := range array {
		if err := evaluation.budget.tick(); err != nil {
			return err
		}

		child := remainder

		if index < len(prefix) {
			child = node.children[keyword+"/"+strconv.Itoa(index)]
		}

		if child == nil {
			continue
		}

		pointer := encodedChildPointer(location.pointer, strconv.Itoa(index))

		if _, err := evaluation.run(child, value, pointer); err != nil {
			return err
		}

		location.coverage.items[index] = true
	}

	return nil
}

// contains probes all elements so later unevaluatedItems sees every successful
// match, including when minContains is zero. Fatal errors cannot become a
// negative match, even if some other element already satisfied the minimum.
func (evaluation *encodedEvaluation) contains(node *Node, array []any, location encodedContainer) error {
	child := node.children["contains"]

	if child == nil {
		return nil
	}

	matches := 0

	for index, value := range array {
		pointer := encodedChildPointer(location.pointer, strconv.Itoa(index))
		_, matched, err := evaluation.match(child, value, pointer)

		if err != nil {
			return err
		}

		if matched {
			matches++
			location.coverage.items[index] = true
		}
	}

	if _, present := node.doc["minContains"]; !present && matches == 0 {
		return encodedMismatch(location.pointer, "contains")
	}

	return checkEncodedCountRange(node, matches, "Contains", location.pointer)
}

// unique builds collision-free exact structural keys rather than comparing all
// pairs. Decimal spellings, object key order, and signed zero are normalized;
// strings and other kinds retain their JSON type distinction.
func (evaluation *encodedEvaluation) unique(array []any, pointer string) error {
	seen := make(map[string]bool, len(array))

	for index, value := range array {
		key, err := evaluation.key(nil, value)

		if err != nil {
			return err
		}

		text := string(key)

		if seen[text] {
			return encodedMismatch(encodedChildPointer(pointer, strconv.Itoa(index)), "uniqueItems")
		}

		seen[text] = true
	}

	return nil
}

// unevaluatedItems applies the final schema only to positions not covered by
// successful same-location applicators. New annotations propagate to parents.
func (evaluation *encodedEvaluation) unevaluatedItems(node *Node, array []any, location encodedContainer) error {
	child := node.children["unevaluatedItems"]

	if child == nil {
		return nil
	}

	for index, value := range array {
		if err := evaluation.budget.tick(); err != nil {
			return err
		}

		if location.coverage.items[index] {
			continue
		}

		pointer := encodedChildPointer(location.pointer, strconv.Itoa(index))

		if _, err := evaluation.run(child, value, pointer); err != nil {
			return err
		}

		location.coverage.items[index] = true
	}

	return nil
}
