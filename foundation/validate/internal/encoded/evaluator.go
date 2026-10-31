package encoded

import (
	"fmt"
	"maps"
	"math/big"
	"strconv"
	"unicode/utf8"

	"arkoren.dev/foundation/validate/internal/faults"
	"arkoren.dev/foundation/validate/internal/fields"
	"arkoren.dev/foundation/validate/internal/numbers"
)

// encodedCoverage records successful evaluations at one instance location.
// It is per-call mutable state; failed composition branches never publish it.
type encodedCoverage struct {
	// properties contains evaluated object member names, not child coverage.
	properties map[string]bool
	// items contains evaluated array positions at this location.
	items map[int]bool
}

// encodedVisit identifies one schema at one instance location on the active
// stack. Re-entering it without consuming input is an unproductive cycle.
type encodedVisit struct {
	// node identifies compiled graph identity, independent of URI aliases.
	node *Node
	// pointer identifies the instance value, including explicit null.
	pointer string
}

// encodedEvaluation owns mutable budgets, recursion guards, and dynamic scope
// for one Validate call. Nothing is cached into the published schema.
type encodedEvaluation struct {
	// budget charges assertion work and observes context cancellation.
	budget encodedBudget
	// active detects non-progressing cycles without banning recursive schemas.
	active map[encodedVisit]bool
	// scope lists resources from outermost to innermost for dynamic references.
	scope []*encodedResource
	// depth bounds composition/reference stack growth independently of JSON depth.
	depth int
	// keyBytes charges cumulative structural key data, not retained bytes alone,
	// so repeated large equality probes also have a fixed encoding bound.
	keyBytes int
}

// newEncodedCoverage creates independent annotation sets. Allocating these only
// at evaluation boundaries prevents a failed branch mutating its parent.
func newEncodedCoverage() encodedCoverage {
	return encodedCoverage{properties: make(map[string]bool), items: make(map[int]bool)}
}

// merge unions successful same-location annotations. Child-location annotations
// are deliberately not merged into their parent object's or array's coverage.
func (coverage encodedCoverage) merge(other encodedCoverage) {
	maps.Copy(coverage.properties, other.properties)
	maps.Copy(coverage.items, other.items)
}

// encodedMismatch returns only a pointer and keyword code, never values or
// schema arguments. Public callers classify this through errors.As(public validation errors).
func encodedMismatch(pointer, keyword string) error {
	if len(pointer) > encodedMaxLocationBytes {
		return fmt.Errorf("%w: instance pointer bytes", faults.ErrEncodedLimit)
	}

	return faults.Mismatch{Pointer: pointer, Keyword: keyword}
}

// run evaluates a node at one instance location. Guards precede every reference
// or composition traversal, and deferred cleanup makes sibling branches fully
// independent. A draft-07 reference replaces its node's assertion siblings.
func (evaluation *encodedEvaluation) run(node *Node, value any, pointer string) (encodedCoverage, error) {
	if err := evaluation.budget.tick(); err != nil {
		return encodedCoverage{}, err
	}

	if node == nil {
		return newEncodedCoverage(), nil
	}

	visit := encodedVisit{node: node, pointer: pointer}

	if evaluation.active[visit] || evaluation.depth >= encodedMaxEvaluationDepth {
		return encodedCoverage{}, fmt.Errorf("%w: evaluation recursion", faults.ErrEncodedLimit)
	}

	if len(pointer) > encodedMaxLocationBytes {
		return encodedCoverage{}, fmt.Errorf("%w: instance pointer bytes", faults.ErrEncodedLimit)
	}

	evaluation.active[visit] = true
	evaluation.depth++

	previousScope := evaluation.scope
	evaluation.scope = append(evaluation.scope, node.resource)
	defer func() {
		delete(evaluation.active, visit)
		evaluation.depth--
		evaluation.scope = previousScope
	}()

	if node.doc == nil {
		if !node.accept {
			return encodedCoverage{}, encodedMismatch(pointer, "false")
		}

		return newEncodedCoverage(), nil
	}

	if node.draft07 && node.reference != nil {
		return evaluation.run(node.reference, value, pointer)
	}

	return evaluation.assert(node, value, pointer)
}

