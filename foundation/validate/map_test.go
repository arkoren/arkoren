package validate_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestMapWithoutSchemasPanics verifies Map and Ptr reject nil schemas.
func TestMapWithoutSchemasPanics(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.Map(func(entries *validate.MapBuilder[string, string]) {
			entries.Keys(func(key *validate.StringBuilder[string]) {
				key.Use(nil)
			})
			entries.Values(func(value *validate.StringBuilder[string]) {
			})
		}).Validate(context.Background(), map[string]string{"a": "b"})
	})
	requirePanic(t, func() {
		validate.Ptr[string](nil).Validate(context.Background(), nil)
	})
}

// TestMapKeysAreOrderedAndFormatted verifies numeric keys sort
// numerically in violations and map defaults can be set.
func TestMapKeysAreOrderedAndFormatted(t *testing.T) {
	t.Parallel()

	schema := validate.Map(func(entries *validate.MapBuilder[int, string]) {
		entries.Keys(func(number *validate.IntBuilder[int]) {
		})
		entries.Values(func(value *validate.StringBuilder[string]) {
			value.Min(2)
		})

		entries.Default(map[int]string{1: "ok"})
	})

	err := schema.Validate(context.Background(), map[int]string{2: "a", 10: "b"})

	requireCodes(t, err, map[string][]string{"/2": {"string.min"}, "/10": {"string.min"}})

	got, perr := schema.Parse(context.Background(), nil)

	if perr != nil || got[1] != "ok" {
		t.Fatalf("got %v, err %v", got, perr)
	}
}

// TestMapRules verifies the map rules and entry paths.
func TestMapRules(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
			value.Min(2)
		})
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(1)
		})
	})

	requireCodes(t, validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Use(schema)
		entries.Min(1)
	}).Validate(ctx, map[string]int{}), map[string][]string{"": {"map.min"}})
	requireCodes(t, validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Use(schema)
		entries.Max(1)
	}).Validate(ctx, map[string]int{"aa": 1, "bb": 1}), map[string][]string{"": {"map.max"}})
	requireCodes(t, validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Use(schema)
		entries.Min(2)
	}).Validate(ctx, nil), nil)

	err := schema.Validate(ctx, map[string]int{"ok": 5, "a": 2, "bb": -1, "/": 3})

	requireCodes(t, err, map[string][]string{
		"/a":  {"string.min"},
		"/bb": {"number.min"},
		"/~1": {"string.min"},
	})
}

// TestMapKeyOrderTotal verifies map entries are visited in a total order.
func TestMapKeyOrderTotal(t *testing.T) {
	t.Parallel()

	schema := validate.Map(func(entries *validate.MapBuilder[string, string]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(value *validate.StringBuilder[string]) {
			value.Min(5)
		})
	})

	input := map[string]string{"10": "a", "1a": "a", "9": "a", "01": "a"}

	for range 20 {
		err := schema.Validate(context.Background(), input)
		errs, _ := errors.AsType[validate.Errors](err)

		var order []string

		for pointer := range errs.All() {
			order = append(order, pointer)
		}

		if !slices.Equal(order, []string{"/01", "/9", "/10", "/1a"}) {
			t.Fatalf("order = %q", order)
		}
	}
}

// diagnosticKey has distinct Go identities that can share a JSON object name.
// Its JSON method must take precedence over both text marshaling and fmt.
type diagnosticKey string

// MarshalJSON models a lowercased wire key containing a pointer delimiter.
// The result is always valid JSON and does not mutate the key.
//
// Example:
//
//	encoded, err := json.Marshal(map[diagnosticKey]int{"A": 1})
//	if err != nil {
//		panic(err)
//	}
//	_ = encoded
func (key diagnosticKey) MarshalJSON() ([]byte, error) {
	return json.Marshal("wire/" + strings.ToLower(string(key)))
}

// MarshalText supplies a different name so the tests detect wrong precedence.
// JSON-v2 must choose MarshalJSON rather than this otherwise valid text method.
//
// Example:
//
//	encoded, err := json.Marshal(map[diagnosticKey]int{"A": 1})
//	if err != nil {
//		panic(err)
//	}
//	_ = encoded
func (key diagnosticKey) MarshalText() ([]byte, error) {
	return []byte("text/" + string(key)), nil
}

// TestMapNaNKeyKeepsAssociatedValue checks value validation for a Go map key
// that is not equal to itself. Re-looking up a ranged NaN key cannot recover
// its value; the engine must retain the actual key/value pair from iteration.
func TestMapNaNKeyKeepsAssociatedValue(t *testing.T) {
	t.Parallel()

	schema := validate.Map(func(entries *validate.MapBuilder[float64, int]) {
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(5)
		})
	})

	_, err := schema.Parse(context.Background(), map[float64]int{math.NaN(): 3})

	requireCodes(t, err, map[string][]string{"/NaN": {"number.min"}})
}

