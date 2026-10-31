package pagination

import (
	"cmp"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"math"
	"slices"

	"arkoren.dev/foundation/collection"
	"arkoren.dev/foundation/problem"
)

// statusBadRequest is the HTTP 400 status code. It is a literal so the
// package needs no net/http import.
const statusBadRequest = 400

// ErrCursorEncode classifies serialization, callback, invalid-codec, and token
// size failures. Wrapped callback and serialization causes remain discoverable
// with errors.Is/errors.As; never treat a partial cursor result as successful.
//
// Example:
//
//	_, err := pagination.CursorEncode(make(chan int))
//	if errors.Is(err, pagination.ErrCursorEncode) {
//		fmt.Println("cursor could not be encoded")
//	}
var ErrCursorEncode = errors.New("failed to encode cursor")

// ErrCursorDecode is returned when a cursor string fails to decode. It is a
// [problem.Details], so a handler that returns it responds 400 Bad Request.
// Callers match it, or an error wrapping it, with [errors.Is].
//
// Example:
//
//	failure := pagination.ErrCursorDecode.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrCursorDecode = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:pagination:cursor-decode")
	details.Title("Invalid cursor")
	details.Detail("The cursor could not be decoded.")
	details.Status(statusBadRequest)
})

// ErrInvalidConfig is returned when a pagination maximum is less
// than one after zero defaults are completed. It is a configuration failure,
// not a request overflow; callers match it through errors.Is.
//
// Example:
//
//	_, err := (pagination.PageParams{}).Normalize(pagination.WithMaxPerPage(-1))
//	if errors.Is(err, pagination.ErrInvalidConfig) {
//		fmt.Println("configure a positive maximum")
//	}
var ErrInvalidConfig = errors.New("invalid pagination config")

// ErrPageOverflow is returned when a page is above [Config.MaxPage] or the
// offset of a page would overflow an int. It is a [problem.Details], so a handler that returns it responds 400
// Bad Request. Callers match it with [errors.Is].
//
// Example:
//
//	failure := pagination.ErrPageOverflow.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrPageOverflow = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:pagination:page-overflow")
	details.Title("Page out of range")
	details.Detail("The requested page is out of range.")
	details.Status(statusBadRequest)
})

// maxCursorLength bounds complete encoded tokens for both codecs. Encoders
// reject oversized data before base64 allocation; decoders reject raw input.
const maxCursorLength = 4096

// Config configures offset-based request pagination. Zero fields use the
// built-in defaults when PageParams.Normalize consumes the configuration.
// NewConfig itself applies options without validation or zero completion.
//
// Example:
//
//	config := pagination.Config{DefaultPerPage: 10, MaxPerPage: 50}
//	params, err := (pagination.PageParams{}).Normalize(pagination.WithConfig(config))
//	if err != nil {
//		return err
//	}
//	_ = params.PerPage // 10.
type Config struct {
	// DefaultPage is the fallback page number when a request omits page.
	// Normalize also uses it for explicit zero, then raises negatives to one.
	//
	// Example:
	//
	//	config := pagination.NewConfig(pagination.WithDefaultPage(2))
	//	_ = config.DefaultPage // 2.
	DefaultPage int

	// DefaultPerPage is the fallback page size when a request omits per_page.
	// Normalize also uses it for explicit zero, then clamps to positive limits.
	//
	// Example:
	//
	//	config := pagination.NewConfig(pagination.WithDefaultPerPage(10))
	//	_ = config.DefaultPerPage // 10.
	DefaultPerPage int

	// MaxPerPage caps the page size accepted from a request.
	// Zero uses 100 during normalization; negative values are invalid.
	//
	// Example:
	//
	//	config := pagination.NewConfig(pagination.WithMaxPerPage(50))
	//	_ = config.MaxPerPage // 50.
	MaxPerPage int

	// MaxPage caps the page number accepted from a request, because a deep
	// page forces the database to scan and discard every row before it.
	// Zero uses 10000. Raise it with [WithMaxPage] for datasets that need
	// deeper offsets, or prefer cursor pagination for them.
	//
	// Example:
	//
	//	config := pagination.NewConfig(pagination.WithMaxPage(500))
	//	_ = config.MaxPage // 500.
	MaxPage int
}

