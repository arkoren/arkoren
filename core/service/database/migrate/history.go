package migrate

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// orderStatusHistory preserves deterministic reporting for unregistered rows:
// ascending batch, then wall-clock timestamp, then byte-exact id. This order is
// presentation only; it must never decide the reverse application sequence.
func orderStatusHistory(records []record) {
	slices.SortFunc(records, func(a, b record) int {
		if byBatch := cmp.Compare(a.Batch, b.Batch); byBatch != 0 {
			return byBatch
		}

		if byTime := parseTime(a.RanAt).Compare(parseTime(b.RanAt)); byTime != 0 {
			return byTime
		}

		return strings.Compare(a.ID, b.ID)
	})
}

// timeLayouts lists the text formats drivers use for timestamps.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
}

// parseTime converts a scanned ran_at value to UTC. An unknown value gives the
// zero time.
func parseTime(value any) time.Time {
	var text string

	switch typed := value.(type) {
	case time.Time:
		return typed.UTC()
	case string:
		text = typed
	case []byte:
		text = string(typed)
	default:
		return time.Time{}
	}

	// SQLite's driver writes time.Time with a monotonic clock suffix.
	text, _, _ = strings.Cut(text, " m=")

	for _, layout := range timeLayouts {
		if parsedAt, err := time.Parse(layout, text); err == nil {
			return parsedAt.UTC()
		}
	}

	return time.Time{}
}

// lateMigrations reports, for each definition, whether it is pending and added
// before an applied definition of the same group.
func lateMigrations(definitions []Definition, applied map[string]bool) []bool {
	highest := map[string]int{}

	for index, definition := range definitions {
		if applied[definition.ID()] {
			highest[definition.Group()] = index
		}
	}

	late := make([]bool, len(definitions))

	for index, definition := range definitions {
		last, ok := highest[definition.Group()]
		late[index] = ok && !applied[definition.ID()] && index < last
	}

	return late
}

// checkOutOfOrder applies the out-of-order mode to definitions.
func (migrator *Migrator) checkOutOfOrder(ctx context.Context, definitions []Definition) error {
	if len(definitions) == 0 || migrator.config.OutOfOrder == OutOfOrderAllow {
		return nil
	}

	ids := make([]string, len(definitions))

	for index, definition := range definitions {
		ids[index] = definition.ID()
	}

	if migrator.config.OutOfOrder == OutOfOrderError {
		return fmt.Errorf("migrate up: %w: %s", ErrOutOfOrder, strings.Join(ids, ", "))
	}

	migrator.config.Logger.WarnContext(ctx, "running out-of-order migrations", slog.Any("ids", ids))

	return nil
}

// orderRollback orders history newest batch first, then reverse Add order.
// Every Up batch executes in Add order, whereas wall-clock timestamps can tie
// or move backward. Unknown definitions sort first within their batch so the
// caller refuses an ambiguous target rather than applying a potentially unsafe
// partial rollback. The records slice is owned by the locked operation.
func orderRollback(records []record, definitions []Definition) {
	positions := make(map[string]int, len(definitions))

	for index, definition := range definitions {
		positions[definition.ID()] = index + 1
	}

	unknown := len(definitions) + 1
	slices.SortStableFunc(records, func(left, right record) int {
		if byBatch := cmp.Compare(left.Batch, right.Batch); byBatch != 0 {
			return byBatch
		}

		return cmp.Compare(cmp.Or(positions[left.ID], unknown), cmp.Or(positions[right.ID], unknown))
	})
	slices.Reverse(records)
}
