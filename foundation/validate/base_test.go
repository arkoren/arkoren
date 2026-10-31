package validate_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestTypedParseCanceledBeforePreparation checks the outer execution boundary:
// a canceled request must not run normalization or checks, even for valid data.
func TestTypedParseCanceledBeforePreparation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cancel()
	calls := 0

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Normalize(func(value string) string {
			calls++

			return value
		})
		text.Check(func(context.Context, string) error {
			calls++

			return nil
		})
	})

	value, err := schema.Parse(ctx, "Ada")

	if !errors.Is(err, context.Canceled) || value != "" || calls != 0 {
		t.Fatalf("Parse = %q, %v; callbacks = %d", value, err, calls)
	}
}

// TestTypedParseCancellationStopsNormalizerChain checks cancellation between
// arbitrary normalizers. A callback may cancel its request; subsequent
// normalizers and rule callbacks must not run on its discarded result.
func TestTypedParseCancellationStopsNormalizerChain(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Normalize(func(value string) string {
			cancel()

			return value
		})
		text.Normalize(func(value string) string {
			calls++

			return value
		})
	})

	_, err := schema.Parse(ctx, "Ada")

	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("Parse error = %v; later normalizers = %d", err, calls)
	}
}

// TestTypedParseCancellationStopsCollectionPreparation checks propagation
// through composite pre-hooks. Canceling the first element's normalizer must
// stop before preparing another element and leave caller-owned input unchanged.
func TestTypedParseCancellationStopsCollectionPreparation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0

	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Normalize(func(value int) int {
				calls++
				cancel()

				return value + 1
			})
		})
	})

	input := []int{1, 2}

	value, err := schema.Parse(ctx, input)

	if !errors.Is(err, context.Canceled) || value != nil || calls != 1 {
		t.Fatalf("Parse = %v, %v; preparations = %d", value, err, calls)
	}

	if !reflect.DeepEqual(input, []int{1, 2}) {
		t.Fatalf("canceled preparation changed input: %v", input)
	}
}

// TestTypedValidateCancellationStopsLaterChecks checks cancellation even when
// the canceling Check returns nil rather than explicitly returning ctx.Err().
func TestTypedValidateCancellationStopsLaterChecks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			cancel()

			return nil
		})
		text.Check(func(context.Context, string) error {
			calls++

			return nil
		})
	})

	err := schema.Validate(ctx, "Ada")

	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("Validate = %v; later checks = %d", err, calls)
	}
}

// TestDefaultValidationDoesNotNormalizeConfiguredScalar checks construction
// against the value Parse actually inserts. Normalizers run before absence is
// resolved, not again on the configured scalar default.
func TestDefaultValidationDoesNotNormalizeConfiguredScalar(t *testing.T) {
	t.Parallel()

	// profile supplies the enclosing schema that verifies registered defaults.
	type profile struct {
		// Name has a default that becomes valid only if incorrectly normalized.
		Name string
	}

	requirePanic(t, func() {
		validate.Struct(func(object *validate.StructBuilder[profile]) {
			object.Field("Name", func(text *validate.StringBuilder[string]) {
				text.ToUpper().OneOf("ADA").Default("ada")
			})
		})
	})
}

// TestDefaultExportKeepsConfiguredScalar checks that export is descriptive:
// it retains the raw default, does not execute the normalizer, and does not
// silently change Parse's established normalize-before-default ordering.
func TestDefaultExportKeepsConfiguredScalar(t *testing.T) {
	t.Parallel()

	calls := 0

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Normalize(func(value string) string {
			calls++

			return value + "!"
		}).Default("ada")
	})

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if calls != 0 || string(encoded) != `{"default":"ada","type":"string"}` {
		t.Fatalf("export = %s, normalizer calls = %d", encoded, calls)
	}

	value, err := schema.Parse(context.Background(), "")

	if err != nil || value != "!" || calls != 1 {
		t.Fatalf("Parse = %q, %v, normalizer calls = %d", value, err, calls)
	}
}

// normName is a named string used to exercise normalization without
// conversion. Parse must return this type rather than its underlying string
// type.
type normName string

