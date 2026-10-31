// Package prometheustelemetry provides a Prometheus telemetry MetricDriver that serves
// metrics for scraping.
//
// The package name carries the vendor prefix, so it does not clash with the
// telemetry contract package:
//
//	import "arkoren.dev/driver/prometheus/prometheustelemetry"
//
//	driver := prometheustelemetry.New()
//	service := telemetry.New(driver)
//	mux := http.NewServeMux()
//	mux.Handle("/metrics", driver.Handler())
//	_ = service
//
// The driver implements metrics only. Prometheus has no instrumentation scope,
// so the scope of an instrument is ignored. Names and label keys are translated
// from OpenTelemetry semantic conventions; see [Driver] for the rules.
//
// By default each instrument retains at most 1000 label combinations and each
// driver admits at most 1000 instrument definitions. [Builder.MaxSeries] drops new
// combinations at capacity before creating Prometheus children, histogram
// buckets, or exemplars. Admitted combinations keep recording without eviction
// or cumulative resets. [Builder.MaxInstruments] also bounds dynamically named empty
// collectors. Zero limits after construction callbacks select
// the finite defaults of 1000; negative limits disable their admission. There
// is no unlimited mode. Runtime, target_info, and caller-owned native collectors
// are outside these limits. These are count bounds, not byte quotas: use fixed
// names, keys, and buckets, and avoid unbounded label-value sizes. Recording
// retains children even if no scraper connects or trace driver is configured.
// The telemetry facade separately caches definitions, including rejected ones;
// never derive instrument names or keys from untrusted input.
//
// Start rejects typed-nil native pointers and invalid final target_info label
// values. Managed histogram sample-name collisions fail at binding rather than
// at scrape time. Native collectors and registries remain caller-managed; see
// [Driver.Instrument] and [Driver.Stop] for ownership and cancellation limits.
package prometheustelemetry
