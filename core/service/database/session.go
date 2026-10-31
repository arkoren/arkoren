package database

import "context"

// WithSession runs fn on one retained physical connection without beginning a
// transaction. Every facade over the same driver joins through the supplied
// context, using the matching rules of [Database.WithTransaction]. Transactions
// started with it use that connection and retain their individual commits.
// An error does not undo autocommit work; a lost connection never reconnects.
//
// Nested sessions borrow the existing connection but have their own callback
// lifetime and nontransactional locks. Within a transaction they preserve it,
// and new locks remain owned by its outermost transaction. The context and
// cursors expire when fn returns; later operations fail with [ErrSessionDone].
// Use it synchronously from one goroutine, and use facade methods rather than
// [Database.Driver], which bypasses the scope. Cleanup releases outstanding
// cursors and locks even after cancellation or panic; cleanup errors are joined
// with the body error. Arbitrary raw SQL session state is not reset.
//
// Example:
//
//	err := db.WithSession(ctx, func(ctx context.Context) error {
//		return db.WithTransaction(ctx, func(ctx context.Context) error {
//			_, err := db.Exec(ctx, updateCounter)
//			return err
//		})
//	})
func (database *Database) WithSession(ctx context.Context, fn func(ctx context.Context) error) error {
	start, err := database.driverFor(ctx)

	if err != nil {
		return err
	}

	parent, _ := ctx.Value(scopeKey{}).(*scope)
	root, owner := database.driver, database

	if enclosing := database.scopeFor(ctx); enclosing != nil {
		root, owner = enclosing.root, enclosing.owner
	}

	return start.WithSession(ctx, func(driver Driver) error {
		level := &scope{
			root:   root,
			owner:  owner,
			driver: driver,
			parent: parent,
		}
		defer func() {
			level.done = true
		}()

		return fn(context.WithValue(ctx, scopeKey{}, level))
	})
}
