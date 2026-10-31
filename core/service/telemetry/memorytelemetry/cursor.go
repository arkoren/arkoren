package memorytelemetry

import (
	"context"
	"encoding/json/v2"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/internal/diagnostic"
)

// logSnapshot atomically copies retained records, their high-water sequence,
// store generation, and next-change channel. Reset cannot fall between them.
func (driver *Driver) logSnapshot() diagnostic.Snapshot[Log] {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.logChanged == nil {
		driver.logChanged = make(chan struct{})
	}

	items := slices.Concat(driver.logRing[driver.logOldest:], driver.logRing[:driver.logOldest])

	for index := range items {
		items[index].Attrs = slices.Clone(items[index].Attrs)
	}

	return diagnostic.Snapshot[Log]{Items: items, Total: driver.logSeq, Instance: driver.logInstance, Changed: driver.logChanged}
}

// pollLogs binds normalized filters and tool identity to a bounded poll call.
// Since is measured once at call entry, not extended by nonmatching arrivals.
func (driver *Driver) pollLogs(ctx context.Context, name string, input logsInput) (diagnostic.Page[logView], error) {
	filters, err := json.Marshal(struct {
		Level, TraceID string
		Since          int64
	}{
		Level: input.Level, TraceID: strings.ToLower(input.TraceID), Since: int64(input.Since),
	})

	if err != nil {
		return diagnostic.Page[logView]{}, err
	}

	filter := newLogFilter(input, time.Now())

	return diagnostic.Poll(ctx, diagnostic.CursorRequest{Key: name + string(filters), Cursor: input.Cursor, Wait: input.Wait.Std(), Limit: input.Limit},
		diagnostic.Feed[Log, logView]{Snapshot: driver.logSnapshot, Match: filter.matches,
			Render: func(record Log) logView { return logRecord(record, time.Now()) },
		})
}
