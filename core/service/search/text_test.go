package search_test

import (
	"slices"
	"testing"

	"arkoren.dev/core/service/search"
)

// term builds an expected term.
func term(value string, flags string) search.Term {
	return search.Term{
		Value:   value,
		Phrase:  slices.Contains([]string{"phrase", "excluded phrase"}, flags),
		Exclude: flags == "excluded" || flags == "excluded phrase",
		Prefix:  flags == "prefix",
	}
}

// parse returns normalized terms by inspecting a query over a valid test index.
func parse(text string) []search.Term {
	return search.NewQuery(search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}), text).Inspect().Text
}

// TestParseWordsAreRequiredAndTheLastIsAPrefix checks ordinary words use conjunction and final-word completion.
func TestParseWordsAreRequiredAndTheLastIsAPrefix(t *testing.T) {
	t.Parallel()

	got := parse("red  running\tshoes")
	want := []search.Term{term("red", ""), term("running", ""), term("shoes", "prefix")}

	if !slices.Equal(got, want) {
		t.Fatalf("terms = %+v, want %+v", got, want)
	}
}

// TestParsePhrasesAndExclusions checks quotes and minus signs retain phrase and exclusion flags.
func TestParsePhrasesAndExclusions(t *testing.T) {
	t.Parallel()

	got := parse(`red "running shoes" -boots -"wool socks" tail`)
	want := []search.Term{
		term("red", ""), term("running shoes", "phrase"), term("boots", "excluded"),
		term("wool socks", "excluded phrase"), term("tail", "prefix"),
	}

	if !slices.Equal(got, want) {
		t.Fatalf("terms = %+v, want %+v", got, want)
	}
}

// TestParsePrefixSkipsTrailingPhrasesAndExclusions checks completion applies to the last eligible word.
func TestParsePrefixSkipsTrailingPhrasesAndExclusions(t *testing.T) {
	t.Parallel()

	got := parse(`soc "a b" -red`)
	want := []search.Term{term("soc", "prefix"), term("a b", "phrase"), term("red", "excluded")}

	if !slices.Equal(got, want) {
		t.Fatalf("terms = %+v, want %+v", got, want)
	}

	if got := parse(`"only phrase"`); got[0].Prefix {
		t.Fatalf("a phrase is a prefix: %+v", got)
	}
}

// TestParseUnclosedQuoteRunsToTheEnd checks incomplete phrases consume the remaining text.
func TestParseUnclosedQuoteRunsToTheEnd(t *testing.T) {
	t.Parallel()

	got := parse(`red "running shoes`)
	want := []search.Term{term("red", "prefix"), term("running shoes", "phrase")}

	if !slices.Equal(got, want) {
		t.Fatalf("terms = %+v, want %+v", got, want)
	}
}

// TestParseKeepsLoneMinusAndInnerQuotesInWords checks punctuation does not become syntax inside words.
func TestParseKeepsLoneMinusAndInnerQuotesInWords(t *testing.T) {
	t.Parallel()

	got := parse(`a - b foo"bar -`)
	want := []search.Term{term("a", ""), term("b", ""), term(`foo"bar`, "prefix")}

	if !slices.Equal(got, want) {
		t.Fatalf("terms = %+v, want %+v", got, want)
	}
}

// TestParseDropsTermsWithoutLettersOrDigits checks punctuation-only tokens cannot constrain a search.
func TestParseDropsTermsWithoutLettersOrDigits(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"", "   ", `"`, `""`, "-", "--", `-"`, "!!! ???", `"-"`, `- -`} {
		if got := parse(text); len(got) != 0 {
			t.Errorf("parse(%q) = %+v, want no terms", text, got)
		}
	}
}

// TestParseNonASCIIWords checks Unicode letters and digits survive term normalization.
func TestParseNonASCIIWords(t *testing.T) {
	t.Parallel()

	got := parse("-café über")
	want := []search.Term{term("café", "excluded"), term("über", "prefix")}

	if !slices.Equal(got, want) {
		t.Fatalf("terms = %+v, want %+v", got, want)
	}
}
