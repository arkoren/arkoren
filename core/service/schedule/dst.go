package schedule

import (
	"context"
	"log/slog"
	"time"

	"arkoren.dev/core/service/schedule/internal/calendar"
)

// warnDST logs once when entry has a time inside a daylight saving
// transition during the next year. A time in the spring gap is skipped, and a
// time in the autumn overlap runs once. The warning never fails anything.
func warnDST(logger *slog.Logger, entry Entry) {
	location := entry.Spec().Location()

	if location == nil || location == time.UTC {
		return
	}

	schedule, err := entry.Spec().schedule()

	if err != nil {
		return
	}

	start := time.Now().In(location).Truncate(time.Hour)
	end := start.AddDate(1, 0, 0)

	for moment := start; moment.Before(end); moment = moment.Add(time.Hour) {
		_, before := moment.Zone()
		_, after := moment.Add(time.Hour).Zone()

		if before == after {
			continue
		}

		transition := calendar.FindTransition(moment, moment.Add(time.Hour))

		// The wall times that are skipped or repeated lie between the wall
		// readings of the transition instant under the two offsets.
		low := time.Unix(transition.Unix()+int64(min(before, after)), 0).UTC()
		high := time.Unix(transition.Unix()+int64(max(before, after)), 0).UTC()

		wall, found := schedule.FirstWall(low, high.Add(-time.Second))

		if !found {
			continue
		}

		effect := "skipped"

		if after < before {
			effect = "run once"
		}

		// Add is contextless registration, not an execution callback. Warn
		// already uses context.Background internally; passing it explicitly
		// preserves that context while satisfying context-aware logging.
		logger.WarnContext(context.Background(), "schedule entry has a time inside a daylight saving transition",
			slog.String("entry", entry.Name()),
			slog.String("zone", location.String()),
			slog.String("date", transition.In(location).Format(time.DateOnly)),
			slog.String("time", wall.Format(time.TimeOnly)),
			slog.String("effect", effect),
		)

		return
	}
}