// assert combines same-location applicator annotations before processing the
// unevaluated keywords. Kind-specific assertions never apply to other kinds.
func (evaluation *encodedEvaluation) assert(node *Node, value any, pointer string) (encodedCoverage, error) {
	coverage, err := evaluation.references(node, value, pointer)

	if err != nil {
		return encodedCoverage{}, err
	}

	composed, err := evaluation.compose(node, value, pointer)

	if err != nil {
		return encodedCoverage{}, err
	}

	coverage.merge(composed)

	if err := evaluation.scalar(node, value, pointer); err != nil {
		return encodedCoverage{}, err
	}

	switch instance := value.(type) {
	case map[string]any:
		return evaluation.object(node, instance, encodedContainer{pointer: pointer, coverage: coverage})
	case []any:
		return evaluation.array(node, instance, encodedContainer{pointer: pointer, coverage: coverage})
	}

	return coverage, nil
}

// references evaluates static and dynamic targets. Dynamic lookup starts at
// the outermost resource, but only for a statically resolved dynamic anchor.
func (evaluation *encodedEvaluation) references(node *Node, value any, pointer string) (encodedCoverage, error) {
	coverage := newEncodedCoverage()
	targets := []*Node{node.reference, node.dynamic}

	for _, resource := range evaluation.scope {
		if target := resource.anchors[node.dynamicName]; target != nil {
			targets[1] = target
			break
		}
	}

	for _, target := range targets {
		if target == nil {
			continue
		}

		result, err := evaluation.run(target, value, pointer)

		if err != nil {
			return encodedCoverage{}, err
		}

		coverage.merge(result)
	}

	return coverage, nil
}

// scalar checks common equality/type assertions before kind-specific rules.
// Exact equality traversal has its own work charges and never applies defaults.
func (evaluation *encodedEvaluation) scalar(node *Node, value any, pointer string) error {
	if raw, present := node.doc["type"]; present {
		if !matchesEncodedType(raw, value) {
			return encodedMismatch(pointer, "type")
		}
	}

	if expected, present := node.doc["const"]; present {
		equal, err := evaluation.equal(value, expected)

		if err != nil {
			return err
		}

		if !equal {
			return encodedMismatch(pointer, "const")
		}
	}

	if err := evaluation.enum(node, value, pointer); err != nil {
		return err
	}

	if text, ok := value.(string); ok {
		if err := evaluation.budget.text(text); err != nil {
			return err
		}

		return evaluateEncodedString(node, text, pointer)
	}

	if number, ok := numbers.Exact(value); ok {
		return checkEncodedLimits(node, number, numbers.Numbers, pointer)
	}

	return nil
}

// enum searches exact candidates, preserving cancellation/resource errors.
// A missing enum is unconstrained; the compiler rejects empty enum arrays.
func (evaluation *encodedEvaluation) enum(node *Node, value any, pointer string) error {
	candidates, present := node.doc["enum"].([]any)

	if !present {
		return nil
	}

	for _, candidate := range candidates {
		equal, err := evaluation.equal(value, candidate)

		if err != nil {
			return err
		}

		if equal {
			return nil
		}
	}

	return encodedMismatch(pointer, "enum")
}

// matchesEncodedType handles either checked type spelling without coercion.
// A number's integrality is mathematical, not based on its token spelling.
func matchesEncodedType(raw, value any) bool {
	if names, ok := raw.([]any); ok {
		for _, name := range names {
			if matchesEncodedType(name, value) {
				return true
			}
		}

		return false
	}

	name := encodedType(value)

	if raw == "integer" {
		number, ok := numbers.Exact(value)

		return ok && number.IsInt()
	}

	return raw == name
}

// encodedType names only decoder-produced types. Non-JSON values cannot enter
// this evaluator through its public byte-oriented API.
func encodedType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "number"
	}
}

// checkEncodedLimits reuses exact numeric boundary predicates without exposing
// schema literals through diagnostic Params. The slice is fixed trusted data.
func checkEncodedLimits(node *Node, value *big.Rat, limits []numbers.Limit, pointer string) error {
	for _, limit := range limits {
		bound, present := node.doc[limit.Keyword]

		if !present {
			continue
		}

		number, _ := numbers.Exact(bound)

		if limit.Fails(value, number) {
			return encodedMismatch(pointer, limit.Keyword)
		}
	}

	return nil
}

