---
title: Integration Tests
description: Verify backend-specific behavior against disposable infrastructure with bounded operations and test-owned cleanup.
order: 60
summary:
  - Select infrastructure tests explicitly and start a disposable, loopback-bound PostgreSQL stack.
  - Own connection cleanup and test PostgreSQL-specific SQL with bounded operation contexts.
  - Isolate fixtures, retain failure evidence, and remove only the test-owned stack.
---

# Integration Tests

Use a real backend when the behavior you need to verify belongs to that backend: PostgreSQL-specific SQL, Redis expiry, or broker recovery after a restart. Memory drivers and recording fakes cannot establish those semantics. Keep ordinary application-policy tests in process, and give each infrastructure test one specific backend claim.

## Select Infrastructure

Go does not classify integration tests automatically. Put a short-mode guard before construction or connection attempts in each test that requires infrastructure:

```go
if testing.Short() {
	t.Skip("integration test: requires PostgreSQL")
}
```

This fragment belongs at the start of a test function; the complete test below includes it. `go test -short ./...` skips guarded tests, while an ordinary run includes them. If a selected backend is unavailable, fail the test rather than silently skipping it: otherwise a broken setup can look like verified behavior.

## Start PostgreSQL

For the example, use PostgreSQL 17 in a disposable local stack. Match the server version and important settings to your deployed backend when adapting it. Install Docker with Compose support and save this `compose.yaml` in your application's module directory:

```yaml
services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: local-test-only
    ports:
      - "127.0.0.1:15432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 1s
      timeout: 3s
      retries: 30
```

The test password is suitable only for this loopback-bound local service. Do not use it for a shared or remotely reachable database. The health check waits for the server; the test still verifies connectivity with its actual credentials.

From that directory, choose a new Compose project name and start the stack. Run all command blocks on this page in the same shell so the project selection remains active:

```sh
export COMPOSE_PROJECT_NAME="reports-integration-$$"
docker compose up -d --wait
```

Use this project name only for disposable test resources. It isolates stack teardown, but not the fixed host port: simultaneous runs need separate ports and matching connection strings, or separate hosts.

## Own Connections

Install the driver in your application's module:

```sh
go get arkoren.dev/driver/postgres
```

In a `_test.go` file, import `context`, `testing`, `time`, and `arkoren.dev/driver/postgres/postgresdatabase`. This helper constructs a pool, immediately registers its cleanup, then verifies connectivity with a bounded context:

```go
func connectPostgres(t *testing.T) *postgresdatabase.Driver {
	t.Helper()
	driver, err := postgresdatabase.New(
		"postgres://postgres:local-test-only@127.0.0.1:15432/postgres?sslmode=disable",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := driver.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return driver
}
```

Construction does not prove a server connection; `Start` checks it. Cleanup uses its own context because `t.Context()` is canceled before cleanup callbacks run. Finish transactions and close cursors before stopping the pool. [PostgreSQL setup](../database/drivers.md#postgresql) owns pool configuration and native-client ownership.

## Assert Backend Behavior

This test checks PostgreSQL JSON containment through the database facade. Add imports for `arkoren.dev/core/service/database` and `arkoren.dev/foundation/query`:

```go
func TestPostgresJSONContains(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: requires PostgreSQL")
	}
	db := database.New(connectPostgres(t))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	statement := query.Raw("SELECT ?::jsonb @> ?::jsonb", `{"state":"ready","count":2}`, `{"state":"ready"}`)
	contains, err := db.Find[bool](ctx, statement)
	if err != nil {
		t.Fatal(err)
	}
	if !contains {
		t.Fatal("JSON object did not contain the expected state")
	}
}
```

The query needs no schema or persistent fixture, and cannot be justified by a passing SQLite test. For your application's queries, apply its [migrations](../migrations/overview.md) to a disposable database, seed the necessary rows, and call the real store operation. Driver startup does not create application tables.

## Isolate Fixtures

Tests sharing a server need distinct resource names and ownership. Allocate a unique schema, key prefix, queue, or database for each independent fixture, including across simultaneous test runs; `t.Name()` alone does not distinguish separate processes. Register cleanup as soon as acquisition succeeds, and report cleanup errors with `t.Error`.

Delete only resources that test created. Avoid database-wide resets or prefix scans that might match another test's data. A transaction rolled back after a test can isolate some database work, but it cannot undo broker messages or operations performed through a different connection. Use explicit cleanup for those effects.

Use separate fixtures for interruption, timeout, and recovery tests. State which process or connection you stop, bound waits for the expected result, and assert the application outcome after recovery. A successful reconnect alone does not prove that an interrupted write committed or a message survived.

## Run and Clean

With the stack healthy, run the focused test without reusing a cached result:

```sh
go test -count=1 -race -run '^TestPostgresJSONContains$' ./...
```

`-count=1` forces execution against the current server. A busy port or unhealthy service is a setup failure, not evidence about the driver's semantics. If startup or testing fails, capture logs before removing the stack:

```sh
docker compose logs --no-color
```

After either success or failure, remove this disposable project and its database volumes. This deletes test data; run it only with the same test-only project selection and Compose file used above:

```sh
docker compose down --volumes
```

In CI, make teardown an always-run step and preserve the startup or test command's failure status. Capture logs before teardown on failure. Separate fast `-short` tests from infrastructure jobs, and give each job its own disposable project and available ports.

If the backend implements a custom public driver, add [Driver Conformance](conformance-suites.md) alongside your application-specific integration tests.
