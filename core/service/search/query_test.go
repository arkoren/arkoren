package search_test

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"arkoren.dev/core/service/search"
)

// TestQueryWhereAcceptsOperatorConstants exercises every public comparison
// constant at the declaration boundary; membership keeps its separate methods.
func TestQueryWhereAcceptsOperatorConstants(t *testing.T) {
	t.Parallel()

	for _, operator := range []search.Operator{
		search.OpEqual, search.OpNotEqual, search.OpLess,
		search.OpLessOrEqual, search.OpGreater, search.OpGreaterOrEqual,
	} {
		query := search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
			query.Where("price", operator, 10)
		})

		if err := query.Err(); err != nil {
			t.Fatalf("Where with %q: %v", operator, err)
		}
	}
}

// TestZeroQueryReportsInvalidDefinition checks zero queries expose both the
// query classification and their missing index definition before execution.
func TestZeroQueryReportsInvalidDefinition(t *testing.T) {
	t.Parallel()

	var query search.Query[doc]

	err := query.Err()

	if !errors.Is(err, search.ErrInvalidQuery) || !errors.Is(err, search.ErrInvalidName) {
		t.Fatalf("zero query Err = %v, want invalid query and index", err)
	}
}

// TestZeroQueryDerivePreservesMissingIndex checks callbacks run on invalid
// bases without replacing the missing-index cause with a later field error.
func TestZeroQueryDerivePreservesMissingIndex(t *testing.T) {
	t.Parallel()

	var base search.Query[doc]
	calls := 0
	query := base.Derive(func(query *search.QueryBuilder) {
		calls++
		query.Where("missing", 1)
	})

	if calls != 1 || !errors.Is(query.Err(), search.ErrInvalidName) {
		t.Fatalf("Derive calls = %d, Err = %v", calls, query.Err())
	}
}

// ExampleQuery_Derive shows local typed-operator filters do not change the base
// query or reapply declaration callbacks to its published state.
func ExampleQuery_Derive() {
	base := search.NewQuery(exampleProducts, "red")
	cheap := base.Derive(func(query *search.QueryBuilder) {
		query.Where("price", search.OpLessOrEqual, 100)
		query.OrderBy("price")
	})

	if err := cheap.Err(); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(len(base.Inspect().Where), cheap.Inspect().Where[0].Operator)
	// Output: 0 <=
}

// ExampleQuery_Err distinguishes invalid zero definitions from empty text,
// which is a valid query when constructed against a valid index.
func ExampleQuery_Err() {
	var zero search.Query[exampleProduct]
	fmt.Println(errors.Is(zero.Err(), search.ErrInvalidQuery))
	fmt.Println(search.NewQuery(exampleProducts, "").Err() == nil)
	// Output:
	// true
	// true
}

// TestQueryCallbacksShareOneDraft verifies ordered edits and same-receiver
// returns for every root setting, including typed membership methods.
func TestQueryCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var retained *search.QueryBuilder
	query := search.NewQuery(testIndex, "red", func(query *search.QueryBuilder) {
		retained = query

		if query.Where("group", "a") != query || query.OrWhere("group", "b") != query {
			t.Fatal("comparison returned another draft")
		}
	}, func(query *search.QueryBuilder) {
		if query != retained || query.WhereIn("group", "c") != query {
			t.Fatal("membership received another draft")
		}

		if query.WhereNotIn("price", 1) != query || query.OrderBy("price") != query {
			t.Fatal("membership or order returned another draft")
		}

		if query.OrderByDesc("title") != query || query.WhereGroup(func(predicate *search.PredicateBuilder) {
			predicate.Where("on", true)
		}) != query {
			t.Fatal("descending order or group returned another draft")
		}
	})

	if query.Err() != nil || len(query.Inspect().Where) != 5 {
		t.Fatalf("query = %+v, %v", query.Inspect(), query.Err())
	}

	if query.Inspect().Where[0].Value != "a" || !query.Inspect().Where[1].Or {
		t.Fatalf("callbacks ran out of order: %+v", query.Inspect())
	}
}

