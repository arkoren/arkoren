// Package tracegroup organizes retained span snapshots without storage or DTOs.
package tracegroup

import (
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// Group contains the recorded spans of one trace in encounter order.
type Group struct {
	// ID is the trace identity shared by all spans.
	ID telemetry.TraceID

	// Spans retains the supplied snapshots without copying their containers.
	Spans []telemetry.FinishedSpan
}

// Groups partitions spans by trace ID in first-appearance order. Input snapshots
// must remain read-only while the resulting groups are used.
func Groups(spans []telemetry.FinishedSpan) []*Group {
	var groups []*Group
	index := map[telemetry.TraceID]*Group{}

	for _, span := range spans {
		group, ok := index[span.Context.TraceID]

		if !ok {
			group = &Group{ID: span.Context.TraceID}
			index[group.ID] = group
			groups = append(groups, group)
		}

		group.Spans = append(group.Spans, span)
	}

	return groups
}

// ByStart returns spans ordered by start, retaining encounter order for ties.
func (group *Group) ByStart() []telemetry.FinishedSpan {
	return slices.SortedStableFunc(slices.Values(group.Spans), func(a, b telemetry.FinishedSpan) int {
		return a.Start.Compare(b.Start)
	})
}

// HasParent reports whether span's parent is among the retained spans.
func (group *Group) HasParent(span telemetry.FinishedSpan) bool {
	return !span.Parent.SpanID.IsZero() && slices.ContainsFunc(group.Spans, func(other telemetry.FinishedSpan) bool {
		return other.Context.SpanID == span.Parent.SpanID
	})
}

// Root returns the earliest span with no retained parent. A group is nonempty;
// when every span has a retained parent, the earliest span is the fallback.
func (group *Group) Root() telemetry.FinishedSpan {
	ordered := group.ByStart()

	for _, span := range ordered {
		if !group.HasParent(span) {
			return span
		}
	}

	return ordered[0]
}

// Matches checks exact root duration and route, plus contained span-name text.
func (group *Group) Matches(root telemetry.FinishedSpan, minimum time.Duration, route, name string) bool {
	if root.End.Sub(root.Start) < minimum {
		return false
	}

	if name != "" && !slices.ContainsFunc(group.Spans, func(span telemetry.FinishedSpan) bool {
		return strings.Contains(span.Name, name)
	}) {
		return false
	}

	attr, _ := root.Attr(semconv.HTTPRoute)

	return route == "" || attr.Text() == route || root.Name == route
}
