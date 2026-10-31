package memorytelemetry

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry/internal/tracegroup"
	"arkoren.dev/foundation/validate"
)

const (
	// defaultTraceLimit is the number of summaries returned when "limit" is
	// absent.
	defaultTraceLimit = 20

	// defaultLogLimit is the number of log records returned when "limit" is
	// absent.
	defaultLogLimit = 100
)

// ErrTraceNotFound is the safe MCP failure the trace operation returns when no
// recorded trace has the requested ID or ID prefix. The trace may have been
// evicted from the ring.
//
// Example:
//
//	failure, ok := mcp.ToolFailureInfo(memorytelemetry.ErrTraceNotFound)
//	if ok { fmt.Println(failure.Code) }
var ErrTraceNotFound = mcp.ToolFailure("trace_not_found", "No recorded trace has that ID; it may have been evicted", nil)

// ErrAmbiguousTrace is the safe MCP failure the trace operation returns when a
// trace ID prefix matches more than one recorded trace. It lists no
// candidates; retry with a longer prefix.
//
// Example:
//
//	failure, ok := mcp.ToolFailureInfo(memorytelemetry.ErrAmbiguousTrace)
//	if ok { fmt.Println(failure.Code) }
var ErrAmbiguousTrace = mcp.ToolFailure("ambiguous_trace", "The prefix matches more than one trace; use a longer prefix", nil)

// hexPattern matches the characters of a trace ID or a prefix of one.
var hexPattern = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// traceSummary is one item of the traces operation: a
// trace reduced to what is needed to decide whether to open it.
type traceSummary struct {
	// TraceID is the 32-hex trace ID.
	TraceID string `json:"trace_id"`

	// Name is the name of the root span: the span whose parent is not among
	// the recorded spans of the trace.
	Name string `json:"name"`

	// Start is when the earliest span started.
	Start time.Time `json:"start"`

	// Age is how long before the response Start was, such as "12s ago".
	Age string `json:"age"`

	// DurationMS is the root span's duration in milliseconds.
	DurationMS float64 `json:"duration_ms"`

	// Duration is DurationMS as text, such as "182.1ms".
	Duration validate.Duration `json:"duration"`

	// SpanCount is the number of recorded spans.
	SpanCount int `json:"span_count"`

	// ErrorCount is the number of spans that ended with an error.
	ErrorCount int `json:"error_count"`

	// Status is error when ErrorCount is positive, otherwise ok.
	Status string `json:"status"`

	// HTTPStatus is the root span's http.response.status_code attribute;
	// omitted when absent.
	HTTPStatus int `json:"http_status,omitzero"`

	// ErrorType is the error.type attribute of the first errored span in
	// start order; omitted when absent.
	ErrorType string `json:"error_type,omitzero"`
}

// traceView is the output of the trace operation: the
// spans of one trace as a tree, with each log record attached to the span it
// was written in.
type traceView struct {
	// TraceID is the 32-hex trace ID.
	TraceID string `json:"trace_id"`

	// Roots are the spans whose parent is not among the recorded spans,
	// ordered by start; never null. There is usually one. There are several
	// when the trace entered the process more than once (each entry has a
	// remote parent) or when the ring evicted a middle span, and the first
	// is the root the summary uses.
	Roots []spanView `json:"roots"`

	// OrphanLogs are the trace's log records whose span is not among the
	// recorded spans, oldest first; never null.
	OrphanLogs []logView `json:"orphan_logs"`
}

// spanView is one finished span with its logs and child spans.
type spanView struct {
	// SpanID is the 16-hex span ID.
	SpanID string `json:"span_id"`

	// ParentID is the parent span's ID whenever that span ID is nonzero,
	// including when that parent is a node above this one. It is omitted for
	// a root with no parent; remote and evicted parents retain their IDs.
	ParentID string `json:"parent_id,omitzero"`

	// Name is the span name.
	Name string `json:"name"`

	// Kind is "internal", "server", "client", "producer", or "consumer".
	Kind string `json:"kind"`

	// Scope is the instrumentation scope that started the span.
	Scope string `json:"scope"`

	// Status is unset, ok, or error.
	Status string `json:"status"`

	// StatusDescription explains an error status.
	StatusDescription string `json:"status_description,omitzero"`

	// Start is when the span started.
	Start time.Time `json:"start"`

	// Age is how long before the response Start was, such as "12s ago".
	Age string `json:"age"`

	// DurationMS is the span duration in milliseconds.
	DurationMS float64 `json:"duration_ms"`

	// Duration is DurationMS as text, such as "182.1ms".
	Duration validate.Duration `json:"duration"`

	// Attrs are the span attributes.
	Attrs map[string]any `json:"attrs"`

	// Events are the span events in order; recorded errors are "exception".
	Events []spanEventView `json:"events"`

	// Logs are the log records written inside this span, oldest first; never
	// null.
	Logs []logView `json:"logs"`

	// Children are the spans started inside this one, ordered by start;
	// never null.
	Children []spanView `json:"children"`
}

