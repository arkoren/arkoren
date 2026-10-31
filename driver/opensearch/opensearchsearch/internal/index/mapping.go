// Package index owns OpenSearch mappings, physical documents, and typed queries.
package index

import (
	"encoding/json/v2"
	"fmt"
	"slices"

	"arkoren.dev/core/service/search"
)

const (
	// textAnalyzer is the analyzer of searchable fields. It lowercases and
	// splits on every run of characters that is not a letter or digit, which
	// is the contract's language-neutral text processing. The standard
	// analyzer is not used because it keeps underscores, apostrophes, and
	// decimal points inside words.
	textAnalyzer = "arkoren_text"

	// wordTokenizer is the tokenizer of [textAnalyzer].
	wordTokenizer = "arkoren_words"

	// keywordNormalizer lowercases the values of filterable and sortable
	// fields, so string comparison is case-insensitive and sorts by
	// lowercase bytes.
	keywordNormalizer = "arkoren_keyword"

	// SchemaMeta is the deployed key of the mapping's stored schema checkpoint.
	SchemaMeta = "arkoren_schema"
)

// keywordPath returns the path of the field that holds field's values as
// normalized string keywords, including timestamp strings. Other scalar kinds
// live in separate physical containers and cannot satisfy string conditions.
// A searchable string is mapped as text, so its keyword is a subfield.
func keywordPath(schema search.Schema, field string) string {
	if slices.Contains(schema.Searchable, field) {
		return "strings." + field + ".keyword"
	}

	return "strings." + field
}

// numberPath selects the physical field populated only by JSON numbers.
// Missing, null and other scalar kinds have no entry at this path.
func numberPath(field string) string {
	return "numbers." + field
}

// Body returns the request body that creates the index for schema: one
// shard, so relevance scores do not vary with shard statistics, the text
// analysis, and the mapping.
func Body(schema search.Schema) map[string]any {
	return map[string]any{
		"settings": map[string]any{
			"index": map[string]any{"number_of_shards": 1},
			"analysis": map[string]any{
				"tokenizer": map[string]any{
					wordTokenizer: map[string]any{"type": "pattern", "pattern": `[^\p{L}\p{N}]+`},
				},
				"analyzer": map[string]any{
					textAnalyzer: map[string]any{"type": "custom", "tokenizer": wordTokenizer, "filter": []string{"lowercase"}},
				},
				"normalizer": map[string]any{
					keywordNormalizer: map[string]any{"type": "custom", "filter": []string{"lowercase"}},
				},
			},
		},
		"mappings": MappingBody(schema),
	}
}

// MappingBody returns the mapping of schema, used to create the index and to
// update an existing one. Original JSON is an unindexed string; separate typed
// objects map only declared fields. Undeclared scalars remain in _source for
// later reindexing but cannot be searched. The logical schema stays in _meta.
//
// A field that is searchable is text. Otherwise it is a keyword. A field that
// is also filterable or sortable gets independent numeric and boolean fields,
// and a text field gets a keyword subfield. Because only fields are added when
// a field joins another list, Sync can apply most definition changes; turning a keyword
// field into a searchable one changes its type, which OpenSearch rejects.
func MappingBody(schema search.Schema) map[string]any {
	text, numbers, booleans := map[string]any{}, map[string]any{}, map[string]any{}

	for _, field := range slices.Concat(schema.Searchable, schema.Filterable, schema.Sortable) {
		searchable := slices.Contains(schema.Searchable, field)
		compared := slices.Contains(schema.Filterable, field) || slices.Contains(schema.Sortable, field)
		keyword := map[string]any{"type": "keyword", "normalizer": keywordNormalizer}

		switch {
		case searchable && compared:
			text[field] = map[string]any{
				"type": "text", "analyzer": textAnalyzer,
				"fields": map[string]any{"keyword": keyword},
			}
		case searchable:
			text[field] = map[string]any{"type": "text", "analyzer": textAnalyzer}
		default:
			text[field] = keyword
		}

		if compared {
			numbers[field] = map[string]any{"type": "double", "coerce": false}
			booleans[field] = map[string]any{"type": "boolean"}
		}
	}

	return map[string]any{
		"dynamic": false,
		"_meta":   map[string]any{SchemaMeta: schema},
		"properties": map[string]any{
			"source":   map[string]any{"type": "text", "index": false},
			"strings":  map[string]any{"type": "object", "dynamic": false, "properties": text},
			"numbers":  map[string]any{"type": "object", "dynamic": false, "properties": numbers},
			"booleans": map[string]any{"type": "object", "dynamic": false, "properties": booleans},
		},
	}
}

// StoredSchema extracts the schema Sync stored in the _meta of the response of
// GET /{index}/_mapping. It reports false when the index has no stored schema.
func StoredSchema(body []byte) (search.Schema, bool, error) {
	var indexes map[string]struct {
		// Mappings contains the index's field mapping and stored metadata.
		Mappings struct {
			// Meta carries application metadata alongside native mapping fields.
			Meta struct {
				// Schema is absent when this driver has not stored its schema.
				Schema *search.Schema `json:"arkoren_schema"`
			} `json:"_meta"`
		} `json:"mappings"`
	}

	if err := json.Unmarshal(body, &indexes); err != nil {
		return search.Schema{}, false, fmt.Errorf("opensearch: decode mapping: %w", err)
	}

	for _, index := range indexes {
		if schema := index.Mappings.Meta.Schema; schema != nil {
			return *schema, true, nil
		}
	}

	return search.Schema{}, false, nil
}
