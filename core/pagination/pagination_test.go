package pagination_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
)

// ExamplePageParams demonstrates normalized offset arithmetic before query use.
func ExamplePageParams() {
	params, err := (pagination.PageParams{Page: 3, PerPage: 20}).Normalize()

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(params.Offset())
	// Output: 40
}

// ExampleCursorParams demonstrates positive window limits without interpreting
// a continuation token or claiming it supplies authorization.
func ExampleCursorParams() {
	params, err := (pagination.CursorParams{}).Normalize(pagination.WithCursorMaxPerPage(10))

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(params.PerPage, params.Cursor == "")
	// Output: 10 true
}

// ExampleCursorPaginate builds both tokens using the public generic encoder.
func ExampleCursorPaginate() {
	page, err := pagination.CursorPaginate([]int{41, 42}, 2, true, true, pagination.CursorEncode[int])

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(len(page.Items), page.NextCursor != "", page.PrevCursor != "")
	// Output: 2 true true
}

// ExampleCursorPaginateFunc keeps forward and backward boundary metadata
// separate while leaving the caller's original order unchanged.
func ExampleCursorPaginateFunc() {
	page, err := pagination.CursorPaginateFunc([]int{41, 42}, 2, true, true,
		func(id int) (string, error) {
			return fmt.Sprintf("next-%d", id), nil
		},
		func(id int) (string, error) {
			return fmt.Sprintf("previous-%d", id), nil
		})

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(page.NextCursor, page.PrevCursor)
	// Output: next-42 previous-41
}

// TestPaginateNormalizesPage verifies a zero page request is normalized.
func TestPaginateNormalizesPage(t *testing.T) {
	t.Parallel()

	page := pagination.Paginate([]int(nil), 0, 0, 0)

	if page.Items == nil {
		t.Fatal("page normalization failed")
	}

	if page.CurrentPage != 1 {
		t.Fatal("page normalization failed")
	}

	if page.LastPage != 1 {
		t.Fatal("page normalization failed")
	}
}

// TestPaginateMaximumTotalRoundsUpWithoutOverflow checks the remainder case
// at the largest supported total and preserves the platform's page cap.
func TestPaginateMaximumTotalRoundsUpWithoutOverflow(t *testing.T) {
	t.Parallel()

	page := pagination.Paginate([]int(nil), math.MaxInt64, 1, 2)
	want := int(min(int64(math.MaxInt64/2+1), int64(math.MaxInt)))

	if page.LastPage != want {
		t.Fatalf("LastPage = %d, want %d", page.LastPage, want)
	}
}

// TestPaginateMaximumTotalExactDivision checks that the overflow-safe ceiling
// does not add a page when a maximum-width page already covers all items.
func TestPaginateMaximumTotalExactDivision(t *testing.T) {
	t.Parallel()

	page := pagination.Paginate([]int(nil), int64(math.MaxInt), 1, math.MaxInt)

	if page.LastPage != 1 {
		t.Fatalf("LastPage = %d, want 1", page.LastPage)
	}
}

// TestPaginateNegativeTotalRetainsMinimumPage checks that even the minimum
// total remains visible to callers while page calculations stay at one.
func TestPaginateNegativeTotalRetainsMinimumPage(t *testing.T) {
	t.Parallel()

	page := pagination.Paginate([]int(nil), math.MinInt64, 0, 0)

	if page.Total != math.MinInt64 || page.LastPage != 1 {
		t.Fatalf("Total, LastPage = %d, %d; want %d, 1", page.Total, page.LastPage, int64(math.MinInt64))
	}

	if page.CurrentPage != 1 || page.PerPage != 1 {
		t.Fatalf("CurrentPage, PerPage = %d, %d; want 1, 1", page.CurrentPage, page.PerPage)
	}
}

// TestCursorIntegerRoundTrip verifies integer cursor encoding and decoding.
func TestCursorIntegerRoundTrip(t *testing.T) {
	t.Parallel()

	cursor, err := pagination.CursorEncode(42)

	if err != nil {
		t.Fatal(err)
	}

	value, err := pagination.CursorDecode[int](cursor)

	if err != nil {
		t.Fatal(err)
	}

	if value != 42 {
		t.Fatal("cursor round trip failed")
	}
}

// TestCursorDecodeFailureWrapsSentinel verifies malformed cursors include the
// sentinel error.
func TestCursorDecodeFailureWrapsSentinel(t *testing.T) {
	t.Parallel()

	_, err := pagination.CursorDecode[int]("bad")

	if !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatal("cursor error not wrapped")
	}
}

