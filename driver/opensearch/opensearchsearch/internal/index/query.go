package index

import (
	"reflect"
	"slices"
	"strings"
	"unicode"

	"arkoren.dev/core/service/search"
)

// SearchBody compiles spec into the body of a search request for the window of
// limit results starting at offset.
//
// Text terms and filters are separate clauses of one bool query. Every
// positive term is a must clause, so relevance is the sum of its terms; with
// no positive term the query runs in filter context and every score is 0, as
// the contract requires. Filters never score. The sort is the query's orders,
// then relevance when there is a positive term, then the document ID.
func SearchBody(schema search.Schema, spec search.Spec, limit, offset int) map[string]any {
	var must, mustNot []any

	for _, term := range spec.Text {
		clause := termClause(schema.Searchable, term)

		if term.Exclude {
			mustNot = append(mustNot, clause)
		} else {
			must = append(must, clause)
		}
	}

	filter := []any{}

	if len(spec.Where) > 0 {
		filter = append(filter, sequence(schema, spec.Where))
	}

	if len(must) == 0 {
		filter = append(filter, map[string]any{"match_all": map[string]any{}})
	}

	sort := make([]any, 0, 3*len(spec.Orders)+2)

	for _, order := range spec.Orders {
		direction := "asc"

		if order.Desc {
			direction = "desc"
		}

		// Only one typed key is present for any scalar. The other two tie;
		// missing/null/nonscalar values sort last on every key.
		sort = append(sort,
			map[string]any{numberPath(order.Field): map[string]any{"order": direction, "missing": "_last", "unmapped_type": "double"}},
			map[string]any{keywordPath(schema, order.Field): map[string]any{"order": direction, "missing": "_last", "unmapped_type": "keyword"}},
			map[string]any{"booleans." + order.Field: map[string]any{"order": direction, "missing": "_last", "unmapped_type": "boolean"}},
		)
	}

	if len(must) > 0 {
		sort = append(sort, map[string]any{"_score": map[string]any{"order": "desc"}})
	}

	sort = append(sort, map[string]any{"_id": map[string]any{"order": "asc"}})

	query := map[string]any{"filter": filter}

	if len(must) > 0 {
		query["must"] = must
	}

	if len(mustNot) > 0 {
		query["must_not"] = mustNot
	}

	return map[string]any{
		"_source":          []string{"source"},
		"query":            map[string]any{"bool": query},
		"sort":             sort,
		"from":             offset,
		"size":             limit,
		"track_total_hits": search.MaxHits,
		"track_scores":     true,
	}
}

// termClause matches one text term in any of the searchable fields. A phrase
// is a match_phrase; nonphrase words become separately required field
// alternatives. Only the last word of a prefix term uses match_bool_prefix.
func termClause(fields []string, term search.Term) map[string]any {
	if term.Phrase {
		return fieldTermClause(fields, term)
	}

	words := strings.FieldsFunc(term.Value, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	})
	clauses := make([]any, len(words))

	for i, word := range words {
		clauses[i] = fieldTermClause(fields, search.Term{Value: word, Prefix: term.Prefix && i == len(words)-1})
	}

	if len(clauses) == 1 {
		return clauses[0].(map[string]any)
	}

	return map[string]any{"bool": map[string]any{"must": clauses}}
}

// fieldTermClause compiles one phrase or normalized word as alternatives over
// searchable fields. termClause joins word alternatives, so nonphrase words can
// match different fields while phrases still require consecutive words in one.
func fieldTermClause(fields []string, term search.Term) map[string]any {
	clauses := make([]any, len(fields))

	for i, field := range fields {
		field = "strings." + field

		switch {
		case term.Phrase:
			clauses[i] = map[string]any{"match_phrase": map[string]any{field: term.Value}}
		case term.Prefix:
			clauses[i] = map[string]any{"match_bool_prefix": map[string]any{field: map[string]any{"query": term.Value, "operator": "and"}}}
		default:
			clauses[i] = map[string]any{"match": map[string]any{field: map[string]any{"query": term.Value, "operator": "and"}}}
		}
	}

	return map[string]any{"bool": map[string]any{"should": clauses, "minimum_should_match": 1}}
}

