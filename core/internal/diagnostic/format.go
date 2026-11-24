// Package diagnostic contains shared rendering and bounded cursor mechanics
// for owner-defined MCP diagnostics. It has no HTTP transport or dispatcher.
// Owners retain their typed records and decide which data is safe to expose.
//
// Example: diagnostic.Millis(time.Second) yields 1000 for duration_ms.
package diagnostic

import (
	"context"
	"fmt"
	"time"

	"arkoren.dev/foundation/validate"
)

// List is an object-shaped diagnostic result. Items is non-nil at owner
// boundaries; Truncated describes an intentionally bounded history snapshot.
//
// Example: result := diagnostic.List[string]{Items: []string{"ready"}}
type List[T any] struct {
	// Items holds matching records in the owner's documented order.
	// Example: result.Items = append(result.Items, "ready")
	Items []T `json:"items"`
	// Truncated indicates that matching history exceeded the requested limit.
	// Example: if result.Truncated { fmt.Println("more history exists") }
	Truncated bool `json:"truncated"`
}

const (
	// StatusOK describes a successful observation, not a transport status.
	// Example: result.Status = diagnostic.StatusOK
	StatusOK = "ok"
	// StatusFailing describes fresh failed health checks.
	// Example: if report.Status == diagnostic.StatusFailing { fmt.Println("unhealthy") }
	StatusFailing = "failing"
	// StatusPending means a background check has not completed yet.
	// Example: if report.Status == diagnostic.StatusPending { fmt.Println("waiting") }
	StatusPending = "pending"
	// StatusError marks an errored trace or span.
	// Example: if span.Status == diagnostic.StatusError { fmt.Println("failed") }
	StatusError = "error"
	// StatusUnset means the span never received an explicit status.
	// Example: if span.Status == diagnostic.StatusUnset { fmt.Println("no status") }
	StatusUnset = "unset"
)

// Millis returns fractional milliseconds for machine-readable duration fields.
// Example: milliseconds := diagnostic.Millis(250 * time.Millisecond)
func Millis(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

// DurationSchema describes a nonnegative duration filter. Zero means that the
// owner imposes no age or duration restriction.
// Example: schema := diagnostic.DurationSchema("Maximum age, such as 30s")
func DurationSchema(description string) validate.TextSchema[validate.Duration] {
	return validate.Text(func(duration *validate.TextBuilder[validate.Duration]) {
		duration.Describe(description)
		duration.Check(func(_ context.Context, value validate.Duration) error {
			if value < 0 {
				return validate.Fail("number.non_negative", nil)
			}

			return nil
		})
	})
}

// LimitSchema bounds diagnostic pages to 1000 items and supplies fallback when
// the caller omits the property or supplies zero, matching typed defaults.
// Example: schema := diagnostic.LimitSchema(100)
func LimitSchema(fallback int) validate.IntSchema[int] {
	return validate.Int(func(limit *validate.IntBuilder[int]) {
		limit.Min(1).Max(1000).Default(fallback)
		limit.Describe(fmt.Sprintf("Most items to return (default %d, at most 1000)", fallback))
	})
}
