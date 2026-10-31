package database

import (
	"context"
	"database/sql"
	"errors"
)

// scopeKey stores the ordered session and transaction chain. A single chain
// preserves lexical ordering when sessions and transactions enclose each other.
type scopeKey struct{}

// scope binds one callback lifetime to its driver. Completed matching scopes
// remain in escaped contexts so routing cannot fall back to another executor.
type scope struct {
	// root is the driver of the Database that started the outer scope. A
	// Database joins its scopes when its driver equals root.
	root Driver

	// owner is the Database that started the outer scope. It decides the
	// join when root cannot be compared.
	owner *Database

	// driver is the scoped driver every joined operation runs on.
	driver Driver

	// parent preserves session/transaction ordering, including scopes of
	// different drivers enclosing each other.
	parent *scope

	// transaction distinguishes transaction boundaries (which own hooks) from
	// sessions, whose nesting does not change commit behavior.
	transaction bool

	// done is set before the callback returns to its driver. The callback and
	// its context are synchronous and must not be used concurrently.
	done bool

	// hooks are the [Database.AfterCommit] functions registered on this
	// level, in registration order.
	hooks []func(context.Context) error
}

// transactionFor returns the innermost transaction in ctx that database
// joins.
func transactionFor(ctx context.Context, database *Database) (*scope, bool) {
	tx, _ := ctx.Value(scopeKey{}).(*scope)

	for ; tx != nil; tx = tx.parent {
		if tx.transaction && tx.joinedBy(database) {
			return tx, true
		}
	}

	return nil, false
}

// joinedBy reports whether database runs its operations in this scope:
// it must be over the same root driver. Driver values that cannot be compared
// (== panics for an uncomparable dynamic type) join only through the same
// *Database.
func (scope *scope) joinedBy(database *Database) (joined bool) {
	if database == scope.owner {
		return true
	}

	defer func() {
		if recover() != nil {
			joined = false
		}
	}()

	return database.driver == scope.root
}

// scopeFor finds the innermost binding for this root driver. It deliberately
// keeps completed matches: using an escaped context must not escape its scope.
func (database *Database) scopeFor(ctx context.Context) *scope {
	current, _ := ctx.Value(scopeKey{}).(*scope)

	for ; current != nil; current = current.parent {
		if current.joinedBy(database) {
			return current
		}
	}

	return nil
}

// driverFor resolves the innermost matching callback, rejecting completed
// handles before any native operation or pool acquisition can occur.
func (database *Database) driverFor(ctx context.Context) (Driver, error) {
	if current := database.scopeFor(ctx); current != nil {
		if current.done {
			if current.transaction {
				return nil, errors.Join(sql.ErrTxDone, ctx.Err())
			}

			return nil, errors.Join(ErrSessionDone, ctx.Err())
		}

		return current.driver, nil
	}

	return database.driver, nil
}