// TestQueryPublicationDetachesRetainedDrafts covers nested predicates, typed
// input lists, and late root/predicate edits after publication.
func TestQueryPublicationDetachesRetainedDrafts(t *testing.T) {
	t.Parallel()

	values := []string{"a", "b"}
	var retained *search.QueryBuilder
	var retainedPredicate *search.PredicateBuilder
	query := search.NewQuery(testIndex, "red", func(query *search.QueryBuilder) {
		retained = query
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
			retainedPredicate = predicate
			predicate.WhereGroup(func(predicate *search.PredicateBuilder) {
				predicate.WhereIn("group", values...)
			})
		})
		query.OrderBy("price")
		retainedPredicate.OrWhere("on", false)
	})

	if len(query.Inspect().Where[0].Group) != 1 {
		t.Fatal("a retained predicate changed a completed group before publication")
	}

	want := query.Inspect()
	values[0] = "changed"
	retained.Where("group", "late")
	retained.OrderByDesc("title")
	retainedPredicate.OrWhere("on", true)

	snapshot := query.Inspect()
	snapshot.Where[0].Group[0].Group[0].Values[0] = "changed"

	if !specEqual(query.Inspect(), want) {
		t.Fatalf("retained containers changed query: %+v", query.Inspect())
	}
}

// TestQueryDerivePreservesBase verifies independent publication and inherited
// text/filter/order state without reparsing or resetting the query.
func TestQueryDerivePreservesBase(t *testing.T) {
	t.Parallel()

	base := search.NewQuery(testIndex, "red", func(query *search.QueryBuilder) {
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
			predicate.WhereIn("group", "a", "b")
		})
		query.OrderBy("price")
	})

	want := base.Inspect()
	var retained *search.QueryBuilder
	derived := base.Derive(func(query *search.QueryBuilder) {
		retained = query
		query.Where("price", "<", 10)
	}, func(query *search.QueryBuilder) {
		if query != retained {
			t.Fatal("Derive callbacks received separate drafts")
		}

		query.OrderByDesc("title")
	})
	retained.Where("group", "late")

	snapshot := derived.Inspect()
	snapshot.Where[0].Group[0].Values[0] = "changed"

	if !specEqual(base.Inspect(), want) || !specEqual(base.Derive().Inspect(), want) {
		t.Fatalf("Derive changed base: %+v", base.Inspect())
	}

	if derived.Err() != nil || len(derived.Inspect().Where) != 2 || len(derived.Inspect().Orders) != 2 {
		t.Fatalf("derived = %+v, %v", derived.Inspect(), derived.Err())
	}

	if derived.Inspect().Where[0].Group[0].Values[0] != "a" {
		t.Fatal("derived Inspect shared a nested membership list")
	}
}

// TestQueryDeriveKeepsInheritedError verifies callbacks still run while later
// invalid input cannot replace an inherited domain error.
func TestQueryDeriveKeepsInheritedError(t *testing.T) {
	t.Parallel()

	base := search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("missing", 1)
	})

	calls := 0
	derived := base.Derive(func(query *search.QueryBuilder) {
		calls++
		query.OrderBy("missing")
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
			calls++
			predicate.Where("missing", nil)
		})
	})

	if calls != 2 || !errors.Is(derived.Err(), base.Err()) {
		t.Fatalf("callbacks = %d, Err = %v, want inherited %v", calls, derived.Err(), base.Err())
	}
}

// TestInvalidDeclarationsStillRunCallbacks preserves direct, ordered policy
// invocation even when positional names or key functions are already invalid.
func TestInvalidDeclarationsStillRunCallbacks(t *testing.T) {
	t.Parallel()

	var calls []int
	index := search.NewIndex[doc]("Bad", nil, func(index *search.IndexBuilder) {
		calls = append(calls, 1)
	}, func(index *search.IndexBuilder) {
		calls = append(calls, 2)
		index.Searchable("title")
	})

	query := search.NewQuery(index, "", func(query *search.QueryBuilder) {
		calls = append(calls, 3)
	}, func(query *search.QueryBuilder) {
		calls = append(calls, 4)
		query.OrderBy("missing")
	})

	if !slices.Equal(calls, []int{1, 2, 3, 4}) {
		t.Fatalf("callbacks = %v", calls)
	}

	if !errors.Is(query.Err(), search.ErrInvalidQuery) || !errors.Is(query.Err(), search.ErrInvalidName) {
		t.Fatalf("query lost invalid index error: %v", query.Err())
	}
}

