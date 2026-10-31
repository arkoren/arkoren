package validate_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestUnsignedSchemaDefaults verifies typed Uint, NotOneOf and the numeric
// field comparisons with real values.
func TestUnsignedSchemaDefaults(t *testing.T) {
	t.Parallel()

	type span struct {
		// Low provides a lower bound for cross-field numeric comparisons.
		Low uint8
		// High provides an upper bound for cross-field numeric comparisons.
		High uint8
		// Pad provides an additional signed value for cross-field comparisons.
		Pad float32
		// Cap provides the numeric upper bound for sibling comparison checks.
		Cap float32
	}

	schema := validate.Struct[span](func(object *validate.StructBuilder[span]) {
		object.Field("Low", func(number *validate.UintBuilder[uint8]) {
			number.NotOneOf(9)
		})
		object.Field("High", func(number *validate.UintBuilder[uint8]) {
			number.GtField("Low")
			number.GteField("Low")
			number.LtField("Low")
			number.LteField("Low")
		})
		object.Field("Cap", func(number *validate.FloatBuilder[float32]) {
			number.GtField("Pad")
			number.LteField("Pad")
		})
	})

	err := schema.Validate(context.Background(), span{
		Low:  9,
		High: 9,
		Pad:  2,
		Cap:  3,
	})

	requireCodes(t, err, map[string][]string{
		"/Low":  {"not_one_of"},
		"/High": {"gt_field", "lt_field"},
		"/Cap":  {"lte_field"},
	})
}

// TestFloatMultipleOfUsesDecimalJSONValues checks JSON Schema's integer-
// quotient rule, not a binary remainder or an epsilon. The JSON decimal 0.3
// is three times 0.1, while the next representable float is not a multiple.
func TestFloatMultipleOfUsesDecimalJSONValues(t *testing.T) {
	t.Parallel()

	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.MultipleOf(0.1)
	})

	if _, err := schema.Parse(context.Background(), 0.3); err != nil {
		t.Fatalf("decimal multiple rejected: %v", err)
	}

	requireCode(t, schema.Validate(context.Background(), math.Nextafter(0.3, 1)), "number.multiple_of")

	if err := schema.Validate(context.Background(), -0.3); err != nil {
		t.Fatalf("negative decimal multiple rejected: %v", err)
	}
}

// TestFloat32MultipleOfKeepsEncodedFactor checks that float32 precision is used
// both for decimal division and metadata. Widening the factor before encoding
// would incorrectly publish 0.10000000149011612 instead of 0.1.
func TestFloat32MultipleOfKeepsEncodedFactor(t *testing.T) {
	t.Parallel()

	schema := validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.MultipleOf(0.1)
	})

	if _, err := schema.Parse(context.Background(), float32(0.3)); err != nil {
		t.Fatal(err)
	}

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"multipleOf":0.1,"type":"number"}` {
		t.Fatalf("float32 factor changed on export: %s", encoded)
	}
}

// TestImportedMultipleOfUsesDecimalJSONValues applies the same integer-
// quotient contract to imported constraints. It deliberately rejects a nearby
// nonmultiple so a broad floating tolerance cannot satisfy the regression.
func TestImportedMultipleOfUsesDecimalJSONValues(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"multipleOf":0.1}`)

	if _, err := schema.Parse(context.Background(), 0.3); err != nil {
		t.Fatalf("imported decimal multiple rejected: %v", err)
	}

	requireCode(t, schema.Validate(context.Background(), math.Nextafter(0.3, 1)), "number.multiple_of")
}

// TestFloat32BoundsKeepEncodedPrecision covers each numeric boundary keyword.
// Go's JSON encoder prints float32(0.1) as 0.1, not the widened binary64 value
// 0.10000000149011612. Constraint metadata must retain that same precision.
func TestFloat32BoundsKeepEncodedPrecision(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Min(0.1)
	}), `{"minimum":0.1,"type":"number"}`)
	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Max(0.3)
	}), `{"maximum":0.3,"type":"number"}`)
	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Between(0.1, 0.3)
	}), `{"maximum":0.3,"minimum":0.1,"type":"number"}`)
	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Gt(0.1)
	}), `{"exclusiveMinimum":0.1,"type":"number"}`)
	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Gte(0.1)
	}), `{"minimum":0.1,"type":"number"}`)
	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Lt(0.3)
	}), `{"exclusiveMaximum":0.3,"type":"number"}`)
	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Lte(0.3)
	}), `{"maximum":0.3,"type":"number"}`)
}

