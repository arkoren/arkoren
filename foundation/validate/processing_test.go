package validate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// jsonWorkContext counts cancellation polls on a single validating goroutine.
// It cancels its real underlying context at an exact work checkpoint, making
// in-flight cancellation and work bounds deterministic without sleeps.
type jsonWorkContext struct {
	// Context carries cancellation and the other ordinary context methods.
	context.Context
	// checks counts Err calls, including the one that triggers cancellation.
	checks int
	// cancelAfter is the checkpoint that cancels; zero never cancels.
	cancelAfter int
	// cancel closes the underlying context when cancelAfter is reached.
	cancel context.CancelFunc
}

// Err advances the work counter before checking the underlying context.
// Tests own each instance and never call it concurrently.
func (ctx *jsonWorkContext) Err() error {
	ctx.checks++

	if ctx.cancelAfter > 0 && ctx.checks == ctx.cancelAfter {
		ctx.cancel()
	}

	return ctx.Context.Err()
}

// TestJSONSchemaUniqueItemsLinearWorkWithoutLimit bounds observed traversal
// work for increasing distinct inputs with no maxItems. Every structural-key
// node and every recursive equality comparison polls cancellation, so a
// pairwise scan through jsonEqual exceeds this linear budget deterministically.
func TestJSONSchemaUniqueItemsLinearWorkWithoutLimit(t *testing.T) {
	t.Parallel()

	schema, err := FromJSONSchema([]byte(`{"uniqueItems":true}`))

	if err != nil {
		t.Fatal(err)
	}

	for _, size := range []int{128, 256, 512, 1024} {
		items := make([]any, size)

		for index := range items {
			items[index] = float64(index)
		}

		ctx := &jsonWorkContext{
			Context: context.Background(),
		}

		if err := schema.Validate(ctx, items); err != nil {
			t.Fatal(err)
		}

		if ctx.checks < size || ctx.checks > 8*size+16 {
			t.Fatalf("%d items used %d work checkpoints, want [%d,%d]", size, ctx.checks, size, 8*size+16)
		}
	}
}

// TestJSONSchemaOversizedArrayUsesConstantWork checks public Parse: oversized
// arrays yield no result and no child or uniqueness traversal, even when enum
// and items would require walking every element. Parse adds one final context
// check to the two checkpoints permitted before array-size rejection.
func TestJSONSchemaOversizedArrayUsesConstantWork(t *testing.T) {
	t.Parallel()

	schema, err := FromJSONSchema([]byte(`{"maxItems":1,"uniqueItems":true,"enum":[[]],"items":{}}`))

	if err != nil {
		t.Fatal(err)
	}

	for _, size := range []int{16, 256, 1024} {
		ctx := &jsonWorkContext{
			Context: context.Background(),
		}
		parsed, err := schema.Parse(ctx, make([]any, size))

		if parsed != nil {
			t.Fatalf("oversized Parse returned a result: %v", parsed)
		}

		var violations Errors

		if !errors.As(err, &violations) {
			t.Fatalf("oversized Parse error = %v, want validation violations", err)
		}

		if ctx.checks == 0 || ctx.checks > 3 {
			t.Fatalf("size %d: %d checkpoints, want at most three", size, ctx.checks)
		}

		if len(violations) != 1 || len(violations[""]) != 1 || violations[""][0].Code != "slice.max" {
			t.Fatalf("size %d: violations %v, want only slice.max at root", size, violations)
		}
	}
}

// TestJSONSchemaUniqueItemsCancelsInsideNestedItem triggers cancellation
// during the first item's structural encoding, before any item-schema work.
// Parse must return only context.Canceled and leave the input untouched.
func TestJSONSchemaUniqueItemsCancelsInsideNestedItem(t *testing.T) {
	t.Parallel()

	schema, err := FromJSONSchema([]byte(`{"uniqueItems":true}`))

	if err != nil {
		t.Fatal(err)
	}

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: 32,
		cancel:      cancel,
	}
	nested := make([]any, 256)

	for index := range nested {
		nested[index] = float64(index)
	}

	input := []any{map[string]any{"nested": nested}}

	parsed, err := schema.Parse(ctx, input)

	if !errors.Is(err, context.Canceled) || parsed != nil || ctx.checks > ctx.cancelAfter+1 {
		t.Fatalf("canceled Parse = %v, %v after %d checkpoints", parsed, err, ctx.checks)
	}

	for index, item := range nested {
		if item != float64(index) {
			t.Fatalf("input[%d] changed to %v", index, item)
		}
	}
}