// TestCursorDecodeRejectsDuplicateObjectNames verifies duplicate JSON keys fail.
func TestCursorDecodeRejectsDuplicateObjectNames(t *testing.T) {
	t.Parallel()

	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"id":1,"id":2}`))

	_, err := pagination.CursorDecode[map[string]int](cursor)

	if !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("expected ErrCursorDecode, got %v", err)
	}
}

// TestCursorEncodeWrapsSerializationError verifies unsupported values preserve
// the cursor encoding sentinel.
func TestCursorEncodeWrapsSerializationError(t *testing.T) {
	t.Parallel()

	_, err := pagination.CursorEncode(make(chan int))

	if !errors.Is(err, pagination.ErrCursorEncode) {
		t.Fatalf("expected ErrCursorEncode, got %v", err)
	}
}

// TestOffset checks conversion of a one-based page to its skipped-item count.
// The third 25-item page must begin after the first 50 items.
func TestOffset(t *testing.T) {
	t.Parallel()

	if got := pagination.Offset(3, 25); got != 50 {
		t.Fatalf("offset = %d, want 50", got)
	}
}

// TestCursorDecodeRejectsOversizedCursor checks the raw cursor length boundary.
// Oversized input must retain the normal cursor-decoding sentinel.
func TestCursorDecodeRejectsOversizedCursor(t *testing.T) {
	t.Parallel()

	if _, err := pagination.CursorDecode[string](strings.Repeat("a", 5000)); !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("error = %v, want ErrCursorDecode", err)
	}
}

// TestCursorDecodeErrorIsBadRequest verifies malformed cursors render as 400.
func TestCursorDecodeErrorIsBadRequest(t *testing.T) {
	t.Parallel()

	_, err := pagination.CursorDecode[string]("%%%")
	var status interface{ HTTPStatus() int }

	if !errors.As(err, &status) || status.HTTPStatus() != badRequestStatus {
		t.Fatalf("error = %v, want HTTP 400", err)
	}
}

// badRequestStatus is the HTTP status 400 expected from client errors.
const badRequestStatus = 400

// matchesError reports whether err matches want.
func matchesError(err, want error) bool {
	return errors.Is(err, want)
}

// TestPageParamsOffsetFirstPage verifies the offset for firstpage.
func TestPageParamsOffsetFirstPage(t *testing.T) {
	t.Parallel()

	params := pagination.PageParams{Page: 1, PerPage: 25}

	if got := params.Offset(); got != 0 {
		t.Fatalf("offset = %d, want 0", got)
	}
}

// TestPageParamsOffsetThirdPage verifies the offset for thirdpage.
func TestPageParamsOffsetThirdPage(t *testing.T) {
	t.Parallel()

	params := pagination.PageParams{Page: 3, PerPage: 25}

	if got := params.Offset(); got != 50 {
		t.Fatalf("offset = %d, want 50", got)
	}
}

// TestPageParamsOffsetLargeSize verifies the offset for largesize.
func TestPageParamsOffsetLargeSize(t *testing.T) {
	t.Parallel()

	params := pagination.PageParams{Page: 2, PerPage: 100}

	if got := params.Offset(); got != 100 {
		t.Fatalf("offset = %d, want 100", got)
	}
}

// checkPageNormalize fails the test unless params.Normalize(opts...) returns
// want and an error matching wantErr.
func checkPageNormalize(t *testing.T, params pagination.PageParams, want pagination.PageParams, wantErr error, opts ...pagination.Option) {
	t.Helper()

	got, err := params.Normalize(opts...)

	if !matchesError(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}

	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestPageParamsNormalizeZeroUsesDefaults verifies zero params take defaults.
func TestPageParamsNormalizeZeroUsesDefaults(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{}, pagination.PageParams{Page: 1, PerPage: 25}, nil)
}

// TestPageParamsNormalizeCustomDefaults verifies configured defaults apply.
func TestPageParamsNormalizeCustomDefaults(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{}, pagination.PageParams{Page: 3, PerPage: 10}, nil,
		pagination.WithDefaultPage(3),
		pagination.WithDefaultPerPage(10),
	)
}

// TestPageParamsNormalizeCapsPerPage verifies the default maximum.
func TestPageParamsNormalizeCapsPerPage(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: 2, PerPage: 999}, pagination.PageParams{Page: 2, PerPage: 100}, nil)
}

// TestPageParamsNormalizeCustomMaximum verifies a configured maximum.
func TestPageParamsNormalizeCustomMaximum(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: 1, PerPage: 80}, pagination.PageParams{Page: 1, PerPage: 50}, nil,
		pagination.WithMaxPerPage(50),
	)
}

// TestPageParamsNormalizeNegativeRaisedToOne verifies negative values clamp.
func TestPageParamsNormalizeNegativeRaisedToOne(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: -4, PerPage: -2}, pagination.PageParams{Page: 1, PerPage: 1}, nil)
}

// TestPageParamsNormalizeNegativeMaximum verifies an invalid configuration.
func TestPageParamsNormalizeNegativeMaximum(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{}, pagination.PageParams{}, pagination.ErrInvalidConfig,
		pagination.WithConfig(pagination.Config{MaxPerPage: -1}),
	)
}

// TestPageParamsNormalizeOverflowingPage verifies page overflow is rejected.
func TestPageParamsNormalizeOverflowingPage(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: math.MaxInt, PerPage: 100}, pagination.PageParams{}, pagination.ErrPageOverflow)
}

// TestPageParamsNormalizeAcceptsLargestSafeOffset checks the offset bound, not
// page*size: the last representable page may have a partial final window.
func TestPageParamsNormalizeAcceptsLargestSafeOffset(t *testing.T) {
	t.Parallel()

	params := pagination.PageParams{Page: math.MaxInt/2 + 1, PerPage: 2}

	normalized, err := params.Normalize(pagination.WithMaxPage(math.MaxInt))

	if err != nil {
		t.Fatal(err)
	}

	if normalized != params || normalized.Offset() != math.MaxInt-1 {
		t.Fatalf("Normalize() = %+v, offset %d", normalized, normalized.Offset())
	}
}

// TestPageParamsNormalizeRejectsFirstOverflowingOffset checks the adjacent
// invalid page when the configured maximum does not mask arithmetic overflow.
func TestPageParamsNormalizeRejectsFirstOverflowingOffset(t *testing.T) {
	t.Parallel()

	_, err := (pagination.PageParams{Page: math.MaxInt/2 + 2, PerPage: 2}).Normalize(
		pagination.WithMaxPage(math.MaxInt),
	)

	if !errors.Is(err, pagination.ErrPageOverflow) {
		t.Fatalf("Normalize() = %v, want ErrPageOverflow", err)
	}
}

// TestCursorEncodeRejectsUndecodableLength verifies successful encoding cannot
// issue a token that CursorDecode immediately rejects as too long.
func TestCursorEncodeRejectsUndecodableLength(t *testing.T) {
	t.Parallel()

	_, err := pagination.CursorEncode(strings.Repeat("a", 4096))

	if !errors.Is(err, pagination.ErrCursorEncode) {
		t.Fatalf("CursorEncode() = %v, want ErrCursorEncode", err)
	}
}

// TestCursorEncodeAcceptsMaximumLength checks the inclusive unsigned limit
// and exercises both public codec functions with the same boundary token.
func TestCursorEncodeAcceptsMaximumLength(t *testing.T) {
	t.Parallel()

	value := strings.Repeat("a", 3070)

	cursor, err := pagination.CursorEncode(value)

	if err != nil {
		t.Fatal(err)
	}

	if len(cursor) != 4096 {
		t.Fatalf("token length = %d, want 4096", len(cursor))
	}

	decoded, err := pagination.CursorDecode[string](cursor)

	if err != nil || decoded != value {
		t.Fatalf("CursorDecode() = %q, %v", decoded, err)
	}
}

// TestPageParamsNormalizeRejectsPageAboveMax verifies the default page cap.
func TestPageParamsNormalizeRejectsPageAboveMax(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: 10001}, pagination.PageParams{}, pagination.ErrPageOverflow)
}

// TestPageParamsNormalizeAcceptsMaximumPage checks the inclusive page boundary.
// A page exactly at the default maximum must retain its requested size.
func TestPageParamsNormalizeAcceptsMaximumPage(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: 10000, PerPage: 10}, pagination.PageParams{Page: 10000, PerPage: 10}, nil)
}

// TestPageParamsNormalizeWithMaxPage verifies the cap can be raised.
func TestPageParamsNormalizeWithMaxPage(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: 20000, PerPage: 10}, pagination.PageParams{Page: 20000, PerPage: 10}, nil, pagination.WithMaxPage(50000))
}

// TestPageParamsNormalizeRejectsPageAboveCustomMaximum checks a lowered cap.
// The configured maximum must replace the default rather than merely raise it.
func TestPageParamsNormalizeRejectsPageAboveCustomMaximum(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: 6}, pagination.PageParams{}, pagination.ErrPageOverflow, pagination.WithMaxPage(5))
}

// TestPageParamsNormalizeRejectsNegativeMaxPage verifies an invalid cap.
func TestPageParamsNormalizeRejectsNegativeMaxPage(t *testing.T) {
	t.Parallel()

	checkPageNormalize(t, pagination.PageParams{Page: 1}, pagination.PageParams{}, pagination.ErrInvalidConfig, pagination.WithMaxPage(-1))
}

// checkCursorNormalize fails the test unless params.Normalize(opts...) returns
// want and an error matching wantErr.
func checkCursorNormalize(t *testing.T, params pagination.CursorParams, want pagination.CursorParams, wantErr error, opts ...pagination.CursorOption) {
	t.Helper()

	got, err := params.Normalize(opts...)

	if !matchesError(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}

	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestCursorParamsNormalizeZeroUsesDefault verifies zero params take defaults.
func TestCursorParamsNormalizeZeroUsesDefault(t *testing.T) {
	t.Parallel()

	checkCursorNormalize(t, pagination.CursorParams{}, pagination.CursorParams{PerPage: 25}, nil)
}

// TestCursorParamsNormalizeKeepsCursor verifies the cursor is preserved.
func TestCursorParamsNormalizeKeepsCursor(t *testing.T) {
	t.Parallel()

	checkCursorNormalize(t,
		pagination.CursorParams{Cursor: "abc", PerPage: 5},
		pagination.CursorParams{Cursor: "abc", PerPage: 5}, nil)
}

// TestCursorParamsNormalizeCapsPerPage verifies the default maximum.
func TestCursorParamsNormalizeCapsPerPage(t *testing.T) {
	t.Parallel()

	checkCursorNormalize(t, pagination.CursorParams{PerPage: 999}, pagination.CursorParams{PerPage: 100}, nil)
}

// TestCursorParamsNormalizeCustomDefaultAndMaximum verifies configured limits.
func TestCursorParamsNormalizeCustomDefaultAndMaximum(t *testing.T) {
	t.Parallel()

	checkCursorNormalize(t, pagination.CursorParams{}, pagination.CursorParams{PerPage: 7}, nil,
		pagination.WithCursorDefaultPerPage(7),
		pagination.WithCursorMaxPerPage(9),
	)
}

// TestCursorParamsNormalizeNegativeRaisedToOne verifies negative values clamp.
func TestCursorParamsNormalizeNegativeRaisedToOne(t *testing.T) {
	t.Parallel()

	checkCursorNormalize(t, pagination.CursorParams{PerPage: -3}, pagination.CursorParams{PerPage: 1}, nil)
}

// TestCursorParamsNormalizeNegativeMaximum verifies an invalid configuration.
func TestCursorParamsNormalizeNegativeMaximum(t *testing.T) {
	t.Parallel()

	checkCursorNormalize(t, pagination.CursorParams{}, pagination.CursorParams{}, pagination.ErrInvalidConfig,
		pagination.WithCursorConfig(pagination.CursorConfig{MaxPerPage: -1}),
	)
}

// TestCursorPaginateFuncSeparateEncoders verifies next and previous cursors
// use their own encoders.
func TestCursorPaginateFuncSeparateEncoders(t *testing.T) {
	t.Parallel()

	result, err := pagination.CursorPaginateFunc(
		[]int{1, 2, 3},
		3,
		true,
		true,
		func(item int) (string, error) {
			return fmt.Sprintf("next-%d", item), nil
		},
		func(item int) (string, error) {
			return fmt.Sprintf("prev-%d", item), nil
		},
	)

	if err != nil {
		t.Fatalf("CursorPaginateFunc() error = %v", err)
	}

	if result.NextCursor != "next-3" || result.PrevCursor != "prev-1" {
		t.Fatalf("cursors = %q, %q, want next-3, prev-1", result.NextCursor, result.PrevCursor)
	}
}

// TestPaginatePastLastPageKeepsRequestedPage verifies a page beyond the end
// reports the requested page, matching its empty items.
func TestPaginatePastLastPageKeepsRequestedPage(t *testing.T) {
	t.Parallel()

	page := pagination.Paginate([]int{}, 30, 9, 10)

	if page.CurrentPage != 9 || page.LastPage != 3 || len(page.Items) != 0 {
		t.Fatalf("page = %+v, want current 9, last 3, no items", page)
	}
}

// TestPaginateOwnsItemSlice verifies the result owns its outer item container,
// while item objects themselves retain their normal shallow-copy semantics.
func TestPaginateOwnsItemSlice(t *testing.T) {
	t.Parallel()

	items := []int{1, 2}
	page := pagination.Paginate(items, 2, 1, 2)
	items[0] = 9

	if page.Items[0] != 1 {
		t.Fatalf("published Items = %v, want [1 2]", page.Items)
	}

	page.Items[1] = 8

	if items[1] != 2 {
		t.Fatalf("input items = %v, want [9 2]", items)
	}
}

// TestCursorPaginateOwnsItemSlice checks the cursor result has the same outer
// container ownership as offset pages, even when no encoders are needed.
func TestCursorPaginateOwnsItemSlice(t *testing.T) {
	t.Parallel()

	items := []int{1, 2}

	page, err := pagination.CursorPaginate(items, 2, false, false, nil)

	if err != nil {
		t.Fatal(err)
	}

	items[0] = 9

	if page.Items[0] != 1 {
		t.Fatalf("published Items = %v, want [1 2]", page.Items)
	}

	page.Items[1] = 8

	if items[1] != 2 {
		t.Fatalf("input items = %v, want [9 2]", items)
	}
}

// TestCursorPaginateEncodesCapturedItems verifies the next encoder cannot
// replace the previous boundary by modifying the caller's outer input slice.
func TestCursorPaginateEncodesCapturedItems(t *testing.T) {
	t.Parallel()

	items := []int{1, 2}

	page, err := pagination.CursorPaginateFunc(items, 2, true, true,
		func(item int) (string, error) {
			items[0] = 9

			return fmt.Sprintf("next-%d", item), nil
		},
		func(item int) (string, error) {
			return fmt.Sprintf("prev-%d", item), nil
		})

	if err != nil {
		t.Fatal(err)
	}

	if page.PrevCursor != "prev-1" || page.Items[0] != 1 {
		t.Fatalf("page = %+v, want captured first boundary", page)
	}
}

// TestCursorPaginateEmptyItemsSkipEncoders pins empty-array output and retained
// metadata without requiring a callable encoder for a nonexistent boundary.
func TestCursorPaginateEmptyItemsSkipEncoders(t *testing.T) {
	t.Parallel()

	page, err := pagination.CursorPaginate([]int(nil), 0, true, true, nil)

	if err != nil {
		t.Fatal(err)
	}

	if page.Items == nil || page.PerPage != 0 {
		t.Fatalf("page = %+v, want non-nil items and unchanged size", page)
	}

	if page.NextCursor != "" || page.PrevCursor != "" {
		t.Fatalf("empty page advertised navigation: %+v", page)
	}
}

// TestCursorPaginateNextFailureStopsPrevious verifies error identity, copied
// items, unnormalized size metadata, and callback short-circuit order together.
func TestCursorPaginateNextFailureStopsPrevious(t *testing.T) {
	t.Parallel()

	cause := errors.New("next encoding failed")
	previousCalled := false

	page, err := pagination.CursorPaginateFunc([]int{1, 2}, -3, true, true,
		func(int) (string, error) {
			return "", cause
		},
		func(int) (string, error) {
			previousCalled = true

			return "previous", nil
		})

	if !errors.Is(err, pagination.ErrCursorEncode) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cursor sentinel and cause", err)
	}

	if previousCalled || page.PerPage != -3 || len(page.Items) != 2 {
		t.Fatalf("partial page = %+v, previous called = %v", page, previousCalled)
	}
}

// TestCursorPaginatePreviousFailureReturnsPartialNext verifies a successful
// next token remains visible alongside a later error, not as a successful page.
func TestCursorPaginatePreviousFailureReturnsPartialNext(t *testing.T) {
	t.Parallel()

	cause := errors.New("previous encoding failed")

	page, err := pagination.CursorPaginateFunc([]int{1, 2}, 2, true, true,
		func(int) (string, error) {
			return "next", nil
		},
		func(int) (string, error) {
			return "", cause
		})

	if !errors.Is(err, pagination.ErrCursorEncode) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cursor sentinel and cause", err)
	}

	if page.NextCursor != "next" || page.PrevCursor != "" {
		t.Fatalf("partial cursors = %+v", page)
	}
}