// evaluateEncodedString counts runes and matches the precompiled RE2 pattern.
// format remains an annotation regardless of the format name.
func evaluateEncodedString(node *Node, text, pointer string) error {
	length := new(big.Rat).SetInt64(int64(utf8.RuneCountInString(text)))

	if err := checkEncodedLimits(node, length, numbers.Strings, pointer); err != nil {
		return err
	}

	if pattern := node.patterns["pattern"]; pattern != nil {
		if !pattern.MatchString(text) {
			return encodedMismatch(pointer, "pattern")
		}
	}

	return nil
}

// encodedChildPointer appends an escaped object name or array index to an
// instance pointer. Callers never use schema pointer fragments here.
func encodedChildPointer(pointer, name string) string {
	return pointer + "/" + fields.EscapePointer(name)
}

// compose combines successful branches. Probe errors are discarded only when
// they are schema mismatches, never for cancellation or resource exhaustion.
func (evaluation *encodedEvaluation) compose(node *Node, value any, pointer string) (encodedCoverage, error) {
	coverage := newEncodedCoverage()

	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		result, err := evaluation.branches(node, value, encodedBranchLocation{keyword: keyword, pointer: pointer})

		if err != nil {
			return encodedCoverage{}, err
		}

		coverage.merge(result)
	}

	conditional, err := evaluation.conditional(node, value, pointer)

	if err != nil {
		return encodedCoverage{}, err
	}

	coverage.merge(conditional)

	if child := node.children["not"]; child != nil {
		_, matched, err := evaluation.match(child, value, pointer)

		if err != nil {
			return encodedCoverage{}, err
		}

		if matched {
			return encodedCoverage{}, encodedMismatch(pointer, "not")
		}
	}

	return coverage, nil
}

// encodedBranchLocation binds an applicator group to its instance location,
// keeping composition/dependency policy separate from the evaluated value.
type encodedBranchLocation struct {
	// keyword selects the composition or dependency map being evaluated.
	keyword string
	// pointer locates the instance, not the branch's schema.
	pointer string
}

// branches evaluates every successful anyOf branch to collect complete
// annotations. allOf can stop at its first mismatch; fatal errors always stop.
func (evaluation *encodedEvaluation) branches(
	node *Node, value any, location encodedBranchLocation,
) (encodedCoverage, error) {
	coverage := newEncodedCoverage()

	values, present := node.doc[location.keyword].([]any)

	if !present {
		return coverage, nil
	}

	matches := 0

	for index := range values {
		child := node.children[location.keyword+"/"+strconv.Itoa(index)]
		result, matched, err := evaluation.match(child, value, location.pointer)

		if err != nil {
			return encodedCoverage{}, err
		}

		if !matched && location.keyword == "allOf" {
			return encodedCoverage{}, encodedMismatch(location.pointer, "allOf")
		}

		if matched {
			matches++
		}

		coverage.merge(result)
	}

	if matches == 0 || (location.keyword == "oneOf" && matches != 1) {
		return encodedCoverage{}, encodedMismatch(location.pointer, location.keyword)
	}

	return coverage, nil
}

// conditional evaluates only the chosen then/else branch. A successful if
// contributes its annotations; a failed if and its unchosen branch do not.
func (evaluation *encodedEvaluation) conditional(
	node *Node, value any, pointer string,
) (encodedCoverage, error) {
	coverage := newEncodedCoverage()

	condition := node.children["if"]

	if condition == nil {
		return coverage, nil
	}

	result, matched, err := evaluation.match(condition, value, pointer)

	if err != nil {
		return encodedCoverage{}, err
	}

	branch := "then"

	if !matched {
		branch = "else"
	} else {
		coverage.merge(result)
	}

	if child := node.children[branch]; child != nil {
		result, err := evaluation.run(child, value, pointer)

		if err != nil {
			return encodedCoverage{}, err
		}

		coverage.merge(result)
	}

	return coverage, nil
}
