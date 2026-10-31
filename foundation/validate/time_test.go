package validate_test

import (
	"context"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestTimeBeforeField verifies BeforeField.
func TestTimeBeforeField(t *testing.T) {
	t.Parallel()

	type window struct {
		// Start provides the lower time endpoint for sibling checks.
		Start time.Time
		// End provides the upper time endpoint for sibling checks.
		End time.Time
	}

	schema := validate.Struct[window](func(object *validate.StructBuilder[window]) {
		object.Field("Start", func(moment *validate.TimeBuilder) {
			moment.BeforeField("End")
		})
	})

	moment := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	requireCodes(t, schema.Validate(context.Background(), window{
		Start: moment,
		End:   moment,
	}),
		map[string][]string{"/Start": {"before_field"}})
}

// TestZeroTimeWithLocationIsAbsent verifies a zero instant in any
// location is absent everywhere presence is evaluated.
func TestZeroTimeWithLocationIsAbsent(t *testing.T) {
	t.Parallel()

	loc := time.FixedZone("X", 3600)
	zero := time.Time{}.In(loc)
	ctx := context.Background()

	if err := validate.Time(func(moment *validate.TimeBuilder) {
		moment.Required()
	}).Validate(ctx, zero); err == nil {
		t.Fatal("Required accepted a zero time in a location")
	}

	if err := validate.Time(func(moment *validate.TimeBuilder) {
		moment.After(time.Now())
	}).Validate(ctx, zero); err != nil {
		t.Fatalf("rules ran on an absent time: %v", err)
	}

	schema := validate.Struct[timeHolder](func(object *validate.StructBuilder[timeHolder]) {
		object.Field("Flag", func(value *validate.StringBuilder[string]) {
		})
		object.Field("At", func(moment *validate.TimeBuilder) {
			moment.RequiredWith("Flag")
			moment.Default(time.Unix(5, 0))
		})
		object.Field("Other", func(moment *validate.TimeBuilder) {
			moment.ProhibitedIf(func(condition *validate.ConditionBuilder) {
				condition.Present("At")
			})
			moment.RequiredWithout("At")
		})
	})

	got, err := schema.Parse(ctx, timeHolder{
		At:    zero,
		Other: zero,
	})

	if err != nil || !got.At.Equal(time.Unix(5, 0)) {
		t.Fatalf("Default not applied: %+v, %v", got, err)
	}

	requireCodes(t, schema.Validate(ctx, timeHolder{
		At:    zero,
		Other: zero,
	}), nil)

	requiredWith := validate.Struct[timeHolder](func(object *validate.StructBuilder[timeHolder]) {
		object.Field("Flag", func(value *validate.StringBuilder[string]) {
		})
		object.Field("At", func(moment *validate.TimeBuilder) {
		})
		object.Field("Other", func(moment *validate.TimeBuilder) {
			moment.RequiredWith("Flag")
		})
	})

	requireCodes(t, requiredWith.Validate(ctx, timeHolder{
		Flag:  "x",
		Other: zero,
	}),
		map[string][]string{"/Other": {"required_with"}})

	present := validate.Struct[timeHolder](func(object *validate.StructBuilder[timeHolder]) {
		object.Field("Flag", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Present("At")
			})
		})
		object.Field("At", func(moment *validate.TimeBuilder) {
		})
		object.Field("Other", func(moment *validate.TimeBuilder) {
		})
	})

	requireCodes(t, present.Validate(ctx, timeHolder{
		At: zero,
	}), nil)
	requireCodes(t, present.Validate(ctx, timeHolder{
		At: time.Unix(1, 0).In(loc),
	}),
		map[string][]string{"/Flag": {"required_if"}})

	rejected, err := validate.Slice(func(items *validate.SliceBuilder[time.Time]) {
		items.Values(func(moment *validate.TimeBuilder) {
		})

		items.RejectZero()
	}).Parse(ctx, []time.Time{zero, time.Unix(1, 0)})

	if err != nil || len(rejected) != 1 {
		t.Fatalf("RejectZero = %v, %v", rejected, err)
	}
}

// TestNormalizeTime checks UTC normalization without changing the instant or
// the absence of zero time.
//
// Example:
//
//	go test -run "^TestNormalizeTime$" .
func TestNormalizeTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	zone := time.FixedZone("x", 3600)
	in := time.Date(2024, 1, 1, 1, 0, 0, 0, zone)

	got, err := validate.Time(func(moment *validate.TimeBuilder) {
		moment.UTC()
	}).Parse(ctx, in)

	if err != nil || got.Location() != time.UTC || !got.Equal(in) {
		t.Fatalf("UTC = %v, %v", got, err)
	}

	if in.Location() != zone {
		t.Fatal("input mutated")
	}

	got, err = validate.Time(func(moment *validate.TimeBuilder) {
		moment.UTC()
	}).Parse(ctx, time.Time{})

	if err != nil || !got.IsZero() {
		t.Fatalf("zero time = %v, %v", got, err)
	}

	err = validate.Time(func(moment *validate.TimeBuilder) {
		moment.UTC()
		moment.Required()
	}).Validate(ctx, time.Time{})

	requireCodes(t, err, map[string][]string{"": {"required"}})
}

