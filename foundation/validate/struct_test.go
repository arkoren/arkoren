package validate_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// tenFields combines scalar, pointer and collection fields for whole-object
// benchmarks. Each field has an independent registration so the benchmark
// includes dispatch overhead.
type tenFields struct {
	// A provides one independently registered value for sibling-rule checks.
	A, B, C, D, E string
	// F begins the pointer-field group in the object benchmark fixture.
	F, G, H int
	// I provides slice storage in the object benchmark fixture.
	I []string
	// J provides map storage in the object benchmark fixture.
	J *int
}

// tenSchema is the immutable schema shared by the ten-field benchmarks. Reuse
// excludes schema construction from timed execution.
var tenSchema = validate.Struct[tenFields](func(object *validate.StructBuilder[tenFields]) {
	object.Field("A", func(value *validate.StringBuilder[string]) {
		value.Required()
		value.Min(2)
	})
	object.Field("B", func(value *validate.StringBuilder[string]) {
		value.Min(2)
		value.Max(10)
	})
	object.Field("C", func(value *validate.StringBuilder[string]) {
		value.Email()
	})
	object.Field("D", func(value *validate.StringBuilder[string]) {
		value.Default("d")
	})
	object.Field("E", func(value *validate.StringBuilder[string]) {
		value.NotBlank()
	})
	object.Field("F", func(number *validate.IntBuilder[int]) {
		number.Min(1)
	})
	object.Field("G", func(number *validate.IntBuilder[int]) {
		number.Max(100)
	})
	object.Field("H", func(number *validate.IntBuilder[int]) {
		number.Default(7)
	})
	object.Field("I", func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.Min(1)
		})
		items.Min(1)
	})
	object.Field("J", func(pointer *validate.PtrBuilder[int]) {
		pointer.Value(func(number *validate.IntBuilder[int]) {
			number.Min(0)
		})
	})
})

// BenchmarkStructTenFields measures a valid ten field struct.
func BenchmarkStructTenFields(b *testing.B) {
	number := 3
	input := tenFields{
		A: "ab",
		B: "abc",
		C: "a@b.co",
		E: "e",
		F: 1,
		G: 2,
		I: []string{"x"},
		J: &number,
	}
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		if err := tenSchema.Validate(ctx, input); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStructTenFieldsParse measures Parse including defaults.
func BenchmarkStructTenFieldsParse(b *testing.B) {
	number := 3
	input := tenFields{
		A: "ab",
		B: "abc",
		C: "a@b.co",
		E: "e",
		F: 1,
		G: 2,
		I: []string{"x"},
		J: &number,
	}
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		if _, err := tenSchema.Parse(ctx, input); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStructTenFieldsInvalid measures the failing path.
func BenchmarkStructTenFieldsInvalid(b *testing.B) {
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		if err := tenSchema.Validate(ctx, tenFields{}); err == nil {
			b.Fatal("expected errors")
		}
	}
}

// line is one cart entry used by nested collection benchmarks. Its SKU and
// quantity rules exercise both string and numeric children.
type line struct {
	// SKU identifies a line item for the nested string rule benchmark.
	SKU string
	// Qty provides a line-item quantity for the numeric rule benchmark.
	Qty int
}

// cart groups line items for nested parsing benchmarks. The collection adds
// another traversal level above the line schemas.
type cart struct {
	// Lines provides nested cart entries for collection benchmarks.
	Lines [][]line
}

// cartSchema is the immutable nested schema shared by cart benchmarks. Its
// child registrations are constructed before timing begins.
var cartSchema = validate.Struct[cart](func(object *validate.StructBuilder[cart]) {
	object.Field("Lines", func(items *validate.SliceBuilder[[]line]) {
		items.Values(func(items *validate.SliceBuilder[line]) {
			items.Values(func(object *validate.StructBuilder[line]) {
				object.Field("SKU", func(value *validate.StringBuilder[string]) {
					value.Required()
				})
				object.Field("Qty", func(number *validate.IntBuilder[int]) {
					number.Min(1)
				})
			})
		})
	})
})

// BenchmarkNestedSlices measures a 10x10 nested slice of objects.
func BenchmarkNestedSlices(b *testing.B) {
	input := cart{
		Lines: make([][]line, 10),
	}

	for index := range input.Lines {
		input.Lines[index] = make([]line, 10)

		for position := range input.Lines[index] {
			input.Lines[index][position] = line{
				SKU: "sku",
				Qty: 2,
			}
		}
	}

	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		if err := cartSchema.Validate(ctx, input); err != nil {
			b.Fatal(err)
		}
	}
}

// sharedSchema is reused by concurrent validation calls. The concurrency test
// checks that execution state never leaks between calls.
var sharedSchema = validate.Struct[settings](func(object *validate.StructBuilder[settings]) {
	object.Field("Name", func(value *validate.StringBuilder[string]) {
		value.Required()
		value.Min(3)
	})
	object.Field("Role", func(value *validate.StringBuilder[string]) {
		value.Default("member")
	})
	object.Field("Retries", func(number *validate.IntBuilder[int]) {
		number.Default(3)
	})
	object.Field("Tags", func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.Min(2)
		})
	})
	object.Field("Limit", func(pointer *validate.PtrBuilder[int]) {
		pointer.Value(func(number *validate.IntBuilder[int]) {
			number.Min(1)
		})
		pointer.Default(5)
	})
})

// TestSharedSchemaConcurrentUse verifies one schema serves many goroutines
// under the race detector, with independent results.
func TestSharedSchemaConcurrentUse(t *testing.T) {
	t.Parallel()

	var group sync.WaitGroup

	for worker := range 16 {
		group.Add(1)

		go func() {
			defer group.Done()

			for range 200 {
				got, err := sharedSchema.Parse(context.Background(), settings{
					Name: "alice",
					Tags: []string{"aa"},
				})

				if err != nil || got.Role != "member" || *got.Limit != 5 {
					t.Errorf("worker %d: got %+v, err %v", worker, got, err)

					return
				}

				*got.Limit = worker

				if err := sharedSchema.Validate(context.Background(), settings{
					Tags: []string{"a"},
				}); err == nil {
					t.Errorf("worker %d: invalid input passed", worker)

					return
				}
			}
		}()
	}

	group.Wait()
}

// signup is the registration input shared by the public examples.
type signup struct {
	// Email is the registrant's email address.
	Email string `json:"email"`
	// Password is the chosen password.
	Password string `json:"password"`
	// Confirm must match Password.
	Confirm string `json:"confirm"`
	// Plan selects the membership level.
	Plan string `json:"plan"`
	// Terms records acceptance of the terms of service.
	Terms bool `json:"terms"`
}

