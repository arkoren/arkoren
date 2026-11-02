---
title: Row-Level Access
nav: Row Access
description: Enforce record visibility in store queries and preserve authorization boundaries during concurrent writes.
summary:
  - Keep reads, pagination totals, and writes within the authenticated actor's query scope.
  - Return private absence without confusing database failures with missing records.
  - Constrain writable fields and coordinate cross-record permissions and invariants under a shared lock protocol.
order: 30
---

# Row-Level Access

An invoice-list permission does not tell you which invoices a caller may read.
Put that record boundary in the SQL query, rather than loading all invoices and
discarding forbidden ones afterward. The database then filters the rows before
pagination, counting, or returning private data to the application.

A **scope** is a reusable query predicate. This is application-side SQL
filtering, not a database server's row-level security configuration. Keep scopes
inside your store methods so callers cannot accidentally omit them.

## Ownership Scopes

Assume your authenticator supplies `User` from trusted account data, and the
`invoices` table has `id`, `customer_id`, and `reference` columns. These are
application-owned types; database tags name SQL columns, not JSON input fields:

```go
type User struct {
	ID         int
	CustomerID int
}

type Invoice struct {
	ID         int    `db:"id"`
	CustomerID int    `db:"customer_id"`
	Reference  string `db:"reference"`
}
```

Import `query` from `arkoren.dev/foundation/query`. The customer's identity,
not a customer ID submitted in the request, determines the invoice boundary:

```go
func visibleInvoices(user User) query.Scope {
	return func(predicate *query.PredicateBuilder) {
		predicate.Where("customer_id", user.CustomerID)
	}
}
```

`Builder.Scope` ANDs each scope with the complete ordinary condition chain,
grouping both so a later `OrWhere` or an OR in `WhereRaw` cannot bypass it.
Scopes also apply to updates, deletes, and derived counts. An empty scope adds
no restriction, so every authorization scope must actually express its boundary.
Raw SQL still needs trusted SQL text and separately bound values; grouping does
not make arbitrary input safe SQL.

## Scoped Stores

Make the actor an explicit store argument. With standard-library `context` and
`database` from `arkoren.dev/core/service/database`, the single-invoice lookup
owns both its requested ID and its customer restriction:

```go
type Invoices struct {
	db *database.Database
}

func (invoices *Invoices) Find(ctx context.Context, user User, id int) (Invoice, error) {
	return invoices.db.Find[Invoice](ctx, query.Table("invoices", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.Scope(visibleInvoices(user))
	}))
}
```

Apply the same scope in list methods. For an existing `db`, authenticated
`user`, and `page` of type `pagination.PageParams` from
`arkoren.dev/core/pagination`, the page and its total both use scoped rows:

```go
invoices := query.Table("invoices", func(statement *query.Builder) {
	statement.Scope(visibleInvoices(user))
	statement.OrderBy("id")
})
result, err := db.Paginate[Invoice](ctx, invoices, page)
```

The unique `id` provides a stable order. Filtering after fetching would leave
gaps and could expose unscoped totals; [pagination](../queries/pagination.md)
belongs after the authorization predicate. Give genuinely unrestricted billing
jobs a separately named store operation with its own admission boundary, rather
than making a missing scope the way to request all records.

For membership-based visibility, use a correlated `WhereExists` subquery that
matches both the record's team and the authenticated user's ID. That checks
membership within SQL rather than running a separate rule for each returned
row. [Query Builder](../queries/query-builder.md) explains subqueries; indexes
and uniqueness constraints should match the membership keys you query.

## Private Absence

A requested invoice outside the caller's scope and a nonexistent invoice both
produce `database.ErrNoRows`. In the handler, translate that absence to `404`;
do not turn other database failures into missing records. Here `store` is your
`Invoices` store, `id` is the parsed request ID, `errors` is standard-library,
and `problem` is `arkoren.dev/foundation/problem`:

```go
invoice, err := store.Find(r.Context(), user, id)
if errors.Is(err, database.ErrNoRows) {
	return problem.NotFound()
}
if err != nil {
	return err
}
```

