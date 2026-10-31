package validate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

func TestEncodedSchemaAndInstanceBytesAreOwnedCorrectly(t *testing.T) {
	document := []byte(`{"const":{"n":9007199254740993}}`)
	original := bytes.Clone(document)

	check, err := validate.FromJSONSchemaStrict(document)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(document, original) {
		t.Fatal("compilation changed schema bytes")
	}

	for index := range document {
		document[index] = ' '
	}

	instance := []byte(`{"n":9007199254740993}`)
	before := bytes.Clone(instance)

	if err := check.Validate(t.Context(), instance); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(instance, before) {
		t.Fatal("validation changed instance bytes")
	}

	for index := range instance {
		instance[index] = ' '
	}

	if err := check.Validate(t.Context(), []byte(`{"n":9007199254740993}`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedConcurrentReuse(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"array","uniqueItems":true,"items":{"$ref":"#/$defs/n"},"$defs":{"n":{"type":"integer","minimum":9007199254740992}}}`))

	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup

	results := make(chan error, 16)

	for range 16 {
		workers.Go(func() {
			results <- check.Validate(t.Context(), []byte(`[9007199254740992,9007199254740993]`))
		})
	}

	workers.Wait()
	close(results)

	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestEncodedCanceledCallRetainsContextIdentity(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = check.Validate(ctx, []byte(`null`))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("cancellation classified as schema mismatch")
	}
}

func TestEncodedZeroValueIsNotAMismatch(t *testing.T) {
	var check validate.EncodedSchema

	err := check.Validate(t.Context(), []byte(`null`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("invalid zero schema classified as mismatch")
	}
}

func TestEncodedExpiredDeadlineRetainsIdentity(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	t.Cleanup(cancel)

	err = check.Validate(ctx, []byte(`null`))

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Validate = %v", err)
	}
}

// ExampleFromJSONSchemaStrict demonstrates exact wire validation without an
// application value, normalization, defaults, or a binary64 conversion.
func ExampleFromJSONSchemaStrict() {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"integer","minimum":9007199254740993}`))

	if err != nil {
		fmt.Println("compile:", err)

		return
	}

	fmt.Println(check.Validate(context.Background(), []byte(`9007199254740993`)))
	// Output: <nil>
}

// ExampleEncodedSchema_Validate demonstrates the adapter's classification
// boundary: only validate.Errors means that a valid JSON instance mismatches.
func ExampleEncodedSchema_Validate() {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"properties":{"count":{"type":"integer"}}}`))

	if err != nil {
		fmt.Println("compile:", err)

		return
	}

	err = check.Validate(context.Background(), []byte(`{"count":null}`))

	var failures validate.Errors

	if errors.As(err, &failures) {
		fmt.Println("mismatch:", failures["/count"][0].Code)

		return
	}

	fmt.Println("operational error:", err)
	// Output: mismatch: type
}