// registration validates signups and defaults the membership plan.
var registration = validate.Struct(func(object *validate.StructBuilder[signup]) {
	object.Field("Email", func(value *validate.StringBuilder[string]) {
		value.Required()
		value.Email()
	})
	object.Field("Password", func(value *validate.StringBuilder[string]) {
		value.Required()
		value.Min(12)
	})
	object.Field("Confirm", func(value *validate.StringBuilder[string]) {
		value.EqField("Password")
	})
	object.Field("Plan", func(value *validate.StringBuilder[string]) {
		value.OneOf("free", "pro")
		value.Default("free")
	})
	object.Field("Terms", func(value *validate.BoolBuilder[bool]) {
		value.Accepted()
	})
})

// ExampleStruct validates a struct and prints the violations by JSON
// Pointer.
func ExampleStruct() {
	err := registration.Validate(context.Background(), signup{
		Email:    "not-an-email",
		Password: "short",
		Confirm:  "different",
	})

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		fmt.Println(err)

		return
	}

	for pointer, violations := range errs.All() {
		for _, violation := range violations {
			fmt.Printf("%s %s: %s\n", pointer, violation.Code, violation.Message)
		}
	}

	// Output:
	// /confirm eq_field: must equal password
	// /email string.email: must be a valid email address
	// /password string.min: must be at least 12 characters
	// /terms bool.accepted: must be accepted
}

// ExampleStruct_nested composes a value struct and an optional pointer schema.
func ExampleStruct_nested() {
	type Address struct {
		// City is required when an address is present.
		City string `json:"city"`
	}
	type User struct {
		// Address is the user's primary address.
		Address Address `json:"address"`
		// Billing is an optional billing address.
		Billing *Address `json:"billing"`
	}

	address := validate.Struct(func(object *validate.StructBuilder[Address]) {
		object.Field("City", func(value *validate.StringBuilder[string]) {
			value.Trim()
			value.Required()
		})
	})
	user := validate.Struct(func(object *validate.StructBuilder[User]) {
		object.Field("Address", func(object *validate.StructBuilder[Address]) {
			object.Use(address)
		})
		object.Field("Billing", func(pointer *validate.PtrBuilder[Address]) {
			pointer.Value(func(object *validate.StructBuilder[Address]) {
				object.Use(address)
			})
		})
	})

	err := user.Validate(context.Background(), User{})

	failures, ok := errors.AsType[validate.Errors](err)

	if !ok {
		fmt.Println(err)

		return
	}

	for pointer, violations := range failures.All() {
		fmt.Println(pointer, violations[0].Code)
	}

	fmt.Println(user.Validate(context.Background(), User{
		Address: Address{
			City: "Paris",
		},
	}))

	// Output:
	// /address/city required
	// <nil>
}

// ExampleStructSchema_Parse applies defaults and returns a copy.
func ExampleStructSchema_Parse() {
	input := signup{
		Email:    "ada@example.com",
		Password: "correct horse battery",
		Confirm:  "correct horse battery",
		Terms:    true,
	}

	parsed, err := registration.Parse(context.Background(), input)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(err, parsed.Plan, input.Plan == "")

	// Output: <nil> free true
}

// ExampleStructBuilder_Check reports a cross-field failure at a sibling
// field.
func ExampleStructBuilder_Check() {
	type booking struct {
		// Start is the first time slot.
		Start int
		// End must not precede Start.
		End int
	}

	schema := validate.Struct(func(object *validate.StructBuilder[booking]) {
		object.Check(func(_ context.Context, value booking) error {
			if value.End < value.Start {
				return validate.At("End", "booking.order", nil)
			}

			return nil
		})
	})

	err := schema.Validate(context.Background(), booking{
		Start: 5,
		End:   1,
	})

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		fmt.Println(err)

		return
	}

	for pointer, violations := range errs.All() {
		fmt.Println(pointer, violations[0].Code)
	}

	// Output: /End booking.order
}

// ExampleStruct_conditions requires a VAT identifier for company contacts,
// while leaving it optional for people.
func ExampleStruct_conditions() {
	type contact struct {
		// Kind distinguishes a person from a company.
		Kind string
		// VAT is the company's tax identifier.
		VAT string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[contact]) {
		object.Field("Kind", func(value *validate.StringBuilder[string]) {
			value.OneOf("person", "company")
		})
		object.Field("VAT", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Kind", "company")
			})
		})
	})

	fmt.Println(schema.Validate(context.Background(), contact{
		Kind: "company",
	}))

	// Output: validation failed: /VAT: is required when Kind is company
}

// TestNilEmbeddedPointerRequired verifies promoted fields behind a nil
// embedded pointer still run Required.
func TestNilEmbeddedPointerRequired(t *testing.T) {
	t.Parallel()

	required := validate.Struct[embedOuter](func(object *validate.StructBuilder[embedOuter]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
				value.Min(1)
			})
			items.Required()
		})
	})

	input := embedOuter{}

	requireCodes(t, required.Validate(context.Background(), input),
		map[string][]string{"/Name": {"required"}, "/Tags": {"required"}})

	parsed, err := required.Parse(context.Background(), input)

	if err == nil || parsed.EmbedInner != nil {
		t.Fatalf("Parse = %+v, %v; want an error and no allocation", parsed, err)
	}

	if input.EmbedInner != nil {
		t.Fatal("input modified")
	}
}

// TestNilEmbeddedPointerNoDefaults verifies defaults, normalizers and
// children do not panic behind a nil embedded pointer and that no
// default is applied there.
func TestNilEmbeddedPointerNoDefaults(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[embedOuter](func(object *validate.StructBuilder[embedOuter]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Default("anon")
			value.Trim()
			value.ToUpper()
			value.Min(2)
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
				value.Min(1)
			})
			items.Sort(strings.Compare)
			items.Dedupe()
			items.Min(1)
		})
	})

	parsed, err := schema.Parse(context.Background(), embedOuter{})

	if err != nil || parsed.EmbedInner != nil {
		t.Fatalf("Parse = %+v, %v", parsed, err)
	}

	requireCodes(t, schema.Validate(context.Background(), embedOuter{}), nil)
}

// TestNilEmbeddedPointerConditions verifies conditions read promoted
// fields behind a nil pointer and RequiredIf fires.
func TestNilEmbeddedPointerConditions(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[embedOuter](func(object *validate.StructBuilder[embedOuter]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("X", 1)
			})
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
			})
			items.ProhibitedIf(func(condition *validate.ConditionBuilder) {
				condition.Present("X")
			})
		})
		object.Field("X", func(number *validate.IntBuilder[int]) {
		})
	})

	requireCodes(t, schema.Validate(context.Background(), embedOuter{
		X: 1,
	}),
		map[string][]string{"/Name": {"required_if"}})
	requireCodes(t, schema.Validate(context.Background(), embedOuter{}), nil)
}