// normMeters is a named integer used to exercise numeric normalization. Parse
// must retain the named result type.
type normMeters int

// normRatio is a named floating-point value used for rounding checks. Its
// parsed value keeps the named type after normalization.
type normRatio float64

// normForm groups nested mutable values and sibling conditions for
// normalization tests. Parsing must normalize copies without changing caller-
// owned storage.
type normForm struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string
	// Other provides an independent sibling for comparison or registration
	// checks.
	Other string
	// Tags provides slice storage for child rules and copy-isolation checks.
	Tags []string
	// Ptr distinguishes an absent pointer from a present zero pointee.
	Ptr *string
	// Nums provides numeric elements for nested normalization checks.
	Nums []int
}

// mustPanicInvalid runs a schema construction expected to panic with
// ErrInvalidSchema. It fails the calling test for missing panics or unrelated
// recovered values.
func mustPanicInvalid(t *testing.T, fn func()) {
	t.Helper()

	defer func() {
		r := recover()

		err, _ := r.(error)

		if !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("recover = %v, want ErrInvalidSchema", r)
		}
	}()

	fn()
}

// TestNormalizeDefaultPresence checks that normalized absence selects defaults
// or Required without normalizing the chosen default again.
//
// Example:
//
//	go test -run "^TestNormalizeDefaultPresence$" .
func TestNormalizeDefaultPresence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	got, err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Default("d")
		value.ToUpper()
	}).Parse(ctx, "")

	if err != nil || got != "d" {
		t.Fatalf("Default = %q, %v", got, err)
	}

	got, err = validate.String(func(value *validate.StringBuilder[string]) {
		value.Default("d")
		value.TrimPrefix("x")
	}).Parse(ctx, "x")

	if err != nil || got != "d" {
		t.Fatalf("emptied value takes default = %q, %v", got, err)
	}

	err = validate.String(func(value *validate.StringBuilder[string]) {
		value.Required()
		value.TrimSuffix("!")
	}).Validate(ctx, "!")

	requireCodes(t, err, map[string][]string{"": {"required"}})
}

// TestNormalizeConditions checks that sibling comparisons and presence
// observations see normalized values.
//
// Example:
//
//	go test -run "^TestNormalizeConditions$" .
func TestNormalizeConditions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Struct[normForm](func(object *validate.StructBuilder[normForm]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.ToLower()
		})
		object.Field("Other", func(value *validate.StringBuilder[string]) {
			value.EqField("Name")
		})
	})

	if err := schema.Validate(ctx, normForm{
		Name:  "ABC",
		Other: "abc",
	}); err != nil {
		t.Fatalf("EqField should see normalized value: %v", err)
	}

	cond := validate.Struct[normForm](func(object *validate.StructBuilder[normForm]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.TrimPrefix("x")
		})
		object.Field("Other", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Present("Name")
			})
		})
	})

	if err := cond.Validate(ctx, normForm{
		Name: "x",
	}); err != nil {
		t.Fatalf("emptied sibling should be absent: %v", err)
	}
}

// TestRedesignWhenCombinesTypedRuleCallbacks ensures conditional restrictions
// share the selected value's type and all apply when the observation holds.
func TestRedesignWhenCombinesTypedRuleCallbacks(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name, Mode string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.When(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Mode", "active")
			}, func(selected *validate.StringBuilder[string]) {
				selected.Min(3)
			}, func(selected *validate.StringBuilder[string]) {
				selected.Max(5)
			})
		})
	})

	if err := schema.Validate(context.Background(), form{
		Name: "Al",
	}); err != nil {
		t.Fatal(err)
	}

	if err := schema.Validate(context.Background(), form{
		Name: "Al",
		Mode: "active",
	}); err == nil {
		t.Fatal("first conditional callback was not applied")
	}

	if err := schema.Validate(context.Background(), form{
		Name: "Alexandra",
		Mode: "active",
	}); err == nil {
		t.Fatal("second conditional callback was not applied")
	}
}

