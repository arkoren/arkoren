// Package collector owns native metric construction, translation, and bounded
// per-recorder admission. Registry lifecycle remains with the driver.
package collector

import (
	"strings"

	"arkoren.dev/core/service/telemetry"
)

// unitSuffixes maps UCUM units onto the Prometheus base-unit suffixes of the
// OpenTelemetry to Prometheus compatibility specification.
var unitSuffixes = map[string]string{
	"d": "days", "h": "hours", "min": "minutes", "s": "seconds",
	"ms": "milliseconds", "us": "microseconds", "ns": "nanoseconds",
	"By": "bytes", "KiBy": "kibibytes", "MiBy": "mebibytes", "GiBy": "gibibytes", "TiBy": "tebibytes",
	"KBy": "kilobytes", "MBy": "megabytes", "GBy": "gigabytes", "TBy": "terabytes",
	"m": "meters", "V": "volts", "A": "amperes", "J": "joules", "W": "watts",
	"g": "grams", "Cel": "celsius", "Hz": "hertz", "%": "percent",
}

// perUnits maps the UCUM time units that may follow a "/" onto the singular
// form used after "_per_".
var perUnits = map[string]string{
	"d": "day", "h": "hour", "min": "minute", "s": "second",
	"ms": "millisecond", "us": "microsecond", "ns": "nanosecond",
}

// ratioUnit is the UCUM unit of dimensionless values.
const ratioUnit = "1"

// totalSuffix ends the name of every counter.
const totalSuffix = "_total"

// labelAlphabet is the portable ASCII vocabulary used for Prometheus labels.
// Keeping it explicit avoids Unicode classification or locale-dependent names.
const labelAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"

// metricAlphabet additionally permits the colon reserved for recording rules.
// Metrics and namespaces use the same vocabulary; label keys do not allow it.
const metricAlphabet = labelAlphabet + ":"

// sanitize replaces every character that is not valid in a Prometheus name
// with an underscore and collapses runs of underscores. The explicit alphabet
// selects metric or label spelling without a mode flag. A leading digit gets an
// underscore prefix; empty input stays empty for native descriptor validation.
func sanitize(name, alphabet string) string {
	var builder strings.Builder

	builder.Grow(len(name) + 1)

	for index, char := range name {
		if !strings.ContainsRune(alphabet, char) {
			char = '_'
		}

		if index == 0 && char >= '0' && char <= '9' {
			builder.WriteByte('_')
		}

		if char == '_' && strings.HasSuffix(builder.String(), "_") {
			continue
		}

		builder.WriteRune(char)
	}

	return builder.String()
}

// LabelName translates a declared key into a Prometheus label name.
func LabelName(key string) string {
	return sanitize(key, labelAlphabet)
}

// unitSuffix returns the name suffix of unit, or "" when it has none. Units in
// curly braces, such as {request}, are annotations and have no suffix, and
// unknown units are ignored. A "/" followed by a time unit adds "_per_" and
// that unit, as in By/s becoming "bytes_per_second". The ratio unit "1" has a suffix only for gauges.
func unitSuffix(unit string, kind telemetry.InstrumentKind) string {
	if unit == ratioUnit {
		if kind == telemetry.GaugeKind {
			return "ratio"
		}

		return ""
	}

	numerator, denominator, divided := strings.Cut(unit, "/")
	suffix := unitSuffixes[numerator]

	if suffix == "" {
		return ""
	}

	if !divided {
		return suffix
	}

	if per := perUnits[denominator]; per != "" {
		return suffix + "_per_" + per
	}

	return suffix
}

// MetricName translates an instrument into its Prometheus metric name: the
// sanitized name with the namespace prefix, then the unit suffix, then _total
// for counters. A suffix the name already ends with is not repeated.
func MetricName(namespace string, spec telemetry.InstrumentSpec) string {
	name := sanitize(spec.Name, metricAlphabet)

	if namespace != "" {
		name = sanitize(namespace, metricAlphabet) + "_" + name
	}

	if suffix := unitSuffix(spec.Unit, spec.Kind); suffix != "" && !strings.HasSuffix(name, "_"+suffix) {
		name += "_" + suffix
	}

	if spec.Kind == telemetry.CounterKind && !strings.HasSuffix(name, totalSuffix) {
		name += totalSuffix
	}

	return name
}