// spanEventView is a timestamped annotation on a [spanView].
type spanEventView struct {
	// Name is the event name.
	Name string `json:"name"`

	// Time is when the event was recorded.
	Time time.Time `json:"time"`

	// Attrs are the event attributes.
	Attrs map[string]any `json:"attrs"`
}

// logView is one log record. It is the item of the
// logs operation and its cursor pages, and appears inside a [traceView].
type logView struct {
	// Time is the record time.
	Time time.Time `json:"time"`

	// Age is how long before the response Time was, such as "12s ago".
	Age string `json:"age"`

	// Level is "debug", "info", "warn", or "error".
	Level string `json:"level"`

	// Message is the log message.
	Message string `json:"message"`

	// TraceID is the trace the record was logged in; omitted outside a span.
	TraceID string `json:"trace_id,omitzero"`

	// SpanID is the span the record was logged in; it joins [spanView.SpanID].
	SpanID string `json:"span_id,omitzero"`

	// Attrs are the record attributes.
	Attrs map[string]any `json:"attrs"`
}

// metricView is one instrument and its aggregates.
type metricView struct {
	// Scope is the instrumentation scope of the meter.
	Scope string `json:"scope"`

	// Name is the instrument name.
	Name string `json:"name"`

	// Description is the help text.
	Description string `json:"description,omitzero"`

	// Unit is the UCUM unit.
	Unit string `json:"unit,omitzero"`

	// Kind is "counter", "up_down_counter", "histogram", or "gauge".
	Kind string `json:"kind"`

	// Keys are the label keys; each point's Values follow this order.
	Keys []string `json:"keys"`

	// Points are the aggregates, one per label combination.
	Points []pointView `json:"points"`
}

// pointView is the aggregate of one label combination of a [metricView].
type pointView struct {
	// Values are the label values, one per key of the metric.
	Values []string `json:"values"`

	// Count is the number of recordings.
	Count uint64 `json:"count"`

	// Sum is the total of recorded values.
	Sum float64 `json:"sum"`

	// Last is the most recent value, which is the value of a gauge.
	Last float64 `json:"last"`
}

// tracesInput is the input of the traces operation.
type tracesInput struct {
	// Status filters traces by their summarized status; empty accepts any status.
	Status string `json:"status,omitzero"`
	// Route matches the root's HTTP route attribute or its exact span name.
	Route string `json:"route,omitzero"`
	// Name selects traces containing the requested span-name substring.
	Name string `json:"name,omitzero"`
	// MinDuration excludes traces shorter than this duration; zero has no minimum.
	MinDuration validate.Duration `json:"min_duration,omitzero"`
	// Since restricts trace age relative to the operation's current time.
	Since validate.Duration `json:"since,omitzero"`
	// Limit caps the newest-first results; zero selects the default trace limit.
	Limit int `json:"limit,omitzero"`
}

// traceInput is the input of the trace operation.
type traceInput struct {
	// ID is a full hexadecimal trace ID or a uniquely matching prefix.
	ID string `json:"id"`
}

// logsInput is the input of the logs operation.
type logsInput struct {
	// Level sets the minimum severity; empty leaves all levels eligible.
	Level string `json:"level,omitzero"`
	// TraceID restricts records to the supplied hexadecimal trace ID.
	TraceID string `json:"trace_id,omitzero"`
	// Since restricts record age relative to the operation's current time.
	Since validate.Duration `json:"since,omitzero"`
	// Limit caps snapshot results; zero selects the default log limit.
	Limit int `json:"limit,omitzero"`
	// Cursor selects a history snapshot, tail anchor, or bounded continuation.
	Cursor string `json:"cursor,omitzero"`
	// Wait bounds an idle continuation call to at most 25 seconds.
	Wait validate.Duration `json:"wait,omitzero"`
}

