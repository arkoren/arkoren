package diagnostic

import (
	"testing"
	"time"

	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/internal/mcptest"
)

func TestMCPEventRingBoundsAndCursorLoss(t *testing.T) {
	t.Parallel()

	ring := NewRing(2)
	tool := ring.tool("app")
	anchor := mcptest.Call[diagnostic.Page[EventView]](t, tool, `{"cursor":"tail"}`)

	for _, name := range []string{"first", "second", "third"} {
		ring.add(EventView{ReceivedAt: time.Now(), Name: name})
	}

	page := mcptest.Call[diagnostic.Page[EventView]](t, tool, `{"cursor":"`+anchor.Cursor+`","limit":1}`)

	if page.Dropped != 1 || !page.HasMore || len(page.Items) != 1 || page.Items[0].Name != "second" {
		t.Fatalf("page=%+v", page)
	}

	next := mcptest.Call[diagnostic.Page[EventView]](t, tool, `{"cursor":"`+page.Cursor+`","limit":1}`)

	if next.HasMore || next.Items[0].Name != "third" {
		t.Fatalf("next=%+v", next)
	}

	history := mcptest.Call[diagnostic.Page[EventView]](t, tool, `{"limit":1}`)

	if !history.Truncated || history.Items[0].Name != "third" {
		t.Fatalf("history=%+v", history)
	}
}
