package diagnostic_test

import (
	"testing"
	"time"

	"arkoren.dev/core/internal/diagnostic"
)

// TestDurationPairsPreserveMachinePrecision keeps display rounding separate
// from fractional millisecond values used by programs.
func TestDurationPairsPreserveMachinePrecision(t *testing.T) {
	t.Parallel()

	duration := 182123456 * time.Nanosecond

	if diagnostic.Millis(duration) != 182.123456 {
		t.Fatal("machine duration rounded")
	}

	if diagnostic.DurationText(duration).Std() != 182123*time.Microsecond {
		t.Fatal("display duration not rounded to microseconds")
	}

	if diagnostic.DurationText(time.Second+400*time.Microsecond).Std() != time.Second {
		t.Fatal("long display duration not rounded to milliseconds")
	}
}

// TestAgeUsesWholeLargestUnits retains human-readable age semantics including
// future timestamps, absent timestamps, and subsecond observations.
func TestAgeUsesWholeLargestUnits(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000000, 0)

	if diagnostic.Age(now, time.Time{}) != "" {
		t.Fatal("absent timestamp has an age")
	}

	if diagnostic.Age(now, now.Add(-500*time.Millisecond)) != "just now" {
		t.Fatal("subsecond age")
	}

	if diagnostic.Age(now, now.Add(-119*time.Second)) != "1m ago" {
		t.Fatal("minute age")
	}

	if diagnostic.Age(now, now.Add(3*time.Hour)) != "in 3h" {
		t.Fatal("future age")
	}

	if diagnostic.Age(now, now.Add(-49*time.Hour)) != "2d ago" {
		t.Fatal("day age")
	}
}

// TestBytesUsesBinaryPrefixes retains paired-size display at unit boundaries.
func TestBytesUsesBinaryPrefixes(t *testing.T) {
	t.Parallel()

	if diagnostic.Bytes(512) != "512B" {
		t.Fatal("byte display")
	}

	if diagnostic.Bytes(1024) != "1.0KiB" {
		t.Fatal("kibibyte display")
	}

	if diagnostic.Bytes(3*1024*1024) != "3.0MiB" {
		t.Fatal("mebibyte display")
	}
}