// metricsInput is the input of the metrics operation.
type metricsInput struct {
	// Name is an optional instrument-name prefix used to filter aggregates.
	Name string `json:"name,omitzero"`
}

// traceIDSchema is the schema of a trace ID or prefix of one: 8 to 32 hex
// digits.
func traceIDSchema() validate.StringSchema[string] {
	return validate.String(func(traceID *validate.StringBuilder[string]) {
		traceID.Min(8)
		traceID.Max(32)
		traceID.Regex(hexPattern)
	})
}

// MCPTools publishes SDK-independent read-only diagnostics over retained
// traces, trace trees, logs, and metrics. Empty prefix produces unqualified
// names in the telemetry CLI group; a nonempty prefix produces prefix.traces,
// prefix.trace, prefix.logs, and prefix.metrics in that prefix's CLI group.
// Native roles identify each diagnostic operation independently of its name.
// Applications compose these declarations explicitly with other owners' tools.
//
// The declarations own no resources and do not start the driver. Retention
// follows [Builder.MaxSpans], [Builder.MaxLogs], and [Builder.MaxSeries]. Logs
// support history snapshots, immediate tail anchoring, and bounded continuation
// polling. Reset invalidates the store generation and returns a fresh tail
// with instance_changed. Malformed/foreign/future cursors return safe failures.
// Availability and outer transport authentication remain composition concerns.
//
// Example:
//
//	store := memorytelemetry.New()
//	tools := store.MCPTools("memory")
//	for _, tool := range tools {
//		if err := tool.Err(); err != nil { return err }
//	}
func (driver *Driver) MCPTools(prefix string) []mcp.Tool {
	group, stem := "telemetry", ""

	if prefix != "" {
		group, stem = prefix, prefix+"."
	}

	traces := mcp.NewTool(stem+"traces", func(_ context.Context, input tracesInput) (diagnostic.List[traceSummary], error) {
		return listTraces(driver, input), nil
	}, func(read *mcp.ToolBuilder[tracesInput, diagnostic.List[traceSummary]]) {
		read.CLI(group, "traces").Role("traces").ReadOnly(true).Idempotent(true).OpenWorld(false)
		read.GroupSummary("Traces, logs, and metrics held in memory")
		read.Description("Recent traces, newest first, filtered by outcome, route, span name, duration, and age")
		read.Input(validate.Struct(func(object *validate.StructBuilder[tracesInput]) {
			object.Field("Status", func(status *validate.StringBuilder[string]) {
				status.OneOf(diagnostic.StatusError, diagnostic.StatusOK)
				status.Describe("Only traces with this outcome")
			})
			object.Field("Route", func(route *validate.StringBuilder[string]) {
				route.Describe("Only traces whose root has this http.route or span name")
			})
			object.Field("Name", func(name *validate.StringBuilder[string]) {
				name.Describe("Only traces with a span whose name contains this text")
			})
			object.Field("MinDuration", func(duration *validate.TextBuilder[validate.Duration]) {
				duration.Use(diagnostic.DurationSchema("Only traces whose root took at least this long, such as 250ms"))
			})
			object.Field("Since", func(since *validate.TextBuilder[validate.Duration]) {
				since.Use(diagnostic.DurationSchema("Only traces that started within this long ago, such as 5m"))
			})
			object.Field("Limit", func(limit *validate.IntBuilder[int]) {
				limit.Use(diagnostic.LimitSchema(defaultTraceLimit))
			})
		}))
		read.Example("Failed traces from the last 5 minutes", `{"status":"error","since":"5m"}`)
		read.Example("Slow requests", `{"min_duration":"500ms"}`)
	})

	trace := mcp.NewTool(stem+"trace", func(_ context.Context, input traceInput) (traceView, error) {
		return findTrace(driver, input.ID)
	}, func(read *mcp.ToolBuilder[traceInput, traceView]) {
		read.CLI(group, "trace").Role("trace").ReadOnly(true).Idempotent(true).OpenWorld(false)
		read.Description("One trace as a tree of spans with their logs; the ID may be a unique prefix of 8 or more hex digits")
		read.Input(validate.Struct(func(object *validate.StructBuilder[traceInput]) {
			object.Field("ID", func(traceID *validate.StringBuilder[string]) {
				traceID.Use(traceIDSchema())
				traceID.Required()
				traceID.Describe("traceView ID, or a unique prefix of at least 8 hex digits")
			})
		}))
		read.Positional("id")
	})

	logs := mcp.NewTool(stem+"logs", func(ctx context.Context, input logsInput) (diagnostic.Page[logView], error) {
		return driver.pollLogs(ctx, stem+"logs", input)
	}, func(read *mcp.ToolBuilder[logsInput, diagnostic.Page[logView]]) {
		read.CLI(group, "logs").Role("logs").ReadOnly(true).Idempotent(true).OpenWorld(false).Cursor(true)
		read.Description("Recent log records, oldest first, with bounded cursor polling")
		read.Input(validate.Struct(func(object *validate.StructBuilder[logsInput]) {
			object.Field("Level", func(level *validate.StringBuilder[string]) {
				level.OneOf("debug", "info", "warn", "error")
				level.Describe("Only records at or above this level")
			})
			object.Field("TraceID", func(traceID *validate.StringBuilder[string]) {
				traceID.Use(traceIDSchema())
				traceID.Describe("Only records of this trace, by ID or prefix of at least 8 hex digits")
			})
			object.Field("Since", func(since *validate.TextBuilder[validate.Duration]) {
				since.Use(diagnostic.DurationSchema("Only records written within this long ago, such as 30s"))
			})
			object.Field("Limit", func(limit *validate.IntBuilder[int]) {
				limit.Use(diagnostic.LimitSchema(defaultLogLimit))
			})
			object.Field("Cursor", func(cursor *validate.StringBuilder[string]) {
				cursor.Describe("Opaque continuation cursor; tail anchors without history")
			})
			object.Field("Wait", func(wait *validate.TextBuilder[validate.Duration]) {
				wait.Use(diagnostic.WaitSchema())
			})
		}))
		read.Example("Errors of the last 10 minutes", `{"level":"error","since":"10m"}`)
	})

	metrics := mcp.NewTool(stem+"metrics", func(_ context.Context, input metricsInput) (diagnostic.List[metricView], error) {
		return listMetrics(driver, input), nil
	}, func(read *mcp.ToolBuilder[metricsInput, diagnostic.List[metricView]]) {
		read.CLI(group, "metrics").Role("metrics").ReadOnly(true).Idempotent(true).OpenWorld(false)
		read.Description("Metric aggregates by instrument")
		read.Input(validate.Struct(func(object *validate.StructBuilder[metricsInput]) {
			object.Field("Name", func(name *validate.StringBuilder[string]) {
				name.Describe("Only instruments whose name starts with this prefix")
			})
		}))
		read.Positional("name")
		read.Example("HTTP server metrics", `{"name":"http.server"}`)
	})

	return []mcp.Tool{traces, trace, logs, metrics}
}