// TestQueryOmittedCallbacksKeepMeaning covers expanded nil/empty callbacks for
// both initial publication and derivation, with empty text matching everything.
func TestQueryOmittedCallbacksKeepMeaning(t *testing.T) {
	t.Parallel()

	var omitted []func(*search.QueryBuilder)
	base := search.NewQuery(testIndex, "")

	for _, query := range []search.Query[doc]{
		search.NewQuery(testIndex, "", omitted...),
		search.NewQuery(testIndex, "", []func(*search.QueryBuilder){}...),
		base.Derive(omitted...),
		base.Derive([]func(*search.QueryBuilder){}...),
	} {
		if query.Err() != nil || !specEqual(query.Inspect(), base.Inspect()) {
			t.Fatalf("empty callbacks changed meaning: %+v, %v", query.Inspect(), query.Err())
		}
	}
}

// TestTypedMembershipNormalizesNamedScalars preserves generic argument
// inference and named-type normalization through root and predicate methods.
func TestTypedMembershipNormalizesNamedScalars(t *testing.T) {
	t.Parallel()

	type category string
	type price int16
	type available bool
	query := search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.WhereIn("group", category("shoes"), category("boots"))
		query.WhereNotIn("price", price(10), price(20))
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
			predicate.WhereIn("on", available(true))
			predicate.WhereNotIn("group", category("socks"))
		})
	})

	if err := query.Err(); err != nil {
		t.Fatal(err)
	}

	where := query.Inspect().Where

	if !slices.Equal(where[0].Values, []any{"shoes", "boots"}) ||
		!slices.Equal(where[1].Values, []any{float64(10), float64(20)}) {
		t.Fatalf("typed lists were not normalized: %+v", where)
	}

	if where[2].Group[0].Values[0] != true || where[2].Group[1].Values[0] != "socks" {
		t.Fatalf("typed predicates were not normalized: %+v", where[2])
	}
}

// TestDeclarationsUseFreshDrafts verifies that reusing a construction policy
// does not reuse mutable index or query state between separate publications.
func TestDeclarationsUseFreshDrafts(t *testing.T) {
	t.Parallel()

	var indexDrafts []*search.IndexBuilder
	indexPolicy := func(index *search.IndexBuilder) {
		indexDrafts = append(indexDrafts, index)
		index.Searchable("title")
	}

	first := search.NewIndex("docs", docID, indexPolicy)
	second := search.NewIndex("docs", docID, indexPolicy)

	if indexDrafts[0] == indexDrafts[1] || !first.Schema().Equal(second.Schema()) {
		t.Fatal("index constructors reused a draft or changed meaning")
	}

	var queryDrafts []*search.QueryBuilder
	queryPolicy := func(query *search.QueryBuilder) {
		queryDrafts = append(queryDrafts, query)
		query.Where("group", "a")
	}

	base := search.NewQuery(testIndex, "red", queryPolicy)
	other := search.NewQuery(testIndex, "red", queryPolicy)

	if queryDrafts[0] == queryDrafts[1] || !specEqual(base.Inspect(), other.Inspect()) {
		t.Fatal("query constructors reused a draft or changed meaning")
	}
}

// TestPredicateCallbacksShareOneDraft verifies final group validation, nested
// groups, typed memberships, and each restricted method's receiver identity.
func TestPredicateCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var retained *search.PredicateBuilder
	query := search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
			retained = predicate
		}, func(predicate *search.PredicateBuilder) {
			if predicate != retained || predicate.Where("on", true) != predicate {
				t.Fatal("predicates received separate drafts")
			}

			if predicate.OrWhere("price", 1) != predicate || predicate.WhereIn("group", "a") != predicate {
				t.Fatal("comparison or membership returned another draft")
			}

			if predicate.WhereNotIn("price", 2) != predicate {
				t.Fatal("exclusion returned another draft")
			}

			if predicate.WhereGroup(func(predicate *search.PredicateBuilder) {
				predicate.Where("group", "b")
			}) != predicate {
				t.Fatal("nested group returned another draft")
			}
		})
	})

	if query.Err() != nil || len(query.Inspect().Where[0].Group) != 5 {
		t.Fatalf("group = %+v, %v", query.Inspect(), query.Err())
	}
}

