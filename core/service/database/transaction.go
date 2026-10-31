package database

import (
	"context"
	"errors"
	"fmt"
)

// WithTransaction executes fn within a database transaction and passes it a
// context that carries the transaction. fn returning nil commits; an error
// rolls back and is returned; a panic rolls back and is re-raised.
//
// Every operation of this Database, and of any other Database over the same
// driver, that is given the context runs inside the transaction: Exec,
// Select, Find, Cursor, Paginate, CursorPaginate, Each, and Lock. Operations
// given any other context are not part of it. The context is valid until fn
// returns; using it afterwards fails with [sql.ErrTxDone]. Do not use it from
// several goroutines at once. Canceling ctx rolls back the whole
// transaction, nested transactions included, and WithTransaction then fails
// even when fn returns nil.
//
// Calling WithTransaction with a context that is already inside a transaction
// of the same driver starts a nested transaction through [Driver.WithTransaction]
// on the transaction driver, or fails with [ErrNestedTransaction] when the
// driver cannot nest.
// A transaction inside [Database.WithSession] uses the retained connection;
// its successful commit does not end that session or its preexisting locks.
//
// The transaction has no span of its own, because the database semantic
// conventions define none; operations inside it become children of the span
// in ctx. With [Builder.Events], the outermost transaction publishes
// [TransactionCommitted] after its commit and before the after-commit hooks,
// or [TransactionRolledBack] when its body or commit fails. Post-commit driver
// cleanup failures wrap [ErrAfterCommit]: the committed event and hooks still
// run. A body error bearing this marker still requests rollback. Nested
// transactions publish nothing.
//
// Example:
//
//	err := db.WithTransaction(ctx, func(ctx context.Context) error {
//		if _, err := db.Exec(ctx, insertOrder); err != nil {
//			return err
//		}
//
//		return orders.Reserve(ctx, items) // joins the transaction
//	})
func (database *Database) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	start, err := database.driverFor(ctx)

	if err != nil {
		return err
	}

	parent, _ := ctx.Value(scopeKey{}).(*scope)
	root, owner := database.driver, database

	if current := database.scopeFor(ctx); current != nil {
		root, owner = current.root, current.owner
	}

	enclosing, nested := transactionFor(ctx, database)

	var level *scope
	// bodyFailed distinguishes callback failure from post-commit cleanup failure.
	bodyFailed := false
	called := false

	err = start.WithTransaction(ctx, func(driver Driver) error {
		called = true
		level = &scope{
			root:        root,
			owner:       owner,
			driver:      driver,
			parent:      parent,
			transaction: true,
		}
		defer func() {
			level.done = true
		}()
		err := fn(context.WithValue(ctx, scopeKey{}, level))
		bodyFailed = err != nil

		return err
	})

	if err != nil && (!called || bodyFailed || !errors.Is(err, ErrAfterCommit)) {
		if !nested {
			database.config.Events.Emit(ctx, TransactionRolledBack, database.transactionEvent(err))
		}

		return err
	}

	if nested {
		enclosing.hooks = append(enclosing.hooks, level.hooks...)

		return nil
	}

	database.config.Events.Emit(ctx, TransactionCommitted, database.transactionEvent(nil))

	errs := []error{err}

	for _, hook := range level.hooks {
		errs = append(errs, hook(ctx))
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w: %w", ErrAfterCommit, err)
	}

	return nil
}

// AfterCommit runs fn once the outermost transaction that ctx carries on this
// database has committed, or immediately when ctx carries no such transaction.
// It is a best-effort hook for effects that must not happen unless the data
// is committed and that tolerate being lost: cache invalidation, broadcasts,
// notifications, and metrics.
//
// It is not durable. The hooks live in memory, so a crash or kill between the
// commit and the end of the hooks loses them without any trace. Work that
// must not be lost, such as sending an email the user is owed, needs a
// durable mechanism (for example a row written in the same transaction and
// processed by a queue), not this hook.
//
// A transaction is joined as for any other operation: through a ctx from
// [Database.WithTransaction] on this Database or on another one over the same
// driver. A ctx without a transaction, or with one of a different driver
// only, runs fn immediately and synchronously with ctx.
//
// Hooks run in registration order, synchronously, before the outermost
// [Database.WithTransaction] returns. They run only when that transaction
// commits: an error from the transaction body, a panic, or a failed commit
// discards them all. A hook registered inside a nested transaction is
// discarded when the nested transaction rolls back; when it succeeds the hook
// is appended to the enclosing level, so it still waits for the outermost
// commit.
//
// fn receives the ctx the outermost WithTransaction was called with. It does
// not carry the finished transaction, so database calls in fn run normally
// outside it, and a transaction of another driver that ctx carried stays.
// An enclosing session also stays: hook SQL uses its retained connection.
// A completed matching callback context is rejected instead of registering
// a hook that can never run.
//
// Every hook runs even when an earlier one fails. Their errors are joined,
// wrapped with [ErrAfterCommit], and returned by the outermost
// WithTransaction after the commit. Because the data is committed, a caller
// that sees ErrAfterCommit must not retry the transaction: retrying would
// duplicate the work. Outside a transaction, AfterCommit returns the error of
// fn wrapped the same way; inside one it registers fn and returns nil. A panic
// in fn propagates out of the outermost WithTransaction after the commit, and
// the hooks after it do not run.
//
// The ctx must not be used from several goroutines at once, so AfterCommit
// takes no lock.
//
// Example:
//
// Import cache from arkoren.dev/core/service/cache. Here store is a
// *cache.Cache, id is a string product ID, and updateProduct is the published
// update query. The raw key preserves the existing product:v1: naming scheme;
// its any value type is sufficient because deletion never decodes the entry.
//
//	productKey := cache.NewKey[any]("product:v1:" + id)
//	err := db.WithTransaction(ctx, func(ctx context.Context) error {
//		if _, err := db.Exec(ctx, updateProduct); err != nil {
//			return err
//		}
//
//		return db.AfterCommit(ctx, func(ctx context.Context) error {
//			return store.Delete(ctx, productKey) // runs only if the update committed
//		})
//	})
//	if errors.Is(err, database.ErrAfterCommit) {
//		logger.WarnContext(ctx, "cache invalidation failed", "error", err) // the update committed
//	}
func (database *Database) AfterCommit(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, err := database.driverFor(ctx); err != nil {
		return err
	}

	tx, ok := transactionFor(ctx, database)

	if !ok {
		if err := fn(ctx); err != nil {
			return fmt.Errorf("%w: %w", ErrAfterCommit, err)
		}

		return nil
	}

	tx.hooks = append(tx.hooks, fn)

	return nil
}
