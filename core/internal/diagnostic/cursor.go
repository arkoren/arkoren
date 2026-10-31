package diagnostic

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"time"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// Page contains a bounded cursor result. History snapshots may be truncated;
// continuation pages instead report HasMore and never skip matching records.
// Example: var page diagnostic.Page[string]; _ = page.Cursor
type Page[T any] struct {
	// Items contains records oldest first, never null.
	// Example: for _, item := range page.Items { fmt.Println(item) }
	Items []T `json:"items"`
	// Truncated reports history omitted from a cursor-less snapshot.
	// Example: if page.Truncated { fmt.Println("history bounded") }
	Truncated bool `json:"truncated"`
	// Cursor is an opaque continuation token, including when Items is empty.
	// Example: arguments.Cursor = page.Cursor
	Cursor string `json:"cursor"`
	// Dropped counts overwritten records, including uninspectable nonmatches.
	// Example: if page.Dropped > 0 { fmt.Println("records lost") }
	Dropped uint64 `json:"dropped"`
	// HasMore means retained matching records remain after this page.
	// Example: if page.HasMore { /* call again without waiting */ }
	HasMore bool `json:"has_more"`
	// InstanceChanged marks store replacement or reset; Cursor is a fresh tail.
	// Example: if page.InstanceChanged { fmt.Println("capture restarted") }
	InstanceChanged bool `json:"instance_changed"`
}

// Snapshot atomically pairs retained contiguous records with the high-water
// mark and notification channel. Owners copy records while holding their lock.
// Example: snapshot := diagnostic.Snapshot[string]{Items: []string{}, Instance: id}
type Snapshot[T any] struct {
	// Items holds the newest len(Items) records, in ascending sequence order.
	// Example: snapshot.Items = []string{"record"}
	Items []T
	// Total is the sequence number of the newest record, even on an empty ring.
	// Example: snapshot.Total = 1
	Total uint64
	// Instance changes on reset and is unique per store, not per tool factory.
	// Example: snapshot.Instance = diagnostic.NewInstance()
	Instance string
	// Changed closes when records arrive or the store resets.
	// Example: select { case <-snapshot.Changed: case <-ctx.Done(): }
	Changed <-chan struct{}
}

// Feed binds a store snapshot to owner-specific filtering and rendering.
// All functions are synchronous; Snapshot must take one atomic observation.
// Example: feed := diagnostic.Feed[string,string]{Snapshot: snapshot, Match: match, Render: render}
type Feed[T, Out any] struct {
	// Snapshot copies a consistent ring state and notification.
	// Example: state := feed.Snapshot()
	Snapshot func() Snapshot[T]
	// Match decides whether a retained record belongs in this query.
	// Example: accepted := feed.Match(record)
	Match func(T) bool
	// Render converts an accepted record to the owner diagnostic DTO.
	// Example: item := feed.Render(record)
	Render func(T) Out
}

// CursorRequest contains one bounded call's controls. Key includes the tool
// identity and normalized filters, excluding cursor, wait, and page size.
// Example: request := diagnostic.CursorRequest{Key: "app.events|*", Limit: 100}
type CursorRequest struct {
	// Key binds a cursor to exactly one tool and filter combination.
	// Example: request.Key = "app.events|billing.*"
	Key string
	// Cursor is empty for history, "tail" for an immediate empty anchor.
	// Example: request.Cursor = "tail"
	Cursor string
	// Wait bounds an idle continuation call; zero returns immediately.
	// Example: request.Wait = time.Second
	Wait time.Duration
	// Limit bounds emitted items and must be between 1 and 1000.
	// Example: request.Limit = 100
	Limit int
}

// position is the versioned, opaque cursor payload. The decimal sequence
// string avoids losing uint64 precision in generic JSON clients.
type position struct {
	// Version selects the token encoding contract.
	Version int `json:"v"`
	// Instance identifies one uninterrupted store generation.
	Instance string `json:"i"`
	// Key is the digest of tool identity and normalized filters.
	Key string `json:"k"`
	// Sequence is encoded as a decimal string to preserve all uint64 values.
	Sequence uint64 `json:"s,string"`
}

// NewInstance creates a nonsecret store-generation identifier. Resetting a
// store assigns a new identifier so empty resets remain observable.
// Example: id := diagnostic.NewInstance()
func NewInstance() string {
	return rand.Text()
}

// WaitSchema describes a polling duration with a hard 25-second bound.
// The owner may omit this property for an immediate diagnostic snapshot.
// Example: schema := diagnostic.WaitSchema()
func WaitSchema() validate.TextSchema[validate.Duration] {
	return validate.Text(func(wait *validate.TextBuilder[validate.Duration]) {
		wait.Use(DurationSchema("Wait for matching records, at most 25s; requires a continuation cursor"))
		wait.Check(func(_ context.Context, value validate.Duration) error {
			if value.Std() > 25*time.Second {
				return validate.Fail("duration.maximum", nil)
			}

			return nil
		})
	})
}

