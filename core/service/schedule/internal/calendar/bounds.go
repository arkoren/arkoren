package calendar

// Bounds holds the inclusive range of one cron field.
type Bounds struct {
	// Name identifies the field in error messages.
	Name string

	// Low is the smallest allowed value.
	Low int

	// High is the largest allowed value.
	High int
}

var (
	// Seconds limits seconds to whole values within one minute.
	Seconds = Bounds{
		Name: "second",
		Low:  0,
		High: 59,
	}

	// Minutes limits minutes to whole values within one hour.
	Minutes = Bounds{
		Name: "minute",
		Low:  0,
		High: 59,
	}

	// Hours uses the 24-hour clock for calendar matching.
	Hours = Bounds{
		Name: "hour",
		Low:  0,
		High: 23,
	}

	// Days accepts any day that can occur in some calendar month.
	Days = Bounds{
		Name: "day of month",
		Low:  1,
		High: 31,
	}

	// Months follows time.Month's one-based calendar numbering.
	Months = Bounds{
		Name: "month",
		Low:  1,
		High: 12,
	}

	// Weekdays numbers Sunday as zero, matching time.Weekday.
	Weekdays = Bounds{
		Name: "day of week",
		Low:  0,
		High: 6,
	}
)
