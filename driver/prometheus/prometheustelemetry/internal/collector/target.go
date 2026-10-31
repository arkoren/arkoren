package collector

import (
	"arkoren.dev/core/service/telemetry"

	"github.com/prometheus/client_golang/prometheus"
)

// targetInfoName is the name of the resource metric.
const targetInfoName = "target_info"

// targetInfo is the collector of the target_info gauge, which carries the
// resource attributes as labels.
type targetInfo struct {
	// metric is validated once at Start; collection cannot panic on label data.
	// Resource metadata is fixed for the lifetime of this collector.
	metric prometheus.Metric
}

// NewTargetInfo describes resource as service_name, service_version,
// deployment_environment_name, and the extra attributes. Empty values and invalid
// telemetry attributes are omitted. A later attribute replaces an earlier one
// with the same translated label name. It returns nil when nothing remains and
// an error for invalid final label values, before registration or collection.
func NewTargetInfo(resource telemetry.Resource) (prometheus.Collector, error) {
	var names, values []string

	set := func(name, value string) {
		name = LabelName(name)

		if name == "" || value == "" {
			return
		}

		for index, existing := range names {
			if existing == name {
				values[index] = value

				return
			}
		}

		names, values = append(names, name), append(values, value)
	}

	set("service.name", resource.Service)
	set("service.version", resource.Version)
	set("deployment.environment.name", resource.Environment)

	for _, attr := range resource.Attrs {
		if attr.Valid() {
			set(attr.Key(), attr.Text())
		}
	}

	if len(names) == 0 {
		return nil, nil
	}

	metric, err := prometheus.NewConstMetric(
		prometheus.NewDesc(targetInfoName, "Target metadata", names, nil),
		prometheus.GaugeValue, 1, values...,
	)

	if err != nil {
		return nil, err
	}

	return &targetInfo{metric: metric}, nil
}

// Describe sends the description of the gauge.
func (targetInfo *targetInfo) Describe(descs chan<- *prometheus.Desc) {
	descs <- targetInfo.metric.Desc()
}

// Collect sends the gauge, whose value is always 1.
func (targetInfo *targetInfo) Collect(metrics chan<- prometheus.Metric) {
	metrics <- targetInfo.metric
}
