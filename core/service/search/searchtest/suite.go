package searchtest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
)

// config holds the settings produced by [Option] values.
type config struct{}

// Option declares a capability of the driver under test. Options never relax
// the contract's semantics; the suite has none yet, and a driver that cannot
// express a feature will declare it here, with [search.ErrUnsupported], once
// one exists.
type Option func(*config)

// suite carries the state shared by the subtests of one [Run] call.
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) search.Driver
}

// Run executes the search conformance suite against drivers produced by
// newDriver. It is called once per subtest, so each subtest gets an isolated
// driver where the backend allows it; subtests also use unique index names so
// a shared backend works, and drop their indexes when they end. Run calls
// t.Run for every subtest and marks them parallel.
//
// Infrastructure-backed drivers call Run only when testing.Short() is false.
func Run(t *testing.T, newDriver func(t *testing.T) search.Driver, opts ...Option) {
	t.Helper()

	var settings config

	for _, opt := range opts {
		opt(&settings)
	}

	s := suite{newDriver: newDriver}

	for _, group := range []struct {
		// name labels the conformance group in test output.
		name string
		// fn runs the group with its shared suite configuration.
		fn func(*testing.T, suite)
	}{
		{"Text", textCases},
		{"Filters", filterCases},
		{"Order", orderCases},
		{"Paging", pagingCases},
		{"Documents", documentCases},
		{"Lifecycle", lifecycleCases},
		{"Cancellation", cancellationCases},
		{"Concurrency", concurrencyCases},
	} {
		t.Run(group.name, func(t *testing.T) {
			group.fn(t, s)
		})
	}
}

// product is the document type of the suite. Its JSON keys are the field
// names of the test indexes.
type product struct {
	// ID is the stable document identity used for indexing and result checks.
	ID string `json:"id"`
	// Title is the searchable display text used by query fixtures.
	Title string `json:"title"`
	// Description adds searchable text outside the title for matching tests.
	Description string `json:"description"`
	// Category is the exact-match category used in filter tests.
	Category string `json:"category"`
	// Price is the numeric value used by filtering and ordering fixtures.
	Price float64 `json:"price"`
	// InStock is the boolean filter value for inventory tests.
	InStock bool `json:"in_stock"`
	// Created is the timestamp used by date comparisons and ordering tests.
	Created time.Time `json:"created_at"`
	// Rating is optional so tests can cover absent sortable values.
	Rating *float64 `json:"rating,omitempty"`
}

// rating returns a pointer to a rating value.
func rating(value float64) *float64 {
	return &value
}

// day returns midnight UTC of the given day of January 2026.
func day(d int) time.Time {
	return time.Date(2026, time.January, d, 0, 0, 0, 0, time.UTC)
}

// catalog is the fixture. Only titles and descriptions are searchable. Prices
// 15 repeat and ratings 4.5 repeat so that ordering ties are exercised, two
// products have no rating, and one category is spelled "Shoes" to exercise
// case-insensitive filters. Searchable words are chosen so that no query
// depends on stemming: "shoes" and "sneakers" are different words.
var catalog = []product{
	{
		ID:          "p01",
		Title:       "Red running shoes",
		Description: "Lightweight shoes for road running",
		Category:    "shoes",
		Price:       80,
		InStock:     true,
		Created:     day(1),
		Rating:      rating(4.5),
	},
	{
		ID:          "p02",
		Title:       "Blue running shoes",
		Description: "Cushioned shoes for trail running",
		Category:    "shoes",
		Price:       120,
		InStock:     true,
		Created:     day(2),
		Rating:      rating(3.5),
	},
	{
		ID:          "p03",
		Title:       "Red leather boots",
		Description: "Waterproof boots for winter",
		Category:    "boots",
		Price:       200,
		InStock:     false,
		Created:     day(3),
		Rating:      rating(4.5),
	},
	{
		ID:          "p04",
		Title:       "Green canvas sneakers",
		Description: "Casual sneakers for summer",
		Category:    "Shoes",
		Price:       45,
		InStock:     true,
		Created:     day(4),
		Rating:      nil,
	},
	{
		ID:          "p05",
		Title:       "Red wool socks",
		Description: "Warm socks for winter",
		Category:    "socks",
		Price:       15,
		InStock:     true,
		Created:     day(5),
		Rating:      rating(2.5),
	},
	{
		ID:          "p06",
		Title:       "Blue wool socks",
		Description: "Warm socks for winter",
		Category:    "socks",
		Price:       15,
		InStock:     false,
		Created:     day(6),
		Rating:      nil,
	},
	{
		ID:          "p07",
		Title:       "Black umbrella",
		Description: "Compact folding umbrella",
		Category:    "accessories",
		Price:       25,
		InStock:     true,
		Created:     day(7),
		Rating:      rating(5),
	},
	{
		ID:          "p08",
		Title:       "Striped wool scarf",
		Description: "Soft scarf for winter",
		Category:    "accessories",
		Price:       30,
		InStock:     false,
		Created:     day(8),
		Rating:      rating(4),
	},
	{
		ID:          "p09",
		Title:       "Shoes for running",
		Description: "Sturdy footwear for the gym",
		Category:    "shoes",
		Price:       60,
		InStock:     true,
		Created:     day(9),
		Rating:      rating(3),
	},
	{
		ID:          "p10",
		Title:       "Yellow rain jacket",
		Description: "Waterproof jacket for rain",
		Category:    "jackets",
		Price:       150,
		InStock:     true,
		Created:     day(10),
		Rating:      rating(4.5),
	},
}

// allIDs lists every catalog ID in ascending order.
var allIDs = []string{"p01", "p02", "p03", "p04", "p05", "p06", "p07", "p08", "p09", "p10"}

// newIndex returns the test index called name.
func newIndex(name string) search.Index[product] {
	return search.NewIndex(name, func(product product) string {
		return product.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title", "description")
		index.Filterable("category", "price", "in_stock", "created_at")
		index.Sortable("price", "created_at", "rating", "title", "category")
	})
}

