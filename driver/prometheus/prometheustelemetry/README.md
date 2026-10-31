# Prometheus telemetry driver

Use this metrics-only driver when a Prometheus scraper reads your application's
HTTP endpoint. Import `arkoren.dev/driver/prometheus/prometheustelemetry` and
`arkoren.dev/core/service/telemetry`; the example also uses `net/http`. Here `mux`
is your existing operator-only `*http.ServeMux`, not a public listener: labels and
resource metadata are visible to readers.

```go
driver := prometheustelemetry.New()
service := telemetry.New(driver)
mux.Handle("/metrics", driver.Handler())
```

Start `service` through the application's lifecycle. No collectors are registered
during construction. Runtime/process collectors and `target_info` start enabled;
explicit builder `false` values remain off. The default private registry retains
at most 1,000 instrument definitions and 1,000 label tuples per instrument. Zero
limits select these finite defaults; negative limits disable admission rather
than remove the bound. Keep names, keys, buckets, and label sizes trusted and
bounded. Protect the metrics endpoint: labels and resource metadata are visible
to its readers.

If your metrics need an application prefix, replace the constructor with:

```go
driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
	metrics.Namespace("billing")
})
```

The namespace prefixes Arkoren instrument names, not runtime collectors or
`target_info`. Configure it before starting the service.

## Binding and native ownership

Instrument and label names are translated into Prometheus spelling. Colliding
translated labels, names, and managed histogram `_count`, `_sum`, or `_bucket`
sample names reject the later instrument without disabling unrelated metrics.
The driver snapshots retained Keys and Buckets. Invalid final `target_info`
values fail Start before they can panic during collection; failed startup removes
newly registered collectors and remains retryable.

Supplied native registries remain borrowed and shared. Stop unregisters only
collectors this driver successfully registered; equivalent preexisting runtime
collectors stay registered. Custom native registerer methods have no context
parameter, so cancellation cannot interrupt them. Stop waits for their release
calls even with an expired context. A stopped driver cannot restart, but its
handler can still expose caller-owned collectors.

Configure native collectors to avoid their own generated sample-name conflicts.
The Registerer interface cannot reveal arbitrary native collection behavior or
all generated names. Unchecked collectors and custom native panics remain the
caller's responsibility. Gather errors can reach protected local logs and the
HTTP error response when no metrics can be served.

## HTTP results

Handler serves Prometheus text or negotiated OpenMetrics, including sampled-span
exemplars for counters and histograms. A registerer-only configuration without a
gatherer answers 501. Typed-nil native pointers instead fail Start and make the
handler answer a fixed 503, including before Start. Construction keeps ordinary
nil-interface default resolution unchanged.

See the [package API](https://pkg.go.dev/arkoren.dev/driver/prometheus/prometheustelemetry)
for builders, mapping rules, and examples.
