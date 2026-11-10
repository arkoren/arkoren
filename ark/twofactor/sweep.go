package twofactor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/service/schedule"
	"arkoren.dev/foundation/query"
)

// sweepPageSize is how many distinct subjects one sweep query reads. Each
// subject costs one loader call.
const sweepPageSize = 100

// Sweep returns the schedule entry that repairs a missed [TwoFactor.Forget]. At
// every run it pages through the distinct subjects in the ark's tables in
// bytewise order, asks the identity's loader whether each still exists, and
// forgets the ones whose loader reports [auth.ErrNotFound]. The entry is named
// "twofactor-sweep", or "twofactor-<instance>-sweep" with underscores written
// as hyphens, and does not overlap itself.
//
// The sweep fails closed. A subject whose loader returns any other error, such
// as [auth.ErrNoCredentials] for a suspended user or a database failure, keeps
// its rows; the sweep continues with the others and returns the errors joined
// so the scheduler reports the run as failed. It never deletes on a read path.
//
// The cost is one loader call per subject per run, so schedule it rarely (for
// example nightly) and rely on calling [TwoFactor.Forget] when deleting users.
// when is the application's choice and is required; an invalid one is reported
// by [schedule.Entry.Err] and [schedule.Scheduler.Add].
//
// Example:
//
//	if err := scheduler.Add(factors.Sweep(schedule.UTC().Hour(3))); err != nil {
//		return err
//	}
func (twofactor *TwoFactor) Sweep(when schedule.Spec) schedule.Entry {
	name := "twofactor-sweep"

	if twofactor.config.instance != "" {
		name = "twofactor-" + strings.ReplaceAll(twofactor.config.instance, "_", "-") + "-sweep"
	}

	return schedule.NewEntry(name, twofactor.sweep, func(entry *schedule.EntryBuilder) {
		entry.When(when)
		entry.WithoutOverlapping()
		entry.Description("Forget two-factor data of deleted users")
	})
}

// sweep is the body of the entry returned by [TwoFactor.Sweep].
func (twofactor *TwoFactor) sweep(ctx context.Context) error {
	quote := twofactor.db.Dialect().Quote
	page := fmt.Sprintf(
		"SELECT user_id FROM (SELECT user_id FROM %s UNION SELECT user_id FROM %s) AS subjects WHERE user_id > ? ORDER BY user_id LIMIT ?",
		quote(twofactor.secrets), quote(twofactor.recovery))

	var failures []error

	// last is the keyset cursor: the highest subject already examined.
	last := ""

	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}

		subjects, err := twofactor.db.Select[string](ctx, query.Raw(page, last, sweepPageSize))

		if err != nil {
			return errors.Join(append(failures, fmt.Errorf("two-factor sweep: %w", err))...)
		}

		for _, subject := range subjects.Items() {
			last = subject
			err := twofactor.directory.Exists(ctx, subject)

			if err == nil {
				continue
			}

			if !errors.Is(err, auth.ErrNotFound) {
				failures = append(failures, fmt.Errorf("two-factor sweep: check subject %q: %w", subject, err))

				continue
			}

			if err := twofactor.Forget(ctx, subject); err != nil {
				failures = append(failures, err)
			}
		}

		if subjects.Len() < sweepPageSize {
			return errors.Join(failures...)
		}
	}
}