// fixture is an engine with one synced test index.
type fixture struct {
	// engine is the facade registered with the fixture index.
	engine *search.Engine
	// index describes the catalog schema used by this fixture.
	index search.Index[product]
}

// sub runs fn as a parallel subtest.
func (suite suite) sub(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Parallel()

		fn(t)
	})
}

// fixtureFor creates a driver and an engine over a uniquely named index made
// by index, drops the index when the test ends, and syncs it when synced is
// true.
func (suite suite) fixtureFor(t *testing.T, index func(name string) search.Index[product], synced bool) fixture {
	t.Helper()

	name := "conf_" + strings.ToLower(rand.Text())[:16]
	fx := fixture{index: index(name)}
	fx.engine = search.New(suite.newDriver(t), func(engine *search.Builder) {
		engine.Indexes(fx.index)
	})
	t.Cleanup(func() {
		if err := fx.engine.Drop(context.WithoutCancel(t.Context()), fx.index); err != nil {
			t.Errorf("cleanup Drop(%s) = %v", name, err)
		}
	})

	if synced {
		must(t, "Sync", fx.engine.Sync(t.Context()))
	}

	return fx
}

// empty returns a synced fixture with no documents.
func (suite suite) empty(t *testing.T) fixture {
	t.Helper()

	return suite.fixtureFor(t, newIndex, true)
}

// loaded returns a synced fixture holding the catalog.
func (suite suite) loaded(t *testing.T) fixture {
	t.Helper()

	fx := suite.empty(t)

	must(t, "Put", fx.engine.Put(t.Context(), fx.index, catalog...))

	return fx
}

// query publishes a query on the fixture's index with optional draft policies.
// Callers refine through callbacks rather than mutating an inferred Query value;
// forwarding the callbacks preserves the public constructor's order and errors.
func (fixture fixture) query(text string, configure ...search.QueryBuilderFunc) search.Query[product] {
	return search.NewQuery(fixture.index, text, configure...)
}

// search runs query for the first page of 100 and fails the test on an error. It
// checks that the total matches the hits, which holds below the page size.
func (fixture fixture) search(t *testing.T, query search.Query[product]) pagination.Page[search.Hit[product]] {
	t.Helper()

	page, err := fixture.engine.Search(t.Context(), query, pagination.PageParams{Page: 1, PerPage: 100})

	must(t, "Search", err)

	if page.Total != int64(len(page.Items)) {
		t.Fatalf("Search total = %d with %d hits on one page", page.Total, len(page.Items))
	}

	return page
}

// ids returns the IDs of the first page of query, in result order.
func (fixture fixture) ids(t *testing.T, query search.Query[product]) []string {
	t.Helper()

	return idsOf(fixture.search(t, query))
}

// idsOf returns the IDs of a page's hits in order.
func idsOf(page pagination.Page[search.Hit[product]]) []string {
	ids := make([]string, len(page.Items))

	for i, hit := range page.Items {
		ids[i] = hit.ID
	}

	return ids
}

// wantSet fails the test unless got holds exactly the IDs in want, in any
// order. Text queries use it because relevance order differs between
// drivers.
func wantSet(t *testing.T, what string, got []string, want ...string) {
	t.Helper()

	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("%s = %v, want %v in any order", what, got, want)
	}
}

