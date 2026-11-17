package apitoken

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/internal/storage"
)

// Prune deletes, for every user, the tokens that can no longer authenticate and
// would otherwise stay in the table forever: tokens past their expiry, tokens
// that a change of the user's version ended (a password reset, or the
// application's own rotation; all of a deleted user's tokens), device tokens
// unused for longer than [Builder.DeviceIdleTTL] (counted from the last use) and
// device tokens that were never used and are older than that (counted from
// creation). With the idle limit turned off (a negative DeviceIdleTTL) only the
// other kinds are deleted. Personal tokens are never idle-pruned. A disabled
// user's tokens are kept, because enabling the user brings them back. Finding
// the stale versions reads every user that has a token once per run.
//
// Pruning only removes rows the bearer check already rejects, so it changes no
// behavior and it is safe to run at any time, concurrently and repeatedly. It
// has the shape func(context.Context) error, so it can be a scheduled entry;
// run it daily. An error stops it at the failing delete, and the next run
// repeats the work.
//
// Example:
//
//	entry := schedule.NewEntry("access-prune", issuer.Prune, func(entry *schedule.EntryBuilder) {
//		entry.When(schedule.UTC().Hour(3))
//	})
func (issuer *Issuer) Prune(ctx context.Context) error {
	now := time.Now()
	deletions := []func(*query.Builder){
		func(statement *query.Builder) {
			statement.Where("expires_at", ">", 0).Where("expires_at", "<=", now.Unix())
		},
	}
	if issuer.deviceIdle > 0 {
		cutoff := now.Add(-issuer.deviceIdle).Unix()
		deletions = append(deletions,
			func(statement *query.Builder) {
				statement.Where("kind", string(KindDevice)).Where("last_used_at", ">", 0).Where("last_used_at", "<", cutoff)
			},
			func(statement *query.Builder) {
				statement.Where("kind", string(KindDevice)).Where("last_used_at", 0).Where("created_at", "<", cutoff)
			},
		)
	}
	for _, condition := range deletions {
		_, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
			statement.Delete()
			condition(statement)
		}))
		if err != nil {
			return fmt.Errorf("apitoken: prune tokens: %w", err)
		}
	}

	return issuer.pruneEnded(ctx)
}

// pruneEnded deletes the tokens whose user version changed since they were
// issued, user by user: it reads each subject that has a token and asks the
// user provider for the current version, which only it knows. The tokens of a
// subject the provider no longer knows are all deleted; a disabled subject is
// skipped.
func (issuer *Issuer) pruneEnded(ctx context.Context) error {
	subjects, err := issuer.db.Select[row](ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Select("subject").Distinct()
	}))
	if err != nil {
		return fmt.Errorf("apitoken: prune tokens: %w", err)
	}
	for _, stored := range subjects {
		record, err := issuer.provider.Find(ctx, stored.Subject)
		switch {
		case errors.Is(err, auth.ErrUserDisabled):
			continue
		case errors.Is(err, auth.ErrUserNotFound):
			err = issuer.deleteSubject(ctx, stored.Subject)
		case err == nil:
			err = issuer.pruneSubject(ctx, stored.Subject, storage.Hash(record.Version))
		}
		if err != nil {
			return fmt.Errorf("apitoken: prune tokens: %w", err)
		}
	}

	return nil
}

// deleteSubject deletes every token of subject.
func (issuer *Issuer) deleteSubject(ctx context.Context, subject string) error {
	_, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))

	return err
}