// TestNilEmbeddedPointerNested verifies the pointer of a nested object.
func TestNilEmbeddedPointerNested(t *testing.T) {
	t.Parallel()

	inner := validate.Struct[embedOuter](func(object *validate.StructBuilder[embedOuter]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})
	schema := validate.Struct[nestedEmbed](func(object *validate.StructBuilder[nestedEmbed]) {
		object.Field("Inner", func(object *validate.StructBuilder[embedOuter]) {
			object.Use(inner)
		})
		object.Field("Opt", func(pointer *validate.PtrBuilder[embedOuter]) {
			pointer.Value(func(object *validate.StructBuilder[embedOuter]) {
				object.Use(inner)
			})
		})
	})

	requireCodes(t, schema.Validate(context.Background(), nestedEmbed{}),
		map[string][]string{"/inner/Name": {"required"}})
	requireCodes(t, schema.Validate(context.Background(), nestedEmbed{
		Opt: &embedOuter{},
	}),
		map[string][]string{"/inner/Name": {"required"}, "/Opt/Name": {"required"}})
}

// TestNormalizeNestedAndInput checks nested normalization on detached results
// and unchanged caller storage.
//
// Example:
//
//	go test -run "^TestNormalizeNestedAndInput$" .
func TestNormalizeNestedAndInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Struct[normForm](func(object *validate.StructBuilder[normForm]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.ToLower()
			value.Required()
		})
		object.Field("Ptr", func(pointer *validate.PtrBuilder[string]) {
			pointer.Value(func(value *validate.StringBuilder[string]) {
				value.ToUpper()
			})
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
				value.ToLower()
			})
			items.Dedupe()
			items.RejectZero()
		})
		object.Field("Nums", func(items *validate.SliceBuilder[int]) {
			items.Values(func(number *validate.IntBuilder[int]) {
				number.Abs()
			})
			items.Sort(cmp.Compare[int])
		})
	})

	pointer := "p"
	input := normForm{
		Name: "ANN",
		Ptr:  &pointer,
		Tags: []string{"A", "a", "", "B"},
		Nums: []int{-3, 2},
	}

	got, err := schema.Parse(ctx, input)

	if err != nil {
		t.Fatal(err)
	}

	if got.Name != "ann" || *got.Ptr != "P" || !slices.Equal(got.Tags, []string{"a", "b"}) {
		t.Fatalf("got %+v", got)
	}

	// Elements are normalized first, then the slice sorts.
	if !slices.Equal(got.Nums, []int{2, 3}) {
		t.Fatalf("Nums = %v", got.Nums)
	}

	if input.Name != "ANN" || *input.Ptr != "p" || !slices.Equal(input.Tags, []string{"A", "a", "", "B"}) ||
		!slices.Equal(input.Nums, []int{-3, 2}) {
		t.Fatalf("input mutated: %+v", input)
	}

	if err := schema.Validate(ctx, input); err != nil {
		t.Fatal(err)
	}

	if input.Name != "ANN" || input.Tags[0] != "A" {
		t.Fatalf("input mutated by Validate: %+v", input)
	}
}

// TestPreparedStructChildNormalizersRunOnce checks both levels of a nested
// struct are prepared once, and a sibling condition sees the prepared child.
func TestPreparedStructChildNormalizersRunOnce(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Score changes during preparation before the sibling condition runs.
		Score int
	}
	type form struct {
		// Label is required unless the sibling sees exactly one normalization.
		Label string
		// Profile supplies a struct child with its own normalization chain.
		Profile profile
	}

	structCalls, childCalls := 0, 0

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Label", func(label *validate.StringBuilder[string]) {
			label.RequiredUnless(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Profile.Score", 2)
			})
		})
		object.Field("Profile", func(details *validate.StructBuilder[profile]) {
			details.Normalize(func(value profile) profile {
				structCalls++

				return value
			})
			details.Field("Score", func(score *validate.IntBuilder[int]) {
				score.Normalize(func(value int) int {
					childCalls++

					return value + 1
				})
			})
		})
	})

	input := form{Profile: profile{Score: 1}}

	value, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if structCalls != 1 || childCalls != 1 {
		t.Fatalf("struct calls=%d child calls=%d", structCalls, childCalls)
	}

	if value.Profile.Score != 2 || input.Profile.Score != 1 {
		t.Fatalf("result=%+v input=%+v", value, input)
	}
}

// TestRedesignFieldCallbacksShareDraft ensures child callbacks accumulate on
// one draft rather than replacing or independently publishing the child.
func TestRedesignFieldCallbacksShareDraft(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}
	var first *validate.StringBuilder[string]

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			first = name
			name.Trim()
		}, func(name *validate.StringBuilder[string]) {
			if name != first {
				t.Fatal("field callbacks received different drafts")
			}

			name.ToUpper()
		})
	})

	got, err := schema.Parse(context.Background(), form{
		Name: " ada ",
	})

	if err != nil {
		t.Fatal(err)
	}

	if got.Name != "ADA" {
		t.Fatalf("Name = %q, want ADA", got.Name)
	}
}

// TestRedesignNestedCallbacksNormalizeCopies demonstrates direct struct,
// slice, map, and pointer children without preconstructing child schemas.
func TestRedesignNestedCallbacksNormalizeCopies(t *testing.T) {
	t.Parallel()

	type address struct {
		// City holds a nested string whose diagnostic path uses the JSON field name.
		City string
	}
	type form struct {
		// Address provides a nested object for traversal and diagnostic-path checks.
		Address *address
		// Tags provides slice storage for child rules and copy-isolation checks.
		Tags []string
		// Scores provides collection values for predicates and derived-count
		// constraints.
		Scores map[string]int
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Address", func(location *validate.PtrBuilder[address]) {
			location.Value(func(details *validate.StructBuilder[address]) {
				details.Field("City", func(city *validate.StringBuilder[string]) {
					city.Trim()
				})
			})
		})
		object.Field("Tags", func(tags *validate.SliceBuilder[string]) {
			tags.Values(func(tag *validate.StringBuilder[string]) {
				tag.Trim()
			})
		})
		object.Field("Scores", func(scores *validate.MapBuilder[string, int]) {
			scores.Keys(func(key *validate.StringBuilder[string]) {
				key.Min(1)
			})
			scores.Values(func(score *validate.IntBuilder[int]) {
				score.Abs()
			})
		})
	})

	input := form{
		Address: &address{
			City: " Paris ",
		},
		Tags:   []string{" go "},
		Scores: map[string]int{" a ": -2},
	}

	got, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	want := form{
		Address: &address{
			City: "Paris",
		},
		Tags:   []string{"go"},
		Scores: map[string]int{" a ": 2},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %#v, want %#v", got, want)
	}

	if input.Address.City != " Paris " || input.Tags[0] != " go " || input.Scores[" a "] != -2 {
		t.Fatalf("Parse mutated caller-owned data: %#v", input)
	}
}

