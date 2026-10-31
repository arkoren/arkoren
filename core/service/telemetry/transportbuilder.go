package telemetry

import (
	"net/http"
	"slices"
	"strings"

	"arkoren.dev/core/service/telemetry/internal/httptrack"
)

// TransportBuilder configures Transport before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	httpClient := &http.Client{Transport: telemetry.Transport(nil, func(transport *telemetry.TransportBuilder) {
//		transport.PropagationHosts("*.example.com")
//	})}
//	_ = httpClient
type TransportBuilder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings transportSettings
}

// TransportBuilderFunc configures an outgoing transport at construction, not
// per request. Use the alias when reusing one propagation policy across clients.
//
//	var configure telemetry.TransportBuilderFunc = func(transport *telemetry.TransportBuilder) {
//		transport.Propagation(telemetry.PropagateNone)
//	}
//	client := &http.Client{Transport: service.Transport(nil, configure)}
//	_ = client
type TransportBuilderFunc = func(*TransportBuilder)

// Propagation decides, per outgoing request, whether the driver's
// propagation headers (traceparent, baggage, ...) are injected. Spans and
// metrics are recorded for every request regardless. Trace context and
// baggage can carry data meant for your own services, so the default,
// nil, is [PropagateInternal]: inject only to internal destinations and
// never leak them to third parties. See [PropagateAll], [PropagateNone],
// [TransportBuilder.PropagationHosts].
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: telemetry.Transport(nil, func(transport *telemetry.TransportBuilder) {
//		transport.Propagation(nil)
//	})}
//	_ = httpClient
func (transport *TransportBuilder) Propagation(propagate func(*http.Request) bool) *TransportBuilder {
	transport.settings.Propagate = propagate

	return transport
}

// PropagationHosts injects trace context into internal destinations (see
// [PropagateInternal]) and into the listed hosts. A host is an exact name
// such as "api.partner.com" or a wildcard such as "*.example.com", which
// matches any subdomain but not "example.com" itself. Matching is
// case-insensitive and ignores the port. It replaces any earlier predicate
// and copies hosts.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: telemetry.Transport(nil, func(transport *telemetry.TransportBuilder) {
//		transport.PropagationHosts("*.example.com")
//	})}
//	_ = httpClient
func (transport *TransportBuilder) PropagationHosts(hosts ...string) *TransportBuilder {
	hosts = slices.Clone(hosts)

	for index, host := range hosts {
		hosts[index] = strings.ToLower(host)
	}

	return transport.Propagation(func(req *http.Request) bool {
		if PropagateInternal(req) {
			return true
		}

		host := httptrack.NormalizeHost(req.URL.Hostname())

		return httptrack.MatchHost(host, hosts)
	})
}