// CursorConfig configures cursor-based request pagination. Zero fields use
// built-in defaults when CursorParams.Normalize consumes it. Construction
// alone neither completes missing fields nor validates a supplied maximum.
//
// Example:
//
//	config := pagination.CursorConfig{DefaultPerPage: 10, MaxPerPage: 50}
//	params, err := (pagination.CursorParams{}).Normalize(pagination.WithCursorConfig(config))
//	if err != nil {
//		return err
//	}
//	_ = params.PerPage // 10.
type CursorConfig struct {
	// DefaultPerPage is the fallback page size when a request omits per_page.
	// Zero uses 25 at normalization, which then applies positive size limits.
	//
	// Example:
	//
	//	config := pagination.NewCursorConfig(pagination.WithCursorDefaultPerPage(10))
	//	_ = config.DefaultPerPage // 10.
	DefaultPerPage int

	// MaxPerPage caps the page size accepted from a request.
	// Zero uses 100 at normalization; negative values yield ErrInvalidConfig.
	//
	// Example:
	//
	//	config := pagination.NewCursorConfig(pagination.WithCursorMaxPerPage(50))
	//	_ = config.MaxPerPage // 50.
	MaxPerPage int
}

// defaultConfig returns standard offset pagination defaults.
func defaultConfig() Config {
	return Config{
		DefaultPage:    1,
		DefaultPerPage: 25,
		MaxPerPage:     100,
		MaxPage:        10000,
	}
}

// defaultCursorConfig returns standard cursor pagination defaults.
func defaultCursorConfig() CursorConfig {
	return CursorConfig{
		DefaultPerPage: 25,
		MaxPerPage:     100,
	}
}

// PageParams holds offset pagination parameters. It can be built by hand
// or read from a request with request.Page. Call [PageParams.Normalize]
// to apply defaults and limits before use.
// Normalization does not fetch items and does not mutate the original value.
//
// Example:
//
//	params, err := (pagination.PageParams{Page: 3, PerPage: 20}).Normalize()
//	if err != nil {
//		return err
//	}
//	_ = params.Offset() // 40.
type PageParams struct {
	// Page is the one-based page number. Zero means the default page.
	// Normalize raises negatives to one and rejects values beyond its bounds.
	//
	// Example:
	//
	//	params := pagination.PageParams{Page: 3, PerPage: 20}
	//	_ = params.Page // 3.
	Page int

	// PerPage is the requested page size. Zero means the default size.
	// Normalize raises negatives to one and caps the final size at MaxPerPage.
	//
	// Example:
	//
	//	params := pagination.PageParams{Page: 3, PerPage: 20}
	//	_ = params.PerPage // 20.
	PerPage int
}

// CursorParams holds cursor pagination parameters. It can be built by hand
// or read from a request with request.Cursor. Call
// [CursorParams.Normalize] to apply defaults and limits before use.
// The token stays opaque; normalization neither decodes nor authenticates it.
//
// Example:
//
//	params, err := (pagination.CursorParams{PerPage: 20}).Normalize()
//	if err != nil {
//		return err
//	}
//	_ = params.Cursor // Empty selects the first window.
type CursorParams struct {
	// Cursor is the opaque cursor string. Empty means the first window.
	// Normalize retains it byte-for-byte; the consumer's codec validates it.
	//
	// Example:
	//
	//	params := pagination.CursorParams{Cursor: "", PerPage: 20}
	//	_ = params.Cursor // First window, without a continuation token.
	Cursor string

	// PerPage is the requested window size. Zero means the default size.
	// Normalize raises negatives to one and caps the size at MaxPerPage.
	//
	// Example:
	//
	//	params := pagination.CursorParams{PerPage: 20}
	//	_ = params.PerPage // 20.
	PerPage int
}