// TestRedesignNestedDefaultPrecedesOuterCondition places the dependent field
// first in Go order, so field-by-field execution cannot accidentally pass.
func TestRedesignNestedDefaultPrecedesOuterCondition(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Role selects the identity or condition branch under test.
		Role string
	}
	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string
		// Profile provides the nested object traversed by dotted field selectors.
		Profile profile
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Profile.Role", "admin")
			})
		})
		object.Field("Profile", func(details *validate.StructBuilder[profile]) {
			details.Field("Role", func(role *validate.StringBuilder[string]) {
				role.Default("admin")
			})
		})
	})

	if err := schema.Validate(context.Background(), form{}); err == nil {
		t.Fatal("outer condition did not observe the nested unconditional default")
	}
}

// TestReuseBuilderPointerStructPreservesFields covers pointer schemas passed to
// a mutable struct draft rather than directly to a validation consumer.
func TestReuseBuilderPointerStructPreservesFields(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	shared := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Required()
		})
	})
	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Use(&shared)
	})

	if err := schema.Validate(context.Background(), form{}); err == nil {
		t.Fatal("pointer reuse dropped field validation")
	}
}

// TestReuseBuilderRejectsStructIntoText prevents a same-T text draft from
// inheriting object-wide rules while silently dropping registered field checks.
func TestReuseBuilderRejectsStructIntoText(t *testing.T) {
	t.Parallel()

	shared := validate.Struct(func(object *validate.StructBuilder[reuseTextValue]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Required()
		})
	})

	requireInvalidReuse(t, func() {
		validate.Text[reuseTextValue](func(text *validate.TextBuilder[reuseTextValue]) {
			text.Use(shared)
		})
	})
}

// TestReuseBuilderTypedNilStructPointerIsDomainError covers the manually written
// struct Use implementation as well as the generated scalar/composite family.
func TestReuseBuilderTypedNilStructPointerIsDomainError(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}
	var shared *validate.StructSchema[form]

	requireInvalidReuse(t, func() {
		validate.Struct(func(object *validate.StructBuilder[form]) {
			object.Use(shared)
		})
	})
}

// TestConditionsSeeDefaultsOfLaterFields verifies defaults are applied
// before any condition runs, whatever the field order.
func TestConditionsSeeDefaultsOfLaterFields(t *testing.T) {
	t.Parallel()

	type form struct {
		// A provides one independently registered value for sibling-rule checks.
		A string
		// Z provides a later-declared default observed by an earlier field.
		Z string
	}

	schema := validate.Struct[form](func(object *validate.StructBuilder[form]) {
		object.Field("A", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Z", "on")
			})
		})
		object.Field("Z", func(value *validate.StringBuilder[string]) {
			value.Default("on")
		})
	})

	requireCodes(t, schema.Validate(context.Background(), form{}), map[string][]string{"/A": {"required_if"}})
}

// address is a nested object fixture with a renamed JSON field. Registrations
// use Go names while diagnostics use JSON names.
type address struct {
	// Street provides an ordinary nested string alongside the renamed City field.
	Street string
	// City holds a nested string whose diagnostic path uses the JSON field name.
	City string `json:"city"`
}

// TestStructCallbackReusableAcrossTypes verifies a shared field policy binds
// independently to each struct while preserving typed parsing and JSON names.
func TestStructCallbackReusableAcrossTypes(t *testing.T) {
	t.Parallel()

	type first struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string `json:"first_name"`
	}
	type second struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string `json:"second_name"`
	}

	namePolicy := func(name *validate.StringBuilder[string]) {
		name.Trim()
		name.Required()
	}
	firstCallbacks := []validate.StructBuilderFunc[first]{func(object *validate.StructBuilder[first]) {
		object.Field("Name", namePolicy)
	}}
	secondCallbacks := []validate.StructBuilderFunc[second]{func(object *validate.StructBuilder[second]) {
		object.Field("Name", namePolicy)
	}}

	var firstSchema validate.Schema[first] = validate.Struct[first](firstCallbacks...)
	var secondSchema validate.Schema[second] = validate.Struct[second](secondCallbacks...)

	requireParse(t, firstSchema, first{
		Name: " Ada ",
	}, first{
		Name: "Ada",
	})
	requireParse(t, secondSchema, second{
		Name: " Grace ",
	}, second{
		Name: "Grace",
	})
	requireCodes(t, firstSchema.Validate(context.Background(), first{}), map[string][]string{
		"/first_name": {"required"},
	})
	requireCodes(t, secondSchema.Validate(context.Background(), second{}), map[string][]string{
		"/second_name": {"required"},
	})

	firstDoc, err := firstSchema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	secondDoc, err := secondSchema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(firstDoc), `"first_name"`) || strings.Contains(string(firstDoc), `"second_name"`) {
		t.Fatalf("first schema used another type's JSON names: %s", firstDoc)
	}

	if !strings.Contains(string(secondDoc), `"second_name"`) || strings.Contains(string(secondDoc), `"first_name"`) {
		t.Fatalf("second schema used another type's JSON names: %s", secondDoc)
	}
}

// TestStructSharedCallbackRejectsIncompatibleType verifies reused field policies
// still report a builder that does not match the target struct field.
func TestStructSharedCallbackRejectsIncompatibleType(t *testing.T) {
	t.Parallel()

	type named struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}
	type numbered struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name int
	}

	namePolicy := func(name *validate.StringBuilder[string]) {
		name.Required()
	}

	requireParse(t, validate.Struct[named](func(object *validate.StructBuilder[named]) {
		object.Field("Name", namePolicy)
	}), named{
		Name: "Ada",
	}, named{
		Name: "Ada",
	})

	message := requirePanic(t, func() {
		validate.Struct[numbered](func(object *validate.StructBuilder[numbered]) {
			object.Field("Name", namePolicy)
		})
	})

	if !strings.Contains(message, "Name: the schema validates string but the field has type int") {
		t.Fatalf("missing field type mismatch: %s", message)
	}
}

// TestStructOmittedCallbacksPreserveEmptyRegistration verifies omission does
// not infer field validators, defaults, or required properties.
func TestStructOmittedCallbacksPreserveEmptyRegistration(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[address]()

	requireParse(t, schema, address{}, address{})

	document, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(document), "Street") {
		t.Fatalf("empty registration inferred properties: %s", document)
	}
}

// TestStructEmptyCallbackMatchesOmission verifies an empty callback publishes
// the same validation and JSON Schema behavior as zero callbacks.
func TestStructEmptyCallbackMatchesOmission(t *testing.T) {
	t.Parallel()

	empty := validate.Struct[address](func(*validate.StructBuilder[address]) {
	})

	requireParse(t, empty, address{}, address{})

	want, err := validate.Struct[address]().JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	got, err := empty.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Fatalf("empty callback = %s, omission = %s", got, want)
	}
}

