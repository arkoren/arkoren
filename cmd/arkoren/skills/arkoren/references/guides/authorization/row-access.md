---
title: Row-Level Access
nav: Row Access
description: Keep which records a user may see in query scopes applied by the store, answer 404 for rows outside them, and make writes atomic.
order: 30
---

# Row-Level Access

## Introduction

Rules answer "may this user do this?". They are the wrong tool for "which
records may this user see?". Loading every invoice and checking each one in a
rule is slow, pagination breaks because pages come back with holes, and a
handler that forgets the check leaks data. Visibility belongs in the query
instead, so the database returns only rows the user may see. A route-level gate
check can decide whether the user may perform an action, but it does not filter
the rows that action may reach. Apply the visibility scope in the store as well.

This page shows how to express visibility as query scopes, where to apply them
so no handler can forget them, and how to keep a check and the write that
depends on it from being split by a concurrent request.

## Scopes for visibility

Examples import `query` from `arkoren.dev/foundation/query`, `database` from
`arkoren.dev/core/service/database`, `pagination` from
`arkoren.dev/core/pagination`, and `problem` from `arkoren.dev/foundation/problem`.
`context` and `errors` are standard-library packages. `db` is your configured
database facade; `User`, `Invoice`, `Member`, and `Team` are your application types.

A **scope** is a function that adds conditions to a query. A scope for
visibility takes the user and matches only their rows:

```go
func visibleTo(u User) query.Scope {
	return func(predicate *query.PredicateBuilder) {
		predicate.Where("customer_id", u.CustomerID)
	}
}

invoice, err := db.Find[Invoice](ctx, query.Table("invoices", func(statement *query.Builder) {
	statement.Where("id", id)
	statement.Scope(visibleTo(user))
}))
```

A scope is added as its own group of conditions, ANDed after the entire
nonempty user condition chain in parentheses. Neither a later `OrWhere` nor
an OR in `WhereRaw` can widen it, and it applies to updates and deletes too.
Keep raw SQL text trusted and constant and bind variable values separately;
grouping does not validate arbitrary SQL. The same scope serves a
single row, a list, and a [paginated](../database/pagination.md) list, which
therefore never has holes.

When visibility comes from membership rather than ownership, as with projects
that belong to a team, match rows through the membership table. Build a
subquery with the `query` package:

```go
func visibleTo(u User) query.Scope {
	return func(predicate *query.PredicateBuilder) {
		predicate.WhereExists(query.Table("members", func(statement *query.Builder) {
			statement.WhereColumn("members.team_id", "projects.team_id")
			statement.Where("members.user_id", u.ID)
		}))
	}
}
```

The subquery is part of the same SQL statement, not a separate query from Go
for each row. Give the membership table an index on `(user_id, team_id)` so it stays
fast as teams grow.

## Applying scopes in the store

A scope that each handler must remember is a scope that one handler will
forget. Give each store method the user, and apply the scope inside the store,
so the only way to read invoices is through a method that filters them:

```go
type Invoices struct{ db *database.Database }

func (invoices *Invoices) Find(ctx context.Context, u User, id int) (Invoice, error) {
	return invoices.db.Find[Invoice](ctx, query.Table("invoices", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.Scope(visibleTo(u))
	}))
}

func (invoices *Invoices) List(ctx context.Context, u User, page pagination.PageParams) (pagination.Page[Invoice], error) {
	return invoices.db.Paginate[Invoice](ctx, query.Table("invoices", func(statement *query.Builder) {
		statement.Scope(visibleTo(u))
		statement.OrderBy("id")
	}), page)
}
```

Code that genuinely needs every row, such as a billing job or an admin report,
gets a separately named method, so reading without the scope is a deliberate,
visible choice in review rather than a missing line.

## Answering with 404

When the scoped query finds nothing, the handler returns `404 Not Found`, the
same as for a record that does not exist:

```go
invoice, err := h.invoices.Find(r.Context(), user, id)
if errors.Is(err, database.ErrNoRows) {
	return problem.NotFound()
}
if err != nil {
	return err
}
```

A user therefore cannot even learn that someone else's invoice `42` exists, or
count how many invoices other customers have by trying ids in turn.
[Handling Denials](denials.md#choosing-the-status-code) explains when a `403`
is the better answer.

## Checks and concurrent requests

A check followed by a write leaves a gap: between the two, another request can
change what the check relied on. Close the gap by making the write itself
conditional, so the database checks and writes in one step:

```go
n, err := db.Exec(ctx, query.Table("posts", func(statement *query.Builder) {
	statement.Where("id", id)
	statement.Scope(editableBy(user))
	statement.Update(map[string]any{"title": title})
}))
if err != nil {
	return err
}
if n == 0 {
	return problem.NotFound()
}
```

Here `editableBy` is a scope, like `visibleTo` above, that matches the posts
the user may edit. If no row matches, either the post does not exist or the
user may not edit it, and nothing was written.

### Invariants across rows

Some rules span several rows. A team must keep at least one owner, so two
owners who remove each other at the same moment can each pass the "not the
last owner" check, and leave the team with none. A conditional write on one
row cannot see the others. Lock the parent row inside a transaction before
checking, so concurrent requests for the same team run one after another:

Authorize the actor before entering this operation. Preserving one owner does
not itself grant permission to remove members.

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Find[Team](ctx, query.Table("teams", func(statement *query.Builder) {
		statement.Where("id", teamID)
		statement.ForUpdate()
	})); err != nil {
		return err
	}

	owners, err := db.Select[Member](ctx, query.Table("members", func(statement *query.Builder) {
		statement.Where("team_id", teamID)
		statement.Where("role", "owner")
	}))
	if err != nil {
		return err
	}
	if len(owners) == 1 && owners[0].UserID == userID {
		return problem.Conflict().Derive(func(details *problem.Builder) {
			details.Detail("A team needs at least one owner.")
		})
	}

	_, err = db.Exec(ctx, query.Table("members", func(statement *query.Builder) {
		statement.Where("team_id", teamID)
		statement.Where("user_id", userID)
		statement.Delete()
	}))

	return err
})
```

The second request waits at `ForUpdate` until the first commits, then sees
only one owner left and fails with `409 Conflict`. Row locks need PostgreSQL or
MySQL; [Transactions](../database/transactions.md#locking-rows) explains them
and their limits.

Every operation that can change this invariant must take the same parent-row
lock, including owner removal, demotion, and membership changes. A transaction
alone does not serialize competing checks.
Put this protocol in the shared store or application operation, so a tool or
background job cannot bypass it by calling a different transport.

## Testing

Test a store's visibility with two users and rows for each: every method must
return the first user's rows to the first user and nothing to the second.

```go
_, err := invoices.Find(t.Context(), bob, aliceInvoice.ID)
if !errors.Is(err, database.ErrNoRows) {
	t.Fatalf("bob read alice's invoice: %v", err)
}
```

Write this test for every store method, including updates and deletes. A
method added later without the scope then fails it.

For the query builder, see [Query Builder](../database/query-builder.md); for
the gate, see the [`gate` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth/gate).