// wantOrder fails the test unless got is exactly want, in order.
func wantOrder(t *testing.T, what string, got []string, want ...string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// must fails the test immediately when err is not nil.
func must(t *testing.T, op string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

// wantErr fails the test unless err wraps target.
func wantErr(t *testing.T, op string, err, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("%s error = %v, want %v", op, err, target)
	}
}

// textCases covers query text: words, phrases, exclusions, prefixes, case,
// and the scores of hits.
func textCases(t *testing.T, s suite) {
	s.sub(t, "AllWordsRequired", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "shoes", fx.ids(t, fx.query("shoes")), "p01", "p02", "p09")
		wantSet(t, "red shoes", fx.ids(t, fx.query("red shoes")), "p01")
		wantSet(t, "red wool", fx.ids(t, fx.query("red wool")), "p05")
		wantSet(t, "red umbrella", fx.ids(t, fx.query("red umbrella")))
	})

	s.sub(t, "CaseInsensitive", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "RED Shoes", fx.ids(t, fx.query("RED Shoes")), "p01")
		wantSet(t, "UMBRELLA", fx.ids(t, fx.query("UMBRELLA")), "p07")
	})

	s.sub(t, "SearchesEverySearchableField", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "waterproof", fx.ids(t, fx.query("waterproof")), "p03", "p10")
		wantSet(t, "lightweight", fx.ids(t, fx.query("lightweight")), "p01")
	})

	s.sub(t, "IgnoresFieldsThatAreNotSearchable", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "accessories", fx.ids(t, fx.query("accessories")))
	})

	s.sub(t, "Phrase", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "plain words", fx.ids(t, fx.query("running shoes")), "p01", "p02", "p09")
		wantSet(t, "phrase", fx.ids(t, fx.query(`"running shoes"`)), "p01", "p02")
		wantSet(t, "reversed phrase", fx.ids(t, fx.query(`"shoes running"`)))
		wantSet(t, "phrase and word", fx.ids(t, fx.query(`"running shoes" blue`)), "p02")
	})

	s.sub(t, "UnclosedQuoteRunsToTheEnd", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "unclosed phrase", fx.ids(t, fx.query(`"running shoes`)), "p01", "p02")
	})

	s.sub(t, "Exclusion", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "word minus word", fx.ids(t, fx.query("shoes -red")), "p02", "p09")
		wantSet(t, "word minus phrase", fx.ids(t, fx.query(`shoes -"running shoes"`)), "p09")
		wantSet(t, "exclusion alone", fx.ids(t, fx.query("-red")), "p02", "p04", "p06", "p07", "p08", "p09", "p10")
		wantSet(t, "two exclusions", fx.ids(t, fx.query("shoes -red -blue")), "p09")
	})

	s.sub(t, "LastPlainWordIsAPrefix", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "soc", fx.ids(t, fx.query("soc")), "p05", "p06")
		wantSet(t, "wool soc", fx.ids(t, fx.query("wool soc")), "p05", "p06")
		wantSet(t, "wint", fx.ids(t, fx.query("wint")), "p03", "p05", "p06", "p08")
		wantSet(t, "prefix before an exclusion", fx.ids(t, fx.query("soc -red")), "p06")
		wantSet(t, "complete word", fx.ids(t, fx.query("socks")), "p05", "p06")
	})

	s.sub(t, "OtherWordsAreLiteral", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "prefix not last", fx.ids(t, fx.query("soc wool")))
		wantSet(t, "quoted prefix", fx.ids(t, fx.query(`"wool soc"`)))
		wantSet(t, "excluded word is not a prefix", fx.ids(t, fx.query("wool -soc")), "p05", "p06", "p08")
	})

	s.sub(t, "TextWithoutWordsMatchesAll", func(t *testing.T) {
		fx := s.loaded(t)

		for _, text := range []string{"", "   ", `"`, `""`, "-", "- -", "!!!", `"-"`, `-"`, "--"} {
			got := fx.ids(t, fx.query(text))

			if len(got) != len(catalog) {
				t.Errorf("text %q matched %v, want every document", text, got)
			}
		}
	})

	s.sub(t, "EmptyTextOrdersByID", func(t *testing.T) {
		fx := s.loaded(t)

		page := fx.search(t, fx.query(""))

		wantOrder(t, "empty text", idsOf(page), allIDs...)

		for _, hit := range page.Items {
			if hit.Score != 0 {
				t.Errorf("score of %s = %v for empty text, want 0", hit.ID, hit.Score)
			}
		}
	})

	s.sub(t, "ScoresDescend", func(t *testing.T) {
		fx := s.loaded(t)

		page := fx.search(t, fx.query("winter"))

		if len(page.Items) != 4 {
			t.Fatalf("winter matched %v, want four documents", idsOf(page))
		}

		for i, hit := range page.Items {
			if hit.Score <= 0 {
				t.Errorf("score of %s = %v, want above 0", hit.ID, hit.Score)
			}

			if i > 0 && hit.Score > page.Items[i-1].Score {
				t.Errorf("score of %s = %v rises above the previous hit's %v", hit.ID, hit.Score, page.Items[i-1].Score)
			}
		}
	})

	s.sub(t, "OrderIsStableAcrossCalls", func(t *testing.T) {
		fx := s.loaded(t)

		first := fx.ids(t, fx.query("winter"))

		for range 3 {
			wantOrder(t, "repeated query", fx.ids(t, fx.query("winter")), first...)
		}
	})
}

// filterCases covers conditions, their operators, grouping, and values.
func filterCases(t *testing.T, s suite) {
	s.sub(t, "TypedComparisonConstants", func(t *testing.T) {
		fx := s.loaded(t)
		query := search.NewQuery(fx.index, "", func(query *search.QueryBuilder) {
			query.Where("price", search.OpGreater, 50)
			query.Where("price", search.OpLessOrEqual, 120)
		})

		wantOrder(t, "typed numeric comparisons", fx.ids(t, query), "p01", "p02", "p09")
	})

	s.sub(t, "EqualIsCaseInsensitive", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "shoes", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "shoes")
		})), "p01", "p02", "p04", "p09")
		wantOrder(t, "SHOES", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "SHOES")
		})), "p01", "p02", "p04", "p09")
		wantOrder(t, "= operator", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "=", "Socks")
		})), "p05", "p06")
	})

	s.sub(t, "NotEqual", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "!=", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "!=", "Shoes")
		})),
			"p03", "p05", "p06", "p07", "p08", "p10")
	})

	s.sub(t, "NumberComparisons", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "<=", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("price", "<=", 30)
		})), "p05", "p06", "p07", "p08")
		wantOrder(t, "<", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("price", "<", 30)
		})), "p05", "p06", "p07")
		wantOrder(t, ">", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("price", ">", 150)
		})), "p03")
		wantOrder(t, ">=", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("price", ">=", 120)
		})), "p02", "p03", "p10")
		wantOrder(t, "=", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("price", 15)
		})), "p05", "p06")
		wantOrder(t, "range", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("price", ">", 25)
			query.Where("price", "<", 100)
		})),
			"p01", "p04", "p08", "p09")
	})

	s.sub(t, "Booleans", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "false", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("in_stock", false)
		})), "p03", "p06", "p08")
		wantOrder(t, "true", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("in_stock", true)
			query.Where("category", "socks")
		})), "p05")
	})

	s.sub(t, "Times", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, ">=", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("created_at", ">=", day(9))
		})), "p09", "p10")
		wantOrder(t, "<", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("created_at", "<", day(3))
		})), "p01", "p02")

		plusTwo := time.FixedZone("plus-two", 2*60*60)
		sameInstant := time.Date(2026, time.January, 5, 2, 0, 0, 0, plusTwo)

		wantOrder(t, "same instant in another zone", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("created_at", sameInstant)
		})), "p05")
	})

	s.sub(t, "In", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "in strings", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.WhereIn("category", "boots", "SOCKS")
		})), "p03", "p05", "p06")
		wantOrder(t, "in numbers", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.WhereIn("price", 15, 25)
		})), "p05", "p06", "p07")
		wantOrder(t, "not in", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.WhereNotIn[string]("category", "shoes", "socks")
		})),
			"p03", "p07", "p08", "p10")
	})

	s.sub(t, "Or", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "or", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "boots")
			query.OrWhere("category", "jackets")
		})), "p03", "p10")
	})

	s.sub(t, "AndBindsTighterThanOr", func(t *testing.T) {
		fx := s.loaded(t)

		query := fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "socks")
			query.Where("in_stock", true)
			query.OrWhere("category", "boots")
		})

		wantOrder(t, "a and b or c", fx.ids(t, query), "p03", "p05")
	})

	s.sub(t, "Groups", func(t *testing.T) {
		fx := s.loaded(t)

		query := fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "shoes")
			query.WhereGroup(func(predicate *search.PredicateBuilder) {
				predicate.Where("price", "<", 50)
				predicate.OrWhere("price", ">", 100)
			})
		})

		wantOrder(t, "and group", fx.ids(t, query), "p02", "p04")

		query = fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "boots")
			query.WhereGroup(func(predicate *search.PredicateBuilder) {
				predicate.Where("in_stock", true)
				predicate.Where("price", "<", 100)
			})
			query.OrWhere("category", "jackets")
		})

		wantOrder(t, "group in a chain", fx.ids(t, query), "p10")
	})

	s.sub(t, "FiltersCombineWithText", func(t *testing.T) {
		fx := s.loaded(t)

		wantSet(t, "text and category", fx.ids(t, fx.query("wool", func(query *search.QueryBuilder) {
			query.Where("category", "socks")
		})), "p05", "p06")
		wantSet(t, "text and price", fx.ids(t, fx.query("wool", func(query *search.QueryBuilder) {
			query.Where("price", ">", 20)
		})), "p08")
		wantSet(t, "excluded word and filter", fx.ids(t, fx.query("winter -wool", func(query *search.QueryBuilder) {
			query.Where("in_stock", false)
		})), "p03")
	})
}

