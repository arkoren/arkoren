package validate

import (
	"context"
	"reflect"
)

// Schema validates values of type T. Build schemas with [String], [Int],
// [Struct] and the other constructors in this package.
//
// Validate checks v and returns nil when it passes, an [Errors] when rules
// fail, or any other error returned by a Check function. Parse runs the
// same checks and also returns a copy of v with defaults applied; the
// input is never modified. Both agree on pass or fail.
// Cancellation stops between normalization, rule and child callbacks and
// returns the context error unless an earlier operational error already aborted
// execution. Parse discards partial results in either case.
// For explicit builder-backed schemas, standalone compilation panics with an
// error wrapping [ErrInvalidSchema] if the definition is invalid; this is not
// a user-input violation. [InferSchema] instead returns its inference error.
//
// Schemas expose execution and inspection only and are safe to share between
// concurrent calls. Settings belong to typed construction drafts, not schemas.
// Drafts do not implement Schema. External schema implementations are prohibited.
// Schema-owned state is immutable; arbitrary defaults, native objects and
// function captures are not recursively copied and must be safe for shared use.
//
// Example:
//
//	var schema validate.Schema[string] = validate.String(func(text *validate.StringBuilder[string]) {
//		text.Trim().Required()
//	})
//	value, err := schema.Parse(context.Background(), " Ada ")
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value)
type Schema[T any] interface {
	// Validate checks the prepared v and returns nil, [Errors], or a Check
	// error. Invalid explicit definitions panic during standalone compilation;
	// they are not returned as violations. Caller-owned input is not modified.
	//
	// Example:
	//
	//	schema := validate.String[string]()
	//	if err := schema.Validate(context.Background(), "Ada"); err != nil {
	//		panic(err)
	//	}
	Validate(ctx context.Context, v T) error

	// Parse checks v and returns its normalized, defaulted copy. A validation
	// failure returns the zero T and an error, never a partial result. Invalid
	// explicit definitions panic during standalone compilation, as with Validate.
	//
	// Example:
	//
	//	schema := validate.String(func(text *validate.StringBuilder[string]) {
	//		text.Trim()
	//	})
	//	value, err := schema.Parse(context.Background(), " Ada ")
	//	if err != nil {
	//		panic(err)
	//	}
	//	fmt.Println(value) // Ada
	Parse(ctx context.Context, v T) (T, error)

	// JSONSchema renders the schema as a JSON Schema document (2020-12
	// subset). Rules JSON Schema cannot express (EqField and the other
	// field comparisons, conditions, Check functions, normalizers, and
	// time- or clock-based rules) are left out; they are still enforced by
	// Validate and Parse. A successful export describes supported constraints;
	// it does not prove equivalence with a decoded-JSON validator.
	//
	// The output is deterministic: object keys are sorted. Invalid definitions
	// return nil bytes and an error wrapping [ErrInvalidSchema], including
	// recorded Use errors, invalid rule arguments, unbound field references,
	// and unconstructed composite schemas. A zero [StructSchema] is invalid
	// even through [Ptr]; use Struct[T]() for a valid empty registration.
	// Constructed slice and map schemas may omit child rules deliberately.
	// Values JSON cannot encode, such as a non-JSON default, instead return
	// nil bytes and an error preserving the JSON encoder's cause.
	//
	// Export checks definitions without running Check, Normalize, condition,
	// or collection-predicate callbacks, and never replays construction callbacks.
	// It does not verify defaults against their rules or normalize default
	// annotations. Repeated annotations use the last value while runtime rules
	// accumulate. Go absence semantics and numeric representation also limit
	// interchange; consumers must check their own import and validation results.
	//
	// Example:
	//
	//	type Signup struct {
	//		Email string
	//	}
	//	schema := validate.Struct(func(object *validate.StructBuilder[Signup]) {
	//		object.Field("Email", func(email *validate.StringBuilder[string]) {
	//			email.Email()
	//		})
	//	})
	//	document, err := schema.JSONSchema()
	//	if err != nil {
	//		panic(err)
	//	}
	//	fmt.Println(string(document))
	JSONSchema() ([]byte, error)

	anySchema
}

// anySchema is the type-erased form of a schema that [Struct] binds to
// struct fields through reflection.
type anySchema interface {
	// rtype is the exact Go type the schema validates.
	rtype() reflect.Type

	// jsonSchema builds the JSON Schema keywords of the schema.
	jsonSchema() (map[string]any, error)

	// isRequired reports whether Required was called on the schema, which
	// lists the field in the "required" keyword of the enclosing object.
	isRequired() bool

	// compile builds the executable node. env is nil outside a Struct.
	compile(e *env, rep *report, where string) *node
}