// TestStructNilAndEmptyCallbackSlicesMatchOmission checks both zero-argument
// expansions. Neither represents a supplied nil callback.
func TestStructNilAndEmptyCallbackSlicesMatchOmission(t *testing.T) {
	t.Parallel()

	var callbacks []validate.StructBuilderFunc[address]

	requireParse(t, validate.Struct[address](callbacks...), address{}, address{})

	callbacks = []validate.StructBuilderFunc[address]{}

	requireParse(t, validate.Struct[address](callbacks...), address{}, address{})
}

// TestStructCallbacksShareOneRegistrationDraft verifies synchronous order,
// exactly-once invocation, and visibility of registrations across callbacks.
func TestStructCallbacksShareOneRegistrationDraft(t *testing.T) {
	t.Parallel()

	var first *validate.StructBuilder[address]
	var calls []int

	schema := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		first = object
		calls = append(calls, 1)
		object.Field("City", func(value *validate.StringBuilder[string]) {
			value.Default("Paris")
		})
	}, func(object *validate.StructBuilder[address]) {
		if object != first {
			t.Fatal("callbacks received different builders")
		}

		calls = append(calls, 2)
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("City", "Paris")
			})
		})
	})

	if !slices.Equal(calls, []int{1, 2}) {
		t.Fatalf("callback order = %v", calls)
	}

	requireCodes(t, schema.Validate(context.Background(), address{}), map[string][]string{
		"/Street": {"required_if"},
	})
	requireParse(t, schema, address{
		Street: "Main",
	}, address{
		Street: "Main",
		City:   "Paris",
	})
}

// TestStructDuplicateFieldsAcrossCallbacks verifies duplicate registrations
// are aggregated, with names sorted and same-name diagnostics in input order.
func TestStructDuplicateFieldsAcrossCallbacks(t *testing.T) {
	t.Parallel()

	message := requirePanic(t, func() {
		validate.Struct[address](func(object *validate.StructBuilder[address]) {
			object.Field("Street", func(number *validate.IntBuilder[int]) {
			})
			object.Field("City", func(value *validate.StringBuilder[string]) {
				value.Default("x")
				value.Min(2)
			})
		}, func(object *validate.StructBuilder[address]) {
			object.Field("Street", func(value *validate.StringBuilder[string]) {
				value.Use(nil)
			})
			object.Field("City", func(number *validate.IntBuilder[int]) {
			})
		})
	})

	want := []string{
		"City: default x fails its own schema",
		"City: duplicate field registration",
		"City: the schema validates int but the field has type string",
		"Street: the schema validates int but the field has type string",
		"Street: duplicate field registration",
		"Street: the schema is nil",
	}

	previous := -1

	for _, diagnostic := range want {
		index := strings.Index(message, diagnostic)

		if index <= previous {
			t.Fatalf("missing or out-of-order %q: %s", diagnostic, message)
		}

		previous = index
	}
}

// TestStructCompilesCompletedRegistrationsOnce verifies field defaults are
// checked only after the last callback, through a single field compilation.
// Standalone sealing must reuse the already compiled field nodes.
func TestStructCompilesCompletedRegistrationsOnce(t *testing.T) {
	t.Parallel()

	checks := 0

	validate.Struct[address](func(object *validate.StructBuilder[address]) {
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.Default("Main")
			value.Check(func(context.Context, string) error {
				checks++

				return nil
			})
		})
	}, func(object *validate.StructBuilder[address]) {
		if checks != 0 {
			t.Fatal("field compiled before callbacks completed")
		}

		object.Field("City", func(value *validate.StringBuilder[string]) {
		})
	})

	if checks != 1 {
		t.Fatalf("default checks = %d, want 1", checks)
	}
}

// TestStructSuppliedNilCallbackPanics verifies nil callbacks are invoked
// directly, stopping later callbacks rather than silently skipping nil.
func TestStructSuppliedNilCallbackPanics(t *testing.T) {
	t.Parallel()

	later := false

	defer func() {
		if recover() == nil {
			t.Error("nil callback did not panic")
		}

		if later {
			t.Error("callback ran after the panic")
		}
	}()

	validate.Struct[address](nil, func(*validate.StructBuilder[address]) {
		later = true
	})
}

// TestStructCallbackPanicPropagates verifies application panics keep their
// exact identity and prevent subsequent callbacks from running.
func TestStructCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	want := "application callback panic"

	later := false

	defer func() {
		if got := recover(); got != want {
			t.Errorf("panic = %v, want %v", got, want)
		}

		if later {
			t.Error("callback ran after the panic")
		}
	}()

	validate.Struct[address](func(*validate.StructBuilder[address]) {
		panic(want)
	},
		func(*validate.StructBuilder[address]) {
			later = true
		})
}

// TestStructFieldReturnsReceiver verifies ignored returns and short chains
// share the same draft, including registrations that later fail compilation.
func TestStructFieldReturnsReceiver(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		if object.Field("Street", func(value *validate.StringBuilder[string]) {
		}).Field("City", func(value *validate.StringBuilder[string]) {
		}) != object {
			t.Fatal("Field did not return its receiver")
		}
	})

	requireParse(t, schema, address{}, address{})
	requirePanic(t, func() {
		validate.Struct[address](func(object *validate.StructBuilder[address]) {
			if object.Field("Missing", func(value *validate.StringBuilder[string]) {
				value.Use(nil)
			}) != object {
				t.Fatal("invalid Field did not return its receiver")
			}
		})
	})
}

// TestStructRetainedBuilderCannotChangeSchema verifies publication detaches
// registrations even if a caller replaces the entire retained builder value.
// Validation and schema rendering must both continue using the original fields.
func TestStructRetainedBuilderCannotChangeSchema(t *testing.T) {
	t.Parallel()

	var retained *validate.StructBuilder[address]

	schema := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		retained = object
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.Trim()
			value.Required()
		})
	})

	want, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	*retained = validate.StructBuilder[address]{}
	retained.Field("City", func(value *validate.StringBuilder[string]) {
		value.Required()
	})
	retained.Field("Street", func(value *validate.StringBuilder[string]) {
		value.Default("Other")
	})

	requireParse(t, schema, address{
		Street: " Main ",
	}, address{
		Street: "Main",
	})
	requireCodes(t, schema.Validate(context.Background(), address{}), map[string][]string{
		"/Street": {"required"},
	})

	got, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Fatalf("retained builder changed JSON Schema: %s != %s", got, want)
	}
}

// TestStructExecutesFieldsInStructOrder verifies registration order does not
// change rule execution, including when fields come from separate callbacks.
func TestStructExecutesFieldsInStructOrder(t *testing.T) {
	t.Parallel()

	var calls []string

	schema := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		object.Field("City", func(value *validate.StringBuilder[string]) {
			value.Check(func(context.Context, string) error {
				calls = append(calls, "City")

				return nil
			})
		})
	}, func(object *validate.StructBuilder[address]) {
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.Check(func(context.Context, string) error {
				calls = append(calls, "Street")

				return nil
			})
		})
	})

	if len(calls) != 0 {
		t.Fatalf("construction executed checks: %v", calls)
	}

	if err := schema.Validate(context.Background(), address{
		Street: "Main",
		City:   "Paris",
	}); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(calls, []string{"Street", "City"}) {
		t.Fatalf("check order = %v", calls)
	}
}

