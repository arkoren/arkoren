package search

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/telemetry"
)

var (
	// ErrNotFound is returned when an index is not registered with the
	// engine, before any driver call, and by a driver when an index was never
	// synced (or was dropped) and the call needs it: Put, Delete, Search, and
	// Check. Drop never returns it.
	//
	// Example:
	//
	//	if err := engine.Check(ctx); errors.Is(err, search.ErrNotFound) {
	//		fmt.Println("run the deployment sync")
	//	}
	ErrNotFound = errors.New("search index not found")

	// ErrInvalidName is returned for an invalid index or field name, an
	// index without a searchable field, a nil key function, and a duplicate
	// index name.
	//
	// Example:
	//
	//	if err := Products.Err(); errors.Is(err, search.ErrInvalidName) {
	//		return err
	//	}
	ErrInvalidName = errors.New("search invalid name")

	// ErrInvalidQuery is returned for a query that cannot run: an undeclared
	// field, an unsupported value or operator, an empty group, or a page
	// beyond [MaxHits].
	// The zero query is invalid too, before any driver call.
	//
	// Example:
	//
	//	if err := query.Err(); errors.Is(err, search.ErrInvalidQuery) {
	//		return err
	//	}
	ErrInvalidQuery = errors.New("search invalid query")

	// ErrInvalidID is returned by [Engine.Put] and [Engine.Delete] for a
	// document ID that is empty, longer than 255 bytes, or holds anything
	// but [A-Za-z0-9_-].
	//
	// Example:
	//
	//	if err := engine.Delete(ctx, Products, "invalid/id"); errors.Is(err, search.ErrInvalidID) {
	//		fmt.Println("reject the document identifier")
	//	}
	ErrInvalidID = errors.New("search invalid document id")

	// ErrSchemaChanged is returned by [Engine.Check] when an index exists but
	// its stored definition differs from the registered one. [Engine.Sync]
	// applies the change.
	//
	// Example:
	//
	//	if err := engine.Check(ctx); errors.Is(err, search.ErrSchemaChanged) {
	//		return err // Synchronize in the deployment process, not a health check.
	//	}
	ErrSchemaChanged = errors.New("search schema changed")

	// ErrUnsupported is returned by a driver for a query or setting its
	// backend cannot express.
	// It does not permit an implementation to silently weaken a query.
	//
	// Example:
	//
	//	_, err := engine.Search(ctx, query, pagination.PageParams{})
	//	if errors.Is(err, search.ErrUnsupported) {
	//		fmt.Println("choose a supported query or another driver")
	//	}
	ErrUnsupported = errors.New("search unsupported operation")
)

// MaxHits is the deepest result a search can reach: page times per-page may
// not exceed it, and [pagination.Page.Total] never reports more. Backends
// cap deep paging and exact counting for the same reason.
// The limit bounds reachable results, not the amount of backend indexing work.
//
// Example:
//
//	fmt.Println(search.MaxHits) // 1000
const MaxHits = 1000

// maxIDLength is the longest document ID, in bytes.
const maxIDLength = 255

// Document is one stored document as drivers see it. Facade Put assigns ID
// through the typed index's key function and produces a fresh JSON encoding.
// Raw driver callers must provide valid IDs and object-shaped JSON themselves.
//
// Example:
//
//	document := search.Document{ID: "p1", Source: []byte(`{"title":"Red shoes"}`)}
//	fmt.Println(document.ID)
type Document struct {
	// ID is the document ID, valid under the rules of [ErrInvalidID].
	// It is independent of whether the JSON source also contains an ID field.
	//
	// Example: document := search.Document{ID: "p1", Source: []byte(`{"title":"Red shoes"}`)}
	ID string

	// Source is the document encoded as one JSON object, produced by
	// encoding/json/v2. Field names in a [Schema] are its top-level keys.
	//
	// Example:
	//
	//	document := search.Document{ID: "p1", Source: []byte(`{"title":"Red shoes"}`)}
	//	fmt.Println(string(document.Source))
	Source []byte
}

