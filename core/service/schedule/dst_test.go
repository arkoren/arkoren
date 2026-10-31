package schedule_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
)

// checkDaylightSavingWarning adds spec to a scheduler and fails the test
// unless it logs exactly one warning for the entry when want is true, and none
// when want is false.
func checkDaylightSavingWarning(t *testing.T, spec schedule.Spec, want bool) {
	t.Helper()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	scheduler := schedule.New(scheduletest.New(), func(scheduler *schedule.Builder) {
		scheduler.Logger(logger)
	})

	if err := scheduler.Add(schedule.NewEntry("job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	})); err != nil {
		t.Fatal(err)
	}

	warned := strings.Contains(logs.String(), "level=WARN") && strings.Contains(logs.String(), "entry=job")

	if warned != want {
		t.Errorf("warned=%v, want %v: %s", warned, want, logs.String())
	}

	if want && strings.Count(logs.String(), "level=WARN") != 1 {
		t.Errorf("want exactly one warning: %s", logs.String())
	}
}

// TestSchedulerAddDaylightSavingWarningGapTime checks that registering a time
// inside a spring-forward gap emits one warning for that entry.
func TestSchedulerAddDaylightSavingWarningGapTime(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSavingWarning(t, schedule.In(amsterdam).Hour(2).Minute(30), true)
}

// TestSchedulerAddDaylightSavingWarningOverlapTime checks that a repeated
// autumn wall-clock hour is identified during registration.
func TestSchedulerAddDaylightSavingWarningOverlapTime(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSavingWarning(t, schedule.In(amsterdam).Hour(2), true)
}

// TestSchedulerAddDaylightSavingWarningSafeTime checks that ordinary local
// execution times do not generate unnecessary daylight-saving warnings.
func TestSchedulerAddDaylightSavingWarningSafeTime(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSavingWarning(t, schedule.In(amsterdam).Hour(12), false)
}

// TestSchedulerAddDaylightSavingWarningUtc checks that a fixed UTC schedule
// never warns about transitions in unrelated local time zones.
func TestSchedulerAddDaylightSavingWarningUtc(t *testing.T) {
	t.Parallel()

	checkDaylightSavingWarning(t, schedule.UTC().Hour(2).Minute(30), false)
}