// TestRedesignCheckPropagatesOperationalError ensures an unavailable external
// check remains an operational error rather than becoming a failed predicate.
func TestRedesignCheckPropagatesOperationalError(t *testing.T) {
	t.Parallel()

	want := errors.New("directory unavailable")

	schema := validate.String(func(name *validate.StringBuilder[string]) {
		name.Check(func(context.Context, string) error {
			return want
		})
	})

	if err := schema.Validate(context.Background(), "Ada"); !errors.Is(err, want) {
		t.Fatalf("Validate = %v, want operational error %v", err, want)
	}
}

// entry is a collection child with a defaultable mode. Settings tests reuse it
// in both slices and maps.
type entry struct {
	// Mode holds the mode used by default and condition checks.
	Mode string
}

// settings is the shared input for default, presence and copy-isolation
// checks. Pointer and collection fields expose differences from scalar zero
// values.
type settings struct {
	// Entries provides named child objects for map-default checks.
	Entries map[string]entry
	// Items provides child values for collection traversal and default checks.
	Items []entry
	// Name holds the name observed by string rules or sibling conditions.
	Name string
	// Role selects the identity or condition branch under test.
	Role string
	// Retries holds a numeric setting used by default and constraint checks.
	Retries int
	// Tags provides slice storage for child rules and copy-isolation checks.
	Tags []string
	// Labels provides map storage for child rules and copy-isolation checks.
	Labels map[string]string
	// Limit distinguishes an absent numeric pointer from a present zero value.
	Limit *int
	// Admin distinguishes an absent boolean pointer from an explicitly supplied
	// false.
	Admin *bool
}

// settingsSchema constructs the shared defaults-and-presence fixture from
// fresh drafts. Each call returns an independent immutable schema for the
// settings scenarios.
func settingsSchema() validate.StructSchema[settings] {
	entrySchema := validate.Struct[entry](func(object *validate.StructBuilder[entry]) {
		object.Field("Mode", func(value *validate.StringBuilder[string]) {
			value.Default("x")
		})
	})

	return validate.Struct[settings](func(object *validate.StructBuilder[settings]) {
		object.Field("Entries", func(entries *validate.MapBuilder[string, entry]) {
			entries.Keys(func(value *validate.StringBuilder[string]) {
			})
			entries.Values(func(object *validate.StructBuilder[entry]) {
				object.Use(entrySchema)
			})
		})
		object.Field("Items", func(items *validate.SliceBuilder[entry]) {
			items.Values(func(object *validate.StructBuilder[entry]) {
				object.Use(entrySchema)
			})
		})
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
			value.Min(3)
		})
		object.Field("Role", func(value *validate.StringBuilder[string]) {
			value.Default("member")
			value.OneOf("member", "admin")
		})
		object.Field("Retries", func(number *validate.IntBuilder[int]) {
			number.Default(3)
			number.Max(10)
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
				value.Min(2)
			})
			items.Default([]string{"new"})
		})
		object.Field("Labels", func(entries *validate.MapBuilder[string, string]) {
			entries.Keys(func(value *validate.StringBuilder[string]) {
			})
			entries.Values(func(value *validate.StringBuilder[string]) {
			})
			entries.Default(map[string]string{"a": "b"})
		})
		object.Field("Limit", func(pointer *validate.PtrBuilder[int]) {
			pointer.Value(func(number *validate.IntBuilder[int]) {
				number.Min(1)
			})
			pointer.Default(5)
		})
		object.Field("Admin", func(pointer *validate.PtrBuilder[bool]) {
			pointer.Value(func(value *validate.BoolBuilder[bool]) {
			})
		})
	})
}

// TestParseAppliesDefaults verifies defaults fill absent fields.
func TestParseAppliesDefaults(t *testing.T) {
	t.Parallel()

	got, err := settingsSchema().Parse(context.Background(), settings{
		Name: "alice",
	})

	if err != nil {
		t.Fatal(err)
	}

	if got.Role != "member" || got.Retries != 3 || !reflect.DeepEqual(got.Tags, []string{"new"}) ||
		got.Labels["a"] != "b" || got.Limit == nil || *got.Limit != 5 {
		t.Fatalf("defaults not applied: %+v", got)
	}
}

