---
title: Integration Tests
description: Test against real databases, caches, and brokers started with Docker Compose, skip them with -short, and run them in CI.
order: 60
---

# Integration Tests

## Introduction

Use memory drivers and SQLite for portable application behavior without an
external server. Use the real backend when the behavior depends on it: a query that uses a PostgreSQL
feature, the exact expiry behavior of Redis, a message that must survive a
RabbitMQ restart. **Integration tests** cover these, against the same software
you run in production, started locally.

Keep that distinction visible in the test runner: check `testing.Short()` in
integration tests so `go test -short` skips them. The setup below starts their
external services from a `compose.yaml` file.

## Skipping in short mode

Start each integration test by skipping it in short mode:

```go
func TestInvoiceQueriesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs PostgreSQL")
	}
	// ...
}
```

With this guard in each infrastructure test, `go test -short ./...` skips those
tests while you work. The flag does not skip tests automatically or guarantee
that the remaining tests are fast. `go test ./...` runs both kinds.

## Starting the services

Describe the services the tests need in a `compose.yaml` next to them, on fixed
ports bound to `127.0.0.1`, so they are not reachable from the network.
For the example below, save `compose.yaml` in your application's module root
and install Docker with Compose support. Use the test-only password only with
this loopback-bound local service, never for a remotely accessible database:

```yaml
services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: test
    ports:
      - "127.0.0.1:15432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 1s
      retries: 30
```

Start them, waiting until their health checks pass, then run the tests:

```sh
docker compose up -d --wait
go test -count=1 -race ./...
```

Because the ports are fixed, the tests connect with constant addresses, such
as `postgres://postgres:test@127.0.0.1:15432/postgres`, and need no environment
variables. A non-default port such as `15432` avoids clashing with a server you
may already run on the default one.

`-count=1` prevents a cached test result from replacing a run against the current
server. If Compose reports an occupied port, stop the conflicting test service
or change both the mapping and the test connection string.

After testing, remove this isolated stack and its disposable database volumes.
This deletes the local test database: run it only from the directory containing
this test stack, never for a stack whose data you need to keep:

```sh
docker compose down --volumes
```

## Keeping tests independent

Integration tests share one server, so each test should leave nothing behind
that another could trip over. Two habits help. Give each test its own names,
such as a table, key prefix, or queue named after the test. And clean up with
`t.Cleanup`, so the cleanup runs even when the test fails. For resources with
explicit names, record those names and delete them directly. If your test
helpers support prefix cleanup, keep it restricted to the test's own prefix;
`deleteKeysWithPrefix` below is such an application helper, not a cache API:

```go
prefix := "test:" + t.Name() + ":"
t.Cleanup(func() {
	if err := deleteKeysWithPrefix(context.Background(), prefix); err != nil {
		t.Error(err)
	}
})
```

## In continuous integration

A CI job can run the same commands: start the services with
`docker compose up -d --wait`, then `go test -count=1 -race ./...`. Keep the fast tests
in a separate job with `-short` so that most failures are reported within
seconds, and the slower integration job runs alongside it.

Run cleanup even when tests fail, and capture `docker compose logs` before
removing failed services. Use a separate Compose project name for each CI job
when jobs share a Docker host. A project name isolates cleanup, but fixed host
ports still cannot be shared; allocate distinct ports or separate runners.

Follow your selected [driver's local setup](../drivers/choosing-a-driver.md)
for backend-specific prerequisites. For reusable checks of a custom driver's
contract, continue with [Driver Conformance Suites](conformance-suites.md).
