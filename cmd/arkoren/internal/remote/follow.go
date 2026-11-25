package remote

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// cursorPage is only the explicitly opted-in CLI cursor convention. The server
// owns cursor epochs and filter binding; the client never interprets the token.
type cursorPage struct {
	// Items contains newly observed records, emitted individually as NDJSON.
	Items []jsontext.Value `json:"items"`
	// Cursor is opaque and sent unchanged with the next observation.
	Cursor string `json:"cursor"`
	// Dropped reports records evicted before this observation.
	Dropped uint64 `json:"dropped"`
	// HasMore is the flag older servers send for a continuation page that
	// stopped at its limit; newer servers set Truncated instead.
	HasMore bool `json:"has_more"`
	// InstanceChanged explicitly reports a server restart across observations.
	InstanceChanged bool `json:"instance_changed"`
	// Truncated reports more matching records than the page holds. Follow
	// starts from a tail anchor, so on a continuation page it means more records
	// are waiting and the next call drains them without a long poll.
	Truncated bool `json:"truncated"`
}

// Follow uses tools/call long polling, not a custom event transport. The first
// tail observation establishes a position without replaying historical items.
// Cancellation succeeds unless an output callback has already failed.
// The body is an object from buildInput; its scalar JSON text remains exact
// while cursor controls are added, including arbitrarily precise numeric filters.
func (client *Client) Follow(ctx context.Context, name string, body []byte, emit func([]byte) error) error {
	tree, err := parseTree(body)

	if err != nil {
		return err
	}

	arguments := tree.(map[string]any)
	arguments["cursor"], arguments["wait"] = "tail", "0s"

	for {
		page, err := client.cursorCall(ctx, name, arguments)

		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return err
		}

		if err := emitCursorPage(page, emit); err != nil {
			return err
		}

		arguments["cursor"], arguments["wait"] = page.Cursor, "25s"

		if page.HasMore || page.Truncated {
			arguments["wait"] = "0s"
		}

		if ctx.Err() != nil {
			return nil
		}
	}
}

// cursorCall encodes one poll and validates the page envelope before printing.
// Empty cursors are rejected instead of repeatedly replaying the same page.
func (client *Client) cursorCall(ctx context.Context, name string, arguments map[string]any) (cursorPage, error) {
	var page cursorPage
	body, err := json.Marshal(arguments)

	if err != nil {
		return page, err
	}

	result, err := client.callTool(ctx, name, body)

	if err != nil {
		return page, err
	}

	if err := json.Unmarshal(result, &page); err != nil {
		return page, fmt.Errorf("decode cursor page: %w", err)
	}

	if page.Cursor == "" {
		return page, fmt.Errorf("cursor tool returned an empty cursor")
	}

	return page, nil
}

// emitCursorPage preserves loss/restart notices and item order. Write failures
// return immediately and take precedence over a concurrent cancellation.
func emitCursorPage(page cursorPage, emit func([]byte) error) error {
	if page.Dropped > 0 || page.InstanceChanged {
		notice, err := json.Marshal(struct {
			// Dropped counts evictions; omission distinguishes restart-only notices.
			Dropped uint64 `json:"dropped,omitzero"`
			// InstanceChanged explicitly reports a new server epoch.
			InstanceChanged bool `json:"instance_changed,omitzero"`
		}{Dropped: page.Dropped, InstanceChanged: page.InstanceChanged})

		if err != nil {
			return err
		}

		if err := emit(notice); err != nil {
			return err
		}
	}

	for _, item := range page.Items {
		if err := emit(item); err != nil {
			return err
		}
	}

	return nil
}