// Offset returns the number of items before the page, for use as a query
// OFFSET. It expects normalized parameters.
// No overflow check is repeated here; callers first use Normalize to establish
// a positive size/page and a representable result.
//
// Example:
//
//	params, err := (pagination.PageParams{Page: 3, PerPage: 20}).Normalize()
//	if err != nil {
//		return err
//	}
//	_ = params.Offset() // 40.
func (params PageParams) Offset() int {
	return Offset(params.Page, params.PerPage)
}

// Normalize replaces zero fields with the defaults from opts, raises
// values below one to one, and caps PerPage at the maximum. It returns
// [ErrInvalidConfig] when a maximum is less than one, and
// [ErrPageOverflow] when the page is above [Config.MaxPage] or its offset
// would overflow.
//
// Example:
//
//	params, err := (pagination.PageParams{Page: 3}).Normalize()
//	if err != nil {
//		return err
//	}
//	_ = params.Offset() // 50, using the default size of 25.
func (params PageParams) Normalize(opts ...Option) (PageParams, error) {
	config := NewConfig(opts...).withDefaults()

	if config.MaxPerPage < 1 || config.MaxPage < 1 {
		return PageParams{}, ErrInvalidConfig
	}

	page := max(cmp.Or(params.Page, config.DefaultPage), 1)
	perPage := min(max(cmp.Or(params.PerPage, config.DefaultPerPage), 1), config.MaxPerPage)

	if page > config.MaxPage || page-1 > math.MaxInt/perPage {
		return PageParams{}, ErrPageOverflow
	}

	return PageParams{Page: page, PerPage: perPage}, nil
}

// Normalize replaces a zero PerPage with the default from opts, raises
// values below one to one, and caps PerPage at the maximum. It returns
// [ErrInvalidConfig] when the maximum is less than one.
// The cursor token is retained unchanged and is not decoded or authenticated.
//
// Example:
//
//	params, err := (pagination.CursorParams{}).Normalize(pagination.WithCursorDefaultPerPage(10))
//	if err != nil {
//		return err
//	}
//	_ = params.PerPage // 10.
func (params CursorParams) Normalize(opts ...CursorOption) (CursorParams, error) {
	config := NewCursorConfig(opts...).withDefaults()

	if config.MaxPerPage < 1 {
		return CursorParams{}, ErrInvalidConfig
	}

	perPage := min(max(cmp.Or(params.PerPage, config.DefaultPerPage), 1), config.MaxPerPage)

	return CursorParams{Cursor: params.Cursor, PerPage: perPage}, nil
}

// Offset returns the number of items before page, for use as a query
// OFFSET. page and perPage are one-based values from request.Page.
// This unchecked arithmetic helper expects parameters already accepted by
// PageParams.Normalize; arbitrary input can overflow or produce a negative offset.
//
// Example:
//
//	_ = pagination.Offset(3, 20) // 40.
func Offset(page, perPage int) int {
	return (page - 1) * perPage
}

// withDefaults completes missing offset pagination configuration values.
func (config Config) withDefaults() Config {
	defaults := defaultConfig()

	config.DefaultPage = cmp.Or(config.DefaultPage, defaults.DefaultPage)
	config.DefaultPerPage = cmp.Or(config.DefaultPerPage, defaults.DefaultPerPage)
	config.MaxPerPage = cmp.Or(config.MaxPerPage, defaults.MaxPerPage)
	config.MaxPage = cmp.Or(config.MaxPage, defaults.MaxPage)

	return config
}

