package telemetrytest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// DumpOnFailure makes t print what the fake recorded if t fails, so a failing
// test explains itself without a debugger. It registers a cleanup that checks
// [testing.TB.Failed] and, only then, logs the finished spans as an indented
// tree followed by the log records joined to their span by span ID.
//
// Each span line shows the span name, duration, a status (only "ok" or
// "error: description"; unset is omitted), and its attributes. Its events
// and the log records exported inside it follow, then its child spans.
// Spans whose parent did not finish in the fake, or is remote, are roots, and
// records logged outside any recorded span come last under "logs without
// span". Payload values appear as recorded: do not put secrets in telemetry.
//
// Call it once per test, after creating the fake. It prints nothing when the
// test passes or when nothing was recorded.
//
//	fake := telemetrytest.New()
//	fake.DumpOnFailure(t)
//	service := fake.Telemetry(t)
//	_, span := service.Tracer("example.com/cache").Start(t.Context(), "cache get")
//	span.End()
//
// Output looks like:
//
//	telemetry: 2 spans, 1 logs
//	GET /users/{id} 12ms error: boom {http.route=/users/{id}}
//	  SELECT users 3ms ok {db.system=sqlite}
//	  ! exception {exception.message=boom}
//	  log ERROR "query failed" {attempt=2}
func (fake *Fake) DumpOnFailure(t testing.TB) {
	t.Helper()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}

		if dump := fake.dump(); dump != "" {
			t.Log("\n" + dump)
		}
	})
}

// dump renders the recorded spans and logs, or "" when there are none.
func (fake *Fake) dump() string {
	spans, logs := fake.Spans(), fake.Logs()

	if len(spans) == 0 && len(logs) == 0 {
		return ""
	}

	known := make(map[string]bool, len(spans))
	children := make(map[string][]memorytelemetry.Span, len(spans))
	logsBySpan := make(map[string][]Log, len(logs))

	for _, span := range spans {
		known[span.Context.SpanID.String()] = true
	}

	var roots []memorytelemetry.Span

	for _, span := range spans {
		parent := span.Parent.SpanID.String()

		if known[parent] && !span.Parent.Remote {
			children[parent] = append(children[parent], span)
		} else {
			roots = append(roots, span)
		}
	}

	var orphans []Log

	for _, entry := range logs {
		if known[entry.SpanID] {
			logsBySpan[entry.SpanID] = append(logsBySpan[entry.SpanID], entry)
		} else {
			orphans = append(orphans, entry)
		}
	}

	var out strings.Builder

	fmt.Fprintf(&out, "telemetry: %d spans, %d logs\n", len(spans), len(logs))

	var write func(span memorytelemetry.Span, depth int)

	write = func(span memorytelemetry.Span, depth int) {
		indent := strings.Repeat("  ", depth)
		id := span.Context.SpanID.String()

		fmt.Fprintf(&out, "%s%s %v%s%s\n", indent, span.Name, span.End.Sub(span.Start).Round(time.Microsecond),
			statusText(span), attrsText(span.Attrs))

		for _, event := range span.Events {
			fmt.Fprintf(&out, "%s  ! %s%s\n", indent, event.Name, attrsText(event.Attrs))
		}

		for _, entry := range logsBySpan[id] {
			fmt.Fprintf(&out, "%s  %s\n", indent, logText(entry))
		}

		kids := children[id]
		slices.SortStableFunc(kids, func(a, b memorytelemetry.Span) int {
			return a.Start.Compare(b.Start)
		})

		for _, child := range kids {
			write(child, depth+1)
		}
	}

	slices.SortStableFunc(roots, func(a, b memorytelemetry.Span) int {
		return a.Start.Compare(b.Start)
	})

	for _, root := range roots {
		write(root, 0)
	}

	if len(orphans) > 0 {
		out.WriteString("logs without span\n")

		for _, entry := range orphans {
			fmt.Fprintf(&out, "  %s\n", logText(entry))
		}
	}

	return out.String()
}

// statusText returns " ok" or " error: description" for a span, and "" for an
// unset status.
func statusText(span memorytelemetry.Span) string {
	switch span.Status {
	case telemetry.OK:
		return " ok"
	case telemetry.Error:
		if span.StatusDescription == "" {
			return " error"
		}

		return " error: " + span.StatusDescription
	default:
		return ""
	}
}

// attrsText returns " {k=v, ...}" for attrs and "" when there are none.
func attrsText(attrs []telemetry.Attr) string {
	if len(attrs) == 0 {
		return ""
	}

	parts := make([]string, len(attrs))

	for index, attr := range attrs {
		parts[index] = attr.String()
	}

	return " {" + strings.Join(parts, ", ") + "}"
}

// logText returns `log LEVEL "message" {k=v, ...}` for entry, with its
// attributes sorted by key because [Log] keeps them in a map.
func logText(entry Log) string {
	keys := make([]string, 0, len(entry.Attrs))

	for key := range entry.Attrs {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	parts := make([]string, len(keys))

	for index, key := range keys {
		parts[index] = key + "=" + entry.Attrs[key].String()
	}

	text := fmt.Sprintf("log %v %q", entry.Level, entry.Message)

	if len(parts) > 0 {
		text += " {" + strings.Join(parts, ", ") + "}"
	}

	return text
}