// orderCases covers orders, their tie-breaks, and stability.
func orderCases(t *testing.T, s suite) {
	s.sub(t, "NumberAscending", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "price asc", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderBy("price")
		})),
			"p05", "p06", "p07", "p08", "p04", "p09", "p01", "p02", "p10", "p03")
	})

	s.sub(t, "NumberDescendingTiesByAscendingID", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "price desc", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderByDesc("price")
		})),
			"p03", "p10", "p02", "p01", "p09", "p04", "p08", "p07", "p05", "p06")
	})

	s.sub(t, "SeveralKeys", func(t *testing.T) {
		fx := s.loaded(t)

		query := fx.query("", func(query *search.QueryBuilder) {
			query.OrderBy("category")
			query.OrderByDesc("price")
		})

		wantOrder(t, "category then price desc", fx.ids(t, query),
			"p08", "p07", "p03", "p10", "p02", "p01", "p09", "p04", "p05", "p06")
	})

	s.sub(t, "StringsByLowercaseBytes", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "title asc", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderBy("title")
		})),
			"p07", "p02", "p06", "p04", "p03", "p01", "p05", "p09", "p08", "p10")
		wantOrder(t, "title desc", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderByDesc("title")
		})),
			"p10", "p08", "p09", "p05", "p01", "p03", "p04", "p06", "p02", "p07")
	})

	s.sub(t, "TimesChronologically", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "created desc", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderByDesc("created_at")
		})),
			"p10", "p09", "p08", "p07", "p06", "p05", "p04", "p03", "p02", "p01")
	})

	s.sub(t, "MissingValuesSortLast", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "rating asc", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderBy("rating")
		})),
			"p05", "p09", "p02", "p08", "p01", "p03", "p10", "p07", "p04", "p06")
		wantOrder(t, "rating desc", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderByDesc("rating")
		})),
			"p07", "p01", "p03", "p10", "p08", "p02", "p09", "p05", "p04", "p06")
	})

	s.sub(t, "OrdersApplyBeforeRelevance", func(t *testing.T) {
		fx := s.loaded(t)

		wantOrder(t, "text then order", fx.ids(t, fx.query("wool", func(query *search.QueryBuilder) {
			query.OrderBy("created_at")
		})), "p05", "p06", "p08")
		wantOrder(t, "text then order desc", fx.ids(t, fx.query("wool", func(query *search.QueryBuilder) {
			query.OrderByDesc("created_at")
		})), "p08", "p06", "p05")
	})

	s.sub(t, "FilteredOrder", func(t *testing.T) {
		fx := s.loaded(t)

		query := fx.query("", func(query *search.QueryBuilder) {
			query.Where("in_stock", true)
			query.OrderByDesc("price")
		})

		wantOrder(t, "in stock by price", fx.ids(t, query), "p10", "p02", "p01", "p09", "p04", "p07", "p05")
	})

	s.sub(t, "OrderIsStableAcrossCalls", func(t *testing.T) {
		fx := s.loaded(t)

		first := fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.OrderBy("price")
		}))

		for range 3 {
			wantOrder(t, "repeated query", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
				query.OrderBy("price")
			})), first...)
		}
	})
}

