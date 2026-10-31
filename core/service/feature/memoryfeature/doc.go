// Package memoryfeature provides an in-memory [feature.Driver] for tests,
// local development, and single-process applications.
//
// Flags are set from code with [Driver.Set] and [Driver.SetFor], switched off
// with [Driver.Disable], and removed with [Driver.Delete]. Each flag has an
// optional default value and optional per-subject overrides keyed by
// [feature.Subject.Key]. There are no targeting rules or percentage rollouts:
// attributes are ignored.
// Raw Evaluate checks cancellation, then rejects an empty or unknown Kind with
// feature.ErrTypeMismatch before reading any flag. Disabled flags and flags
// with no matching value do not bypass this validation. Valid kinds are Bool,
// String, Int, and Float; typed zero definitions remain invalid during setup.
//
//	driver := memoryfeature.New()
//	checkout := feature.NewFlag("new-checkout", false)
//	if err := driver.Set(checkout, false); err != nil {
//		log.Print(err)
//		return
//	}
//	if err := driver.SetFor(checkout, "user-42", true); err != nil {
//		log.Print(err)
//		return
//	}
//	flags := feature.New(driver)
//	on, err := flags.Value(ctx, checkout, feature.Subject{Key: "user-42"})
//	if err != nil {
//		log.Print(err) // on is false.
//	}
//
// The driver is safe for concurrent use and has no Start or Stop.
package memoryfeature