// TestFloat32BoundaryAcceptsEncodedEndpoint checks the actual instance boundary
// after export/import, not just the spelling of the generated constraint. The
// exported minimum must admit the shortest JSON encoding of its float32 input.
func TestFloat32BoundaryAcceptsEncodedEndpoint(t *testing.T) {
	t.Parallel()

	source := validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Min(0.1)
	})

	document, err := source.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	imported, err := validate.FromJSONSchema(document)

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(float32(0.1))

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `0.1` {
		t.Fatalf("float32 JSON endpoint = %s", encoded)
	}

	if err := imported.Validate(context.Background(), jsontext.Value(encoded)); err != nil {
		t.Fatalf("export rejects the float32 endpoint's JSON value: %s: %v", document, err)
	}
}

// TestFloat32NonFiniteBoundsKeepStickyErrors protects construction diagnostics
// after metadata retains the original float type, including defined float32
// types. A later finite annotation cannot hide an invalid earlier constraint.
func TestFloat32NonFiniteBoundsKeepStickyErrors(t *testing.T) {
	t.Parallel()

	// decimal32 checks named-type metadata without introducing a public type.
	type decimal32 float32

	requireInvalidExport(t, validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Min(float32(math.NaN())).Min(0.1)
	}))
	requireInvalidExport(t, validate.Float(func(number *validate.FloatBuilder[decimal32]) {
		number.Max(decimal32(math.Inf(1))).Max(0.3)
	}))
}

// TestNormalizeNumbersAbsNegative checks that absolute-value normalization
// changes a negative signed input.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersAbsNegative$" .
func TestNormalizeNumbersAbsNegative(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Abs()
	}), -5, 5)
}

// TestNormalizeNumbersAbsPositive checks that absolute-value normalization
// leaves a positive signed input unchanged.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersAbsPositive$" .
func TestNormalizeNumbersAbsPositive(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Abs()
	}), 5, 5)
}

// TestNormalizeNumbersAbsZero checks that absolute-value normalization
// preserves zero.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersAbsZero$" .
func TestNormalizeNumbersAbsZero(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Abs()
	}), 0, 0)
}

// TestNormalizeNumbersClampLow checks that signed values below the clamp
// interval become its lower bound.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersClampLow$" .
func TestNormalizeNumbersClampLow(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Clamp(0, 10)
	}), -9, 0)
}

// TestNormalizeNumbersClampInside checks that signed values within the clamp
// interval remain unchanged.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersClampInside$" .
func TestNormalizeNumbersClampInside(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Clamp(0, 10)
	}), 3, 3)
}

// TestNormalizeNumbersClampHigh checks that signed values above the clamp
// interval become its upper bound.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersClampHigh$" .
func TestNormalizeNumbersClampHigh(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Clamp(0, 10)
	}), 99, 10)
}

// TestNormalizeNumbersUintClampLow checks the lower clamp boundary for
// unsigned values.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersUintClampLow$" .
func TestNormalizeNumbersUintClampLow(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Clamp(5, 10)
	}), 1, 5)
}

// TestNormalizeNumbersUintClampInside checks that unsigned values inside the
// clamp interval remain unchanged.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersUintClampInside$" .
func TestNormalizeNumbersUintClampInside(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Clamp(5, 10)
	}), 7, 7)
}

// TestNormalizeNumbersUintClampHigh checks the upper clamp boundary for
// unsigned values.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersUintClampHigh$" .
func TestNormalizeNumbersUintClampHigh(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Clamp(5, 10)
	}), 50, 10)
}