// pagingCases covers pages, the page cap, and the capped total.
func pagingCases(t *testing.T, s suite) {
	s.sub(t, "PagesPartitionTheResult", func(t *testing.T) {
		fx := s.loaded(t)

		var all []string

		for page, wantItems := 1, [][]string{{"p01", "p02", "p03"}, {"p04", "p05", "p06"}, {"p07", "p08", "p09"}, {"p10"}}; page <= 4; page++ {
			got, err := fx.engine.Search(t.Context(), fx.query(""), pagination.PageParams{Page: page, PerPage: 3})

			must(t, "Search", err)

			wantOrder(t, "page items", idsOf(got), wantItems[page-1]...)

			if got.Total != 10 || got.LastPage != 4 || got.CurrentPage != page || got.PerPage != 3 {
				t.Fatalf("page %d = total %d last %d current %d per page %d, want 10, 4, %d, 3",
					page, got.Total, got.LastPage, got.CurrentPage, got.PerPage, page)
			}

			all = append(all, idsOf(got)...)
		}

		wantOrder(t, "all pages", all, allIDs...)
	})

	s.sub(t, "PageBeyondTheEndIsEmpty", func(t *testing.T) {
		fx := s.loaded(t)

		got, err := fx.engine.Search(t.Context(), fx.query(""), pagination.PageParams{Page: 5, PerPage: 3})

		must(t, "Search", err)

		if len(got.Items) != 0 || got.Total != 10 {
			t.Fatalf("page 5 = %v with total %d, want no hits and total 10", idsOf(got), got.Total)
		}
	})

	s.sub(t, "TextPagesCoverTheResultOnce", func(t *testing.T) {
		fx := s.loaded(t)

		var all []string

		for page := 1; page <= 2; page++ {
			got, err := fx.engine.Search(t.Context(), fx.query("winter"), pagination.PageParams{
				Page: page, PerPage: 2,
			})

			must(t, "Search", err)

			if got.Total != 4 {
				t.Fatalf("total = %d, want 4", got.Total)
			}

			all = append(all, idsOf(got)...)
		}

		wantSet(t, "pages of winter", all, "p03", "p05", "p06", "p08")
	})

	s.sub(t, "PageTimesPerPageMayReachTheCap", func(t *testing.T) {
		fx := s.loaded(t)

		got, err := fx.engine.Search(t.Context(), fx.query(""), pagination.PageParams{Page: 10, PerPage: 100})

		must(t, "Search at the cap", err)

		if len(got.Items) != 0 || got.Total != 10 {
			t.Fatalf("page 10 of 100 = %v with total %d, want no hits and total 10", idsOf(got), got.Total)
		}
	})

	s.sub(t, "PageBeyondTheCapIsInvalid", func(t *testing.T) {
		fx := s.loaded(t)

		for _, params := range []pagination.PageParams{{Page: 11, PerPage: 100}, {Page: 101, PerPage: 10}} {
			_, err := fx.engine.Search(t.Context(), fx.query(""), params)

			wantErr(t, "Search beyond the cap", err, search.ErrInvalidQuery)
		}
	})

	s.sub(t, "TotalIsCappedAtMaxHits", func(t *testing.T) {
		fx := s.empty(t)

		const count = search.MaxHits + 100
		docs := make([]product, count)

		for i := range docs {
			docs[i] = product{
				ID:          "i" + pad(i),
				Title:       "item " + pad(i),
				Description: "bulk document",
				Category:    "bulk",
				Price:       float64(i),
			}
		}

		must(t, "Put", fx.engine.Put(t.Context(), fx.index, docs...))

		first, err := fx.engine.Search(t.Context(), fx.query("item"), pagination.PageParams{Page: 1, PerPage: 10})

		must(t, "Search", err)

		if first.Total != search.MaxHits || first.LastPage != search.MaxHits/10 || len(first.Items) != 10 {
			t.Fatalf("first page = total %d last page %d with %d hits, want %d, %d, 10",
				first.Total, first.LastPage, len(first.Items), search.MaxHits, search.MaxHits/10)
		}

		last, err := fx.engine.Search(t.Context(), fx.query("item"), pagination.PageParams{Page: 100, PerPage: 10})

		must(t, "Search last page", err)

		if len(last.Items) != 10 {
			t.Fatalf("page 100 holds %d hits, want 10", len(last.Items))
		}

		_, err = fx.engine.Search(t.Context(), fx.query("item"), pagination.PageParams{Page: 101, PerPage: 10})

		wantErr(t, "Search past the cap", err, search.ErrInvalidQuery)

		ordered, err := fx.engine.Search(t.Context(), fx.query("", func(query *search.QueryBuilder) {
			query.OrderByDesc("price")
		}), pagination.PageParams{Page: 1, PerPage: 3})

		must(t, "Search ordered", err)

		wantOrder(t, "top prices", idsOf(ordered), "i"+pad(count-1), "i"+pad(count-2), "i"+pad(count-3))
	})
}

// pad formats n as four digits, so IDs sort like numbers.
func pad(n int) string {
	return fmt.Sprintf("%04d", n)
}