// TestEmptyPredicateGroupsAreInvalid covers omission, expanded nil/empty
// slices, and nested empty groups without confusing nil slices with nil entries.
func TestEmptyPredicateGroupsAreInvalid(t *testing.T) {
	t.Parallel()

	var omitted []func(*search.PredicateBuilder)

	for _, configure := range []func(*search.QueryBuilder){
		func(query *search.QueryBuilder) {
			query.WhereGroup()
		},
		func(query *search.QueryBuilder) {
			query.WhereGroup(omitted...)
		},
		func(query *search.QueryBuilder) {
			query.WhereGroup([]func(*search.PredicateBuilder){}...)
		},
		func(query *search.QueryBuilder) {
			query.WhereGroup(func(predicate *search.PredicateBuilder) {
				predicate.WhereGroup()
			})
		},
	} {
		if err := search.NewQuery(testIndex, "", configure).Err(); !errors.Is(err, search.ErrInvalidQuery) {
			t.Fatalf("empty group error = %v", err)
		}
	}
}

// TestPredicateSurfaceHasNoRootOperations guards the intentional restricted
// boundary; neither ordering nor a newly invented OrWhereGroup belongs here.
func TestPredicateSurfaceHasNoRootOperations(t *testing.T) {
	t.Parallel()

	predicate := reflect.TypeFor[*search.PredicateBuilder]()

	for _, name := range []string{"OrderBy", "OrderByDesc", "OrWhereGroup", "Inspect", "Derive"} {
		if _, found := predicate.MethodByName(name); found {
			t.Fatalf("PredicateBuilder exposes %s", name)
		}
	}
}

// TestPublishedDefinitionsHaveNoDraftSettings guards the publication boundary:
// only construction drafts, not Index or Query values, configure definitions.
func TestPublishedDefinitionsHaveNoDraftSettings(t *testing.T) {
	t.Parallel()

	index := reflect.TypeFor[search.Index[doc]]()

	for _, name := range []string{"Searchable", "Filterable", "Sortable", "Derive"} {
		if _, found := index.MethodByName(name); found {
			t.Fatalf("published Index exposes %s", name)
		}
	}

	query := reflect.TypeFor[search.Query[doc]]()

	for _, name := range []string{"Where", "OrWhere", "WhereGroup", "OrderBy", "OrderByDesc"} {
		if _, found := query.MethodByName(name); found {
			t.Fatalf("published Query exposes %s", name)
		}
	}
}

// requirePanic checks direct propagation of construction callback misuse or
// an application panic. The marker must retain its identity across the call.
func requirePanic(t *testing.T, marker any, run func()) {
	t.Helper()

	defer func() {
		got := recover()

		if got == nil {
			t.Fatal("callback did not panic")
		}

		if marker != nil && got != marker {
			t.Fatalf("panic = %v, want original %v", got, marker)
		}
	}()
	run()
}

// TestQueryNilCallbackPanicsEvenWhenInvalid preserves direct callback invocation
// instead of filtering nil entries after index validation.
func TestQueryNilCallbackPanicsEvenWhenInvalid(t *testing.T) {
	t.Parallel()

	requirePanic(t, nil, func() {
		search.NewQuery(search.NewIndex[doc]("Bad", nil), "", nil)
	})
}

// TestDeriveNilCallbackPanics preserves the construction contract for a local
// variant even when the base already carries a sticky query error.
func TestDeriveNilCallbackPanics(t *testing.T) {
	t.Parallel()

	requirePanic(t, nil, func() {
		search.NewQuery(search.NewIndex[doc]("Bad", nil), "").Derive(nil)
	})
}

// TestPredicateNilCallbackPanicsEvenAfterError ensures a parent's sticky error
// does not suppress invocation of a supplied group construction callback.
func TestPredicateNilCallbackPanicsEvenAfterError(t *testing.T) {
	t.Parallel()

	requirePanic(t, nil, func() {
		search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
			query.Where("missing", 1)
			query.WhereGroup(nil)
		})
	})
}

// TestNestedPredicateNilCallbackPanics preserves direct invocation through the
// restricted draft, including after a predicate has recorded a sticky error.
func TestNestedPredicateNilCallbackPanics(t *testing.T) {
	t.Parallel()

	requirePanic(t, nil, func() {
		search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
			query.WhereGroup(func(predicate *search.PredicateBuilder) {
				predicate.Where("missing", 1)
				predicate.WhereGroup(nil)
			})
		})
	})
}

