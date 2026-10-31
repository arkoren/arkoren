# Collector integration fixture

Contributor fixture for `TestCollectorReceivesFacadeTelemetry` in
[`driver_test.go`](../driver_test.go). The collector configuration writes
OTLP data to `/var/log/otel/collector.log`. By default, Compose mounts this source
directory there and the test reads `testdata/collector.log`.

To preserve an existing source log, have your integration runner replace the
`/var/log/otel` mount with a fresh external host directory through a Compose
override. The directory must be writable by the collector. Keep the collector
configuration mount in place. The runner owns service startup, teardown, and
temporary-directory cleanup; the test does not manage any of them.

Set **`ARKOREN_OTEL_COLLECTOR_LOG`** to the host-side file in that new directory.
An unset or empty value retains `testdata/collector.log`. Relative paths resolve
from the test package directory; use an absolute path for an external fixture.
The override changes only the test's read path, not the collector's output path
or mounts. The OTLP gRPC endpoint remains `127.0.0.1:4317`.

After the runner starts a healthy collector with the isolated mount, run from
`driver/otel`, replacing the example path with the actual host path:

```sh
ARKOREN_OTEL_COLLECTOR_LOG='/absolute/path/to/fresh-fixture/collector.log' \
GOWORK=off GOMAXPROCS=2 GOFLAGS='-mod=readonly -p=2' CGO_ENABLED=1 \
go test -race -count=1 ./oteltelemetry -run '^TestCollectorReceivesFacadeTelemetry$'
```

The test still requires this run's unique service name, HTTP route, response
status, and log message in collector output. Its log reader never creates,
truncates, or removes the selected file. Short tests skip the real collector
test; passing them does not establish collector integration coverage.