// TestParseKeepsPresentValues verifies defaults never overwrite input.
func TestParseKeepsPresentValues(t *testing.T) {
	t.Parallel()

	limit := 9
	input := settings{
		Name:    "alice",
		Role:    "admin",
		Retries: 7,
		Tags:    []string{"xx"},
		Limit:   &limit,
	}

	got, err := settingsSchema().Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if got.Role != "admin" || got.Retries != 7 || got.Tags[0] != "xx" || *got.Limit != 9 {
		t.Fatalf("input overwritten: %+v", got)
	}
}

// TestParseDoesNotMutateInput verifies slices, maps and pointers of the
// input stay untouched while defaults apply to the copy.
func TestParseDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	entries := map[string]entry{"k": {}}
	items := []entry{{}}
	input := settings{
		Name:    "alice",
		Entries: entries,
		Items:   items,
	}

	got, err := settingsSchema().Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if entries["k"].Mode != "" || got.Entries["k"].Mode != "x" {
		t.Fatalf("map input mutated: input %v, output %v", entries, got.Entries)
	}

	if items[0].Mode != "" || got.Items[0].Mode != "x" {
		t.Fatalf("slice input mutated: input %v, output %v", items, got.Items)
	}

	if input.Role != "" || input.Retries != 0 || input.Limit != nil {
		t.Fatalf("input struct mutated: %+v", input)
	}
}

// TestParseDefaultsNotShared verifies a default map is a fresh copy per
// call, so callers can modify results.
func TestParseDefaultsNotShared(t *testing.T) {
	t.Parallel()

	schema := settingsSchema()

	first, _ := schema.Parse(context.Background(), settings{
		Name: "alice",
	})

	first.Labels["a"] = "changed"
	*first.Limit = 99

	second, _ := schema.Parse(context.Background(), settings{
		Name: "alice",
	})

	if second.Labels["a"] != "b" || *second.Limit != 5 {
		t.Fatalf("default shared between calls: %+v", second)
	}
}

// TestValidateAndParseAgree verifies both report the same outcome.
func TestValidateAndParseAgree(t *testing.T) {
	t.Parallel()

	schema := settingsSchema()
	inputs := []settings{
		{},
		{
			Name: "alice",
		},
		{
			Name: "al",
		},
		{
			Name:    "alice",
			Retries: 11,
		},
		{
			Name: "alice",
			Role: "root",
		},
		{
			Name: "alice",
			Tags: []string{"x"},
		},
	}

	for _, input := range inputs {
		validateErr := schema.Validate(context.Background(), input)
		_, parseErr := schema.Parse(context.Background(), input)

		if !reflect.DeepEqual(validateErr, parseErr) {
			t.Fatalf("input %+v: Validate = %v, Parse = %v", input, validateErr, parseErr)
		}
	}
}

// TestValidateTreatsDefaultedFieldAsPresent verifies Required passes for
// an absent field with a default when conditions are involved.
func TestValidateTreatsDefaultedFieldAsPresent(t *testing.T) {
	t.Parallel()

	type form struct {
		// Mode holds the mode used by default and condition checks.
		Mode string
		// Token provides the conditionally required value beside Mode.
		Token string
	}

	schema := validate.Struct[form](func(object *validate.StructBuilder[form]) {
		object.Field("Mode", func(value *validate.StringBuilder[string]) {
			value.Default("secure")
		})
		object.Field("Token", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Mode", "secure")
			})
		})
	})

	requireCodes(t, schema.Validate(context.Background(), form{}), map[string][]string{"/Token": {"required_if"}})
	requireCodes(t, schema.Validate(context.Background(), form{
		Mode: "open",
	}), nil)
}

// TestAllFailuresReported verifies every failing rule of a field is
// reported in order.
func TestAllFailuresReported(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(5)
		value.Email()
		value.Lowercase()
	})

	requireCodes(t, schema.Validate(context.Background(), "A@b"),
		map[string][]string{"": {"string.min", "string.lowercase"}})
	requireCodes(t, schema.Validate(context.Background(), "ab"),
		map[string][]string{"": {"string.min", "string.email"}})
}

