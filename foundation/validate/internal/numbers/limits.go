package numbers

import "math/big"

// Limit is a numeric keyword and the violation for a value that
// breaks it.
type Limit struct {
	// Keyword selects the JSON Schema boundary being checked.
	Keyword string
	// Code identifies the violation emitted when this check fails.
	Code string
	// Param names the interpolation value containing the configured limit.
	Param string
	// Fails reports whether the observed number violates the supplied limit.
	Fails func(value, limit *big.Rat) bool
}

var (
	// Numbers maps supported numeric JSON Schema keywords to violation codes
	// and boundary predicates. The same table drives validation without changing
	// keyword meanings.
	Numbers = []Limit{
		{
			"minimum",
			"number.min",
			"min",
			func(value, limit *big.Rat) bool {
				return value.Cmp(limit) < 0
			},
		},
		{
			"maximum",
			"number.max",
			"max",
			func(value, limit *big.Rat) bool {
				return value.Cmp(limit) > 0
			},
		},
		{
			"exclusiveMinimum",
			"number.gt",
			"value",
			func(value, limit *big.Rat) bool {
				return value.Cmp(limit) <= 0
			},
		},
		{
			"exclusiveMaximum",
			"number.lt",
			"value",
			func(value, limit *big.Rat) bool {
				return value.Cmp(limit) >= 0
			},
		},
		{
			"multipleOf",
			"number.multiple_of",
			"multiple",
			func(value, limit *big.Rat) bool {
				return !new(big.Rat).Quo(value, limit).IsInt()
			},
		},
	}
	// Strings maps supported string-length keywords to their violation
	// codes. Length is measured before invoking these numeric boundary
	// predicates.
	Strings = []Limit{
		{
			"minLength",
			"string.min",
			"min",
			func(length, limit *big.Rat) bool {
				return length.Cmp(limit) < 0
			},
		},
		{
			"maxLength",
			"string.max",
			"max",
			func(length, limit *big.Rat) bool {
				return length.Cmp(limit) > 0
			},
		},
	}
	// Arrays excludes maxItems: run rejects oversized arrays before
	// enum, const, uniqueness, or container copying can inspect their contents.
	Arrays = []Limit{
		{
			"minItems",
			"slice.min",
			"min",
			func(length, limit *big.Rat) bool {
				return length.Cmp(limit) < 0
			},
		},
	}
	// Objects maps supported property-count keywords to their violation
	// codes. Each predicate receives the observed count and configured limit.
	Objects = []Limit{
		{
			"minProperties",
			"map.min",
			"min",
			func(length, limit *big.Rat) bool {
				return length.Cmp(limit) < 0
			},
		},
		{
			"maxProperties",
			"map.max",
			"max",
			func(length, limit *big.Rat) bool {
				return length.Cmp(limit) > 0
			},
		},
	}
)