// summarizeTrace reduces a retained group to its diagnostic summary.
func summarizeTrace(group *tracegroup.Group, root Span, now time.Time) traceSummary {
	rootDuration := root.End.Sub(root.Start)
	ordered := group.ByStart()
	summary := traceSummary{
		TraceID:    group.ID.String(),
		Name:       root.Name,
		Start:      ordered[0].Start,
		Age:        diagnostic.Age(now, ordered[0].Start),
		DurationMS: diagnostic.Millis(rootDuration),
		Duration:   diagnostic.DurationText(rootDuration),
		SpanCount:  len(group.Spans),
		Status:     diagnostic.StatusOK,
	}

	for _, span := range ordered {
		if span.Status != telemetry.Error {
			continue
		}

		if summary.ErrorCount == 0 {
			attr, _ := span.Attr(semconv.ErrorType)
			summary.ErrorType = attr.Text()
		}

		summary.ErrorCount++
	}

	if summary.ErrorCount > 0 {
		summary.Status = diagnostic.StatusError
	}

	attr, _ := root.Attr(semconv.HTTPResponseStatusCode)
	summary.HTTPStatus = int(attr.AsInt64())

	return summary
}

// listTraces answers the traces operation: summaries, newest first.
func listTraces(driver *Driver, input tracesInput) diagnostic.List[traceSummary] {
	now := time.Now()

	summaries := []traceSummary{}

	for _, group := range tracegroup.Groups(driver.Spans()) {
		root := group.Root()
		summary := summarizeTrace(group, root, now)

		if !matchesTraceSummary(summary, input, now) {
			continue
		}

		if !group.Matches(root, input.MinDuration.Std(), input.Route, input.Name) {
			continue
		}

		summaries = append(summaries, summary)
	}

	slices.SortStableFunc(summaries, func(a, b traceSummary) int {
		return b.Start.Compare(a.Start)
	})

	if len(summaries) > input.Limit {
		return diagnostic.List[traceSummary]{
			Items:     summaries[:input.Limit],
			Truncated: true,
		}
	}

	return diagnostic.List[traceSummary]{Items: summaries}
}

