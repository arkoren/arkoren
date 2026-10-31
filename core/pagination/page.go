package pagination

import "arkoren.dev/foundation/collection"

// Page represents an offset-based paginated result set. Paginate constructs its
// counts and snapshots the outer item slice, leaving referenced objects shared.
// Items transformations operate only on this window; they do not fetch more
// items or recompute Total, PerPage, CurrentPage, or LastPage.
//
// Example:
//
//	page := pagination.Paginate([]string{"invoice"}, 21, 2, 10)
//	_ = page.LastPage // 3.
type Page[T any] struct {
	// Items contains the current page as an eager collection, detached from the
	// input supplied to Paginate. Referenced objects are not recursively copied.
	// Map, Filter, and GroupBy transform only these items, not server metadata.
	//
	// Example:
	//
	//	page := pagination.Paginate([]int{41, 42}, 2, 1, 2)
	//	values := page.Items.Map(func(_ int, id int) string {
	//		return fmt.Sprint(id)
	//	})
	//	_ = values[0] // "41".
	Items collection.Slice[T] `json:"items"`

	// Total is the total number of items across all pages.
	// Paginate retains it unchanged, including zero or a negative caller value.
	//
	// Example:
	//
	//	page := pagination.Paginate([]int{1}, 21, 1, 10)
	//	_ = page.Total // 21.
	Total int64 `json:"total"`

	// PerPage is the maximum number of items in each page.
	// Paginate raises it to one when needed but does not trim the item slice.
	//
	// Example:
	//
	//	page := pagination.Paginate([]int{1}, 21, 1, 10)
	//	_ = page.PerPage // 10.
	PerPage int `json:"per_page"`

	// CurrentPage is the one-based number of the current page.
	// Paginate retains a request past LastPage rather than inventing new items.
	//
	// Example:
	//
	//	page := pagination.Paginate([]int(nil), 21, 5, 10)
	//	_ = page.CurrentPage // 5.
	CurrentPage int `json:"current_page"`

	// LastPage is the one-based number of the final available page.
	// Paginate computes it from Total and PerPage, with a minimum of one.
	//
	// Example:
	//
	//	page := pagination.Paginate([]int{1}, 21, 1, 10)
	//	_ = page.LastPage // 3.
	LastPage int `json:"last_page"`
}

// Cursor represents a cursor-based window without a total count. CursorPaginate
// snapshots the outer item slice and encodes only requested navigation tokens.
// Local Items transformations neither fetch another window nor recompute tokens;
// cursors continue to identify the original window's boundaries.
//
// Example:
//
//	page, err := pagination.CursorPaginate([]int{41, 42}, 2, false, false, nil)
//	if err != nil {
//		return err
//	}
//	_ = page.Items
type Cursor[T any] struct {
	// Items contains an eager collection detached from the cursor helper's input.
	// Referenced objects remain shared, including callback-visible objects.
	// Transformations affect only this window and leave its tokens unchanged.
	//
	// Example:
	//
	//	page, err := pagination.CursorPaginate([]int{41, 42}, 2, false, false, nil)
	//	if err != nil {
	//		return err
	//	}
	//	_ = page.Items[0] // 41.
	Items collection.Slice[T] `json:"items"`

	// PerPage is the maximum number of items in the cursor window.
	// Cursor helpers retain this caller-supplied metadata without normalization.
	//
	// Example:
	//
	//	page, err := pagination.CursorPaginate([]int{41, 42}, 2, false, false, nil)
	//	if err != nil {
	//		return err
	//	}
	//	_ = page.PerPage // 2.
	PerPage int `json:"per_page"`

	// NextCursor identifies the next cursor window when one is available.
	// Empty means no next token was emitted; clients must pass tokens unchanged.
	//
	// Example:
	//
	//	page, err := pagination.CursorPaginate([]int{41, 42}, 2, true, false, pagination.CursorEncode[int])
	//	if err != nil {
	//		return err
	//	}
	//	_ = page.NextCursor
	NextCursor string `json:"next_cursor,omitempty"`

	// PrevCursor identifies the previous cursor window when one is available.
	// Empty means no previous token was emitted, including for an empty result.
	//
	// Example:
	//
	//	page, err := pagination.CursorPaginate([]int{41, 42}, 2, false, true, pagination.CursorEncode[int])
	//	if err != nil {
	//		return err
	//	}
	//	_ = page.PrevCursor
	PrevCursor string `json:"prev_cursor,omitempty"`
}