// TestNormalizeNumbersFloatAbsNegative checks absolute-value normalization of
// a negative floating-point input.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersFloatAbsNegative$" .
func TestNormalizeNumbersFloatAbsNegative(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Abs()
	}), -1.5, 1.5)
}

// TestNormalizeNumbersFloatAbsPositive checks that positive floating-point
// values survive absolute-value normalization unchanged.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersFloatAbsPositive$" .
func TestNormalizeNumbersFloatAbsPositive(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Abs()
	}), 2, 2)
}

// TestNormalizeNumbersRoundHalfUp checks that a positive halfway value rounds
// away from zero.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersRoundHalfUp$" .
func TestNormalizeNumbersRoundHalfUp(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Round(0)
	}), 1.5, 2)
}

// TestNormalizeNumbersRoundHalfUpEven checks that halfway rounding does not
// use the nearest-even rule.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersRoundHalfUpEven$" .
func TestNormalizeNumbersRoundHalfUpEven(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Round(0)
	}), 2.5, 3)
}

// TestNormalizeNumbersRoundNegativeHalf checks that a negative halfway value
// rounds away from zero.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersRoundNegativeHalf$" .
func TestNormalizeNumbersRoundNegativeHalf(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Round(0)
	}), -1.5, -2)
}

// TestNormalizeNumbersRoundNegativeHalfEven checks that negative halfway
// rounding also avoids the nearest-even rule.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersRoundNegativeHalfEven$" .
func TestNormalizeNumbersRoundNegativeHalfEven(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Round(0)
	}), -2.5, -3)
}

// TestNormalizeNumbersRoundDown checks that a value below the halfway boundary
// rounds toward the nearest integer.
//
// Example:
//
//	go test -run "^TestNormalizeNumbersRoundDown$" .
func TestNormalizeNumbersRoundDown(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Round(0)
	}), 1.4, 1)
}

// TestNormalizeNumbers checks numeric normalization boundaries and invalid
// construction arguments, including the smallest signed integer.
//
// Example:
//
//	go test -run "^TestNormalizeNumbers$" .
func TestNormalizeNumbers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	const minInt8 = int8(-128)

	if got, err := validate.Int(func(number *validate.IntBuilder[int8]) {
		number.Abs()
	}).Parse(ctx, minInt8); err != nil || got != minInt8 {
		t.Errorf("Abs(min) = %d, %v", got, err)
	}

	if got, _ := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Round(2)
	}).Parse(ctx, 3.14159); got != 3.14 {
		t.Errorf("Round(2) = %v", got)
	}

	if got, _ := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Clamp(0, 1)
	}).Parse(ctx, 1.5); got != 1 {
		t.Errorf("Float Clamp = %v", got)
	}

	mustPanicInvalid(t, func() {
		validate.Int(func(number *validate.IntBuilder[int]) {
			number.Clamp(5, 1)
		})
	})
	mustPanicInvalid(t, func() {
		validate.Uint(func(number *validate.UintBuilder[uint]) {
			number.Clamp(5, 1)
		})
	})
	mustPanicInvalid(t, func() {
		validate.Float(func(number *validate.FloatBuilder[float64]) {
			number.Clamp(5, 1)
		})
	})
	mustPanicInvalid(t, func() {
		validate.Float(func(number *validate.FloatBuilder[float64]) {
			number.Round(-1)
		})
	})
}

// TestNormalizeClampBeforeMax checks normalization before value rules,
// presence checks and default selection.
//
// Example:
//
//	go test -run "^TestNormalizeClampBeforeMax$" .
func TestNormalizeClampBeforeMax(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	if err := validate.Int(func(number *validate.IntBuilder[int]) {
		number.Max(5)
		number.Clamp(0, 5)
	}).Validate(ctx, 100); err != nil {
		t.Fatalf("Clamp should run before Max: %v", err)
	}

	err := validate.Int(func(number *validate.IntBuilder[int]) {
		number.Required()
		number.Clamp(0, 5)
	}).Validate(ctx, -3)

	requireCodes(t, err, map[string][]string{"": {"required"}})

	got, err := validate.Int(func(number *validate.IntBuilder[int]) {
		number.Default(7)
		number.Clamp(0, 5)
	}).Parse(ctx, -3)

	if err != nil || got != 7 {
		t.Fatalf("default after normalize = %d, %v", got, err)
	}
}