// Match is one search result as a driver returns it. The facade decodes its
// source into Hit.Document and preserves its ID and provider relevance score.
//
// Example:
//
//	match := search.Match{Document: search.Document{ID: "p1"}, Score: 1}
//	fmt.Println(match.ID, match.Score)
type Match struct {
	// Document contains the stored identity and encoded payload for this hit.
	//
	// Example:
	//
	//	match := search.Match{Document: search.Document{ID: "p1"}}
	//	_ = match.ID
	Document

	// Score is how relevant the document is to the query text: higher is
	// more relevant, comparable only within one result. It is 0 for empty
	// text.
	//
	// Example:
	//
	//	match := search.Match{Score: 1}
	//	fmt.Println(match.Score)
	Score float64
}

// Matches is the window of results a driver returns. Items holds only the
// requested window; Total counts all matches up to the driver's counting cap.
// A window past the final result is empty, not an index-not-found error.
//
// Example:
//
//	matches := search.Matches{Items: []search.Match{{Document: search.Document{ID: "p1"}}}, Total: 1}
//	fmt.Println(len(matches.Items), matches.Total)
type Matches struct {
	// Items is the requested window of results, in contract order. It is
	// empty, not an error, when the window is past the last result.
	//
	// Example:
	//
	//	matches := search.Matches{Items: []search.Match{{Document: search.Document{ID: "p1"}}}}
	//	for _, match := range matches.Items {
	//		fmt.Println(match.ID)
	//	}
	Items []Match

	// Total is the number of documents matching the query, exact up to
	// [MaxHits]. A driver may stop counting at [MaxHits]; the engine never
	// reports more.
	// It is not necessarily equal to the number of Items in this window.
	//
	// Example:
	//
	//	matches := search.Matches{Total: 42}
	//	fmt.Println(matches.Total, len(matches.Items)) // 42 0
	Total int64
}

// Hit is one result of [Engine.Search]: the decoded document with its ID and
// relevance score.
// Document is decoded independently from the driver's JSON bytes; scores are
// observations of this query, not portable values across engines or queries.
//
// Example:
//
//	hit := search.Hit[Product]{ID: "p1", Document: Product{ID: "p1", Title: "Red shoes"}}
//	fmt.Println(hit.Document.Title)
type Hit[T any] struct {
	// ID is the document ID.
	// It is taken from the stored envelope, not extracted again from Document.
	//
	// Example:
	//
	//	hit := search.Hit[Product]{ID: "p1"}
	//	fmt.Println(hit.ID)
	ID string

	// Score is how relevant the document is to the query text: higher is
	// more relevant, comparable only within one result, and 0 for empty
	// text. Scores from different drivers or queries are not comparable.
	//
	// Example:
	//
	//	hit := search.Hit[Product]{ID: "p1", Score: 1}
	//	fmt.Println(hit.Score)
	Score float64

	// Document is the stored document decoded into T.
	// A decoding failure aborts Search instead of publishing partial hits.
	//
	// Example:
	//
	//	hit := search.Hit[Product]{Document: Product{ID: "p1", Title: "Red shoes"}}
	//	fmt.Println(hit.Document.Title)
	Document T
}