// account combines direct and optional nested values for struct validation.
// Its schema checks that diagnostics keep the correct JSON Pointer path.
type account struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string `json:"name"`
	// Email holds the address checked by email rules and JSON-name discovery.
	Email string
	// Address provides a nested object for traversal and diagnostic-path checks.
	Address address `json:"address"`
	// Nick provides optional text for pointer-presence and nested-path checks.
	Nick *string `json:"nick"`
}

// TestStructNestedPaths verifies nested structs prefix their pointer and
// that JSON names win over Go names.
func TestStructNestedPaths(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[account](func(object *validate.StructBuilder[account]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Email", func(value *validate.StringBuilder[string]) {
			value.Email()
		})
		object.Field("Address", func(object *validate.StructBuilder[address]) {
			object.Field("Street", func(value *validate.StringBuilder[string]) {
				value.Required()
			})
			object.Field("City", func(value *validate.StringBuilder[string]) {
				value.Min(2)
			})
		})
		object.Field("Nick", func(pointer *validate.PtrBuilder[string]) {
			pointer.Value(func(value *validate.StringBuilder[string]) {
				value.Min(3)
			})
		})
	})

	short := "ab"

	err := schema.Validate(context.Background(), account{
		Email: "nope",
		Address: address{
			City: "x",
		},
		Nick: &short,
	})

	requireCodes(t, err, map[string][]string{
		"/name":           {"required"},
		"/Email":          {"string.email"},
		"/address/Street": {"required"},
		"/address/city":   {"string.min"},
		"/nick":           {"string.min"},
	})
}

// TestStructValidPasses verifies a valid value returns a literal nil.
func TestStructValidPasses(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})

	err := schema.Validate(context.Background(), address{
		Street: "Main",
	})

	if err != nil {
		t.Fatalf("err = %#v, want nil", err)
	}
}

// TestStructUnvalidatedFieldsIgnored verifies unregistered fields pass without
// implicit validation, even when their values would fail another field's rules.
func TestStructUnvalidatedFieldsIgnored(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.Min(5)
		})
	})

	if err := schema.Validate(context.Background(), address{
		City: "x",
	}); err != nil {
		t.Fatal(err)
	}
}

// badFields collects intentionally invalid registration targets. Tests
// distinguish unexported, excluded, renamed and wrongly typed fields in one
// construction report.
type badFields struct {
	// Public is an ordinary exported field that can be registered.
	Public string
	// private is deliberately unexported so registration must reject it.
	private string
	// Skipped is excluded from JSON and must not become a visible schema
	// property.
	Skipped string `json:"-"`
	// Renamed has a JSON alias while registration still selects its Go name.
	Renamed string `json:"alias"`
	// Typed provides an integer field for exact-schema-type mismatch checks.
	Typed int
	// Defaults provides a target for invalid-default construction diagnostics.
	Defaults string
}

// TestStructConstructionReport verifies one panic lists every problem.
func TestStructConstructionReport(t *testing.T) {
	t.Parallel()

	_ = badFields{}.private

	message := requirePanic(t, func() {
		validate.Struct[badFields](func(object *validate.StructBuilder[badFields]) {
			object.Field("Publc", func(value *validate.StringBuilder[string]) {
			})
			object.Field("private", func(value *validate.StringBuilder[string]) {
			})
			object.Field("Skipped", func(value *validate.StringBuilder[string]) {
			})
			object.Field("alias", func(value *validate.StringBuilder[string]) {
			})
			object.Field("Typed", func(value *validate.StringBuilder[string]) {
			})
			object.Field("Public", func(value *validate.StringBuilder[string]) {
				value.Use(nil)
			})
			object.Field("Defaults", func(value *validate.StringBuilder[string]) {
				value.Min(5)
				value.Default("ab")
			})
			object.Field("Missing", func(value *validate.StringBuilder[string]) {
			})
		})
	})

	wants := []string{
		`Publc: unknown field "Publc" (did you mean "Public"?)`,
		`private: field "private" cannot be validated: it is unexported`,
		`Skipped: field "Skipped" cannot be validated: it is excluded with json:"-"`,
		`alias: no field named "alias"; use the Go field name "Renamed", not its JSON name`,
		`Typed: the schema validates string but the field has type int`,
		`Public: the schema is nil`,
		`Defaults: default ab fails its own schema`,
		`Missing: unknown field "Missing"`,
	}

	for _, want := range wants {
		if !strings.Contains(message, want) {
			t.Errorf("report misses %q:\n%s", want, message)
		}
	}
}

// TestStructRejectsNonStruct verifies Struct needs a struct type.
func TestStructRejectsNonStruct(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.Struct[string]()
	})
}

// TestStructNilSchemaRegistration verifies a typed nil schema is reported
// through the aggregate domain diagnostic, not a callback invocation panic.
func TestStructNilSchemaRegistration(t *testing.T) {
	t.Parallel()

	message := requirePanic(t, func() {
		validate.Struct[address](func(object *validate.StructBuilder[address]) {
			object.Field("Street", func(value *validate.StringBuilder[string]) {
				value.Use(nil)
			})
		})
	})

	if !strings.Contains(message, "Street: the schema is nil") {
		t.Fatalf("report = %s", message)
	}
}

// tagged contains JSON tag options that cannot be registered as ordinary
// fields. Construction reports the unsupported inline and unknown roles.
type tagged struct {
	// Inline uses the unsupported inline JSON field role.
	Inline string `json:",inline"`
	// Unknown uses the unsupported unknown-member JSON field role.
	Unknown string `json:",unknown"`
}

// TestStructRejectsInlineAndUnknown verifies inline and unknown fields.
func TestStructRejectsInlineAndUnknown(t *testing.T) {
	t.Parallel()

	message := requirePanic(t, func() {
		validate.Struct[tagged](func(object *validate.StructBuilder[tagged]) {
			object.Field("Inline", func(value *validate.StringBuilder[string]) {
			})
			object.Field("Unknown", func(value *validate.StringBuilder[string]) {
			})
		})
	})

	if !strings.Contains(message, "inline") || !strings.Contains(message, "unknown option") {
		t.Fatalf("report = %s", message)
	}
}