// TestIntRulesMinPass verifies the MinPass int rule case.
func TestIntRulesMinPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(5)
	}).Validate(context.Background(), 5), "")
}

// TestIntRulesMinFail verifies the MinFail int rule case.
func TestIntRulesMinFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(5)
	}).Validate(context.Background(), 4), "number.min")
}

// TestIntRulesMinAbsent verifies the MinAbsent int rule case.
func TestIntRulesMinAbsent(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(5)
	}).Validate(context.Background(), 0), "")
}

// TestIntRulesMaxPass verifies the MaxPass int rule case.
func TestIntRulesMaxPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Max(5)
	}).Validate(context.Background(), 5), "")
}

// TestIntRulesMaxFail verifies the MaxFail int rule case.
func TestIntRulesMaxFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Max(5)
	}).Validate(context.Background(), 6), "number.max")
}

// TestIntRulesBetweenPass verifies the BetweenPass int rule case.
func TestIntRulesBetweenPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Between(1, 3)
	}).Validate(context.Background(), 2), "")
}

// TestIntRulesBetweenFailLow verifies the BetweenFailLow int rule case.
func TestIntRulesBetweenFailLow(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Between(1, 3)
	}).Validate(context.Background(), -1), "number.between")
}

// TestIntRulesBetweenFailHigh verifies the BetweenFailHigh int rule case.
func TestIntRulesBetweenFailHigh(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Between(1, 3)
	}).Validate(context.Background(), 4), "number.between")
}

// TestIntRulesGtPass verifies the GtPass int rule case.
func TestIntRulesGtPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Gt(1)
	}).Validate(context.Background(), 2), "")
}

// TestIntRulesGtFail verifies the GtFail int rule case.
func TestIntRulesGtFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Gt(1)
	}).Validate(context.Background(), 1), "number.gt")
}

// TestIntRulesGtePass verifies the GtePass int rule case.
func TestIntRulesGtePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Gte(1)
	}).Validate(context.Background(), 1), "")
}

// TestIntRulesGteFail verifies the GteFail int rule case.
func TestIntRulesGteFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Gte(2)
	}).Validate(context.Background(), 1), "number.gte")
}

// TestIntRulesLtPass verifies the LtPass int rule case.
func TestIntRulesLtPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Lt(3)
	}).Validate(context.Background(), 2), "")
}

// TestIntRulesLtFail verifies the LtFail int rule case.
func TestIntRulesLtFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Lt(3)
	}).Validate(context.Background(), 3), "number.lt")
}

// TestIntRulesLtePass verifies the LtePass int rule case.
func TestIntRulesLtePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Lte(3)
	}).Validate(context.Background(), 3), "")
}

// TestIntRulesLteFail verifies the LteFail int rule case.
func TestIntRulesLteFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Lte(3)
	}).Validate(context.Background(), 4), "number.lte")
}

// TestIntRulesPositivePass verifies the PositivePass int rule case.
func TestIntRulesPositivePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Positive()
	}).Validate(context.Background(), 1), "")
}

// TestIntRulesPositiveFail verifies the PositiveFail int rule case.
func TestIntRulesPositiveFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Positive()
	}).Validate(context.Background(), -1), "number.positive")
}

// TestIntRulesNonNegativePass verifies the NonNegativePass int rule case.
func TestIntRulesNonNegativePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.NonNegative()
	}).Validate(context.Background(), 3), "")
}

// TestIntRulesNonNegativeFail verifies the NonNegativeFail int rule case.
func TestIntRulesNonNegativeFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.NonNegative()
	}).Validate(context.Background(), -3), "number.non_negative")
}

// TestIntRulesMultipleOfPass verifies the MultipleOfPass int rule case.
func TestIntRulesMultipleOfPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.MultipleOf(5)
	}).Validate(context.Background(), 15), "")
}

