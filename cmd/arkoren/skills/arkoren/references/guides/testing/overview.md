---
title: Testing Overview
nav: Overview
description: How Arkoren services are tested with the standard testing package, real facades on in-memory drivers, and no mocking framework.
order: 10
---

# Testing Overview

## Introduction

Test application behavior without starting its production infrastructure: keep
the real facades and choose memory drivers, recording fakes, or SQLite for their
backends. Use Go's standard `testing` package; Arkoren needs no special runner.

Choose the replacement for the question the test asks. A memory cache lets you
check stored values and expiry. A mail fake lets you check that your code
requested a message without sending one. SQLite executes database queries in
the test process, while production-specific SQL still needs the production
engine.

Dependencies arrive as plain arguments: a store receives its database facade,
a handler receives its store, and a router is a value. Build the piece under
test with those chosen dependencies, then assert its observable result.

## Running tests

Run the tests of a module the usual way:

```sh
go test -short -race ./...
```

`-race` enables Go's race detector, which catches unsynchronized access that
ordinary tests miss; services handle requests concurrently, so it is worth
keeping on. `-short` skips infrastructure tests only when those tests check
`testing.Short()`, as
described in [Integration tests](integration-tests.md).

## Testing with in-memory drivers

To test code that uses a service, build the facade with its memory driver.
For example, seed a cache entry and check the typed result. Pass this same
`store` to your application code when testing how it uses cached values.
In your `_test.go` file, import `arkoren.dev/core/service/cache` and
`arkoren.dev/core/service/cache/memorycache`. The fragment runs inside a test
whose `*testing.T` is named `t`:

```go
store := cache.New(memorycache.New())
key := cache.NewKey[float64]("rates.v1.EUR")
if err := store.Put(t.Context(), key, 1.08); err != nil {
	t.Fatal(err)
}
rate, err := store.Get(t.Context(), key)
if err != nil || rate != 1.08 {
	t.Fatalf("rate = %v, err = %v; want 1.08 and nil", rate, err)
}
```

The memory drivers implement the same contracts as the vendor drivers and pass
the same [conformance suites](conformance-suites.md), so a test that passes on
them exercises the portable contract. It does not verify network behavior,
durability, backend configuration, or provider-specific features.

For services whose effects leave the process, such as mail and queues, a
**fake** records what your code asked for and lets you assert on it. See
[Fakes](fakes.md).

## Testing with a database

Database code is best tested against a real SQL engine. SQLite runs inside
the test process and needs no server. A small helper
gives each test a fresh, empty database. First install the SQLite driver from
your application's module root:

```sh
go get arkoren.dev/driver/sqlite
```

In a `_test.go` file, import `context`, `testing`,
`arkoren.dev/core/service/database`, and
`arkoren.dev/driver/sqlite/sqlitedatabase`, then add this helper:

```go
func newTestDB(t *testing.T) *database.Database {
	t.Helper()

	driver, err := sqlitedatabase.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	return database.New(driver)
}
```

Run your migrations on it, or create the tables the test needs, and test your
store as usual. Because the store depends on the database facade, the same
store code runs on PostgreSQL in production.

> [!NOTE]
> SQLite and PostgreSQL differ in places, such as some data types and locking.
> Keep a few [integration tests](integration-tests.md) against your production
> database for the queries that depend on its features.

## Testing handlers

A router can serve a request without a server. `Record` sends a request
through the router and all its middleware and returns the response. With your
application's router named `r`, import `net/http` and `net/http/httptest`.
This assertion assumes invoice 7 is absent and the handler maps that condition
to HTTP 404:

```go
res := r.Record(httptest.NewRequest(http.MethodGet, "/invoices/7", nil))
if res.StatusCode != http.StatusNotFound {
	t.Fatalf("status = %d, want 404", res.StatusCode)
}
```

[HTTP tests](http-tests.md) covers requests with bodies, headers, cookies, and
authenticated users.

## Where to go next

- [HTTP tests](http-tests.md) and [Auth tests](auth-tests.md) test the HTTP
  layer.
- [Fakes](fakes.md) test code that sends mail, dispatches jobs, starts
  workflows, or calls a language model.
- [Controlling time](time.md) tests expiry, timeouts, and schedules without
  waiting.
- [Integration tests](integration-tests.md) run against real infrastructure.

For the available fakes and suites, see the
[testing API reference](overview.md).
