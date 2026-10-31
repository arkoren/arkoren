package search

import (
	"fmt"
	"slices"

	"arkoren.dev/core/service/search/internal/scalar"
)

// maxNameLength is the longest index or field name, in bytes.
const maxNameLength = 64

// Schema is the part of an index definition that drivers need: its name and
// the document fields that take part in searching, filtering, and sorting.
// The facade passes the schema on every driver call, so drivers keep no
// definitions of their own beyond what they must store to implement
// [Driver.Check].
//
// Field names are top-level keys of the JSON object a document encodes to.
// The lists of a schema built by [Index.Schema] are sorted and free of
// duplicates, so two schemas of the same definition are equal under
// [Schema.Equal] whatever order the declarations ran in.
// Raw schemas are mutable driver records; Equal compares list order exactly
// and does not sort, deduplicate, or validate caller-created records.
//
// Example:
//
//	schema := Products.Schema()
//	fmt.Println(schema.Name, schema.Searchable)
type Schema struct {
	// Name is the index name: 1 to 64 bytes of [a-z0-9_].
	// It identifies the backend index independently of its document IDs.
	//
	// Example:
	//
	//	fmt.Println(Products.Schema().Name)
	Name string

	// Searchable lists the string fields that text queries match. It is never
	// empty in a valid schema.
	//
	// Example:
	//
	//	fields := Products.Schema().Searchable
	//	fmt.Println(fields)
	Searchable []string

	// Filterable lists the fields conditions may use. Empty permits no filters.
	//
	// Example:
	//
	//	fmt.Println(Products.Schema().Filterable)
	Filterable []string

	// Sortable lists the fields orders may use. Empty permits no explicit orders.
	//
	// Example:
	//
	//	fmt.Println(Products.Schema().Sortable)
	Sortable []string
}

// Equal reports whether other describes the same index: the same name and
// the same field lists. Drivers use it to implement [Driver.Check].
// Nil and empty lists compare equal; list order otherwise matters.
//
// Example:
//
//	first, second := Products.Schema(), Products.Schema()
//	fmt.Println(first.Equal(second)) // true
func (schema Schema) Equal(other Schema) bool {
	return schema.Name == other.Name &&
		slices.Equal(schema.Searchable, other.Searchable) &&
		slices.Equal(schema.Filterable, other.Filterable) &&
		slices.Equal(schema.Sortable, other.Sortable)
}

// validate returns the first invalid name in the schema, or an error when no
// field is searchable. Every error wraps [ErrInvalidName].
func (schema Schema) validate() error {
	if err := checkName("index name", schema.Name); err != nil {
		return err
	}

	lists := []struct {
		// kind names the field list in validation errors.
		kind string
		// fields contains the index fields validated under the matching kind label.
		fields []string
	}{{"searchable", schema.Searchable}, {"filterable", schema.Filterable}, {"sortable", schema.Sortable}}

	for _, list := range lists {
		for _, field := range list.fields {
			if err := checkName(list.kind+" field of index "+schema.Name, field); err != nil {
				return err
			}
		}
	}

	if len(schema.Searchable) == 0 {
		return fmt.Errorf("index %s: %w: no searchable field", schema.Name, ErrInvalidName)
	}

	return nil
}

// checkName reports whether name is 1 to [maxNameLength] bytes of [a-z0-9_].
// what names the thing being checked in the error, which wraps
// [ErrInvalidName].
func checkName(what, name string) error {
	if name == "" || len(name) > maxNameLength {
		return fmt.Errorf("%s %q: %w: want 1 to %d bytes", what, name, ErrInvalidName, maxNameLength)
	}

	for _, char := range []byte(name) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_':
		default:
			return fmt.Errorf("%s %q: %w: want only a-z, 0-9, and _", what, name, ErrInvalidName)
		}
	}

	return nil
}