// TestCallbacksPropagateApplicationPanics guards all four declaration seams
// without converting application failures into domain-validation errors.
func TestCallbacksPropagateApplicationPanics(t *testing.T) {
	t.Parallel()

	marker := errors.New("application callback")

	for _, run := range []func(){
		func() {
			search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
				panic(marker)
			})
		},
		func() {
			search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
				panic(marker)
			})
		},
		func() {
			search.NewQuery(testIndex, "").Derive(func(query *search.QueryBuilder) {
				panic(marker)
			})
		},
		func() {
			search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
				query.WhereGroup(func(predicate *search.PredicateBuilder) {
					panic(marker)
				})
			})
		},
	} {
		requirePanic(t, marker, run)
	}
}

// testIndex is a valid index for query tests.
var testIndex = search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
	index.Searchable("title")
	index.Filterable("group", "price", "created_at", "on")
	index.Sortable("price", "title")
})

// TestQueryBuildsSpec checks text, predicates, and ordering survive query publication.
func TestQueryBuildsSpec(t *testing.T) {
	t.Parallel()

	query := search.NewQuery(testIndex, "red", func(query *search.QueryBuilder) {
		query.Where("group", "shoes")
		query.OrWhere("price", "<=", 100)
		query.WhereIn("group", "a", "b")
		query.WhereNotIn("price", 1, 2)
		query.OrderBy("price")
		query.OrderByDesc("title")
	})

	if err := query.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}

	got := query.Inspect()
	want := search.Spec{
		Text: []search.Term{term("red", "prefix")},
		Where: []search.Condition{
			{Field: "group", Operator: search.OpEqual, Value: "shoes"},
			{Or: true, Field: "price", Operator: search.OpLessOrEqual, Value: float64(100)},
			{Field: "group", Operator: search.OpIn, Values: []any{"a", "b"}},
			{Field: "price", Operator: search.OpNotIn, Values: []any{float64(1), float64(2)}},
		},
		Orders: []search.Order{{Field: "price"}, {Field: "title", Desc: true}},
	}

	if !specEqual(got, want) {
		t.Fatalf("Inspect() = %+v, want %+v", got, want)
	}
}

// specEqual compares normalized query snapshots while treating nil and empty
// containers as equivalent. It checks nested conditions as well as root state.
func specEqual(actual, expected search.Spec) bool {
	return slices.Equal(actual.Text, expected.Text) && slices.Equal(actual.Orders, expected.Orders) &&
		conditionsEqual(actual.Where, expected.Where)
}

// conditionsEqual compares filter trees recursively, including membership
// operands. Fixtures contain normalized comparable scalars, not arbitrary any.
func conditionsEqual(actual, expected []search.Condition) bool {
	return slices.EqualFunc(actual, expected, func(x, y search.Condition) bool {
		return x.Or == y.Or && x.Field == y.Field && x.Operator == y.Operator && x.Value == y.Value &&
			slices.Equal(x.Values, y.Values) && conditionsEqual(x.Group, y.Group)
	})
}

// TestQueryWhereGroup checks grouped predicates preserve their nested boolean structure.
func TestQueryWhereGroup(t *testing.T) {
	t.Parallel()

	query := search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("group", "shoes")
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
			predicate.Where("on", true)
			predicate.OrWhere("price", ">", 3)
		})
	})

	if err := query.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}

	where := query.Inspect().Where

	if len(where) != 2 || len(where[1].Group) != 2 || where[1].Field != "" || !where[1].Group[1].Or {
		t.Fatalf("where = %+v, want a flat condition then a group of two", where)
	}
}

// TestQueryInspectReturnsACopy verifies callers cannot mutate published query containers.
func TestQueryInspectReturnsACopy(t *testing.T) {
	t.Parallel()

	query := search.NewQuery(testIndex, "x", func(query *search.QueryBuilder) {
		query.WhereIn("group", "a", "b")
		query.OrderBy("price")
	})

	spec := query.Inspect()
	spec.Where[0].Values[0] = "changed"
	spec.Orders[0].Desc = true
	spec.Text[0].Value = "changed"

	again := query.Inspect()

	if again.Where[0].Values[0] != "a" || again.Orders[0].Desc || again.Text[0].Value != "x" {
		t.Fatalf("mutating Inspect() changed the query: %+v", again)
	}
}

