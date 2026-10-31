package validate

import (
	"cmp"
	"context"
	"fmt"
	"iter"
	"maps"
	"reflect"
	"slices"
	"strings"

	"arkoren.dev/foundation/validate/internal/faults"
)

// ErrInvalidSchema is wrapped by the panic value raised when a schema is
// built incorrectly, for example a [Struct] registration that names a missing
// field. The message lists every problem found.
//
// Example:
//
//	defer func() {
//		if err, ok := recover().(error); ok && errors.Is(err, validate.ErrInvalidSchema) {
//			fmt.Println(err)
//		}
//	}()
//	validate.Struct[string]() // Struct requires a supported struct type.
var ErrInvalidSchema = faults.ErrInvalidSchema

// Violation describes one failed rule.
type Violation struct {
	// Code identifies the rule, such as "string.min" or "required".
	Code string `json:"code"`

	// Params holds mutable rule arguments, such as {"min": 3}. Engine-created
	// violations from [Fail] or [At] and copies from [Errors.Localize] own their
	// parameter containers within the copying limits documented by [Fail].
	// [Errors.Add] and [Errors.All] do not copy parameters. Callers must not
	// mutate parameters concurrently with readers.
	//
	// Example:
	//
	//	failures := validate.Errors{"": {{
	//		Code: "string.min", Params: map[string]any{"min": 3},
	//	}}}
	//	localized := failures.Localize(context.Background())
	//	localized[""][0].Params["min"] = 5
	//	fmt.Println(failures[""][0].Params["min"]) // 3
	Params map[string]any `json:"params,omitzero"`

	// Message is the human readable text rendered for the request locale.
	Message string `json:"message"`
}

// Errors collects violations by RFC 6901 JSON Pointer. The pointer ""
// addresses the validated value itself and "/items/0/qty" the qty field of
// the first item.
//
// Errors implements error. Validate and Parse return a nil error, never
// an empty Errors.
type Errors map[string][]Violation

// Error summarizes the violations in English, in the order of [Errors.All].
//
// Example:
//
//	violations := validate.Errors{}
//	violations.Add("/email", validate.Violation{
//		Code: "required",
//	})
//	summary := violations.Error()
//	_ = summary
func (violations Errors) Error() string {
	var builder strings.Builder

	count := 0

	for pointer, violations := range violations.All() {
		for _, violation := range violations {
			if count > 0 {
				builder.WriteString("; ")
			}

			count++

			if pointer == "" {
				pointer = "(root)"
			}

			builder.WriteString(pointer)
			builder.WriteString(": ")
			builder.WriteString(message(context.Background(), violation.Code, violation.Params))
		}
	}

	return "validation failed: " + builder.String()
}

// All yields every pointer with its violations in a stable order. Pointers
// are compared segment by segment, and segments made of digits compare
// numerically, so "/items/2" precedes "/items/10".
//
// Example:
//
//	violations := validate.Errors{}
//	violations.Add("/email", validate.Violation{
//		Code: "required",
//	})
//	for pointer, failures := range violations.All() {
//		_, _ = pointer, failures
//	}
func (violations Errors) All() iter.Seq2[string, []Violation] {
	return func(yield func(string, []Violation) bool) {
		for _, pointer := range slices.SortedFunc(maps.Keys(violations), comparePointers) {
			if !yield(pointer, violations[pointer]) {
				return
			}
		}
	}
}

// Add appends violation at pointer. The receiver must not be nil.
//
// Example:
//
//	violations := validate.Errors{}
//	violations.Add("/email", validate.Violation{
//		Code: "required",
//	})
//	_ = violations["/email"]
func (violations Errors) Add(pointer string, violation Violation) {
	violations[pointer] = append(violations[pointer], violation)
}

// Localize returns a copy whose messages are rendered again using the
// catalog and locale of ctx. Violations whose code no catalog knows keep
// their message. Each copied violation owns independent parameter containers
// within [Fail]'s copying limits, even when its message is unchanged. Arbitrary
// objects in parameters remain shared; Localize does not make them safe for
// concurrent mutation. The receiver is not modified.
//
// Example:
//
//	violations := validate.Errors{}
//	violations.Add("/email", validate.Violation{
//		Code: "string.min", Params: map[string]any{"min": 3},
//	})
//	localized := violations.Localize(context.Background())
//	localized["/email"][0].Params["min"] = 5
//	fmt.Println(violations["/email"][0].Params["min"]) // 3
func (violations Errors) Localize(ctx context.Context) Errors {
	localized := make(Errors, len(violations))

	for pointer, violations := range violations {
		rendered := make([]Violation, len(violations))

		for index, violation := range violations {
			violation.Params = cloneParams(violation.Params)

			if _, ok := lookup(ctx, violation.Code); ok {
				violation.Message = message(ctx, violation.Code, violation.Params)
			}

			rendered[index] = violation
		}

		localized[pointer] = rendered
	}

	return localized
}

// comparePointers orders JSON Pointers segment by segment.
func comparePointers(left, right string) int {
	leftSegments := strings.Split(left, "/")
	rightSegments := strings.Split(right, "/")

	for index := 0; index < len(leftSegments) && index < len(rightSegments); index++ {
		if order := compareSegments(leftSegments[index], rightSegments[index]); order != 0 {
			return order
		}
	}

	return len(leftSegments) - len(rightSegments)
}