// TestBailStopsAtFirstFailure verifies Bail limits a field to one failure.
func TestBailStopsAtFirstFailure(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Bail()
		value.Min(5)
		value.Email()
	})

	requireCodes(t, schema.Validate(context.Background(), "ab"), map[string][]string{"": {"string.min"}})
}

// TestRulesRunInWrittenOrder verifies ordering of violations.
func TestRulesRunInWrittenOrder(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
		value.Min(5)
	})

	requireCodes(t, schema.Validate(context.Background(), "ab"),
		map[string][]string{"": {"string.email", "string.min"}})
}

// TestRequiredWithDefaultPanics verifies the conflict is rejected in both
// orders, and a default that fails its schema is reported by Struct.
func TestRequiredWithDefaultPanics(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.Int(func(number *validate.IntBuilder[int]) {
			number.Required()
			number.Default(1)
		})
	})
	requirePanic(t, func() {
		validate.Int(func(number *validate.IntBuilder[int]) {
			number.Default(1)
			number.Required()
		})
	})

	type holder struct {
		// N provides a numeric value for default and condition checks.
		N int
	}

	message := requirePanic(t, func() {
		validate.Struct[holder](func(object *validate.StructBuilder[holder]) {
			object.Field("N", func(number *validate.IntBuilder[int]) {
				number.Default(1)
				number.Min(5)
			})
		})
	})

	if !strings.Contains(message, "default 1 fails its own schema") {
		t.Fatalf("report = %s", message)
	}
}

// order provides sibling values for conditional and comparison rules. Its
// fields cover contact presence, identity-dependent rules and ordered bounds.
type order struct {
	// Kind selects the identity-dependent validation branch.
	Kind string
	// Country holds a sibling-dependent required value.
	Country string
	// Email holds the address checked by email rules and JSON-name discovery.
	Email string
	// Phone provides the alternate contact for required-without checks.
	Phone string
	// VAT holds the company-only value governed by conditional presence rules.
	VAT string
	// Password provides the source value for password agreement checks.
	Password string
	// Confirm holds the confirmation compared with Password.
	Confirm string
	// Min provides the lower numeric comparison bound.
	Min int
	// Max provides the upper numeric comparison bound.
	Max int
	// From provides the earlier endpoint for time comparisons.
	From time.Time
	// To provides the later endpoint for time comparisons.
	To time.Time
	// Notes provides optional or conditionally constrained text.
	Notes string
	// Count provides a numeric value for conditional or inferred-field checks.
	Count int64
}

// orderSchema constructs sibling-dependent rules for the shared order fixture.
// The returned schema combines identity, contact and bound comparisons without
// retaining mutable drafts.
func orderSchema() validate.StructSchema[order] {
	return validate.Struct[order](func(object *validate.StructBuilder[order]) {
		object.Field("Kind", func(value *validate.StringBuilder[string]) {
			value.OneOf("person", "company", "ghost")
		})
		object.Field("VAT", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Kind", "company")
			})
			value.ProhibitedIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Kind", "person")
			})
		})
		object.Field("Email", func(value *validate.StringBuilder[string]) {
			value.RequiredWithout("Phone")
		})
		object.Field("Phone", func(value *validate.StringBuilder[string]) {
			value.RequiredWithout("Email")
		})
		object.Field("Country", func(value *validate.StringBuilder[string]) {
			value.RequiredUnless(func(condition *validate.ConditionBuilder) {
				condition.In("Kind", "person", "ghost")
			})
		})
		object.Field("Password", func(value *validate.StringBuilder[string]) {
			value.Min(3)
		})
		object.Field("Confirm", func(value *validate.StringBuilder[string]) {
			value.EqField("Password")
		})
		object.Field("Min", func(number *validate.IntBuilder[int]) {
		})
		object.Field("Max", func(number *validate.IntBuilder[int]) {
			number.GteField("Min")
		})
		object.Field("From", func(moment *validate.TimeBuilder) {
		})
		object.Field("To", func(moment *validate.TimeBuilder) {
			moment.AfterField("From")
		})
		object.Field("Notes", func(value *validate.StringBuilder[string]) {
			value.ExcludeIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Kind", "ghost")
			})
			value.Max(3)
		})
		object.Field("Count", func(number *validate.IntBuilder[int64]) {
			number.ProhibitedUnless(func(condition *validate.ConditionBuilder) {
				condition.Not(func(condition *validate.ConditionBuilder) {
					condition.Eq("Kind", "person")
				})
			})
		})
	})
}