// TestMapTextKeyUsesEncodedDiagnosticName checks diagnostics against an actual
// JSON-v2 object name, not fmt's rendering of the underlying duration integer.
// Both child rules and collection assertions must use the same escaped name.
func TestMapTextKeyUsesEncodedDiagnosticName(t *testing.T) {
	t.Parallel()

	input := map[validate.Duration]int{validate.Duration(time.Second): -1}

	encoded, err := json.Marshal(input)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"1s":-1}` {
		t.Fatalf("JSON-v2 object = %s", encoded)
	}

	schema := validate.Map(func(entries *validate.MapBuilder[validate.Duration, int]) {
		entries.Every(func(_ validate.Duration, value int) bool {
			return value >= 0
		})
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(1)
		})
	})

	_, err = schema.Parse(context.Background(), input)

	requireCodes(t, err, map[string][]string{"/1s": {"map.every", "number.min"}})
}

// TestMapJSONKeyUsesEncodedDiagnosticName checks JSON marshaling precedence
// over both text marshaling and fmt. Delimiters in wire names are escaped once.
func TestMapJSONKeyUsesEncodedDiagnosticName(t *testing.T) {
	t.Parallel()

	input := map[diagnosticKey]int{"A": -1}

	encoded, err := json.Marshal(input)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"wire/a":-1}` {
		t.Fatalf("JSON-v2 object = %s", encoded)
	}

	schema := validate.Map(func(entries *validate.MapBuilder[diagnosticKey, int]) {
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(1)
		})
	})

	_, err = schema.Parse(context.Background(), input)

	requireCodes(t, err, map[string][]string{"/wire~1a": {"number.min"}})
}

// TestMapEncodedNameCollisionKeepsBothEntries checks that different Go keys
// with identical wire names do not overwrite one another's violations. Such a
// map cannot be encoded without duplicate names, but typed validation still
// accepts it and reports each failing entry at the shared diagnostic path.
func TestMapEncodedNameCollisionKeepsBothEntries(t *testing.T) {
	t.Parallel()

	schema := validate.Map(func(entries *validate.MapBuilder[diagnosticKey, int]) {
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(1)
		})
	})

	_, err := schema.Parse(context.Background(), map[diagnosticKey]int{"A": -1, "a": -2})

	requireCodes(t, err, map[string][]string{"/wire~1a": {"number.min", "number.min"}})
}

// TestNormalizeMapKeyRejected checks that map keys reject normalization while
// map values can be normalized.
//
// Example:
//
//	go test -run "^TestNormalizeMapKeyRejected$" .
func TestNormalizeMapKeyRejected(t *testing.T) {
	t.Parallel()

	mustPanicInvalid(t, func() {
		_ = validate.Struct[struct {
			// M provides the map whose key normalizer must be rejected.
			M map[string]string
		}](func(object *validate.StructBuilder[struct {
			// M provides the map whose key normalizer must be rejected.
			M map[string]string
		}],

		) {
			object.Field("M", func(entries *validate.MapBuilder[string, string]) {
				entries.Keys(func(value *validate.StringBuilder[string]) {
					value.ToLower()
				})
				entries.Values(func(value *validate.StringBuilder[string]) {
				})
			})
		})
	})

	got, err := validate.Map(func(entries *validate.MapBuilder[string, string]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(value *validate.StringBuilder[string]) {
			value.ToUpper()
		})
	}).
		Parse(context.Background(), map[string]string{"k": "v"})

	if err != nil || got["k"] != "V" {
		t.Fatalf("map value = %v, %v", got, err)
	}
}

// TestReuseBuilderPointerMapPreservesValues covers the map child definitions
// that cannot be recovered by importing the source's collection-level base.
func TestReuseBuilderPointerMapPreservesValues(t *testing.T) {
	t.Parallel()

	shared := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(3)
		})
	})
	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Use(&shared)
	})

	if err := schema.Validate(context.Background(), map[string]int{"age": 1}); err == nil {
		t.Fatal("pointer reuse dropped value validation")
	}
}

// TestReuseBuilderRejectsOverwritingLocalMapChild prevents Use from silently
// replacing a preexisting locally configured value validator.
func TestReuseBuilderRejectsOverwritingLocalMapChild(t *testing.T) {
	t.Parallel()

	shared := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Values(func(number *validate.IntBuilder[int]) {

		})
	})

	requireInvalidReuse(t, func() {
		validate.Map(func(entries *validate.MapBuilder[string, int]) {
			entries.Values(func(number *validate.IntBuilder[int]) {
				number.Min(3)
			})
			entries.Use(shared)
		})
	})
}

// TestReuseBuilderDisjointMapChildrenAccumulate permits independent key and
// value sources without requiring an unnecessary all-or-nothing restriction.
func TestReuseBuilderDisjointMapChildrenAccumulate(t *testing.T) {
	t.Parallel()

	keys := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(key *validate.StringBuilder[string]) {
			key.Min(2)
		})
	})
	values := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(3)
		})
	})
	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Use(keys)
		entries.Use(values)
	})

	var failures validate.Errors

	err := schema.Validate(context.Background(), map[string]int{"a": 1})

	if !errors.As(err, &failures) || len(failures["/a"]) != 2 {
		t.Fatalf("error = %v, want both key and value failures", err)
	}
}

// TestReuseBuilderRejectsReplacingImportedMapKeys covers independent protection
// of key definitions, rather than relying on the map value protection alone.
func TestReuseBuilderRejectsReplacingImportedMapKeys(t *testing.T) {
	t.Parallel()

	shared := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(key *validate.StringBuilder[string]) {
			key.Min(3)
		})
	})

	requireInvalidReuse(t, func() {
		validate.Map(func(entries *validate.MapBuilder[string, int]) {
			entries.Use(shared)
			entries.Keys(func(key *validate.StringBuilder[string]) {

			})
		})
	})
}

// TestReuseBuilderRejectsMultipleMapValues ensures a later collection source
// cannot silently replace an imported value definition.
func TestReuseBuilderRejectsMultipleMapValues(t *testing.T) {
	t.Parallel()

	shared := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(3)
		})
	})

	requireInvalidReuse(t, func() {
		validate.Map(func(entries *validate.MapBuilder[string, int]) {
			entries.Use(shared)
			entries.Use(shared)
		})
	})
}