The request ID selects a candidate; it is not evidence of ownership. This
response does not disclose whether an out-of-scope invoice exists. It does not
promise identical timing or hide information exposed by other endpoints. If the
caller may know the resource exists but lacks an action permission, use a
[forbidden response](denials.md#response-statuses) instead.

## Conditional Writes

Reading an authorized invoice and later updating it by ID alone leaves a gap:
the data used to authorize it might change between statements. Repeat the
boundary in the write so the database evaluates the predicate as part of the
mutation.

For a reference change, assume decoding and validation already produced this
input. Do not accept customer ownership fields in the writable shape:

```go
type ChangeReference struct {
	Reference string `json:"reference" db:"reference"`
}
```

Inside the scoped store operation, with `input` of that type and the requested
`id`, convert the allowlisted struct with `query.Values`:

```go
change := query.Table("invoices", func(statement *query.Builder) {
	statement.Where("id", id)
	statement.Scope(visibleInvoices(user))
	statement.Update(query.Values(input))
})
```

Execute the conditional write and distinguish an execution error from a
zero-row result:

```go
affected, err := db.Exec(ctx, change)
if err != nil {
	return err
}
if affected == 0 {
	return problem.NotFound()
}
```

No invoice outside the scope is changed. Affected-row reporting depends on the
driver: a no-op update can also report zero. Returning the same `404` preserves
the privacy boundary but does not prove why no change was reported. If your API
must distinguish a visible no-op from absence, perform the scoped lookup and
update under an appropriate transaction/lock protocol instead.

Apply the boundary to deletes too. Scopes do not apply to inserts: assign new
rows' customer/team ownership from authorized application values, and validate
every referenced record against that same boundary. A source invoice check
does not authorize attaching it to an arbitrary destination account.

## Shared Invariants

Removing a team member has two separate requirements: the actor must be allowed
to remove that member, and the team must retain at least one owner. A transaction
alone does not stop two owners from concurrently passing the last-owner check
and removing each other.

Serialize these operations by locking the parent team row before checking
current membership. Assume `Team` maps the team's stored ID, `teamID` is the
requested candidate, and `targetUserID` identifies the member to remove—not the
authenticated actor. Your application policy `members.CanRemove(team)` checks
the actor's current owner membership for this exact team, using this same `db`.

Import `gate` from `arkoren.dev/core/http/auth/gate`. In the shared application
operation, the transaction first locks the team, then checks the actor:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	team, err := db.Find[Team](ctx, query.Table("teams", func(statement *query.Builder) {
		statement.Select("id")
		statement.Where("id", teamID)
		statement.ForUpdate()
	}))
	if err != nil {
		return err
	}
	if err := gate.Authorize(ctx, members.CanRemove(team)); err != nil {
		return err
	}

	return removeLockedMember(ctx, team, targetUserID)
})
```

`removeLockedMember` is your application helper, not an Arkoren function. It
must look up the target membership by both `team.ID` and `targetUserID`, count
owners in that same team, and reject removing the last owner with
`problem.Conflict()` (`409`). Otherwise, it deletes using both keys. The count,
target lookup, and delete all use the callback's `ctx` and the same database so
they join its transaction. Keeping one owner is not a substitute for the actor
check shown above.

After those checks, the helper's deletion keeps the target inside the locked
team. Do not delete by `targetUserID` alone, since that could remove the same
user's memberships in other teams:

```go
remove := query.Table("memberships", func(statement *query.Builder) {
	statement.Where("team_id", team.ID)
	statement.Where("user_id", targetUserID)
	statement.Delete()
})
affected, err := db.Exec(ctx, remove)
```

Handle execution failure and a missing target before reporting success. This
fragment runs inside `removeLockedMember` with the transaction context, not
after `WithTransaction` has returned.

This example assumes the caller may know the team exists. For private teams,
use a scoped lookup before exposing any team-specific response and map
out-of-scope absence to `404`; recheck current action permission under the lock.
Do not translate an arbitrary infrastructure error to `404` or `403`.

Every operation that can alter this invariant or its permission basis must
follow the same parent-lock protocol, including owner demotion and removal of
the actor's membership. Use an isolation level whose reads observe the preceding
lock holder's committed changes; verify that behavior for your database.
Keep the protocol in the shared operation, not only an HTTP handler, so tools
and jobs cannot bypass it. Row locks require supported database behavior;
[Locking](../database/locking.md#row-locks) covers PostgreSQL/MySQL support and
the alternatives for SQLite.

Return an error from the transaction callback to roll back. Never replace its
context with the outer context, use it concurrently, or retain it after the
callback returns. [Transactions](../database/transactions.md) explains that
lifetime and commit-failure handling.

## Boundary Tests

Seed two customers and exercise every read, count, update, and delete using the
other customer's record ID. Assert no private rows are returned or changed,
including when ordinary predicates contain OR conditions. Test lists' totals
as well as their items.

For membership changes, test an unauthorized actor, a target in another team,
last-owner rejection, and concurrent owner removals using your real database
driver. Query-generation tests alone cannot prove the database's lock and
isolation behavior. Also verify that every route and tool reaches the shared
store operation rather than an unscoped alternative.

For all query operations, see the
[`query` API reference](https://pkg.go.dev/arkoren.dev/foundation/query); for
execution, see the
[`database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