// requireConditional validates input against the order schema and fails
// unless the violation codes equal want.
func requireConditional(t *testing.T, input order, want map[string][]string) {
	t.Helper()

	requireCodes(t, orderSchema().Validate(context.Background(), input), want)
}

// TestConditionalRulesCompanyNeedsVAT verifies the CompanyNeedsVAT required and prohibited condition case.
func TestConditionalRulesCompanyNeedsVAT(t *testing.T) {
	t.Parallel()

	requireConditional(t, order{
		Kind:    "company",
		Email:   "a@b.co",
		Country: "ES",
	}, map[string][]string{"/VAT": {"required_if"}})
}

// TestConditionalRulesPersonRejectsVAT verifies the PersonRejectsVAT required and prohibited condition case.
func TestConditionalRulesPersonRejectsVAT(t *testing.T) {
	t.Parallel()

	requireConditional(t, order{
		Kind:  "person",
		Email: "a@b.co",
		VAT:   "x",
	}, map[string][]string{"/VAT": {"prohibited_if"}})
}

// TestConditionalRulesMissingBothContacts verifies the MissingBothContacts required and prohibited condition case.
func TestConditionalRulesMissingBothContacts(t *testing.T) {
	t.Parallel()

	requireConditional(t, order{
		Kind: "person",
	}, map[string][]string{"/Email": {"required_without"}, "/Phone": {"required_without"}})
}

// TestConditionalRulesOneContactEnough verifies the OneContactEnough required and prohibited condition case.
func TestConditionalRulesOneContactEnough(t *testing.T) {
	t.Parallel()

	requireConditional(t, order{
		Kind:  "person",
		Phone: "1",
	}, nil)
}

// TestConditionalRulesCountryRequiredUnlessPerson verifies the CountryRequiredUnlessPerson required and prohibited condition case.
func TestConditionalRulesCountryRequiredUnlessPerson(t *testing.T) {
	t.Parallel()

	requireConditional(t, order{
		Kind:  "company",
		VAT:   "v",
		Phone: "1",
	}, map[string][]string{"/Country": {"required_unless"}})
}

// TestConditionalRulesProhibitedUnlessNot verifies the ProhibitedUnlessNot required and prohibited condition case.
func TestConditionalRulesProhibitedUnlessNot(t *testing.T) {
	t.Parallel()

	requireConditional(t, order{
		Kind:  "person",
		Phone: "1",
		Count: 3,
	}, map[string][]string{"/Count": {"prohibited_unless"}})
}

// TestExcludeIfSkipsAndClears verifies ExcludeIf treats a field as absent
// and clears it in Parse.
func TestExcludeIfSkipsAndClears(t *testing.T) {
	t.Parallel()

	schema := orderSchema()
	input := order{
		Kind:    "ghost",
		Phone:   "1",
		Country: "x",
		Notes:   "too long for max",
	}

	got, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if got.Notes != "" || input.Notes == "" {
		t.Fatalf("got %q, input %q", got.Notes, input.Notes)
	}
}