// TestQueryNormalizesValues checks string filter values retain their contents.
func TestQueryNormalizesValues(t *testing.T) {
	t.Parallel()

	wantNormalizedValue(t, "text", "text")
}

// wantNormalizedValue checks a filter operand's published representation.
// It returns after declaration failure so inspection cannot mask the cause.
func wantNormalizedValue(t *testing.T, value, want any) {
	t.Helper()

	query := search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("group", value)
	})

	if err := query.Err(); err != nil {
		t.Errorf("Where(%v) failed: %v", value, err)

		return
	}

	if got := query.Inspect().Where[0].Value; got != want {
		t.Errorf("Where(%v) value = %#v, want %#v", value, got, want)
	}
}

// TestQueryNormalizesBoolean checks boolean operands retain their primitive type.
func TestQueryNormalizesBoolean(t *testing.T) {
	t.Parallel()

	wantNormalizedValue(t, true, true)
}

// TestQueryNormalizesInt8 checks small signed integers publish as float64.
func TestQueryNormalizesInt8(t *testing.T) {
	t.Parallel()

	wantNormalizedValue(t, int8(3), float64(3))
}

// TestQueryNormalizesUint16 checks unsigned integers publish as float64.
func TestQueryNormalizesUint16(t *testing.T) {
	t.Parallel()

	wantNormalizedValue(t, uint16(4), float64(4))
}

// TestQueryNormalizesNegativeInt64 checks numeric normalization retains the sign.
func TestQueryNormalizesNegativeInt64(t *testing.T) {
	t.Parallel()

	wantNormalizedValue(t, int64(-5), float64(-5))
}

// TestQueryNormalizesFloat32 checks narrow floating-point operands widen to float64.
func TestQueryNormalizesFloat32(t *testing.T) {
	t.Parallel()

	wantNormalizedValue(t, float32(1.5), float64(1.5))
}

// TestQueryNormalizesFloat64 checks existing float64 values remain unchanged.
func TestQueryNormalizesFloat64(t *testing.T) {
	t.Parallel()

	wantNormalizedValue(t, 2.25, 2.25)
}

// TestQueryNormalizesNamedString checks named operands lose their Go-specific type.
func TestQueryNormalizesNamedString(t *testing.T) {
	t.Parallel()

	type status string

	wantNormalizedValue(t, status("ok"), "ok")
}

// TestQueryNormalizesTimestamp checks offsets normalize to UTC without losing precision.
func TestQueryNormalizesTimestamp(t *testing.T) {
	t.Parallel()

	moment := time.Date(2026, time.March, 4, 12, 30, 0, 500, time.FixedZone("plus-two", 2*60*60))

	wantNormalizedValue(t, moment, "2026-03-04T10:30:00.0000005Z")
}

// wantInvalidQuery checks declaration errors retain the portable query sentinel.
// name identifies the failing declaration in diagnostics shared by these tests.
func wantInvalidQuery(t *testing.T, name string, query search.Query[doc]) {
	t.Helper()

	if err := query.Err(); !errors.Is(err, search.ErrInvalidQuery) {
		t.Errorf("%s: Err() = %v, want ErrInvalidQuery", name, err)
	}
}

// TestQueryRejectsInvalidInput checks undeclared fields fail before execution.
func TestQueryRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "undeclared field", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("missing", "x")
	}))
}

// TestQueryRejectsNonFilterableField checks searchable fields do not imply filtering support.
func TestQueryRejectsNonFilterableField(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "field not filterable", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("title", "x")
	}))
}

// TestQueryRejectsNonSortableField checks ascending ordering requires a sortable field.
func TestQueryRejectsNonSortableField(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "not sortable", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.OrderBy("group")
	}))
}

// TestQueryRejectsNonSortableFieldDescending checks descending ordering has the same restriction.
func TestQueryRejectsNonSortableFieldDescending(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "not sortable desc", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.OrderByDesc("group")
	}))
}

// TestQueryRejectsNilOperand checks missing values cannot become portable comparisons.
func TestQueryRejectsNilOperand(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "nil value", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("group", nil)
	}))
}

