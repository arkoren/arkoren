package statement

import (
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/service/search"
)

// TestTSQueryBindsEveryWord checks SQL text and argument order together.
// Word splitting, phrases, exclusions, and prefixes must all use bound values.
func TestTSQueryBindsEveryWord(t *testing.T) {
	t.Parallel()

	s := &statement{}

	writeTSQuery(s, []search.Term{
		{
			Value: "red-shoes",
		},
		{
			Value:  "it's a trap",
			Phrase: true,
		},
		{
			Value:   "boots",
			Exclude: true,
		},
		{
			Value:  "soc'; DROP",
			Prefix: true,
		},
	})

	want := []any{"'red'", "'shoes'", "it s a trap", "'boots'", "'soc'", "'DROP':*"}

	if !slices.Equal(s.args, want) {
		t.Fatalf("args = %q, want %q", s.args, want)
	}

	const wantSQL = "(to_tsquery('simple', ?) && to_tsquery('simple', ?)) && (phraseto_tsquery('simple', ?)) && " +
		"!!(to_tsquery('simple', ?)) && (to_tsquery('simple', ?) && to_tsquery('simple', ?))"

	if got := s.sql.String(); got != wantSQL {
		t.Fatalf("sql = %s\nwant %s", got, wantSQL)
	}
}

// TestConditionsKeepContractPrecedence checks flat AND/OR and explicit groups.
// Generated SQL must preserve the contract's precedence and membership joins.
func TestConditionsKeepContractPrecedence(t *testing.T) {
	t.Parallel()

	s := &statement{}

	writeConditions(s, []search.Condition{
		{
			Field:    "a",
			Operator: search.OpEqual,
			Value:    true,
		},
		{
			Field:    "b",
			Operator: search.OpEqual,
			Value:    1.0,
		},
		{
			Or: true,
			Group: []search.Condition{
				{
					Field:    "c",
					Operator: search.OpNotIn,
					Values:   []any{"x", "y"},
				},
			},
		},
	})

	if s.err != nil {
		t.Fatalf("err = %v", s.err)
	}

	// Two comparisons joined by AND, then OR with a parenthesized group.
	got := s.sql.String()

	for _, fragment := range []string{") = ?::boolean AND (", ") = ?::float8 OR ((", `"C" <> lower(?::text) AND (`} {
		if !strings.Contains(got, fragment) {
			t.Errorf("sql lacks %q: %s", fragment, got)
		}
	}
}

// TestUnsupportedConditionValueIsInvalidQuery exercises direct driver misuse.
// An unsupported bound-value type must retain a compilation error, not emit SQL.
func TestUnsupportedConditionValueIsInvalidQuery(t *testing.T) {
	t.Parallel()

	s := &statement{}

	writeConditions(s, []search.Condition{{
		Field:    "a",
		Operator: search.OpEqual,
		Value:    []byte("x"),
	}})

	if s.err == nil {
		t.Fatal("err = nil, want ErrInvalidQuery")
	}
}
