package encoded_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"arkoren.dev/foundation/validate"
)

// encodedCancelContext deterministically cancels on a selected Err observation.
// Atomic counting makes traversal cancellation repeatable without sleeps.
type encodedCancelContext struct {
	context.Context
	calls atomic.Int64
	after int64
}

func (ctx *encodedCancelContext) Err() error {
	if ctx.calls.Add(1) >= ctx.after {
		return context.Canceled
	}

	return nil
}

func TestEncodedCancellationDuringCompositionIsNotANegativeMatch(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"anyOf":[true,true,true,true,true,true,true,true,true,true]}`))

	if err != nil {
		t.Fatal(err)
	}

	ctx := &encodedCancelContext{Context: t.Context(), after: 8}

	err = check.Validate(ctx, []byte(`null`))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedEvaluationStepLimit(t *testing.T) {
	// Repeated local refs create finite exponential work in a small schema.
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$defs":{"a":{"anyOf":[{"$ref":"#/$defs/b"},{"$ref":"#/$defs/b"}]},"b":{"anyOf":[{"$ref":"#/$defs/c"},{"$ref":"#/$defs/c"}]},"c":{"anyOf":[{"$ref":"#/$defs/d"},{"$ref":"#/$defs/d"}]},"d":{"anyOf":[{"$ref":"#/$defs/e"},{"$ref":"#/$defs/e"}]},"e":{"anyOf":[{"$ref":"#/$defs/f"},{"$ref":"#/$defs/f"}]},"f":{"anyOf":[{"$ref":"#/$defs/g"},{"$ref":"#/$defs/g"}]},"g":{"anyOf":[{"$ref":"#/$defs/h"},{"$ref":"#/$defs/h"}]},"h":{"anyOf":[{"$ref":"#/$defs/i"},{"$ref":"#/$defs/i"}]},"i":{"anyOf":[{"$ref":"#/$defs/j"},{"$ref":"#/$defs/j"}]},"j":{"anyOf":[{"$ref":"#/$defs/k"},{"$ref":"#/$defs/k"}]},"k":{"anyOf":[{"$ref":"#/$defs/l"},{"$ref":"#/$defs/l"}]},"l":{"anyOf":[{"$ref":"#/$defs/m"},{"$ref":"#/$defs/m"}]},"m":{"anyOf":[{"$ref":"#/$defs/n"},{"$ref":"#/$defs/n"}]},"n":{"anyOf":[{"$ref":"#/$defs/o"},{"$ref":"#/$defs/o"}]},"o":{"anyOf":[{"$ref":"#/$defs/p"},{"$ref":"#/$defs/p"}]},"p":{"anyOf":[{"$ref":"#/$defs/q"},{"$ref":"#/$defs/q"}]},"q":{"anyOf":[{"$ref":"#/$defs/r"},{"$ref":"#/$defs/r"}]},"r":{"anyOf":[{"$ref":"#/$defs/s"},{"$ref":"#/$defs/s"}]},"s":{"anyOf":[{"$ref":"#/$defs/t"},{"$ref":"#/$defs/t"}]},"t":true},"$ref":"#/$defs/a"}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`null`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedInstancePointerExpansionIsBounded(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"additionalProperties":false}`))

	if err != nil {
		t.Fatal(err)
	}

	document := `{"` + strings.Repeat("a", 16384) + `":null}`

	err = check.Validate(t.Context(), []byte(document))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

// TestEncodedEvaluationDepthIsBounded builds a finite reference chain that
// stays inside JSON container and schema-node limits but exceeds the independent
// evaluation call-stack budget. No recursive schema or wall-clock wait is used.
func TestEncodedEvaluationDepthIsBounded(t *testing.T) {
	var document strings.Builder

	document.WriteString(`{"$ref":"#/$defs/n0","$defs":{`)

	for index := range 512 {
		if index > 0 {
			document.WriteByte(',')
		}

		document.WriteString(`"n` + strconv.Itoa(index) + `":{"$ref":"#/$defs/n` + strconv.Itoa(index+1) + `"}`)
	}

	document.WriteString(`,"n512":true}}`)

	check, err := validate.FromJSONSchemaStrict([]byte(document.String()))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`null`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("evaluation depth limit classified as schema mismatch")
	}
}