// TestQueryRejectsStructOperand checks comparisons accept only supported scalar values.
func TestQueryRejectsStructOperand(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "struct value", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("group", struct{}{})
	}))
}

// TestQueryRejectsSliceOperand checks ordinary comparisons cannot encode membership lists.
func TestQueryRejectsSliceOperand(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "slice value", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("group", []string{"a"})
	}))
}

// TestQueryRejectsNaN checks non-finite values cannot enter published predicates.
func TestQueryRejectsNaN(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "NaN", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("price", math.NaN())
	}))
}

// TestQueryRejectsInfinity checks infinite operands fail before backend encoding.
func TestQueryRejectsInfinity(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "infinity", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("price", math.Inf(1))
	}))
}

// TestQueryRejectsUnknownOperator checks unrecognized comparison syntax fails validation.
func TestQueryRejectsUnknownOperator(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "unknown operator", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("price", "~", 1)
	}))
}

// TestQueryRejectsNonStringOperator checks explicit operators must be strings.
func TestQueryRejectsNonStringOperator(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "non-string operator", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("price", 1, 2)
	}))
}

// TestQueryRejectsMissingOperand checks field-only comparisons are incomplete.
func TestQueryRejectsMissingOperand(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "no value", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("price")
	}))
}

// TestQueryRejectsExtraComparisonArguments checks comparisons have one operand.
func TestQueryRejectsExtraComparisonArguments(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "too many arguments", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("price", "<", 1, 2)
	}))
}

// TestQueryRejectsBooleanOrdering checks booleans cannot use ordered comparisons.
func TestQueryRejectsBooleanOrdering(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "ordering a boolean", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("on", "<", true)
	}))
}

// TestQueryRejectsEmptyMembership checks membership declarations need at least one value.
func TestQueryRejectsEmptyMembership(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "empty in", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.WhereIn[string]("group")
	}))
}

// TestQueryRejectsInvalidMembershipOperand checks each membership value is validated.
func TestQueryRejectsInvalidMembershipOperand(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "in with a bad value", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.WhereIn[any]("group", "a", nil)
	}))
}

// TestQueryRejectsNonFilterableMembership checks exclusion lists require a filterable field.
func TestQueryRejectsNonFilterableMembership(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "in on a bad field", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.WhereNotIn("title", "a")
	}))
}

// TestQueryRejectsEmptyPredicateGroup checks a group must publish at least one condition.
func TestQueryRejectsEmptyPredicateGroup(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "empty group", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
		})
	}))
}

// TestQueryRejectsInvalidGroupedTerm checks child predicate failures reach the root query.
func TestQueryRejectsInvalidGroupedTerm(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "group with a bad term", search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.WhereGroup(func(predicate *search.PredicateBuilder) {
			predicate.Where("missing", 1)
		})
	}))
}

// TestQueryRejectsInvalidIndex checks index declaration failures invalidate a query.
func TestQueryRejectsInvalidIndex(t *testing.T) {
	t.Parallel()

	wantInvalidQuery(t, "invalid index", search.NewQuery(search.NewIndex("Bad", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}), ""))
}

// TestQueryOnAnInvalidIndexCarriesTheIndexError checks query publication retains index diagnostics.
func TestQueryOnAnInvalidIndexCarriesTheIndexError(t *testing.T) {
	t.Parallel()

	err := search.NewQuery(search.NewIndex("Bad", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}), "").Err()

	if !errors.Is(err, search.ErrInvalidQuery) || !errors.Is(err, search.ErrInvalidName) {
		t.Fatalf("Err() = %v, want ErrInvalidQuery and ErrInvalidName", err)
	}
}

// TestQueryKeepsTheFirstError verifies later declarations cannot erase the first failure.
func TestQueryKeepsTheFirstError(t *testing.T) {
	t.Parallel()

	query := search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("missing", 1)
		query.Where("title", 2)
		query.OrderBy("group")
	})

	if err := query.Err(); err == nil || !errors.Is(err, search.ErrInvalidQuery) {
		t.Fatalf("Err() = %v", err)
	}

	if got := query.Err().Error(); got != search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("missing", 1)
	}).
		Err().
		Error() {
		t.Fatalf("Err() = %q is not the first error", got)
	}
}
