// Package searchtest provides a conformance suite for [search.Driver]
// implementations, modeled on testing/slogtest and testing/fstest.
//
// [Run] executes one subtest per documented rule of the contract: query text
// (all words required, phrases, exclusions, last-word prefix, case folding),
// filters and their grouping, ordering and its stable tie-breaks, paging and
// the [search.MaxHits] cap, document and ID handling, upserts and deletes,
// and the Sync, Check, and Drop lifecycle. A driver that passes is
// interchangeable with every other driver behind [search.Engine].
//
// The suite drives the driver through a [search.Engine], the way applications
// do, so the engine's own rules (validation, paging, the total cap) are
// exercised together with the driver's. It avoids anything that depends on
// stemming, typo tolerance, or the relative relevance of results, which
// drivers do not share.
//
//	func TestDriver(t *testing.T) {
//		searchtest.Run(t, func(t *testing.T) search.Driver {
//			return memorysearch.New()
//		})
//	}
//
// Every subtest creates indexes with unique names and drops them when it
// ends, so one backend can serve the whole suite. A driver backed by
// infrastructure calls Run only when testing.Short() is false:
//
//	func TestConformance(t *testing.T) {
//		if testing.Short() {
//			t.Skip("needs a running search server")
//		}
//		searchtest.Run(t, newDriver)
//	}
package searchtest