// matchesTraceSummary applies outcome and age filters to already computed
// summary metadata; age is measured relative to the beginning of this call.
func matchesTraceSummary(summary traceSummary, input tracesInput, now time.Time) bool {
	if input.Status != "" && summary.Status != input.Status {
		return false
	}

	return input.Since == 0 || !summary.Start.Before(now.Add(-input.Since.Std()))
}

// findTrace answers the trace operation for a full trace ID or a unique prefix
// of one.
func findTrace(driver *Driver, id string) (traceView, error) {
	match, err := lookupTrace(driver.Spans(), id)

	if err != nil {
		return traceView{}, err
	}

	now := time.Now()
	ordered := match.ByStart()
	logsBySpan, orphanLogs := correlateLogs(driver.Logs(), match, now)
	trace := traceView{
		TraceID:    match.ID.String(),
		Roots:      []spanView{},
		OrphanLogs: orphanLogs,
	}

	// Children of every span, in start order because ordered is.
	childrenOf := map[telemetry.SpanID][]Span{}

	for _, span := range ordered {
		if match.HasParent(span) {
			childrenOf[span.Parent.SpanID] = append(childrenOf[span.Parent.SpanID], span)
		}
	}

	var build func(span Span) spanView
	build = func(span Span) spanView {
		node := spanNode(span, now)

		if logs := logsBySpan[span.Context.SpanID]; logs != nil {
			node.Logs = logs
		}

		for _, child := range childrenOf[span.Context.SpanID] {
			node.Children = append(node.Children, build(child))
		}

		return node
	}

	for _, span := range ordered {
		if !match.HasParent(span) {
			trace.Roots = append(trace.Roots, build(span))
		}
	}

	return trace, nil
}

// lookupTrace resolves a case-insensitive full ID or unique prefix. Failures
// intentionally reveal no candidate IDs and preserve sentinel error identity.
func lookupTrace(spans []Span, id string) (*tracegroup.Group, error) {
	prefix := strings.ToLower(id)
	var match *tracegroup.Group

	for _, group := range tracegroup.Groups(spans) {
		if !strings.HasPrefix(group.ID.String(), prefix) {
			continue
		}

		if match != nil {
			return nil, ErrAmbiguousTrace
		}

		match = group
	}

	if match == nil {
		return nil, ErrTraceNotFound
	}

	return match, nil
}

// correlateLogs joins retained trace logs to known spans. Records from
// unfinished or evicted spans remain visible as orphans, in capture order.
func correlateLogs(logs []Log, group *tracegroup.Group, now time.Time) (map[telemetry.SpanID][]logView, []logView) {
	known := make(map[telemetry.SpanID]bool, len(group.Spans))

	for _, span := range group.Spans {
		known[span.Context.SpanID] = true
	}

	bySpan := map[telemetry.SpanID][]logView{}
	orphans := []logView{}

	for _, log := range logs {
		if !log.Context.IsValid() || log.Context.TraceID != group.ID {
			continue
		}

		if !known[log.Context.SpanID] {
			orphans = append(orphans, logRecord(log, now))
			continue
		}

		bySpan[log.Context.SpanID] = append(bySpan[log.Context.SpanID], logRecord(log, now))
	}

	return bySpan, orphans
}

// spanNode converts a ring span to its diagnostic DTO, without logs and children.
func spanNode(span Span, now time.Time) spanView {
	duration := span.End.Sub(span.Start)
	node := spanView{
		SpanID:            span.Context.SpanID.String(),
		Name:              span.Name,
		Kind:              spanKindName(span.Kind),
		Scope:             span.Scope,
		Status:            spanStatusName(span.Status),
		StatusDescription: span.StatusDescription,
		Start:             span.Start,
		Age:               diagnostic.Age(now, span.Start),
		DurationMS:        diagnostic.Millis(duration),
		Duration:          diagnostic.DurationText(duration),
		Attrs:             attrMap(span.Attrs),
		Events:            make([]spanEventView, 0, len(span.Events)),
		Logs:              []logView{},
		Children:          []spanView{},
	}

	if !span.Parent.SpanID.IsZero() {
		node.ParentID = span.Parent.SpanID.String()
	}

	for _, event := range span.Events {
		node.Events = append(node.Events, spanEventView{
			Name:  event.Name,
			Time:  event.Time,
			Attrs: attrMap(event.Attrs),
		})
	}

	return node
}