// Driver stores documents and answers queries for the indexes the engine
// registers. Its doc comments are the specification that the searchtest
// conformance suite asserts.
//
// Drivers keep no definitions: the [Schema] arrives on every call, and a
// driver stores only what it needs to implement [Driver.Check]. Lifecycle
// (Start, Stop, Ping) stays on concrete driver types. Every method fails with
// an error wrapping the context's error when ctx is already canceled, and
// then leaves the index unchanged.
//
// Writes are visible to the next call when the method returns. The engine
// has already validated names, IDs, and queries, so drivers may assume them.
// Text is processed without a language: lowercase, split on anything that is
// not a letter or digit, no stemming or typo tolerance.
//
// Example:
//
//	var driver search.Driver = memorysearch.New()
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
//	if err := engine.Sync(ctx); err != nil {
//		return err
//	}
type Driver interface {
	// Sync creates every index in schemas that does not exist and applies
	// changed settings to those that do, keeping their documents. It is safe
	// to call repeatedly, never removes an index, and may reindex existing
	// documents, which can be slow. After it returns, Check passes for the
	// same schemas.
	//
	// Example:
	//
	//	if err := driver.Sync(ctx, []search.Schema{Products.Schema()}); err != nil {
	//		return err
	//	}
	Sync(ctx context.Context, schemas []Schema) error

	// Check reports whether Sync would change nothing, without changing
	// anything. It returns an error wrapping [ErrNotFound] when a schema's
	// index was never synced, and one wrapping [ErrSchemaChanged] when it
	// exists with a different definition. Indexes the backend holds beyond
	// schemas are ignored.
	//
	// Example:
	//
	//	if err := driver.Check(ctx, []search.Schema{Products.Schema()}); err != nil {
	//		return err
	//	}
	Check(ctx context.Context, schemas []Schema) error

	// Put stores docs in the index, replacing documents with the same ID
	// entirely. docs may be any length (the driver splits large batches) and
	// holds unique IDs. It returns an error wrapping [ErrNotFound] when the
	// index was never synced.
	//
	// Example:
	//
	//	docs := []search.Document{{ID: "p1", Source: []byte(`{"title":"Red shoes"}`)}}
	//	if err := driver.Put(ctx, Products.Schema(), docs); err != nil {
	//		return err
	//	}
	Put(ctx context.Context, schema Schema, docs []Document) error

	// Delete removes the documents with ids. Missing IDs succeed. It
	// returns an error wrapping [ErrNotFound] when the index was never
	// synced.
	//
	// Example:
	//
	//	if err := driver.Delete(ctx, Products.Schema(), []string{"p1"}); err != nil {
	//		return err
	//	}
	Delete(ctx context.Context, schema Schema, ids []string) error

	// Search returns the window of at most limit results starting at offset
	// among the documents that match spec, where limit is positive and
	// offset+limit does not exceed [MaxHits]. It returns an error wrapping
	// [ErrNotFound] when the index was never synced.
	//
	// A document matches when it satisfies every [Spec.Text] term, in any
	// searchable field: each word of a word term is a word of the field; the
	// words of a phrase are consecutive in one field; the last word of a
	// [Term.Prefix] term may be a prefix; excluded terms must not match.
	// Words are compared case-insensitively. A document matches [Spec.Where]
	// by its decoded values: strings compare case-insensitively and byte-wise,
	// numbers numerically, and RFC 3339 timestamps as text, so documents
	// should store them in UTC.
	//
	// Results are ordered by each [Spec.Orders] key in turn (strings by their
	// lowercase byte order, documents without the field last whichever the
	// direction), then, for non-empty text, by descending score, and finally
	// by ascending ID in byte order. The order is therefore total and the
	// same on every call.
	//
	// Example:
	//
	//	spec := search.NewQuery(Products, "red").Inspect()
	//	matches, err := driver.Search(ctx, Products.Schema(), spec, 20, 0)
	//	if err != nil {
	//		return err
	//	}
	//	fmt.Println(matches.Total)
	Search(ctx context.Context, schema Schema, spec Spec, limit, offset int) (Matches, error)

	// Drop removes the index and its documents. Dropping a missing index
	// succeeds. A later Sync creates it empty.
	//
	// Example:
	//
	//	if err := driver.Drop(ctx, Products.Schema()); err != nil {
	//		return err
	//	}
	Drop(ctx context.Context, schema Schema) error
}