// documentCases covers IDs, round trips, upserts, and deletes.
func documentCases(t *testing.T, s suite) {
	s.sub(t, "HitsCarryTheStoredDocument", func(t *testing.T) {
		fx := s.loaded(t)

		page := fx.search(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "boots")
		}))

		if len(page.Items) != 1 || page.Items[0].ID != "p03" {
			t.Fatalf("boots matched %v, want p03", idsOf(page))
		}

		got, want := page.Items[0].Document, catalog[2]

		if got.ID != want.ID || got.Title != want.Title || got.Description != want.Description ||
			got.Category != want.Category || got.Price != want.Price || got.InStock != want.InStock ||
			!got.Created.Equal(want.Created) || got.Rating == nil || *got.Rating != *want.Rating {
			t.Fatalf("document = %+v, want %+v", got, want)
		}

		if absent := fx.search(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "shoes")
			query.Where("price", 45)
		})).Items[0].Document; absent.Rating != nil {
			t.Fatalf("document without a rating came back with %v", *absent.Rating)
		}
	})

	s.sub(t, "IDsMayUseTheFullAlphabet", func(t *testing.T) {
		fx := s.empty(t)

		long := strings.Repeat("x", 255)
		ids := []string{"A-b_C0", "UPPER", "with-dash", "with_underscore", "0123456789", long}

		for _, id := range ids {
			must(t, "Put "+id[:min(len(id), 12)], fx.engine.Put(t.Context(), fx.index, product{
				ID: id, Title: "alphabet",
			}))
		}

		wantSet(t, "stored IDs", fx.ids(t, fx.query("alphabet")), ids...)
	})

	s.sub(t, "InvalidIDWritesNothing", func(t *testing.T) {
		fx := s.empty(t)

		for _, id := range []string{"", "has space", "slash/id", "dot.id", "café", strings.Repeat("x", 256)} {
			docs := []product{{ID: "good", Title: "valid"}, {ID: id, Title: "invalid"}}

			wantErr(t, "Put with invalid ID", fx.engine.Put(t.Context(), fx.index, docs...), search.ErrInvalidID)
		}

		if got := fx.ids(t, fx.query("")); len(got) != 0 {
			t.Fatalf("documents after rejected Puts = %v, want none", got)
		}
	})

	s.sub(t, "PutReplacesTheDocument", func(t *testing.T) {
		fx := s.empty(t)

		must(t, "first Put", fx.engine.Put(t.Context(), fx.index, product{
			ID:       "a",
			Title:    "red hat",
			Category: "hats",
			Price:    10,
		}))
		must(t, "second Put", fx.engine.Put(t.Context(), fx.index, product{
			ID:       "a",
			Title:    "green hat",
			Category: "caps",
			Price:    20,
		}))

		wantSet(t, "old word", fx.ids(t, fx.query("red")))
		wantSet(t, "new word", fx.ids(t, fx.query("green")), "a")
		wantSet(t, "old category", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "hats")
		})))
		wantSet(t, "new category", fx.ids(t, fx.query("", func(query *search.QueryBuilder) {
			query.Where("category", "caps")
		})), "a")
		wantSet(t, "every document", fx.ids(t, fx.query("")), "a")
	})

	s.sub(t, "LastDuplicateInOneCallWins", func(t *testing.T) {
		fx := s.empty(t)

		must(t, "Put", fx.engine.Put(t.Context(), fx.index,
			product{ID: "a", Title: "alpha"}, product{ID: "b", Title: "other"}, product{ID: "a", Title: "beta"}))

		wantSet(t, "alpha", fx.ids(t, fx.query("alpha")))
		wantSet(t, "beta", fx.ids(t, fx.query("beta")), "a")
		wantSet(t, "every document", fx.ids(t, fx.query("")), "a", "b")
	})

	s.sub(t, "PutWithoutDocumentsSucceeds", func(t *testing.T) {
		fx := s.empty(t)

		must(t, "Put", fx.engine.Put(t.Context(), fx.index))
	})

	s.sub(t, "WritesAreVisibleImmediately", func(t *testing.T) {
		fx := s.empty(t)

		for i := range 5 {
			id := "doc" + pad(i)

			must(t, "Put", fx.engine.Put(t.Context(), fx.index, product{ID: id, Title: "immediate"}))

			if got := fx.ids(t, fx.query("immediate")); len(got) != i+1 {
				t.Fatalf("documents after Put %d = %v, want %d", i, got, i+1)
			}
		}

		must(t, "Delete", fx.engine.Delete(t.Context(), fx.index, "doc0000"))
		wantSet(t, "after Delete", fx.ids(t, fx.query("immediate")), "doc0001", "doc0002", "doc0003", "doc0004")
	})

	s.sub(t, "DeleteIsIdempotent", func(t *testing.T) {
		fx := s.loaded(t)

		must(t, "Delete", fx.engine.Delete(t.Context(), fx.index, "p01", "p02", "missing"))
		wantOrder(t, "after Delete", fx.ids(t, fx.query("")), allIDs[2:]...)
		wantSet(t, "deleted word", fx.ids(t, fx.query("lightweight")))

		must(t, "second Delete", fx.engine.Delete(t.Context(), fx.index, "p01", "p02", "missing"))
		must(t, "Delete nothing", fx.engine.Delete(t.Context(), fx.index))
		wantErr(t, "Delete invalid ID", fx.engine.Delete(t.Context(), fx.index, "p03", "bad id"), search.ErrInvalidID)
		wantOrder(t, "after invalid Delete", fx.ids(t, fx.query("")), allIDs[2:]...)
	})
}

// countingDriver counts the calls that reach the wrapped driver.
type countingDriver struct {
	// Driver receives operations after their calls have been counted.
	search.Driver

	// calls counts operations observed by this fixture.
	calls int
}

// Sync counts the call and passes it on.
func (driver *countingDriver) Sync(ctx context.Context, schemas []search.Schema) error {
	driver.calls++

	return driver.Driver.Sync(ctx, schemas)
}

// Check counts the call and passes it on.
func (driver *countingDriver) Check(ctx context.Context, schemas []search.Schema) error {
	driver.calls++

	return driver.Driver.Check(ctx, schemas)
}

// Put counts the call and passes it on.
func (driver *countingDriver) Put(ctx context.Context, schema search.Schema, docs []search.Document) error {
	driver.calls++

	return driver.Driver.Put(ctx, schema, docs)
}

// Delete counts the call and passes it on.
func (driver *countingDriver) Delete(ctx context.Context, schema search.Schema, ids []string) error {
	driver.calls++

	return driver.Driver.Delete(ctx, schema, ids)
}

// Search counts the call and passes it on.
func (driver *countingDriver) Search(ctx context.Context, schema search.Schema, spec search.Spec, limit, offset int) (search.Matches, error) {
	driver.calls++

	return driver.Driver.Search(ctx, schema, spec, limit, offset)
}

// Drop counts the call and passes it on.
func (driver *countingDriver) Drop(ctx context.Context, schema search.Schema) error {
	driver.calls++

	return driver.Driver.Drop(ctx, schema)
}