// Poll returns a history snapshot, immediate tail anchor, or bounded
// continuation. One fixed timer spans every notification, including nonmatches.
// Cancellation returns ctx.Err without manufacturing a successful response.
// Malformed, foreign-filter, and future cursors produce a safe ToolFailure.
// Example: page, err := diagnostic.Poll(ctx, request, feed)
func Poll[T, Out any](ctx context.Context, request CursorRequest, feed Feed[T, Out]) (Page[Out], error) {
	if request.Limit < 1 || request.Limit > 1000 {
		return Page[Out]{}, mcp.ToolFailure("invalid_input", "limit must be between 1 and 1000", nil)
	}

	if request.Wait < 0 || request.Wait > 25*time.Second {
		return Page[Out]{}, mcp.ToolFailure("invalid_input", "wait must be between 0s and 25s", nil)
	}

	if err := ctx.Err(); err != nil {
		return Page[Out]{}, err
	}

	state := feed.Snapshot()

	if request.Cursor == "" || request.Cursor == "tail" {
		return initialPage(request, state, feed), nil
	}

	return continuePage(ctx, request, state, feed)
}

// initialPage intentionally bounds historical results to the newest matches.
// Its cursor anchors at the atomic snapshot high-water mark, not at a match.
func initialPage[T, Out any](request CursorRequest, state Snapshot[T], feed Feed[T, Out]) Page[Out] {
	page := Page[Out]{Items: []Out{}, Cursor: encodePosition(request.Key, state.Instance, state.Total)}

	if request.Cursor == "tail" {
		return page
	}

	for _, item := range state.Items {
		if feed.Match(item) {
			page.Items = append(page.Items, feed.Render(item))
		}
	}

	if len(page.Items) > request.Limit {
		page.Items = page.Items[len(page.Items)-request.Limit:]
		page.Truncated = true
	}

	return page
}

// continuePage waits only after an empty, lossless page. Reset is reported as
// an empty page at the new tail, without mixing records from two generations.
func continuePage[T, Out any](
	ctx context.Context,
	request CursorRequest,
	state Snapshot[T],
	feed Feed[T, Out],
) (Page[Out], error) {
	position, err := decodePosition(request, state.Instance, state.Total)

	if err != nil {
		return Page[Out]{}, err
	}

	timer := time.NewTimer(request.Wait)
	defer timer.Stop()

	for {
		page := scanPage(request, state, feed, &position)

		if len(page.Items) > 0 || page.Dropped > 0 || page.InstanceChanged {
			return page, nil
		}

		if request.Wait == 0 {
			return page, nil
		}

		select {
		case <-ctx.Done():
			return Page[Out]{}, ctx.Err()
		case <-timer.C:
			return page, nil
		case <-state.Changed:
			state = feed.Snapshot()
		}
	}
}

// scanPage advances across excluded records but stops before an unreturned
// match. Loss is measured from total and retained length even on an empty ring.
func scanPage[T, Out any](request CursorRequest, state Snapshot[T], feed Feed[T, Out], position *position) Page[Out] {
	page := Page[Out]{Items: []Out{}}

	if position.Instance != state.Instance {
		page.InstanceChanged = true
		page.Cursor = encodePosition(request.Key, state.Instance, state.Total)

		return page
	}

	oldest := state.Total - uint64(len(state.Items))

	if position.Sequence < oldest {
		page.Dropped = oldest - position.Sequence
		position.Sequence = oldest
	}

	for index := int(position.Sequence - oldest); index < len(state.Items); index++ {
		item := state.Items[index]

		if !feed.Match(item) {
			position.Sequence = oldest + uint64(index) + 1
			continue
		}

		if len(page.Items) == request.Limit {
			page.HasMore = true
			break
		}

		page.Items = append(page.Items, feed.Render(item))
		position.Sequence = oldest + uint64(index) + 1
	}

	page.Cursor = encodePosition(request.Key, state.Instance, position.Sequence)

	return page
}

// cursorKey hashes owner-normalized filters so cursor metadata does not reveal
// user filter contents. This is a binding identifier, not an authorization token.
func cursorKey(key string) string {
	digest := sha256.Sum256([]byte(key))

	return hex.EncodeToString(digest[:])
}

// encodePosition produces a deterministic, versioned token with decimal sequence.
func encodePosition(key, instance string, sequence uint64) string {
	// This private record has only primitive fields and cannot fail encoding.
	value, _ := json.Marshal(position{Version: 1, Instance: instance, Key: cursorKey(key), Sequence: sequence})

	return base64.RawURLEncoding.EncodeToString(value)
}

// decodePosition rejects malformed tokens and foreign tool/filter bindings
// before reading records. Future positions are invalid only in the same store
// generation; another generation is a valid reset/replacement observation.
func decodePosition(request CursorRequest, instance string, total uint64) (position, error) {
	var value position

	bytes, err := base64.RawURLEncoding.DecodeString(request.Cursor)

	if err != nil {
		return value, mcp.ToolFailure("invalid_cursor", "invalid diagnostic cursor", nil)
	}

	if err := json.Unmarshal(bytes, &value); err != nil {
		return value, mcp.ToolFailure("invalid_cursor", "invalid diagnostic cursor", nil)
	}

	if value.Version != 1 || value.Key != cursorKey(request.Key) || value.Instance == "" {
		return value, mcp.ToolFailure("invalid_cursor", "cursor does not belong to this tool and filter", nil)
	}

	if value.Instance == instance && value.Sequence > total {
		return value, mcp.ToolFailure("invalid_cursor", "cursor is ahead of this store", nil)
	}

	return value, nil
}