// Engine is the typed search facade over a [Driver]: it registers the
// application's indexes, encodes and decodes documents as JSON, validates
// input, pages results, and records telemetry. Create it with [New].
//
// An Engine is safe for concurrent use when its driver is.
// Raw Driver access bypasses facade validation, encoding, telemetry, and events.
//
// Example:
//
//	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
//	if err := engine.Sync(ctx); err != nil {
//		return err
//	}
type Engine struct {
	// driver stores the documents.
	driver Driver

	// config holds the registered indexes and telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[instruments]

	// schemas maps each registered index name to its schema.
	schemas map[string]Schema

	// err is the first problem found by [New] with the registered indexes.
	// Every operational call returns it; accessors remain available.
	err error
}

// New creates an Engine that stores documents in driver and knows the indexes
// of [Builder.Indexes]. It does no I/O and never fails: an invalid index or a
// duplicate index name is kept and returned, wrapping [ErrInvalidName], by
// every operational call. Custom Indexer implementations must expose Err;
// registration validates the complete definition before capturing its schema.
//
// Sync, Check, Put, Delete, Drop, and Search emit one client span and the
// arkoren.search.operations and arkoren.search.operation.duration metrics when
// the telemetry is enabled, and costs nothing otherwise. Query text and
// documents are never recorded.
//
// Callbacks run directly once in order on one fresh draft. Nil entries panic;
// expanded nil/empty slices omit edits. Registration storage is detached from
// inputs and retained drafts; index implementations and services remain shared.
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products, Users)
//		engine.Telemetry(app.Telemetry())
//		engine.Name("catalog")
//	})
func New(driver Driver, configure ...BuilderFunc) *Engine {
	draft := &Builder{}

	for _, configure := range configure {
		configure(draft)
	}

	config := draft.config
	config.Indexes = slices.Clone(config.Indexes)
	engine := &Engine{
		driver:      driver,
		config:      config,
		instruments: telemetry.NewInstruments(newInstruments),
		schemas:     map[string]Schema{},
	}

	for _, indexer := range engine.config.Indexes {
		if indexer == nil {
			engine.err = fmt.Errorf("search index: %w: nil index", ErrInvalidName)

			return engine
		}

		if err := indexer.Err(); err != nil {
			engine.err = fmt.Errorf("search index: %w: %w", ErrInvalidName, err)

			return engine
		}

		schema := indexer.Schema()

		if err := schema.validate(); err != nil {
			engine.err = err

			return engine
		}

		if _, duplicate := engine.schemas[schema.Name]; duplicate {
			engine.err = fmt.Errorf("index %s: %w: registered twice", schema.Name, ErrInvalidName)

			return engine
		}

		engine.schemas[schema.Name] = cloneSchema(schema)
	}

	return engine
}

// Driver returns the shared underlying driver, not a detached copy. Calls on
// it bypass facade validation, JSON conversion, telemetry, and notifications.
// This accessor performs no I/O and emits no telemetry.
//
// Example:
//
//	driver := engine.Driver()
//	fmt.Printf("%T\n", driver)
func (engine *Engine) Driver() Driver {
	return engine.driver
}

// DriverAs returns the first driver of type T in the chain of the underlying
// driver. A driver that wraps another exposes it with an Unwrap() Driver
// method.
// The returned driver remains shared; this accessor emits no telemetry.
//
// Example:
//
//	if driver, ok := engine.DriverAs[*memorysearch.Driver](); ok {
//		fmt.Printf("%T\n", driver)
//	}
func (engine *Engine) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](engine.Driver())
}