// lifecycleCases covers Sync, Check, Drop, and the errors of an index that
// was never synced.
func lifecycleCases(t *testing.T, s suite) {
	s.sub(t, "InvalidKeyRegistrationFailsEveryOperation", func(t *testing.T) {
		driver := s.newDriver(t)
		index := search.NewIndex[product]("conf_"+strings.ToLower(rand.Text())[:16], nil,
			func(index *search.IndexBuilder) {
				index.Searchable("title")
			})
		t.Cleanup(func() {
			if err := driver.Drop(context.WithoutCancel(t.Context()), index.Schema()); err != nil {
				t.Errorf("cleanup Drop = %v", err)
			}
		})

		engine := search.New(driver, func(engine *search.Builder) {
			engine.Indexes(index)
		})

		ctx := t.Context()

		wantErr(t, "Sync", engine.Sync(ctx), search.ErrInvalidName)
		wantErr(t, "Check", engine.Check(ctx), search.ErrInvalidName)
		wantErr(t, "Put", engine.Put(ctx, index, catalog[0]), search.ErrInvalidName)
		wantErr(t, "Delete", engine.Delete(ctx, index, "p01"), search.ErrInvalidName)
		wantErr(t, "Drop", engine.Drop(ctx, index), search.ErrInvalidName)

		_, err := engine.Search(ctx, search.NewQuery(index, ""), pagination.PageParams{})

		wantErr(t, "Search", err, search.ErrInvalidName)
		wantErr(t, "backend unchanged", driver.Check(ctx, []search.Schema{index.Schema()}), search.ErrNotFound)
	})

	s.sub(t, "UnsyncedIndexIsNotFound", func(t *testing.T) {
		fx := s.fixtureFor(t, newIndex, false)
		ctx := t.Context()

		wantErr(t, "Put", fx.engine.Put(ctx, fx.index, catalog...), search.ErrNotFound)

		_, err := fx.engine.Search(ctx, fx.query(""), pagination.PageParams{})

		wantErr(t, "Search", err, search.ErrNotFound)
		wantErr(t, "Delete", fx.engine.Delete(ctx, fx.index, "p01"), search.ErrNotFound)
		wantErr(t, "Check", fx.engine.Check(ctx), search.ErrNotFound)
		must(t, "Drop", fx.engine.Drop(ctx, fx.index))

		must(t, "Sync", fx.engine.Sync(ctx))
		must(t, "Check after Sync", fx.engine.Check(ctx))
		must(t, "Put after Sync", fx.engine.Put(ctx, fx.index, catalog...))
	})

	s.sub(t, "SyncIsSafeToRepeatAndKeepsDocuments", func(t *testing.T) {
		fx := s.loaded(t)

		must(t, "second Sync", fx.engine.Sync(t.Context()))
		must(t, "Check", fx.engine.Check(t.Context()))

		wantOrder(t, "documents after Sync", fx.ids(t, fx.query("")), allIDs...)
		wantSet(t, "text after Sync", fx.ids(t, fx.query("red shoes")), "p01")
	})

	s.sub(t, "ChangedDefinitionFailsCheckUntilSync", func(t *testing.T) {
		oldDefinition := func(name string) search.Index[product] {
			return search.NewIndex(name, func(product product) string {
				return product.ID
			}, func(index *search.IndexBuilder) {
				index.Searchable("title", "description")
				index.Filterable("category")
				index.Sortable("price")
			})
		}

		fx := s.fixtureFor(t, oldDefinition, true)

		must(t, "Put", fx.engine.Put(t.Context(), fx.index, catalog...))
		must(t, "Check of the synced definition", fx.engine.Check(t.Context()))

		changed := search.NewIndex(fx.index.Name(), func(product product) string {
			return product.ID
		}, func(index *search.IndexBuilder) {
			index.Searchable("title")
			index.Filterable("category", "price")
			index.Sortable("price", "created_at")
		})

		next := search.New(fx.engine.Driver(), func(engine *search.Builder) {
			engine.Indexes(changed)
		})

		wantErr(t, "Check of the new definition", next.Check(t.Context()), search.ErrSchemaChanged)

		must(t, "Sync of the new definition", next.Sync(t.Context()))
		must(t, "Check after Sync", next.Check(t.Context()))
		wantErr(t, "Check of the old definition", fx.engine.Check(t.Context()), search.ErrSchemaChanged)

		cheap, err := next.Search(t.Context(), search.NewQuery(changed, "", func(query *search.QueryBuilder) {
			query.Where("price", "<", 30)
			query.OrderBy("created_at")
		}),
			pagination.PageParams{Page: 1, PerPage: 100})

		must(t, "Search by a newly filterable field", err)
		wantOrder(t, "stored documents filtered after Sync", idsOf(cheap), "p05", "p06", "p07")

		described, err := next.Search(t.Context(), search.NewQuery(changed, "waterproof"), pagination.PageParams{
			Page: 1, PerPage: 100,
		})

		must(t, "Search a field no longer searchable", err)

		if len(described.Items) != 0 {
			t.Fatalf("waterproof matched %v after description stopped being searchable, want none", idsOf(described))
		}
	})

	s.sub(t, "DropRemovesTheIndex", func(t *testing.T) {
		fx := s.loaded(t)
		ctx := t.Context()

		must(t, "Drop", fx.engine.Drop(ctx, fx.index))

		_, err := fx.engine.Search(ctx, fx.query(""), pagination.PageParams{})

		wantErr(t, "Search after Drop", err, search.ErrNotFound)
		wantErr(t, "Put after Drop", fx.engine.Put(ctx, fx.index, catalog[0]), search.ErrNotFound)
		must(t, "second Drop", fx.engine.Drop(ctx, fx.index))

		must(t, "Sync after Drop", fx.engine.Sync(ctx))

		if got := fx.ids(t, fx.query("")); len(got) != 0 {
			t.Fatalf("recreated index holds %v, want it empty", got)
		}

		must(t, "Put after recreating", fx.engine.Put(ctx, fx.index, catalog...))
	})

	s.sub(t, "SeveralIndexesAreIndependent", func(t *testing.T) {
		first := s.loaded(t)
		second := s.empty(t)

		must(t, "Put", second.engine.Put(t.Context(), second.index, product{ID: "only", Title: "red shoes"}))

		wantOrder(t, "second index", second.ids(t, second.query("")), "only")
		wantOrder(t, "first index", first.ids(t, first.query("")), allIDs...)
	})

	s.sub(t, "SyncCreatesEveryRegisteredIndex", func(t *testing.T) {
		driver := s.newDriver(t)
		one := newIndex("conf_" + strings.ToLower(rand.Text())[:16])
		two := newIndex("conf_" + strings.ToLower(rand.Text())[:16])
		engine := search.New(driver, func(engine *search.Builder) {
			engine.Indexes(one, two)
		})
		t.Cleanup(func() {
			for _, index := range []search.Indexer{one, two} {
				if err := engine.Drop(context.WithoutCancel(t.Context()), index); err != nil {
					t.Errorf("cleanup Drop = %v", err)
				}
			}
		})

		wantErr(t, "Check before Sync", engine.Check(t.Context()), search.ErrNotFound)
		must(t, "Sync", engine.Sync(t.Context()))
		must(t, "Check", engine.Check(t.Context()))
		must(t, "Put into the second index", engine.Put(t.Context(), two, catalog[0]))
	})

	s.sub(t, "UnregisteredIndexNeverReachesTheDriver", func(t *testing.T) {
		counter := &countingDriver{Driver: s.newDriver(t)}
		registered := newIndex("conf_" + strings.ToLower(rand.Text())[:16])
		other := newIndex("conf_" + strings.ToLower(rand.Text())[:16])
		engine := search.New(counter, func(engine *search.Builder) {
			engine.Indexes(registered)
		})

		ctx := t.Context()

		wantErr(t, "Put", engine.Put(ctx, other, catalog[0]), search.ErrNotFound)

		_, err := engine.Search(ctx, search.NewQuery(other, ""), pagination.PageParams{})

		wantErr(t, "Search", err, search.ErrNotFound)
		wantErr(t, "Delete", engine.Delete(ctx, other, "p01"), search.ErrNotFound)
		wantErr(t, "Drop", engine.Drop(ctx, other), search.ErrNotFound)

		redefined := search.NewIndex(registered.Name(), func(product product) string {
			return product.ID
		}, func(index *search.IndexBuilder) {
			index.Searchable(registered.SearchableValue()...)
			index.Filterable(registered.FilterableValue()...)
			index.Filterable("rating")
			index.Sortable(registered.SortableValue()...)
		})

		wantErr(t, "Put of another definition", engine.Put(ctx, redefined, catalog[0]), search.ErrNotFound)

		if counter.calls != 0 {
			t.Fatalf("driver received %d calls for unregistered indexes, want 0", counter.calls)
		}
	})

	s.sub(t, "InvalidQueryNeverReachesTheDriver", func(t *testing.T) {
		counter := &countingDriver{Driver: s.newDriver(t)}
		index := newIndex("conf_" + strings.ToLower(rand.Text())[:16])
		engine := search.New(counter, func(engine *search.Builder) {
			engine.Indexes(index)
		})

		for _, query := range []search.Query[product]{
			{},
			search.NewQuery(index, "", func(query *search.QueryBuilder) {
				query.Where("description", "x")
			}),
			search.NewQuery(index, "", func(query *search.QueryBuilder) {
				query.Where("price", "~", 3)
			}),
			search.NewQuery(index, "", func(query *search.QueryBuilder) {
				query.Where("in_stock", "<", true)
			}),
			search.NewQuery(index, "", func(query *search.QueryBuilder) {
				query.OrderBy("description")
			}),
			search.NewQuery(index, "", func(query *search.QueryBuilder) {
				query.Where("price", nil)
			}),
			search.NewQuery(index, "", func(query *search.QueryBuilder) {
				query.WhereIn[float64]("price")
			}),
		} {
			_, err := engine.Search(t.Context(), query, pagination.PageParams{})

			wantErr(t, "Search", err, search.ErrInvalidQuery)
		}

		if counter.calls != 0 {
			t.Fatalf("driver received %d calls for invalid queries, want 0", counter.calls)
		}
	})
}

