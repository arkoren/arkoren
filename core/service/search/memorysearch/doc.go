// Package memorysearch provides an in-memory [search.Driver] for tests, local
// development, and single-process applications.
//
// Documents live in process memory and are lost on exit. Text is lowercased
// and split on every character that is not a letter or digit, with no
// stemming and no typo tolerance. A document matches when every required
// term matches. Every hit has score 1 when the query has a positive term, or
// score 0 for empty and exclusion-only queries. The ID breaks ties. Queries
// scan every document of the index, so the driver suits thousands of documents,
// not millions.
//
// Filters follow the typed comparisons of [search.Condition]. IN needs an
// equal operand of the stored scalar's kind. NOT IN needs a different value
// of the same kind for every operand, so a list mixing kinds matches nothing.
// Missing, null, array, and object fields match neither membership operator.
//
//	driver := memorysearch.New()
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
//	if err := engine.Sync(ctx); err != nil {
//		return err
//	}
//
// The driver has no lifecycle: it needs no Start, Stop, or Ping.
package memorysearch