// Sync creates every registered index and applies changed definitions,
// keeping documents. It is the deploy step that makes the backend match the
// code, safe to repeat, and may reindex, which is slow on large indexes. It
// never removes an index that is no longer registered; use [Engine.Drop].
// With Builder.Events configured, Synced or Failed observes the driver call.
// Success may be a no-op and failure may follow partial changes; events do not
// establish an atomic commit. Invalid registrations emit no notification.
// Driver panics propagate without an outcome event. Nil Events skips event
// timing entirely, independently of the configured telemetry service.
//
// Example:
//
//	if err := engine.Sync(ctx); err != nil {
//		return err
//	}
func (engine *Engine) Sync(ctx context.Context) (err error) {
	ctx, op := engine.begin(ctx, operationSync, "")

	if engine.err != nil {
		op.end(ctx, engine.err)

		return engine.err
	}

	// startedAt avoids event clock reads when notifications are disabled.
	var startedAt time.Time

	if engine.config.Events != nil {
		startedAt = time.Now()
	}

	// returned distinguishes a returned batch outcome from a provider panic.
	returned := false
	defer func() {
		// duration excludes telemetry completion and listener execution.
		var duration time.Duration

		if returned && engine.config.Events != nil {
			duration = time.Since(startedAt)
		}

		op.end(ctx, err)

		if returned {
			engine.emit(ctx, operationSync, duration, err)
		}
	}()

	err = engine.driver.Sync(ctx, engine.registered())
	returned = true

	if err != nil {
		return fmt.Errorf("search sync: %w", err)
	}

	return nil
}

// Check reports whether the backend matches every registered index, without
// changing anything. It returns an error wrapping [ErrSchemaChanged] when an
// index exists with another definition, and one wrapping [ErrNotFound] when
// an index was never synced. Run it from an application start hook
// (compose.Startup), never as a health check: after a deploy runs
// [Engine.Sync], instances still running the old code would turn unhealthy.
//
// Example:
//
//	if err := engine.Check(ctx); err != nil {
//		return err
//	}
func (engine *Engine) Check(ctx context.Context) (err error) {
	ctx, op := engine.begin(ctx, operationCheck, "")
	defer func() {
		op.end(ctx, err)
	}()

	if engine.err != nil {
		return engine.err
	}

	if err := engine.driver.Check(ctx, engine.registered()); err != nil {
		return fmt.Errorf("search check: %w", err)
	}

	return nil
}

// Put stores docs in index, encoding each as JSON and taking its ID from the
// index's key function. A document with an ID that is already stored replaces
// it entirely; if one call holds the same ID twice, the last wins.
//
// Every ID is validated and every document encoded before anything is
// written, so an invalid document ([ErrInvalidID], or an encoding error)
// writes nothing. Past that, a large call is split by the driver and is not
// atomic across the pieces; because writes are upserts, repeating the call
// after a failure is safe. The documents are searchable when Put returns.
// Putting no documents does nothing. An index that is not registered fails
// with [ErrNotFound], and one that was never synced with [ErrNotFound] from
// the driver. Cancellation is observed between key/encoding callbacks and
// before handing the completed batch to the driver; callback execution itself
// is not interrupted. A cancellation during driver writes may follow changes.
//
//	err := engine.Put(ctx, Products, product1, product2)
func (engine *Engine) Put[T any](ctx context.Context, index Index[T], docs ...T) (err error) {
	ctx, op := engine.begin(ctx, operationPut, index.name)
	defer func() {
		op.end(ctx, err)
	}()

	schema := index.Schema()

	if err := engine.use(schema, index.Err()); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if len(docs) == 0 {
		return nil
	}

	encoded, err := encodeDocuments(ctx, index, docs)

	if err != nil {
		return fmt.Errorf("search put %s: %w", index.name, err)
	}

	if err := engine.driver.Put(ctx, schema, encoded); err != nil {
		return fmt.Errorf("search put %s: %w", index.name, err)
	}

	return nil
}