// cancellationCases covers calls made with a canceled context.
func cancellationCases(t *testing.T, s suite) {
	s.sub(t, "CanceledContextFailsEveryCall", func(t *testing.T) {
		fx := s.loaded(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		wantErr(t, "Sync", fx.engine.Sync(ctx), context.Canceled)
		wantErr(t, "Check", fx.engine.Check(ctx), context.Canceled)
		wantErr(t, "Put", fx.engine.Put(ctx, fx.index, product{ID: "late", Title: "late"}), context.Canceled)
		wantErr(t, "Delete", fx.engine.Delete(ctx, fx.index, "p01"), context.Canceled)

		_, err := fx.engine.Search(ctx, fx.query(""), pagination.PageParams{})

		wantErr(t, "Search", err, context.Canceled)
		wantErr(t, "Drop", fx.engine.Drop(ctx, fx.index), context.Canceled)

		wantOrder(t, "documents after canceled calls", fx.ids(t, fx.query("")), allIDs...)
	})
}

// concurrencyCases covers parallel writers and readers.
func concurrencyCases(t *testing.T, s suite) {
	s.sub(t, "ParallelPutsAndSearches", func(t *testing.T) {
		fx := s.empty(t)

		const workers, perWorker = 8, 10
		errs := make(chan error, workers)

		for worker := range workers {
			go func() {
				docs := make([]product, perWorker)

				for i := range docs {
					docs[i] = product{ID: "w" + pad(worker*perWorker+i), Title: "parallel", Price: float64(i)}
				}

				if err := fx.engine.Put(t.Context(), fx.index, docs...); err != nil {
					errs <- err

					return
				}

				_, err := fx.engine.Search(t.Context(), fx.query("parallel"), pagination.PageParams{Page: 1, PerPage: 5})
				errs <- err
			}()
		}

		for range workers {
			must(t, "worker", <-errs)
		}

		page, err := fx.engine.Search(t.Context(), fx.query("parallel"), pagination.PageParams{Page: 1, PerPage: 5})

		must(t, "Search", err)

		if page.Total != workers*perWorker {
			t.Fatalf("total = %d, want %d", page.Total, workers*perWorker)
		}
	})
}