// Indexer describes and validates an index independently of its document type.
// Every [Index] implements it. [New] checks Err before capturing Schema, while
// [Engine.Delete] and [Engine.Drop] also validate their call-site definition.
// Implementations must return a stable definition while an engine uses them.
// Adding Err to custom implementations preserves key and declaration failures
// that cannot be represented by the driver's schema alone.
//
// Example:
//
//	var definition search.Indexer = Products
//	if err := definition.Err(); err != nil {
//		return err
//	}
//	fmt.Println(definition.Schema().Name)
type Indexer interface {
	// Schema returns the field definition drivers work with. It does not
	// replace Err: a schema cannot describe an invalid document key function.
	//
	// Example:
	//
	//	var definition search.Indexer = Products
	//	fmt.Println(definition.Schema().Searchable)
	Schema() Schema

	// Err reports a definition failure, including invalid key functions or
	// settings absent from Schema. Nil means the complete definition is valid.
	// Engine registration wraps failures with [ErrInvalidName].
	//
	// Example:
	//
	//	var definition search.Indexer = Products
	//	if err := definition.Err(); err != nil {
	//		return err
	//	}
	Err() error
}

// Index defines one search index over documents of type T: its name, how a
// document names itself, and which fields are searched, filtered, and sorted.
// Create it with [NewIndex] and configure its draft in callbacks, usually in
// a package-level variable.
//
// An Index is an immutable published value. [Index.Err] reports invalid names
// and keys. [New] records registration failures; operational [Engine] methods
// return them wrapped with [ErrInvalidName] before calling the driver.
// Always create indexes with [NewIndex]; the zero value has no name or key.
//
// Publication detaches field lists from input and retained drafts, but does
// not clone the key function or its captured state.
//
// Fields are top-level keys of the JSON object T encodes to with
// encoding/json/v2, so a document type needs json tags for the names the
// index uses. Searchable fields hold strings, filterable and sortable fields
// hold one string, number, or boolean type each, and a field may appear in
// several lists.
//
// Example:
//
//	type Product struct {
//		ID       string  `json:"id"`
//		Title    string  `json:"title"`
//		Category string  `json:"category"`
//		Price    float64 `json:"price"`
//	}
//
//	var Products = search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, func(index *search.IndexBuilder) {
//		index.Searchable("title")
//		index.Filterable("category", "price")
//		index.Sortable("price")
//	})
//	_ = Products.Schema()
type Index[T any] struct {
	// name is the index name.
	name string

	// key returns the document ID of a document.
	key func(T) string

	// searchable, filterable, and sortable are the field lists in declaration
	// order, with duplicates. Schema sorts and compacts them.
	searchable, filterable, sortable []string
}

// NewIndex publishes the index called name whose documents are identified by
// key. The name must be 1 to 64 bytes of [a-z0-9_]. The key function returns
// the document ID, which must be 1 to 255 bytes of [A-Za-z0-9_-]; [Engine.Put]
// rejects other IDs with [ErrInvalidID]. Declare at least one searchable field.
// [Index.Err] validates the completed definition, not intermediate drafts.
//
// Callbacks run directly once, synchronously in order on one fresh draft,
// including for invalid names or keys. Nil entries panic; expanded nil/empty
// slices omit edits. Field lists are copied at input and publication; key
// functions and their captures are retained, not cloned.
//
// Example:
//
//	index := search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, func(index *search.IndexBuilder) {
//		index.Searchable("title")
//	})
//	if err := index.Err(); err != nil {
//		return err
//	}
func NewIndex[T any](name string, key func(T) string, configure ...IndexBuilderFunc) Index[T] {
	index := &IndexBuilder{}

	for _, configure := range configure {
		configure(index)
	}

	return Index[T]{
		name:       name,
		key:        key,
		searchable: slices.Clone(index.searchable),
		filterable: slices.Clone(index.filterable),
		sortable:   slices.Clone(index.sortable),
	}
}

// IndexBuilder collects field declarations for [NewIndex]. Methods return
// this same draft and accumulate fields, including duplicates. Publication
// detaches field lists, so retaining a draft cannot change a published index.
//
// Example:
//
//	index := search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, func(index *search.IndexBuilder) {
//		index.Searchable("title")
//		index.Sortable("price")
//	})
//	if err := index.Err(); err != nil {
//		return err
//	}
type IndexBuilder struct {
	// searchable lists fields matched by query text.
	searchable []string

	// filterable lists fields accepted by predicates.
	filterable []string

	// sortable lists fields accepted by orders.
	sortable []string
}

// Searchable appends string fields text queries match and returns the same draft.
// Input is copied; empty input adds nothing. [Index.Err] checks names after
// publication. Getters and Schema return sorted, deduplicated copies.
//
// Example:
//
//	index := search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, func(index *search.IndexBuilder) {
//		index.Searchable("title", "description")
//	})
//	if err := index.Err(); err != nil {
//		return err
//	}
func (index *IndexBuilder) Searchable(fields ...string) *IndexBuilder {
	index.searchable = append(index.searchable, fields...)

	return index
}