// logFilter is the set of conditions shared by history and continuation calls
// of the logs tool. Its cutoff remains fixed during one bounded poll.
type logFilter struct {
	// minLevel is the lowest severity kept; math.MinInt keeps all.
	minLevel slog.Level

	// tracePrefix is a lowercase hex trace ID or prefix; empty keeps all.
	tracePrefix string

	// cutoff is the oldest time kept; zero keeps all.
	cutoff time.Time
}

// newLogFilter builds the filter of input; since is relative to now.
func newLogFilter(input logsInput, now time.Time) logFilter {
	filter := logFilter{
		minLevel:    slog.Level(math.MinInt),
		tracePrefix: strings.ToLower(input.TraceID),
	}

	switch input.Level {
	case "debug":
		filter.minLevel = slog.LevelDebug
	case "info":
		filter.minLevel = slog.LevelInfo
	case "warn":
		filter.minLevel = slog.LevelWarn
	case "error":
		filter.minLevel = slog.LevelError
	}

	if input.Since > 0 {
		filter.cutoff = now.Add(-input.Since.Std())
	}

	return filter
}

// matches reports whether log passes the filter.
func (filter logFilter) matches(log Log) bool {
	if log.Level < filter.minLevel || log.Time.Before(filter.cutoff) {
		return false
	}

	return filter.tracePrefix == "" ||
		(log.Context.IsValid() && strings.HasPrefix(log.Context.TraceID.String(), filter.tracePrefix))
}

// listMetrics answers the metrics operation, filtered by instrument name
// prefix.
func listMetrics(driver *Driver, input metricsInput) diagnostic.List[metricView] {
	items := []metricView{}

	for _, metric := range driver.Metrics() {
		if !strings.HasPrefix(metric.Name, input.Name) {
			continue
		}

		wire := metricView{
			Scope:       metric.Scope,
			Name:        metric.Name,
			Description: metric.Description,
			Unit:        metric.Unit,
			Kind:        instrumentKindName(metric.Kind),
			Keys:        metric.Keys,
			Points:      make([]pointView, 0, len(metric.Points)),
		}

		for _, point := range metric.Points {
			wire.Points = append(wire.Points, pointView(point))
		}

		items = append(items, wire)
	}

	return diagnostic.List[metricView]{Items: items}
}

// logRecord converts a ring entry to its wire form, with its age relative to
// now.
func logRecord(log Log, now time.Time) logView {
	record := logView{
		Time:    log.Time,
		Age:     diagnostic.Age(now, log.Time),
		Level:   levelName(log.Level),
		Message: log.Message,
		Attrs:   attrMap(log.Attrs),
	}

	if log.Context.IsValid() {
		record.TraceID, record.SpanID = log.Context.TraceID.String(), log.Context.SpanID.String()
	}

	return record
}

// levelName maps a slog level to the nearest of debug, info, warn, and error
// at or below it.
func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warn"
	case level >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

// attrMap converts attributes to a map for the wire. A repeated key keeps its
// last value. The result is never nil.
func attrMap(attrs []telemetry.Attr) map[string]any {
	values := make(map[string]any, len(attrs))

	for _, attr := range attrs {
		values[attr.Key()] = attr.Value()
	}

	return values
}

// spanKindName names a span kind in lowercase.
func spanKindName(kind telemetry.SpanKind) string {
	switch kind {
	case telemetry.Server:
		return "server"
	case telemetry.Client:
		return "client"
	case telemetry.Producer:
		return "producer"
	case telemetry.Consumer:
		return "consumer"
	default:
		return "internal"
	}
}

// spanStatusName names a span status code.
func spanStatusName(status telemetry.StatusCode) string {
	switch status {
	case telemetry.OK:
		return diagnostic.StatusOK
	case telemetry.Error:
		return diagnostic.StatusError
	default:
		return diagnostic.StatusUnset
	}
}

// instrumentKindName names an instrument kind in snake case.
func instrumentKindName(kind telemetry.InstrumentKind) string {
	switch kind {
	case telemetry.CounterKind:
		return "counter"
	case telemetry.UpDownCounterKind:
		return "up_down_counter"
	case telemetry.HistogramKind:
		return "histogram"
	default:
		return "gauge"
	}
}