// TestIntRulesMultipleOfFail verifies the MultipleOfFail int rule case.
func TestIntRulesMultipleOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.MultipleOf(5)
	}).Validate(context.Background(), 16), "number.multiple_of")
}

// TestIntRulesOneOfPass verifies the OneOfPass int rule case.
func TestIntRulesOneOfPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.OneOf(1, 2)
	}).Validate(context.Background(), 2), "")
}

// TestIntRulesOneOfFail verifies the OneOfFail int rule case.
func TestIntRulesOneOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.OneOf(1, 2)
	}).Validate(context.Background(), 3), "one_of")
}

// TestIntRulesNotOneOfFail verifies the NotOneOfFail int rule case.
func TestIntRulesNotOneOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.NotOneOf(1)
	}).Validate(context.Background(), 1), "not_one_of")
}

// TestIntRulesRequiredFail verifies the RequiredFail int rule case.
func TestIntRulesRequiredFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Required()
	}).Validate(context.Background(), 0), "required")
}

// TestIntNamedType verifies Int with a named integer type.
func TestIntNamedType(t *testing.T) {
	t.Parallel()

	schema := validate.Int(func(number *validate.IntBuilder[time.Duration]) {
		number.Min(time.Second)
	})

	if err := schema.Validate(context.Background(), 2*time.Second); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, schema.Validate(context.Background(), time.Millisecond),
		map[string][]string{"": {"number.min"}})
}

// TestIntSmallWidths verifies narrow integer types keep their own limits.
func TestIntSmallWidths(t *testing.T) {
	t.Parallel()

	schema := validate.Int(func(number *validate.IntBuilder[int8]) {
		number.Max(100)
	})

	requireCodes(t, schema.Validate(context.Background(), 101), map[string][]string{"": {"number.max"}})
}

// TestUintRulesMinPass verifies the MinPass uint rule case.
func TestUintRulesMinPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Min(5)
	}).Validate(context.Background(), 5), "")
}

// TestUintRulesMinFail verifies the MinFail uint rule case.
func TestUintRulesMinFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Min(5)
	}).Validate(context.Background(), 4), "number.min")
}

// TestUintRulesMaxFail verifies the MaxFail uint rule case.
func TestUintRulesMaxFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Max(5)
	}).Validate(context.Background(), 6), "number.max")
}

// TestUintRulesBetweenPass verifies the BetweenPass uint rule case.
func TestUintRulesBetweenPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Between(1, 3)
	}).Validate(context.Background(), 2), "")
}

// TestUintRulesBetweenFail verifies the BetweenFail uint rule case.
func TestUintRulesBetweenFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Between(1, 3)
	}).Validate(context.Background(), 4), "number.between")
}

// TestUintRulesGtFail verifies the GtFail uint rule case.
func TestUintRulesGtFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Gt(1)
	}).Validate(context.Background(), 1), "number.gt")
}

// TestUintRulesGteFail verifies the GteFail uint rule case.
func TestUintRulesGteFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Gte(2)
	}).Validate(context.Background(), 1), "number.gte")
}

// TestUintRulesLtFail verifies the LtFail uint rule case.
func TestUintRulesLtFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Lt(3)
	}).Validate(context.Background(), 3), "number.lt")
}

// TestUintRulesLteFail verifies the LteFail uint rule case.
func TestUintRulesLteFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Lte(3)
	}).Validate(context.Background(), 4), "number.lte")
}

// TestUintRulesPositivePass verifies the PositivePass uint rule case.
func TestUintRulesPositivePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Positive()
	}).Validate(context.Background(), 1), "")
}

// TestUintRulesNonNegativePass verifies the NonNegativePass uint rule case.
func TestUintRulesNonNegativePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.NonNegative()
	}).Validate(context.Background(), 0), "")
}

// TestUintRulesMultipleOfFail verifies the MultipleOfFail uint rule case.
func TestUintRulesMultipleOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.MultipleOf(4)
	}).Validate(context.Background(), 6), "number.multiple_of")
}

// TestUintRulesOneOfFail verifies the OneOfFail uint rule case.
func TestUintRulesOneOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.OneOf(1)
	}).Validate(context.Background(), 2), "one_of")
}