// Filterable appends fields predicates may use and returns the same draft.
// Input is copied; empty input adds nothing. [Index.Err] checks names after
// publication. Getters and Schema return sorted, deduplicated copies.
//
// Example:
//
//	index := search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, func(index *search.IndexBuilder) {
//		index.Searchable("title")
//		index.Filterable("category", "price")
//	})
//	if err := index.Err(); err != nil {
//		return err
//	}
func (index *IndexBuilder) Filterable(fields ...string) *IndexBuilder {
	index.filterable = append(index.filterable, fields...)

	return index
}

// Sortable appends fields orders may use and returns the same draft.
// Input is copied; empty input adds nothing. [Index.Err] checks names after
// publication. Getters and Schema return sorted, deduplicated copies.
//
// Example:
//
//	index := search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, func(index *search.IndexBuilder) {
//		index.Searchable("title")
//		index.Sortable("price")
//	})
//	if err := index.Err(); err != nil {
//		return err
//	}
func (index *IndexBuilder) Sortable(fields ...string) *IndexBuilder {
	index.sortable = append(index.sortable, fields...)

	return index
}

// Name returns the exact declared index name without validating it. A zero
// index returns an empty name; use Err before registering a definition.
//
// Example:
//
//	fmt.Println(Products.Name()) // products
func (index Index[T]) Name() string {
	return index.name
}

// SearchableValue returns the searchable fields, sorted and without
// duplicates.
// The caller owns the returned slice; changing it cannot change the index.
//
// Example:
//
//	fields := Products.SearchableValue()
//	fmt.Println(fields)
func (index Index[T]) SearchableValue() []string {
	return scalar.Fields(index.searchable)
}

// FilterableValue returns the filterable fields, sorted and without
// duplicates.
// The caller owns the returned slice; an index with no filters returns nil.
//
// Example:
//
//	fmt.Println(Products.FilterableValue())
func (index Index[T]) FilterableValue() []string {
	return scalar.Fields(index.filterable)
}

// SortableValue returns the sortable fields, sorted and without duplicates.
// The returned storage is independent of the index and other getter results.
//
// Example:
//
//	fmt.Println(Products.SortableValue())
func (index Index[T]) SortableValue() []string {
	return scalar.Fields(index.sortable)
}

// Schema returns the definition drivers work with. It implements [Indexer].
// Every field list is a sorted, deduplicated, independently owned snapshot.
// This reports a definition even when Err is non-nil; it does not validate it.
//
// Example:
//
//	schema := Products.Schema()
//	fmt.Println(schema.Name, schema.Filterable)
func (index Index[T]) Schema() Schema {
	return Schema{
		Name:       index.name,
		Searchable: index.SearchableValue(),
		Filterable: index.FilterableValue(),
		Sortable:   index.SortableValue(),
	}
}

// Err returns the first problem of the definition, or nil when it is valid.
// Every error wraps [ErrInvalidName]: an invalid index or field name, no
// searchable field, or a nil key function.
// Validation uses completed immutable state; the zero value is invalid.
//
// Example:
//
//	if err := Products.Err(); err != nil {
//		return err
//	}
func (index Index[T]) Err() error {
	if index.key == nil {
		return fmt.Errorf("index %s: %w: nil key function", index.name, ErrInvalidName)
	}

	return index.Schema().validate()
}

// cloneSchema detaches the three owned field lists without normalizing a raw
// driver schema. Registration and driver publication use independent copies so
// neither a custom Indexer nor a driver can mutate the engine's snapshot.
func cloneSchema(schema Schema) Schema {
	schema.Searchable = slices.Clone(schema.Searchable)
	schema.Filterable = slices.Clone(schema.Filterable)
	schema.Sortable = slices.Clone(schema.Sortable)

	return schema
}

// IndexBuilderFunc declares fields on one fresh index draft. Callbacks run once
// in order even for invalid required inputs; field lists detach at publication.
//
// Example:
//
//	var fields search.IndexBuilderFunc = func(index *search.IndexBuilder) {
//		index.Searchable("title")
//	}
//	index := search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, fields)
//	_ = index.Schema()
type IndexBuilderFunc = func(*IndexBuilder)