// TestJSONSchemaCancelsDuringUnconstrainedCopy covers containers whose
// children have no schemas. Cancellation must reach cloneJSON, not just the
// uniqueItems loop or schema-directed traversal.
func TestJSONSchemaCancelsDuringUnconstrainedCopy(t *testing.T) {
	t.Parallel()

	schema, err := FromJSONSchema([]byte(`{}`))

	if err != nil {
		t.Fatal(err)
	}

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: 32,
		cancel:      cancel,
	}
	input := []any{map[string]any{"nested": make([]any, 256)}}

	parsed, err := schema.Parse(ctx, input)

	if !errors.Is(err, context.Canceled) || parsed != nil || ctx.checks > ctx.cancelAfter+1 {
		t.Fatalf("canceled copy = %v, %v after %d checkpoints", parsed, err, ctx.checks)
	}
}

// TestJSONSchemaCancelsDuringChildTraversal exercises actual item rules after
// a violation has accumulated. Cancellation replaces those partial violations
// and stops further child checks instead of returning an incomplete Errors map.
func TestJSONSchemaCancelsDuringChildTraversal(t *testing.T) {
	t.Parallel()

	schema, err := FromJSONSchema([]byte(`{"properties":{"values":{"items":{"type":"string"}}}}`))

	if err != nil {
		t.Fatal(err)
	}

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: 32,
		cancel:      cancel,
	}
	input := make([]any, 256)

	for index := range input {
		input[index] = float64(index)
	}

	parsed, err := schema.Parse(ctx, map[string]any{"values": input})

	if !errors.Is(err, context.Canceled) || parsed != nil || ctx.checks > ctx.cancelAfter+1 {
		t.Fatalf("canceled traversal = %v, %v after %d checkpoints", parsed, err, ctx.checks)
	}
}

// TestJSONSchemaCancelsDuringEnumComparison exercises the public enum search
// with a matching nested candidate. Cancellation inside equality must abort
// the search and return the context error, not success or a one_of violation.
func TestJSONSchemaCancelsDuringEnumComparison(t *testing.T) {
	t.Parallel()

	doc := `{"enum":[[` + strings.Repeat("null,", 255) + `null]]}`

	schema, err := FromJSONSchema([]byte(doc))

	if err != nil {
		t.Fatal(err)
	}

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: 32,
		cancel:      cancel,
	}

	parsed, err := schema.Parse(ctx, make([]any, 256))

	if !errors.Is(err, context.Canceled) || parsed != nil || ctx.checks > ctx.cancelAfter+1 {
		t.Fatalf("canceled enum = %v, %v after %d checkpoints", parsed, err, ctx.checks)
	}
}

// TestJSONSchemaReturnsDeadlineExceeded makes deadline errors distinguishable
// from cancellation and from validation violations. An already expired
// deadline is deterministic and does not wait for the wall clock to advance.
func TestJSONSchemaReturnsDeadlineExceeded(t *testing.T) {
	t.Parallel()

	schema, err := FromJSONSchema([]byte(`{}`))

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Time{})
	t.Cleanup(cancel)

	parsed, err := schema.Parse(ctx, nil)

	if !errors.Is(err, context.DeadlineExceeded) || parsed != nil {
		t.Fatalf("expired Parse = %v, %v", parsed, err)
	}
}

// TestJSONSchemaCancelsWhileCopyingDefault checks missing-property defaults,
// a path that does not visit the property's schema node during Parse. Partial
// default copies must be abandoned with the same cancellation semantics.
func TestJSONSchemaCancelsWhileCopyingDefault(t *testing.T) {
	t.Parallel()

	doc := `{"properties":{"values":{"default":[` + strings.Repeat("null,", 255) + `null]}}}`

	schema, err := FromJSONSchema([]byte(doc))

	if err != nil {
		t.Fatal(err)
	}

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: 32,
		cancel:      cancel,
	}

	parsed, err := schema.Parse(ctx, map[string]any{})

	if !errors.Is(err, context.Canceled) || parsed != nil || ctx.checks > ctx.cancelAfter+1 {
		t.Fatalf("canceled default copy = %v, %v after %d checkpoints", parsed, err, ctx.checks)
	}
}
