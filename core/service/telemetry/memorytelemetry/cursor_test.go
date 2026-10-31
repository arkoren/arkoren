package memorytelemetry

import (
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/internal/mcptest"
	"arkoren.dev/core/mcp"
)

// TestMCPLogPollWakesForMatchingRecord verifies snapshot/notification pairing
// with a real driver write after the poll has become idle.
func TestMCPLogPollWakesForMatchingRecord(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		tool := mcptest.Find(t, capture.tools, "memory.logs")
		anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail"}`)
		done := make(chan diagnostic.Page[logView], 1)

		go func() {
			done <- mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+anchor.Cursor+`","wait":"25s"}`)
		}()

		synctest.Wait()

		if err := capture.store.Export(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, "arrived", 0)); err != nil {
			t.Fatal(err)
		}

		result := <-done

		if len(result.Items) != 1 || result.Items[0].Message != "arrived" || result.HasMore {
			t.Fatalf("result=%+v", result)
		}
	})
}

// TestMCPLogPollWakesForEmptyReset proves reset loss is visible even when no
// log existed before the poll and no record is exported after Reset.
func TestMCPLogPollWakesForEmptyReset(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		tool := mcptest.Find(t, capture.tools, "memory.logs")
		anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail"}`)
		done := make(chan diagnostic.Page[logView], 1)

		go func() {
			done <- mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+anchor.Cursor+`","wait":"25s"}`)
		}()

		synctest.Wait()
		capture.store.Reset()

		result := <-done

		if !result.InstanceChanged || len(result.Items) != 0 || result.Cursor == anchor.Cursor {
			t.Fatalf("result=%+v", result)
		}
	})
}

// TestMCPLogCursorBindsToolName rejects a valid cursor presented to a second
// tool declaration, even when both declarations read the same store.
func TestMCPLogCursorBindsToolName(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)
	anchor := mcptest.Call[diagnostic.Page[logView]](t, mcptest.Find(t, capture.tools, "memory.logs"), `{"cursor":"tail"}`)
	other := mcptest.Find(t, capture.store.MCPTools("other"), "other.logs")

	_, err := other.Call(t.Context(), []byte(`{"cursor":"`+anchor.Cursor+`"}`))
	failure, ok := mcp.ToolFailureInfo(err)

	if !ok || failure.Code != "invalid_cursor" {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
}

// TestMCPLogCursorDetectsStoreReplacement returns a fresh tail when the tool
// name and filters survive but capture comes from another driver instance.
func TestMCPLogCursorDetectsStoreReplacement(t *testing.T) {
	t.Parallel()

	before := newCaptured(t)
	anchor := mcptest.Call[diagnostic.Page[logView]](t, mcptest.Find(t, before.tools, "memory.logs"), `{"cursor":"tail"}`)
	after := newCaptured(t)
	page := mcptest.Call[diagnostic.Page[logView]](t, mcptest.Find(t, after.tools, "memory.logs"), `{"cursor":"`+anchor.Cursor+`"}`)

	if !page.InstanceChanged || len(page.Items) != 0 || page.Cursor == anchor.Cursor {
		t.Fatalf("page=%+v", page)
	}
}