// withDefaults completes missing cursor pagination configuration values.
func (config CursorConfig) withDefaults() CursorConfig {
	defaults := defaultCursorConfig()

	config.DefaultPerPage = cmp.Or(config.DefaultPerPage, defaults.DefaultPerPage)
	config.MaxPerPage = cmp.Or(config.MaxPerPage, defaults.MaxPerPage)

	return config
}

// Paginate creates a new [Page] from the given items, total count, current page
// number, and items per page. It computes the last page automatically. A
// current page below 1 becomes 1; a page past Page.LastPage is kept, so a
// request beyond the end reports the page it asked for with no items.
// Per-page values below 1 become 1. LastPage is at least 1 and capped at
// math.MaxInt, even for totals up to math.MaxInt64; Total is retained unchanged.
// Items are copied into a new outer slice; values referenced by individual
// items are shared, not recursively copied. Nil items become a non-nil empty
// slice. The helper does not trim items to PerPage or fetch missing rows.
// The returned Items collection supports local transformations without changing
// the pagination metadata.
//
//	page := pagination.Paginate([]string{"invoice"}, 21, 2, 10)
//	fmt.Println(page.CurrentPage, page.LastPage) // 2 3
func Paginate[S ~[]T, T any](items S, total int64, page, perPage int) Page[T] {
	items = slices.Clone(items)
	perPage = max(perPage, 1)
	pages := max(total, 0) / int64(perPage)

	if total%int64(perPage) > 0 {
		pages++
	}

	lastPage := max(int(min(pages, math.MaxInt)), 1)
	page = max(page, 1)

	if items == nil {
		items = S{
			// Preserve an empty, non-nil result slice.
		}
	}

	return Page[T]{
		Items:       collection.NewSliceAs(items),
		Total:       total,
		PerPage:     perPage,
		CurrentPage: page,
		LastPage:    lastPage,
	}
}

// CursorPaginate creates a new [Cursor] from the given items. The encode
// function determines how each item is transformed into an opaque cursor string.
// The outer item slice is copied; referenced item objects remain shared.
// PerPage is retained verbatim as metadata, without normalization or trimming.
// An empty result invokes no encoder and has neither cursor, regardless of flags.
// Otherwise hasNext encodes the last item before hasPrev encodes the first item.
// Supply a non-nil encoder for each requested cursor; callback panics propagate.
// Encoder failures wrap [ErrCursorEncode] and preserve their cause. The returned
// result still contains the copied items and any cursor already encoded, so
// callers must check the error rather than treating that partial result as success.
//
// Example:
//
//	page, err := pagination.CursorPaginate([]int{41, 42}, 2, true, false, func(id int) (string, error) {
//		return pagination.CursorEncode(id)
//	})
//	if err != nil {
//		return err
//	}
//	_ = page.NextCursor
func CursorPaginate[S ~[]T, T any](items S, perPage int, hasNext, hasPrev bool, encode func(T) (string, error)) (Cursor[T], error) {
	return CursorPaginateFunc(items, perPage, hasNext, hasPrev, encode, encode)
}

// CursorPaginateFunc creates a new [Cursor] like [CursorPaginate] but with
// separate encoders: encodeNext turns the final item into the next cursor
// and encodePrev turns the first item into the previous cursor. Use it when
// the two cursors must carry different information, such as a direction.
// Ownership, unnormalized metadata, callback timing, and partial-error results
// follow CursorPaginate. Encoders read the captured outer slice, so changing
// the caller's slice entries during encodeNext does not replace encodePrev's
// boundary. Referenced item objects and callback captures are still shared.
//
// Example:
//
//	page, err := pagination.CursorPaginateFunc([]int{41, 42}, 2, true, true,
//		func(id int) (string, error) {
//			return pagination.CursorEncode(map[string]any{"id": id, "backward": false})
//		},
//		func(id int) (string, error) {
//			return pagination.CursorEncode(map[string]any{"id": id, "backward": true})
//		})
//	if err != nil {
//		return err
//	}
//	_ = page.PrevCursor
func CursorPaginateFunc[S ~[]T, T any](
	items S,
	perPage int,
	hasNext, hasPrev bool,
	encodeNext, encodePrev func(T) (string, error),
) (Cursor[T], error) {
	result := cursorResult(items, perPage)

	if len(items) == 0 {
		return result, nil
	}

	if hasNext {
		if err := setNextCursor(&result, encodeNext); err != nil {
			return result, err
		}
	}

	if hasPrev {
		return result, setPreviousCursor(&result, encodePrev)
	}

	return result, nil
}

