package encoded

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"arkoren.dev/foundation/validate/internal/faults"
	"arkoren.dev/foundation/validate/internal/numbers"
)

// match probes an assertion while preserving non-validation error identities.
// It centralizes the boundary used by composition, conditionals, and contains:
// only Errors means a negative match; all other failures abort the operation.
func (evaluation *encodedEvaluation) match(
	node *Node, value any, pointer string,
) (encodedCoverage, bool, error) {
	coverage, err := evaluation.run(node, value, pointer)

	if err == nil {
		return coverage, true, nil
	}

	if _, ok := errors.AsType[faults.Mismatch](err); ok {
		return newEncodedCoverage(), false, nil
	}

	return encodedCoverage{}, false, err
}

// equal compares collision-free structural keys. Reusing the uniqueness key
// ensures identical exact-number semantics for enum, const, and uniqueItems,
// including numbers nested inside differently ordered object members.
func (evaluation *encodedEvaluation) equal(left, right any) (bool, error) {
	leftKey, err := evaluation.key(nil, left)

	if err != nil {
		return false, err
	}

	rightKey, err := evaluation.key(nil, right)

	if err != nil {
		return false, err
	}

	return bytes.Equal(leftKey, rightKey), nil
}

// key appends a typed, length-delimited structural encoding. Number rationals
// are borrowed immutably from the shared exact-number representation. Values
// are always decoder-produced, so there are no arbitrary Go types or cycles.
func (evaluation *encodedEvaluation) key(key []byte, value any) ([]byte, error) {
	if err := evaluation.budget.tick(); err != nil {
		return nil, err
	}

	if err := evaluation.reserveKeyBytes(1 + binary.MaxVarintLen64); err != nil {
		return nil, err
	}

	if number, ok := numbers.Exact(value); ok {
		return evaluation.keyText(append(key, 'd'), number.RatString())
	}

	switch instance := value.(type) {
	case nil:
		return append(key, 'n'), nil
	case bool:
		if instance {
			return append(key, 't'), nil
		}

		return append(key, 'f'), nil
	case string:
		return evaluation.keyText(append(key, 's'), instance)
	case []any:
		return evaluation.keyArray(key, instance)
	case map[string]any:
		return evaluation.keyObject(key, instance)
	default:
		return nil, faults.ErrEncodedJSON
	}
}

// keyText length-delimits strings and bounds uninterruptible copies. It also
// charges long scalar content rather than treating a megabyte string as free.
func (evaluation *encodedEvaluation) keyText(key []byte, text string) ([]byte, error) {
	// chunkSize is a work accounting unit, not an additional accepted-size cap.
	const chunkSize = 4096

	if err := evaluation.reserveKeyBytes(len(text) + binary.MaxVarintLen64); err != nil {
		return nil, err
	}

	key = binary.AppendUvarint(key, uint64(len(text)))

	for len(text) > 0 {
		if err := evaluation.budget.tick(); err != nil {
			return nil, err
		}

		count := min(len(text), chunkSize)
		key = append(key, text[:count]...)
		text = text[count:]
	}

	return key, nil
}

// reserveKeyBytes accounts for structural key storage before appending it.
// Callers charge payload lengths and conservative upper bounds for tag/length
// prefixes, preventing both large scalars and many empty containers escaping
// the cumulative encoding policy. Failed reservations append no key data.
func (evaluation *encodedEvaluation) reserveKeyBytes(count int) error {
	evaluation.keyBytes += count

	if evaluation.keyBytes > encodedMaxKeyBytes {
		return fmt.Errorf("%w: structural key bytes", faults.ErrEncodedLimit)
	}

	return nil
}

// keyArray preserves array order and distinguishes arrays from other kinds.
// The length prefix also distinguishes nested boundaries without separators.
func (evaluation *encodedEvaluation) keyArray(key []byte, array []any) ([]byte, error) {
	key = binary.AppendUvarint(append(key, 'a'), uint64(len(array)))

	for _, value := range array {
		var err error

		key, err = evaluation.key(key, value)

		if err != nil {
			return nil, err
		}
	}

	return key, nil
}

// keyObject sorts member names without changing the input. Length prefixes
// distinguish names from values; numbers keep exact meaning at every depth.
func (evaluation *encodedEvaluation) keyObject(key []byte, object map[string]any) ([]byte, error) {
	key = binary.AppendUvarint(append(key, 'o'), uint64(len(object)))
	names, err := evaluation.objectKeys(object)

	if err != nil {
		return nil, err
	}

	for _, name := range names {
		var err error

		key, err = evaluation.keyText(key, name)

		if err != nil {
			return nil, err
		}

		key, err = evaluation.key(key, object[name])

		if err != nil {
			return nil, err
		}
	}

	return key, nil
}

// objectKeys charges the whole collection before sorting. A branch that fails
// on its first member must still pay for sorting every member; otherwise many
// failing alternatives could repeatedly sort a large object almost for free.
// Sorting is a bounded stdlib operation; collection observes cancellation.
func (evaluation *encodedEvaluation) objectKeys(object map[string]any) ([]string, error) {
	names := make([]string, 0, len(object))

	for name := range object {
		if err := evaluation.budget.tick(); err != nil {
			return nil, err
		}

		if err := evaluation.budget.text(name); err != nil {
			return nil, err
		}

		names = append(names, name)
	}

	slices.Sort(names)

	return names, nil
}