// Base supplies untagged embedded fields for promotion tests. Descendants can
// hide ID with a shallower declaration.
//
// Example:
//
//	value := Base{
//		ID: 7,
//	}
//	_ = value.ID
type Base struct {
	// ID provides an identifier for JSON naming and embedding checks.
	// Descendants can hide it with a direct field of a different type.
	//
	// Example:
	//
	//	value := Base{}
	//	value.ID = 7
	ID int `json:"id"`
	// Kind provides a second promoted field independent of ID hiding.
	// Its default JSON name remains the Go field name.
	//
	// Example:
	//
	//	value := Base{}
	//	value.Kind = "person"
	Kind string
}

// Tagged supplies an embedded object with an explicit JSON name. Unlike an
// untagged embedding, its fields remain nested.
//
// Example:
//
//	value := Tagged{
//		Label: "shipping",
//	}
//	_ = value.Label
type Tagged struct {
	// Label provides the child value of an explicitly named embedded object.
	// The embedding's JSON tag keeps this value beneath its parent property.
	//
	// Example:
	//
	//	value := Tagged{}
	//	value.Label = "shipping"
	Label string
}

// embedding combines promoted and explicitly named embedded objects. Field
// selection must follow the JSON naming rules for each role.
type embedding struct {
	// Base promotes base fields unless a shallower field hides them.
	Base
	// Tagged keeps its fields nested under an explicit JSON property name.
	Tagged `json:"tagged"`
	// Name holds the name observed by string rules or sibling conditions.
	Name string
}

// TestStructEmbeddedPromotion verifies untagged embedded fields are
// promoted and tagged embedded structs nest.
func TestStructEmbeddedPromotion(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[embedding](func(object *validate.StructBuilder[embedding]) {
		object.Field("ID", func(number *validate.IntBuilder[int]) {
			number.Required()
		})
		object.Field("Kind", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Tagged", func(object *validate.StructBuilder[Tagged]) {
			object.Field("Label", func(value *validate.StringBuilder[string]) {
				value.Required()
			})
		})
	})

	err := schema.Validate(context.Background(), embedding{})

	requireCodes(t, err, map[string][]string{
		"/id":           {"required"},
		"/Kind":         {"required"},
		"/Name":         {"required"},
		"/tagged/Label": {"required"},
	})

	requirePanic(t, func() {
		validate.Struct[embedding](func(object *validate.StructBuilder[embedding]) {
			object.Field("Label", func(value *validate.StringBuilder[string]) {
			})
		})
	})
}

// left supplies one side of an ambiguous promoted field. A same-depth field
// from right must prevent unqualified selection.
type left struct {
	// Value provides the selected or conflicting value in field-resolution checks.
	Value string
}

// right supplies the other side of an ambiguous promoted field. Tests pair it
// with left to exercise conflict detection.
type right struct {
	// Value provides the selected or conflicting value in field-resolution checks.
	Value string
}

// conflict embeds two same-depth declarations of Value. Other remains
// available even when the promoted name is ambiguous.
type conflict struct {
	// left promotes one candidate for the conflicting Value selector.
	left
	// right promotes a second candidate for the conflicting Value selector.
	right
	// Other provides an independent sibling for comparison or registration
	// checks.
	Other string
}

// TestStructEmbeddedConflict verifies same-depth name conflicts drop both
// fields.
func TestStructEmbeddedConflict(t *testing.T) {
	t.Parallel()

	message := requirePanic(t, func() {
		validate.Struct[conflict](func(object *validate.StructBuilder[conflict]) {
			object.Field("Value", func(value *validate.StringBuilder[string]) {
			})
		})
	})

	if !strings.Contains(message, "conflicts") {
		t.Fatalf("report = %s", message)
	}
}

// outerWins hides a promoted ID with a direct declaration. JSON field
// discovery must prefer the shallower field.
type outerWins struct {
	// Base promotes base fields unless a shallower field hides them.
	Base
	// ID provides an identifier for JSON naming and embedding checks.
	ID string `json:"id"`
}

// TestStructShallowFieldDominates verifies the shallowest field wins.
func TestStructShallowFieldDominates(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[outerWins](func(object *validate.StructBuilder[outerWins]) {
		object.Field("ID", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})

	requireCodes(t, schema.Validate(context.Background(), outerWins{}), map[string][]string{"/id": {"required"}})
}

// taggedWins pairs an embedded Value with a directly tagged Value. Tests
// verify the selected field and unaffected sibling metadata.
type taggedWins struct {
	// left promotes one candidate for the conflicting Value selector.
	left
	// Value provides the selected or conflicting value in field-resolution
	// checks.
	Value string `json:"Value"`
	// Extra provides an additional value outside the primary child constraints.
	Extra string `json:"x"`
}

// TestStructTaggedFieldWinsConflict verifies an explicit tag beats an
// embedded field of the same depth rules.
func TestStructTaggedFieldWinsConflict(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[taggedWins](func(object *validate.StructBuilder[taggedWins]) {
		object.Field("Value", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})

	requireCodes(t, schema.Validate(context.Background(), taggedWins{}), map[string][]string{"/Value": {"required"}})
}

// stamp has custom JSON encoding despite containing a field. Embedding it must
// not promote its internal fields into validation paths.
type stamp struct {
	// Seconds is intentionally hidden behind the enclosing custom JSON marshaler.
	Seconds int
}

// MarshalJSON makes stamp a leaf for naming purposes.
func (stamp) MarshalJSON() ([]byte, error) {
	return []byte("1"), nil
}

// withMarshaler embeds a custom JSON marshaler alongside an ordinary name.
// Discovery treats the embedded marshaler as a leaf.
type withMarshaler struct {
	// stamp provides a custom JSON encoding that must remain a leaf.
	stamp
	// Name holds the name observed by string rules or sibling conditions.
	Name string
}

// TestStructMarshalerEmbeddedIsLeaf verifies embedded Marshaler types are
// not promoted.
func TestStructMarshalerEmbeddedIsLeaf(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.Struct[withMarshaler](func(object *validate.StructBuilder[withMarshaler]) {
			object.Field("Seconds", func(number *validate.IntBuilder[int]) {
			})
		})
	})

	schema := validate.Struct[withMarshaler](func(object *validate.StructBuilder[withMarshaler]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})

	requireCodes(t, schema.Validate(context.Background(), withMarshaler{}), map[string][]string{"/Name": {"required"}})
}

// pointerEmbed places promoted Base fields behind a pointer. A nil embedding
// must be checked without allocating a parent.
type pointerEmbed struct {
	// Base promotes base fields unless a shallower field hides them.
	*Base
	// Name holds the name observed by string rules or sibling conditions.
	Name string
}

// TestStructNilEmbeddedPointer verifies a nil embedded pointer checks its
// promoted fields as zero values instead of panicking or skipping them.
func TestStructNilEmbeddedPointer(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[pointerEmbed](func(object *validate.StructBuilder[pointerEmbed]) {
		object.Field("ID", func(number *validate.IntBuilder[int]) {
			number.Required()
		})
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})

	requireCodes(t, schema.Validate(context.Background(), pointerEmbed{}),
		map[string][]string{"/id": {"required"}, "/Name": {"required"}})

	requireCodes(t, schema.Validate(context.Background(), pointerEmbed{
		Base: &Base{},
		Name: "n",
	}),
		map[string][]string{"/id": {"required"}})
}