// TestTimeRulesAfterPass verifies the AfterPass time rule case with a pinned clock.
func TestTimeRulesAfterPass(t *testing.T) {
	t.Parallel()

	ctx, now, earlier, _ := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.After(earlier)
	}).Validate(ctx, now), "")
}

// TestTimeRulesAfterFail verifies the AfterFail time rule case with a pinned clock.
func TestTimeRulesAfterFail(t *testing.T) {
	t.Parallel()

	ctx, now, _, _ := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.After(now)
	}).Validate(ctx, now), "time.after")
}

// TestTimeRulesBeforePass verifies the BeforePass time rule case with a pinned clock.
func TestTimeRulesBeforePass(t *testing.T) {
	t.Parallel()

	ctx, now, _, later := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Before(later)
	}).Validate(ctx, now), "")
}

// TestTimeRulesBeforeFail verifies the BeforeFail time rule case with a pinned clock.
func TestTimeRulesBeforeFail(t *testing.T) {
	t.Parallel()

	ctx, now, _, _ := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Before(now)
	}).Validate(ctx, now), "time.before")
}

// TestTimeRulesBetweenPass verifies the BetweenPass time rule case with a pinned clock.
func TestTimeRulesBetweenPass(t *testing.T) {
	t.Parallel()

	ctx, now, earlier, later := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Between(earlier, later)
	}).Validate(ctx, now), "")
}

// TestTimeRulesBetweenEdgePass verifies the BetweenEdgePass time rule case with a pinned clock.
func TestTimeRulesBetweenEdgePass(t *testing.T) {
	t.Parallel()

	ctx, now, _, later := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Between(now, later)
	}).Validate(ctx, now), "")
}

// TestTimeRulesBetweenFail verifies the BetweenFail time rule case with a pinned clock.
func TestTimeRulesBetweenFail(t *testing.T) {
	t.Parallel()

	ctx, now, earlier, later := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Between(earlier, now)
	}).Validate(ctx, later), "time.between")
}

// TestTimeRulesFuturePass verifies the FuturePass time rule case with a pinned clock.
func TestTimeRulesFuturePass(t *testing.T) {
	t.Parallel()

	ctx, _, _, later := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Future()
	}).Validate(ctx, later), "")
}

// TestTimeRulesFutureFail verifies the FutureFail time rule case with a pinned clock.
func TestTimeRulesFutureFail(t *testing.T) {
	t.Parallel()

	ctx, _, earlier, _ := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Future()
	}).Validate(ctx, earlier), "time.future")
}

// TestTimeRulesPastPass verifies the PastPass time rule case with a pinned clock.
func TestTimeRulesPastPass(t *testing.T) {
	t.Parallel()

	ctx, _, earlier, _ := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Past()
	}).Validate(ctx, earlier), "")
}

// TestTimeRulesPastFail verifies the PastFail time rule case with a pinned clock.
func TestTimeRulesPastFail(t *testing.T) {
	t.Parallel()

	ctx, _, _, later := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Past()
	}).Validate(ctx, later), "time.past")
}

// TestTimeRulesAbsentSkipped verifies the AbsentSkipped time rule case with a pinned clock.
func TestTimeRulesAbsentSkipped(t *testing.T) {
	t.Parallel()

	ctx, _, _, _ := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Future()
	}).Validate(ctx, time.Time{}), "")
}

// TestTimeRulesRequiredFail verifies the RequiredFail time rule case with a pinned clock.
func TestTimeRulesRequiredFail(t *testing.T) {
	t.Parallel()

	ctx, _, _, _ := pinnedClock()

	requireCode(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Required()
	}).Validate(ctx, time.Time{}), "required")
}

// TestTimeDefaultClock verifies Future uses the system clock without
// NewClockContext.
func TestTimeDefaultClock(t *testing.T) {
	t.Parallel()

	if err := validate.Time(func(moment *validate.TimeBuilder) {
		moment.Future()
	}).Validate(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

// TestTimeInvalidBuilders verifies Between panics for an inverted range.
func TestTimeInvalidBuilders(t *testing.T) {
	t.Parallel()

	now := time.Now()

	requirePanic(t, func() {
		validate.Time(func(moment *validate.TimeBuilder) {
			moment.Between(now, now.Add(-time.Hour))
		})
	})
}
