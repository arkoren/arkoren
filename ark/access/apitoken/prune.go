package apitoken

import (
	"context"
	"fmt"
	"time"

	"arkoren.dev/foundation/query"
)

// Prune deletes, for every user, the tokens that can no longer authenticate and
// would otherwise stay in the table forever: tokens past their expiry, device
// tokens unused for longer than [Builder.DeviceIdleTTL] (counted from the last
// use) and device tokens that were never used and are older than that (counted
// from creation). With the idle limit turned off (a negative DeviceIdleTTL) only
// the expired tokens are deleted. Personal tokens are never idle-pruned.
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

	return nil
}