// TestWhenAppliesExtraSchema verifies When adds rules only when the
// condition holds.
func TestWhenAppliesExtraSchema(t *testing.T) {
	t.Parallel()

	type payment struct {
		// Method selects the payment branch that controls Card presence.
		Method string
		// Card holds the payment detail required by the Method observation.
		Card string
	}

	schema := validate.Struct[payment](func(object *validate.StructBuilder[payment]) {
		object.Field("Method", func(value *validate.StringBuilder[string]) {
		})
		object.Field("Card", func(value *validate.StringBuilder[string]) {
			value.When(func(condition *validate.ConditionBuilder) {
				condition.Eq("Method", "card")
			}, func(card *validate.StringBuilder[string]) {
				card.Required()
				card.Len(16)
			})
		})
	})

	ctx := context.Background()

	requireCodes(t, schema.Validate(ctx, payment{
		Method: "card",
	}), map[string][]string{"/Card": {"required"}})
	requireCodes(t, schema.Validate(ctx, payment{
		Method: "card",
		Card:   "123",
	}), map[string][]string{"/Card": {"string.len"}})
	requireCodes(t, schema.Validate(ctx, payment{
		Method: "cash",
	}), nil)
	requireCodes(t, schema.Validate(ctx, payment{
		Method: "cash",
		Card:   "123",
	}), nil)
}

// TestWhenDefaultApplies verifies a default inside When is applied.
func TestWhenDefaultApplies(t *testing.T) {
	t.Parallel()

	type shipment struct {
		// Express selects the conditional default branch for delivery speed.
		Express bool
		// Speed holds the delivery setting supplied by a conditional default.
		Speed string
	}

	schema := validate.Struct[shipment](func(object *validate.StructBuilder[shipment]) {
		object.Field("Express", func(value *validate.BoolBuilder[bool]) {
		})
		object.Field("Speed", func(value *validate.StringBuilder[string]) {
			value.When(func(condition *validate.ConditionBuilder) {
				condition.Eq("Express", true)
			}, func(speed *validate.StringBuilder[string]) {
				speed.Default("fast")
			})
		})
	})

	got, err := schema.Parse(context.Background(), shipment{
		Express: true,
	})

	if err != nil || got.Speed != "fast" {
		t.Fatalf("got %+v, err %v", got, err)
	}

	got, _ = schema.Parse(context.Background(), shipment{})

	if got.Speed != "" {
		t.Fatalf("speed = %q", got.Speed)
	}
}

// condForm is the object the condition combinator tests validate.
type condForm struct {
	// A provides one independently registered value for sibling-rule checks.
	A string
	// B provides the second sibling value for comparison and presence checks.
	B string
	// N provides a numeric value for default and condition checks.
	N int
	// X provides an independent sibling for condition and promotion checks.
	X string
}

// requireCondition verifies RequiredIf(cond) on X fires for in exactly when want is set.
func requireCondition(t *testing.T, cond validate.ConditionBuilderFunc, in condForm, want bool) {
	t.Helper()

	schema := validate.Struct[condForm](func(object *validate.StructBuilder[condForm]) {
		object.Field("X", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(cond)
		})
	})

	err := schema.Validate(context.Background(), in)

	if want {
		requireCodes(t, err, map[string][]string{"/X": {"required_if"}})
	} else {
		requireCodes(t, err, nil)
	}
}

// TestRequiredWithVariants verifies RequiredWith and RequiredWithAll.
func TestRequiredWithVariants(t *testing.T) {
	t.Parallel()

	type form struct {
		// A provides one independently registered value for sibling-rule checks.
		A string
		// B provides the second sibling value for comparison and presence checks.
		B string
		// C provides another sibling used by grouped presence checks.
		C string
	}

	ctx := context.Background()

	with := validate.Struct[form](func(object *validate.StructBuilder[form]) {
		object.Field("C", func(value *validate.StringBuilder[string]) {
			value.RequiredWith("A", "B")
		})
	})
	all := validate.Struct[form](func(object *validate.StructBuilder[form]) {
		object.Field("C", func(value *validate.StringBuilder[string]) {
			value.RequiredWithAll("A", "B")
		})
	})

	requireCodes(t, with.Validate(ctx, form{
		A: "a",
	}), map[string][]string{"/C": {"required_with"}})
	requireCodes(t, all.Validate(ctx, form{
		A: "a",
	}), nil)
	requireCodes(t, all.Validate(ctx, form{
		A: "a",
		B: "b",
	}), map[string][]string{"/C": {"required_with_all"}})
	requireCodes(t, with.Validate(ctx, form{}), nil)
}