// escaped uses slash and tilde characters in JSON names. Diagnostics must
// escape both characters according to JSON Pointer rules.
type escaped struct {
	// Slash uses a slash-containing JSON name to test pointer escaping.
	Slash string `json:"a/b"`
	// Tilde uses a tilde-containing JSON name to test pointer escaping.
	Tilde string `json:"c~d"`
}

// TestStructPointerEscaping verifies RFC 6901 escaping of names.
func TestStructPointerEscaping(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[escaped](func(object *validate.StructBuilder[escaped]) {
		object.Field("Slash", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Tilde", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})

	requireCodes(t, schema.Validate(context.Background(), escaped{}), map[string][]string{
		"/a~1b": {"required"},
		"/c~0d": {"required"},
	})
}

// optionalNested contains an optional nested address. A missing pointer skips
// the pointee rules rather than allocating an object.
type optionalNested struct {
	// Home provides the optional nested address whose absent pointer skips child
	// rules.
	Home *address
}

// TestStructOptionalNestedPointer verifies Ptr(Struct) skips nil and
// checks present objects.
func TestStructOptionalNestedPointer(t *testing.T) {
	t.Parallel()

	inner := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})
	schema := validate.Struct[optionalNested](func(object *validate.StructBuilder[optionalNested]) {
		object.Field("Home", func(pointer *validate.PtrBuilder[address]) {
			pointer.Value(func(object *validate.StructBuilder[address]) {
				object.Use(inner)
			})
		})
	})

	if err := schema.Validate(context.Background(), optionalNested{}); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, schema.Validate(context.Background(), optionalNested{
		Home: &address{},
	}),
		map[string][]string{"/Home/Street": {"required"}})
}

// bundle places the same child object in slices and maps. Nested failures must
// retain their index or map-key path.
type bundle struct {
	// Items provides child values for collection traversal and default checks.
	Items []address
	// Index provides map-address children whose keys extend diagnostic paths.
	Index map[string]address
}

// TestStructNestedSliceAndMap verifies deep paths through slices and maps.
func TestStructNestedSliceAndMap(t *testing.T) {
	t.Parallel()

	inner := validate.Struct[address](func(object *validate.StructBuilder[address]) {
		object.Field("Street", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
	})
	schema := validate.Struct[bundle](func(object *validate.StructBuilder[bundle]) {
		object.Field("Items", func(items *validate.SliceBuilder[address]) {
			items.Values(func(object *validate.StructBuilder[address]) {
				object.Use(inner)
			})
		})
		object.Field("Index", func(entries *validate.MapBuilder[string, address]) {
			entries.Keys(func(value *validate.StringBuilder[string]) {
			})
			entries.Values(func(object *validate.StructBuilder[address]) {
				object.Use(inner)
			})
		})
	})

	err := schema.Validate(context.Background(), bundle{
		Items: []address{{
			Street: "x",
		}, {}},
		Index: map[string]address{"k": {}},
	})

	requireCodes(t, err, map[string][]string{
		"/Items/1/Street": {"required"},
		"/Index/k/Street": {"required"},
	})
}

// customCheck provides sibling values for object-level Check callbacks. Fail
// and At attach diagnostics to the intended object or field.
type customCheck struct {
	// Start provides the lower time endpoint for sibling checks.
	Start time.Time
	// End provides the upper time endpoint for sibling checks.
	End time.Time
	// Name holds the name observed by string rules or sibling conditions.
	Name string
}

// TestStructCheckFailAndAt verifies Fail attaches to the struct and At to
// a sibling field.
func TestStructCheckFailAndAt(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[customCheck](func(object *validate.StructBuilder[customCheck]) {
		object.Check(func(_ context.Context, value customCheck) error {
			if value.Name == "root" {
				return validate.Fail("custom.root", map[string]any{"name": value.Name})
			}

			return validate.At("End", "custom.order", nil)
		})
	})

	requireCodes(t, schema.Validate(context.Background(), customCheck{
		Name: "root",
	}),
		map[string][]string{"": {"custom.root"}})
	requireCodes(t, schema.Validate(context.Background(), customCheck{
		Name: "x",
	}),
		map[string][]string{"/End": {"custom.order"}})
}

// TestStructCheckAtUnknownField verifies At with a bad field aborts with
// ErrInvalidSchema.
func TestStructCheckAtUnknownField(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[customCheck](func(object *validate.StructBuilder[customCheck]) {
		object.Check(func(context.Context, customCheck) error {
			return validate.At("Nope", "x", nil)
		})
	})

	err := schema.Validate(context.Background(), customCheck{
		Name: "x",
	})

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("err = %v", err)
	}
}

// TestCheckInfrastructureErrorPassesThrough verifies a plain error from a
// Check is returned unchanged and is not a validation error.
func TestCheckInfrastructureErrorPassesThrough(t *testing.T) {
	t.Parallel()

	boom := errors.New("database down")

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Check(func(context.Context, string) error {
			return boom
		})
	})

	err := schema.Validate(context.Background(), "x")

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the original error", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("infrastructure error must not be validate.Errors")
	}
}

// TestCheckWrappedFailIsViolation verifies a wrapped Fail still counts.
func TestCheckWrappedFailIsViolation(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Check(func(context.Context, string) error {
			return errors.Join(errors.New("context"), validate.Fail("taken", nil))
		})
	})

	requireCodes(t, schema.Validate(context.Background(), "x"), map[string][]string{"": {"taken"}})
}

// TestCheckSkippedWhenAbsent verifies Check only runs for present values.
func TestCheckSkippedWhenAbsent(t *testing.T) {
	t.Parallel()

	called := false

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Check(func(context.Context, string) error {
			called = true

			return nil
		})
	})

	if err := schema.Validate(context.Background(), ""); err != nil || called {
		t.Fatalf("called = %v, err = %v", called, err)
	}
}

// TestFailCopiesParams verifies Fail deep-copies its params.
func TestFailCopiesParams(t *testing.T) {
	t.Parallel()

	params := map[string]any{"nested": map[string]any{"a": 1}, "list": []any{1, 2}}
	failure := validate.Fail("x", params)

	params["nested"].(map[string]any)["a"] = 2
	params["list"].([]any)[0] = 9

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Check(func(context.Context, string) error {
			return failure
		})
	})

	err := schema.Validate(context.Background(), "v")

	errs, _ := errors.AsType[validate.Errors](err)

	got := errs[""][0].Params

	if got["nested"].(map[string]any)["a"] != 1 || got["list"].([]any)[0] != 1 {
		t.Fatalf("params were not copied: %#v", got)
	}
}