// encodeDocuments validates IDs and captures JSON before any driver write.
// Duplicate IDs keep their last value in the first occurrence's position.
// Cancellation stops application callbacks between documents and after key
// selection; encoding failures and cancellation never publish a partial batch.
func encodeDocuments[T any](ctx context.Context, index Index[T], docs []T) ([]Document, error) {
	position := make(map[string]int, len(docs))
	encoded := make([]Document, 0, len(docs))

	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		id := index.key(doc)

		if err := checkID(id); err != nil {
			return nil, err
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		source, err := json.Marshal(doc)

		if err != nil {
			return nil, fmt.Errorf("encode document %s: %w", id, err)
		}

		if source[0] != '{' {
			return nil, fmt.Errorf("document %s does not encode to a JSON object", id)
		}

		if at, seen := position[id]; seen {
			encoded[at] = Document{ID: id, Source: source}

			continue
		}

		position[id] = len(encoded)
		encoded = append(encoded, Document{ID: id, Source: source})
	}

	return encoded, ctx.Err()
}

// Delete removes the documents with ids from index. IDs that are not stored
// succeed, so deleting is safe to repeat. Invalid IDs fail with
// [ErrInvalidID] before anything is deleted. Deleting no IDs does nothing.
// Definition errors precede cancellation; cancellation is checked before and
// during ID validation. Cancellation after driver work starts implies no rollback.
//
//	err := engine.Delete(ctx, Products, "p1", "p2")
func (engine *Engine) Delete(ctx context.Context, index Indexer, ids ...string) (err error) {
	schema := index.Schema()
	ctx, op := engine.begin(ctx, operationDelete, schema.Name)
	defer func() {
		op.end(ctx, err)
	}()

	if err := engine.use(schema, index.Err()); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if len(ids) == 0 {
		return nil
	}

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := checkID(id); err != nil {
			return fmt.Errorf("search delete %s: %w", schema.Name, err)
		}
	}

	if err := engine.driver.Delete(ctx, schema, ids); err != nil {
		return fmt.Errorf("search delete %s: %w", schema.Name, err)
	}

	return nil
}

// Drop removes index and every document in it. It is explicit and never part
// of [Engine.Sync]. Dropping an index that does not exist succeeds; the index
// must still be registered. [Engine.Sync] creates it again, empty.
// With Builder.Events configured, Dropped or Failed observes the driver call;
// success includes an already absent index. Invalid registrations emit none.
// Driver panics propagate without an outcome event; nil Events skips event timing.
//
// Example:
//
//	if err := engine.Drop(ctx, Products); err != nil {
//		return err
//	}
func (engine *Engine) Drop(ctx context.Context, index Indexer) (err error) {
	schema := index.Schema()
	ctx, op := engine.begin(ctx, operationDrop, schema.Name)

	if err := engine.use(schema, index.Err()); err != nil {
		op.end(ctx, err)

		return err
	}

	// startedAt avoids event clock reads when notifications are disabled.
	var startedAt time.Time

	if engine.config.Events != nil {
		startedAt = time.Now()
	}

	// returned excludes driver panics from observed administrative outcomes.
	returned := false
	defer func() {
		// duration excludes telemetry completion and listener execution.
		var duration time.Duration

		if returned && engine.config.Events != nil {
			duration = time.Since(startedAt)
		}

		op.end(ctx, err)

		if returned {
			engine.emit(ctx, operationDrop, duration, err)
		}
	}()

	err = engine.driver.Drop(ctx, schema)
	returned = true

	if err != nil {
		return fmt.Errorf("search drop %s: %w", schema.Name, err)
	}

	return nil
}

