// Package encoded implements bounded, exact validation of encoded JSON.
// Its graph and budgets are independent from the decoded-value engine.
package encoded

import (
	"context"
	"fmt"

	"arkoren.dev/foundation/validate/internal/faults"
)

// Validate checks one strict JSON instance against a compiled graph.
func Validate(ctx context.Context, root *Node, document []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if root == nil {
		return fmt.Errorf("%w: uninitialized encoded schema", faults.ErrInvalidSchema)
	}

	value, err := decodeEncoded(ctx, document)

	if err != nil {
		return err
	}

	evaluation := encodedEvaluation{
		budget: encodedBudget{ctx: ctx},
		active: make(map[encodedVisit]bool),
	}

	_, err = evaluation.run(root, value, "")

	return err
}
