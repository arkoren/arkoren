package encoded

import (
	"context"
	"fmt"

	"arkoren.dev/foundation/validate/internal/faults"
)

const (
	// encodedMaxBytes bounds either complete wire document before decoding.
	encodedMaxBytes = 8 << 20
	// encodedMaxDepth bounds JSON container nesting before recursive decoding.
	encodedMaxDepth = 256
	// encodedMaxNodes bounds compiled schema nodes, including annotation schemas.
	encodedMaxNodes = 16384
	// encodedMaxSteps bounds token decoding and evaluation independently.
	encodedMaxSteps = 1000000
	// encodedMaxEvaluationDepth bounds reference/composition call-stack growth.
	encodedMaxEvaluationDepth = 512
	// encodedMaxDigits bounds coefficient digits before allocating a rational.
	encodedMaxDigits = 4096
	// encodedMaxExponent bounds absolute decimal exponents, even for zero.
	encodedMaxExponent = 4096
	// encodedMaxNumericDigits bounds cumulative coefficient/exponent expansion
	// before rational allocation, preventing many short exponent tokens from
	// multiplying the wire byte bound into a much larger numeric graph.
	encodedMaxNumericDigits = 64 << 20
	// encodedMaxKeyBytes bounds cumulative structural equality/uniqueness output.
	// Repeated comparisons consume the same per-validation encoding budget.
	encodedMaxKeyBytes = 64 << 20
	// encodedMaxLocationBytes bounds URI and JSON Pointer expansion/storage.
	encodedMaxLocationBytes = 16384
	// encodedMaxIndexBytes bounds cumulative compiled URI alias storage.
	encodedMaxIndexBytes = 32 << 20
)

// encodedBudget belongs to one operation, never to a published schema. Steps
// account for decoded values, assertion visits, comparisons, and item/property
// traversal. Bounded library operations (RE2 matching, rational arithmetic,
// token/string copies) are not individually preemptible.
type encodedBudget struct {
	// ctx provides cooperative cancellation without background goroutines.
	ctx context.Context
	// steps counts work already charged to this operation.
	steps int
}

// tick checks cancellation before consuming another bounded unit of work.
// Resource errors deliberately have no relationship to validation public validation errors.
func (budget *encodedBudget) tick() error {
	if err := budget.ctx.Err(); err != nil {
		return err
	}

	budget.steps++

	if budget.steps > encodedMaxSteps {
		return fmt.Errorf("%w: steps", faults.ErrEncodedLimit)
	}

	return nil
}

// text charges string traversal/matching work in bounded chunks. A schema with
// many branches cannot repeatedly scan a multi-megabyte scalar for one step.
func (budget *encodedBudget) text(text string) error {
	// chunkSize shares the structural key builder's byte accounting granularity.
	const chunkSize = 4096

	for offset := 0; offset < len(text); offset += chunkSize {
		if err := budget.tick(); err != nil {
			return err
		}
	}

	return nil
}