// TestFloatRulesMinPass verifies the MinPass float rule case.
func TestFloatRulesMinPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Min(1.5)
	}).Validate(context.Background(), 1.5), "")
}

// TestFloatRulesMinFail verifies the MinFail float rule case.
func TestFloatRulesMinFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Min(1.5)
	}).Validate(context.Background(), 1.4), "number.min")
}

// TestFloatRulesMaxFail verifies the MaxFail float rule case.
func TestFloatRulesMaxFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Max(1.5)
	}).Validate(context.Background(), 1.6), "number.max")
}

// TestFloatRulesBetweenFail verifies the BetweenFail float rule case.
func TestFloatRulesBetweenFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Between(0.1, 0.2)
	}).Validate(context.Background(), 0.3), "number.between")
}

// TestFloatRulesGtFail verifies the GtFail float rule case.
func TestFloatRulesGtFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Gt(1)
	}).Validate(context.Background(), 1), "number.gt")
}

// TestFloatRulesGteFail verifies the GteFail float rule case.
func TestFloatRulesGteFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Gte(2)
	}).Validate(context.Background(), 1), "number.gte")
}

// TestFloatRulesLtFail verifies the LtFail float rule case.
func TestFloatRulesLtFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Lt(1)
	}).Validate(context.Background(), 1), "number.lt")
}

// TestFloatRulesLteFail verifies the LteFail float rule case.
func TestFloatRulesLteFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Lte(1)
	}).Validate(context.Background(), 1.1), "number.lte")
}

// TestFloatRulesPositiveFail verifies the PositiveFail float rule case.
func TestFloatRulesPositiveFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Positive()
	}).Validate(context.Background(), -0.5), "number.positive")
}

// TestFloatRulesNonNegativeFail verifies the NonNegativeFail float rule case.
func TestFloatRulesNonNegativeFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.NonNegative()
	}).Validate(context.Background(), -0.5), "number.non_negative")
}

// TestFloatRulesMultipleOfPass verifies the MultipleOfPass float rule case.
func TestFloatRulesMultipleOfPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.MultipleOf(0.5)
	}).Validate(context.Background(), 2.5), "")
}

// TestFloatRulesMultipleOfFail verifies the MultipleOfFail float rule case.
func TestFloatRulesMultipleOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.MultipleOf(0.5)
	}).Validate(context.Background(), 2.7), "number.multiple_of")
}

// TestFloatRulesOneOfFail verifies the OneOfFail float rule case.
func TestFloatRulesOneOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.OneOf(1.5)
	}).Validate(context.Background(), 2.5), "one_of")
}

// TestFloat32 verifies Float with float32.
func TestFloat32(t *testing.T) {
	t.Parallel()

	schema := validate.Float(func(number *validate.FloatBuilder[float32]) {
		number.Max(1)
	})

	requireCodes(t, schema.Validate(context.Background(), 2), map[string][]string{"": {"number.max"}})
}

// TestNumberInvalidBuilders verifies programmer errors panic.
func TestNumberInvalidBuilders(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.Int(func(number *validate.IntBuilder[int]) {
			number.MultipleOf(0)
		})
	})
	requirePanic(t, func() {
		validate.Uint(func(number *validate.UintBuilder[uint]) {
			number.MultipleOf(0)
		})
	})
	requirePanic(t, func() {
		validate.Float(func(number *validate.FloatBuilder[float64]) {
			number.MultipleOf(0)
		})
	})
	requirePanic(t, func() {
		validate.Int(func(number *validate.IntBuilder[int]) {
			number.Between(5, 1)
		})
	})
}

// flag is a named boolean used to check scalar builder inference. Boolean
// acceptance rules must work without conversion to bool.
type flag bool

// pinnedClock returns a context whose clock is fixed at now, with times an
// hour before and after it.
func pinnedClock() (ctx context.Context, now, earlier, later time.Time) {
	now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ctx = validate.NewClockContext(context.Background(), func() time.Time {
		return now
	})

	return ctx, now, now.Add(-time.Hour), now.Add(time.Hour)
}