// sequence compiles a condition sequence: conditions are joined with AND, or
// OR when marked, and AND binds tighter than OR.
func sequence(schema search.Schema, conditions []search.Condition) map[string]any {
	var runs []any
	var run []any

	for i, condition := range conditions {
		if i > 0 && condition.Or {
			runs = append(runs, map[string]any{"bool": map[string]any{"filter": run}})
			run = nil
		}

		run = append(run, compileCondition(schema, condition))
	}

	runs = append(runs, map[string]any{"bool": map[string]any{"filter": run}})

	if len(runs) == 1 {
		return runs[0].(map[string]any)
	}

	return map[string]any{"bool": map[string]any{"should": runs, "minimum_should_match": 1}}
}

// compileCondition compiles one comparison or group. The operand's type picks
// the physical container that is compared: numbers, booleans, or normalized
// string keywords (including timestamps). A different stored kind has no value
// at that path. Negations require presence as well as a nonmatching value.
func compileCondition(schema search.Schema, condition search.Condition) map[string]any {
	if len(condition.Group) > 0 {
		return sequence(schema, condition.Group)
	}

	switch condition.Operator {
	case search.OpIn, search.OpNotIn:
		return membership(schema, condition)
	}

	path, value := operand(schema, condition.Field, condition.Value)

	switch condition.Operator {
	case search.OpEqual:
		return map[string]any{"term": map[string]any{path: value}}
	case search.OpNotEqual:
		return present(path, map[string]any{"term": map[string]any{path: value}})
	case search.OpLess:
		return map[string]any{"range": map[string]any{path: map[string]any{"lt": value}}}
	case search.OpLessOrEqual:
		return map[string]any{"range": map[string]any{path: map[string]any{"lte": value}}}
	case search.OpGreater:
		return map[string]any{"range": map[string]any{path: map[string]any{"gt": value}}}
	default:
		return map[string]any{"range": map[string]any{path: map[string]any{"gte": value}}}
	}
}

// membership compiles typed membership over normalized, nonempty operand lists.
// IN joins equalities as alternatives. NOT IN requires every typed inequality,
// so a mixed-kind list cannot match any scalar, regardless of native coercion.
// Homogeneous lists share a terms clause at their typed comparison path.
func membership(schema search.Schema, condition search.Condition) map[string]any {
	if condition.Operator == search.OpNotIn {
		kind := reflect.TypeOf(condition.Values[0])

		if slices.ContainsFunc(condition.Values[1:], func(value any) bool {
			return reflect.TypeOf(value) != kind
		}) {
			return map[string]any{"match_none": map[string]any{}}
		}
	}

	byPath := map[string][]any{}
	var paths []string

	for _, value := range condition.Values {
		path, converted := operand(schema, condition.Field, value)

		if _, seen := byPath[path]; !seen {
			paths = append(paths, path)
		}

		byPath[path] = append(byPath[path], converted)
	}

	clauses := make([]any, len(paths))

	for i, path := range paths {
		clauses[i] = map[string]any{"terms": map[string]any{path: byPath[path]}}
	}

	alternatives := map[string]any{"bool": map[string]any{"should": clauses, "minimum_should_match": 1}}

	if condition.Operator == search.OpIn {
		return alternatives
	}

	// Every remaining NOT IN operand has one kind, so presence at its typed
	// path establishes compatibility before negating the equalities.
	return present(paths[0], alternatives)
}

// present matches documents that have a value at path and do not match
// excluded: the shape of "!=" and "not in".
func present(path string, excluded map[string]any) map[string]any {
	return map[string]any{"bool": map[string]any{
		"filter":   []any{map[string]any{"exists": map[string]any{"field": path}}},
		"must_not": []any{excluded},
	}}
}

// operand selects the physical path for a normalized condition without scalar
// conversion. String keywords lowercase on both sides; bools remain native bools.
func operand(schema search.Schema, field string, value any) (string, any) {
	switch value := value.(type) {
	case float64:
		return numberPath(field), value
	case bool:
		return "booleans." + field, value
	default:
		return keywordPath(schema, field), value
	}
}