// Search runs query and returns one page of hits, decoded into T and ordered
// as [Driver.Search] specifies: by the query's orders, then by relevance when
// it has text, then by ascending ID. params is normalized with the default
// pagination limits. Page times per-page may not exceed [MaxHits], or the
// search fails with [ErrInvalidQuery]; [pagination.Page.Total] is exact up to
// [MaxHits] and never above it.
// Items is an eager collection of typed hits for this window, retaining each
// hit's ID and score. Map, Filter, and GroupBy run locally without another
// search or changes to Total or other page metadata.
//
// A query with an error ([Query.Err]) fails with it, and an index that is not
// registered with [ErrNotFound], both before any driver call.
// Cancellation is observed between result decodes and before publishing the
// page. Custom JSON decoders finish their current call before cancellation is
// observed; an incomplete page is never returned as a successful result.
//
// Example:
//
//	query := search.NewQuery(Products, "red shoes", func(query *search.QueryBuilder) {
//		query.Where("price", "<=", 100)
//	})
//	page, err := engine.Search(ctx, query,
//		pagination.PageParams{Page: 1, PerPage: 20})
//	if err != nil {
//		return err
//	}
//	_ = page.Items
func (engine *Engine) Search[T any](
	ctx context.Context,
	query Query[T],
	params pagination.PageParams,
) (page pagination.Page[Hit[T]], err error) {
	ctx, op := engine.begin(ctx, operationSearch, query.index.name)
	defer func() {
		op.end(ctx, err)
	}()

	schema := query.index.Schema()

	if err := engine.use(schema, query.Err()); err != nil {
		return page, err
	}

	params, err = params.Normalize()

	if err != nil {
		return page, fmt.Errorf("search %s: %w", schema.Name, err)
	}

	if params.Page*params.PerPage > MaxHits {
		return page, fmt.Errorf("search %s: %w: page %d of %d results is beyond the first %d hits",
			schema.Name, ErrInvalidQuery, params.Page, params.PerPage, MaxHits)
	}

	matches, err := engine.driver.Search(ctx, schema, query.Inspect(), params.PerPage, params.Offset())

	if err != nil {
		return page, fmt.Errorf("search %s: %w", schema.Name, err)
	}

	hits := make([]Hit[T], len(matches.Items))

	for i, match := range matches.Items {
		if err := ctx.Err(); err != nil {
			return page, err
		}

		hits[i] = Hit[T]{ID: match.ID, Score: match.Score}

		if err := json.Unmarshal(match.Source, &hits[i].Document); err != nil {
			return page, fmt.Errorf("search %s: decode document %s: %w", schema.Name, match.ID, err)
		}
	}

	if err := ctx.Err(); err != nil {
		return page, err
	}

	op.setHits(len(hits))

	return pagination.Paginate(hits, min(matches.Total, MaxHits), params.Page, params.PerPage), nil
}

// registered returns the schemas of every registered index, ordered by name.
// Each call detaches field lists before transferring the records to a driver.
func (engine *Engine) registered() []Schema {
	schemas := slices.SortedFunc(maps.Values(engine.schemas), func(first, second Schema) int {
		return cmp.Compare(first.Name, second.Name)
	})

	for index := range schemas {
		schemas[index] = cloneSchema(schemas[index])
	}

	return schemas
}

// use is the check every per-index call makes before any I/O: the error of
// [New], the caller's own error (an invalid index or query), then whether
// schema is registered with exactly this definition.
func (engine *Engine) use(schema Schema, err error) error {
	if engine.err != nil {
		return engine.err
	}

	if err != nil {
		return err
	}

	if known, ok := engine.schemas[schema.Name]; !ok || !known.Equal(schema) {
		return fmt.Errorf("search index %q: %w: not registered with this definition (see Builder.Indexes)",
			schema.Name, ErrNotFound)
	}

	return nil
}

// checkID reports whether id is 1 to [maxIDLength] bytes of [A-Za-z0-9_-].
// The error wraps [ErrInvalidID] and does not repeat the ID.
func checkID(id string) error {
	if id == "" || len(id) > maxIDLength {
		return fmt.Errorf("%w: want 1 to %d bytes, got %d", ErrInvalidID, maxIDLength, len(id))
	}

	for _, char := range []byte(id) {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-':
		default:
			return fmt.Errorf("%w: want only A-Z, a-z, 0-9, _, and -", ErrInvalidID)
		}
	}

	return nil
}