// cursorResult snapshots the outer item container without recursively copying
// its elements. Nil becomes an empty non-nil slice for stable JSON arrays.
func cursorResult[S ~[]T, T any](items S, perPage int) Cursor[T] {
	items = slices.Clone(items)

	if items == nil {
		items = S{
			// Preserve an empty, non-nil result slice.
		}
	}

	return Cursor[T]{
		Items:   collection.NewSliceAs(items),
		PerPage: perPage,
	}
}

// setNextCursor encodes the final captured item after the caller established a
// nonempty window. It changes NextCursor only on success; callback failures
// retain their cause under ErrCursorEncode without replacing existing metadata.
func setNextCursor[T any](result *Cursor[T], encode func(T) (string, error)) error {
	encoded, err := encode(result.Items[len(result.Items)-1])

	if err != nil {
		return errors.Join(ErrCursorEncode, err)
	}

	result.NextCursor = encoded

	return nil
}

// setPreviousCursor encodes the first captured item of a nonempty window.
// It changes PrevCursor only on success and wraps failures with ErrCursorEncode,
// leaving a previously encoded next token available in the partial result.
func setPreviousCursor[T any](result *Cursor[T], encode func(T) (string, error)) error {
	encoded, err := encode(result.Items[0])

	if err != nil {
		return errors.Join(ErrCursorEncode, err)
	}

	result.PrevCursor = encoded

	return nil
}

// CursorEncode encodes a value into an opaque cursor string using JSON
// serialization and base64url encoding. Serialization failures wrap
// [ErrCursorEncode]. A result longer than the 4096-byte [CursorDecode] limit
// fails with ErrCursorEncode instead of returning an unusable cursor.
//
// Example:
//
//	cursor, err := pagination.CursorEncode(42)
//	if err != nil {
//		return err
//	}
//	position, err := pagination.CursorDecode[int](cursor)
//	if err != nil {
//		return err
//	}
//	_ = position
func CursorEncode[T any](value T) (string, error) {
	data, err := json.Marshal(value)

	if err != nil {
		return "", errors.Join(ErrCursorEncode, err)
	}

	if len(data) > base64.RawURLEncoding.DecodedLen(maxCursorLength) {
		return "", ErrCursorEncode
	}

	return base64.RawURLEncoding.EncodeToString(data), nil
}

// CursorDecode decodes an opaque cursor string back into a typed value. It
// reverses the encoding performed by [CursorEncode]. Cursors are not signed:
// treat decoded values as untrusted client input.
// Inputs above 4096 bytes, invalid base64url, or incompatible JSON return an
// error matching ErrCursorDecode. A valid token supplies data, not authorization.
//
// Example:
//
//	cursor, err := pagination.CursorEncode(42)
//	if err != nil {
//		return err
//	}
//	value, err := pagination.CursorDecode[int](cursor)
//	if err != nil {
//		return err
//	}
//	_ = value // 42.
func CursorDecode[T any](cursor string) (T, error) {
	var value T

	if len(cursor) > maxCursorLength {
		return value, ErrCursorDecode
	}

	data, err := base64.RawURLEncoding.DecodeString(cursor)

	if err != nil {
		return value, errors.Join(ErrCursorDecode, err)
	}

	if err := json.Unmarshal(data, &value); err != nil {
		return value, errors.Join(ErrCursorDecode, err)
	}

	return value, nil
}
