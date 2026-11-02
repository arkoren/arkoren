---
title: Overview
description: Choose test boundaries for application code, HTTP routes, and external services using Go's standard testing package.
order: 10
summary:
  - Choose in-process drivers, recording fakes, or real backends according to the behavior under test.
  - Test typed state through a real facade and run tests with Go's standard runner.
  - Keep database-engine semantics separate from portable application behavior.
---

# Overview

Keep the application code you want to verify real, and replace only the dependencies outside the test's scope. Arkoren facades accept drivers directly, so your tests can use an in-process backend or a recording fake without changing the code that calls the facade. Go's standard `testing` package is the test runner; no mocking framework is required.

## Choose Boundaries

A test's assertions determine which dependencies it needs:

| Question | Test boundary |
| --- | --- |
| Does application code store and retrieve the right value? | Real facade with an in-process driver. |
| Does an endpoint return the promised status, headers, and body? | Requests through the router, as shown in [HTTP Tests](http-tests.md). |
| Does an operation request the right external effect? | Real facade with a [recording or scripted fake](fakes.md). |
| Does production-specific SQL, expiry, or broker behavior work? | [Integration tests](integration-tests.md) against that backend. |
| Does a custom driver satisfy the tested public contract? | The service's [conformance suite](conformance-suites.md). |

A memory driver stores and processes data in your test process. A recording fake captures requests instead of performing the external effect; a scripted fake also supplies chosen replies or failures. Neither proves that a production vendor delivers messages, persists data across crashes, or behaves identically under network failures.

## Test Facades

For cached application state, construct the real cache facade over `memorycache`. This complete test uses `testing`, `arkoren.dev/core/service/cache`, and `arkoren.dev/core/service/cache/memorycache` in a `_test.go` file:

```go
func TestCachedLanguage(t *testing.T) {
	store := cache.New(memorycache.New())
	key := cache.NewKey[string]("preferences:language")
	if err := store.Put(t.Context(), key, "en"); err != nil {
		t.Fatal(err)
	}

	language, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if language != "en" {
		t.Fatalf("language = %q, want en", language)
	}
}
```

This establishes the fixture and checks typed retrieval. To test your own operation, pass this `store` into that operation, then assert the state it should leave behind. Give each independent test its own driver so previous tests cannot supply missing setup.

The memory cache checks expiry on access without starting background cleanup. Other drivers have their own lifecycle requirements; follow their setup instructions rather than assuming construction starts them. [Cache Drivers](../cache/drivers.md) owns cache setup, and [Cache Keys](../cache/keys.md) explains typed key definitions.

## Run Tests

From your application's module directory, run:

```sh
go test -short -race ./...
```

`-race` checks executed code for data races and requires a supported race-detector toolchain and C compiler. `-short` is a flag your tests inspect, not an automatic infrastructure filter: tests skip only if they call `testing.Short()` and act on it. [Integration Tests](integration-tests.md#select-infrastructure) shows that guard and the separate backend run.

For expiry and timers, use [Controlling Time](time.md) instead of adding real delays to otherwise in-process tests.

## Test SQL

Use a real SQL engine for queries and result mapping. An in-process SQLite database can serve tests whose SQL and schema support SQLite; follow [SQLite setup](../database/drivers.md#sqlite), start the driver, register cleanup, and apply the schema your operation needs before calling it.

SQLite is not a substitute for PostgreSQL or MySQL semantics. Test engine-specific types, locking, isolation, and SQL against the production engine. Keep schema changes application-owned, following [Migrations](../migrations/overview.md), rather than expecting driver startup to create tables.

For your next application test, start with [HTTP Tests](http-tests.md) for a route or [Fakes](fakes.md) for an operation that requests an external effect.