// compareSegments compares two pointer segments, numerically when both
// are non-negative integers.
func compareSegments(left, right string) int {
	leftNumeric, rightNumeric := isDigits(left), isDigits(right)

	switch {
	case leftNumeric && rightNumeric:
		leftTrimmed := strings.TrimLeft(left, "0")
		rightTrimmed := strings.TrimLeft(right, "0")

		if order := cmp.Compare(len(leftTrimmed), len(rightTrimmed)); order != 0 {
			return order
		}

		if order := strings.Compare(leftTrimmed, rightTrimmed); order != 0 {
			return order
		}

		return strings.Compare(left, right)
	case leftNumeric:
		return -1
	case rightNumeric:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for index := 0; index < len(s); index++ {
		if s[index] < '0' || s[index] > '9' {
			return false
		}
	}

	return true
}

// failure is the error produced by [Fail] and [At]. The engine turns it
// into a [Violation].
type failure struct {
	// field selects a sibling Go field for At; empty attaches the failure to the
	// current value.
	field string
	// code identifies the violation for catalog lookup and programmatic matching.
	code string
	// params owns the parameter containers captured by Fail or At within
	// Fail's documented copying limits. Reporting detaches those containers
	// again so a caller cannot mutate this reusable failure through Errors.
	params map[string]any
}

// Error implements error.
//
// Example:
//
//	err := validate.Fail("required", nil)
//	_ = err.Error()
func (failure *failure) Error() string {
	if failure.field != "" {
		return fmt.Sprintf("validation failed at %s: %s", failure.field, failure.code)
	}

	return "validation failed: " + failure.code
}

// Fail reports a validation failure from a Check function. The engine
// records it as a violation with the given code at the checked value.
// Any other error returned from a Check passes through unchanged. The
// returned error may be reused across checks and schema executions; each
// reported violation receives independent parameter containers.
//
// Fail snapshots slices, arrays and string-keyed maps recursively, including
// containers reached through interfaces. Named container and string-key types,
// scalar types and nil-versus-empty distinctions are preserved. All other
// values are retained without traversing struct fields, following pointers or
// inspecting maps with non-string keys. Opaque native objects, functions and
// captures remain application-owned. This is container copying, not a general
// object deep copy or JSON conversion.
//
// params must be acyclic along copied containers; cycles are not detected.
// Values must be serializable to JSON if the violations will be encoded; Fail
// does not validate or encode them and never invokes marshaling methods. A nil
// params map remains nil.
//
// Example:
//
//	allowed := []int{3, 5}
//	failure := validate.Fail("reserved", map[string]any{"allowed": allowed})
//	allowed[0] = 99 // Does not change the captured parameter.
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Check(func(_ context.Context, value string) error {
//			return failure
//		})
//	})
//	_, err := schema.Parse(context.Background(), "admin")
//	if failures, ok := errors.AsType[validate.Errors](err); ok {
//		fmt.Println(failures[""][0].Params["allowed"]) // [3 5]
//	}
func Fail(code string, params map[string]any) error {
	return &failure{
		code:   code,
		params: cloneParams(params),
	}
}

// At reports a validation failure at a sibling field from a [Struct]
// Check function. The field is a Go field name, as in [StructBuilder.Field].
// An unknown field makes validation fail with an error wrapping [ErrInvalidSchema].
// Parameters follow [Fail]'s copying limits: supported containers are captured
// now and detached again for each reported violation; arbitrary objects remain
// shared. A nil params map remains nil.
//
// Example:
//
//	type Booking struct {
//		Start, End int
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Booking]) {
//		object.Field("End", func(number *validate.IntBuilder[int]) {
//		})
//		object.Check(func(_ context.Context, value Booking) error {
//			if value.End < value.Start {
//				return validate.At("End", "order", nil)
//			}
//			return nil
//		})
//	})
//	err := schema.Validate(context.Background(), Booking{
//		Start: 2,
//		End:   1,
//	})
//	if err != nil {
//		fmt.Println(err)
//	}
func At(field, code string, params map[string]any) error {
	return &failure{
		field:  field,
		code:   code,
		params: cloneParams(params),
	}
}

// cloneParams snapshots diagnostic parameter containers at capture and output
// boundaries. It preserves concrete Go types and nil maps; arbitrary objects
// remain shared as documented by Fail. Callers must supply acyclic containers.
func cloneParams(params map[string]any) map[string]any {
	return cloneParam(reflect.ValueOf(params)).Interface().(map[string]any)
}

// cloneParam recursively detaches supported containers without invoking user
// code. Non-nil interfaces are unwrapped and their copies are assigned back to
// the original container's interface type. Nil interfaces and opaque values
// retain their original values without traversing their contents.
func cloneParam(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}

		return cloneParam(value.Elem())
	case reflect.Map:
		return cloneParamMap(value)
	case reflect.Array, reflect.Slice:
		return cloneParamSequence(value)
	default:
		return value
	}
}

// cloneParamMap copies string-keyed maps and their supported nested containers.
// Nil maps and maps outside the supported key domain are retained unchanged;
// keys are immutable string values and do not need their own copies.
func cloneParamMap(value reflect.Value) reflect.Value {
	if value.IsNil() || value.Type().Key().Kind() != reflect.String {
		return value
	}

	cloned := reflect.MakeMapWithSize(value.Type(), value.Len())

	for entries := value.MapRange(); entries.Next(); {
		cloned.SetMapIndex(entries.Key(), cloneParam(entries.Value()))
	}

	return cloned
}

// cloneParamSequence detaches slice storage or array elements while retaining
// the sequence type and nil-versus-empty distinction. Slice capacity is limited
// to its length so the snapshot cannot expose unused caller-owned elements.
func cloneParamSequence(value reflect.Value) reflect.Value {
	if value.Kind() == reflect.Slice && value.IsNil() {
		return value
	}

	cloned := reflect.New(value.Type()).Elem()

	if value.Kind() == reflect.Slice {
		cloned = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
	}

	for index := range value.Len() {
		cloned.Index(index).Set(cloneParam(value.Index(index)))
	}

	return cloned
}
