package conformance

import (
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// eventTB captures assertion failures without failing the enclosing regression
// test. The embedded TB provides the testing-only interface; requireEvent uses
// only Helper and Errorf, which this recorder implements directly.
type eventTB struct {
	testing.TB

	// failed records whether requireEvent rejected the event snapshot.
	failed bool
}

// Helper leaves the real test's call-site tracking unchanged. The recorder is
// used only to inspect the assertion's verdict, not its diagnostic location.
func (*eventTB) Helper() {}

// Errorf records an unsuccessful assertion without formatting its diagnostic.
// The regression tests check the verdict independently of diagnostic wording.
func (recorder *eventTB) Errorf(string, ...any) {
	recorder.failed = true
}

// TestRequireEventRejectsPartialAttributes verifies that one matching attribute
// cannot hide another requested attribute with a different value.
func TestRequireEventRejectsPartialAttributes(t *testing.T) {
	t.Parallel()

	recorder := &eventTB{}
	span := memorytelemetry.Span{Events: []memorytelemetry.Event{{
		Name: "progress", Attrs: []telemetry.Attr{telemetry.Int("step", 1), telemetry.String("state", "waiting")},
	}}}

	requireEvent(recorder, span, "progress", telemetry.Int("step", 1), telemetry.String("state", "done"))

	if !recorder.failed {
		t.Fatal("event with only one matching attribute passed")
	}
}

// TestRequireEventAcceptsNameWithoutAttributes checks that omitting requested
// attributes matches the event name, including an event with no attributes.
func TestRequireEventAcceptsNameWithoutAttributes(t *testing.T) {
	t.Parallel()

	recorder := &eventTB{}
	span := memorytelemetry.Span{Events: []memorytelemetry.Event{{Name: "progress"}}}

	requireEvent(recorder, span, "progress")

	if recorder.failed {
		t.Fatal("matching name without requested attributes failed")
	}
}

// TestRequireEventAcceptsAllAttributes allows extra recorded attributes and
// searches past a partial event to find one event satisfying the whole request.
func TestRequireEventAcceptsAllAttributes(t *testing.T) {
	t.Parallel()

	recorder := &eventTB{}
	span := memorytelemetry.Span{Events: []memorytelemetry.Event{
		{Name: "progress", Attrs: []telemetry.Attr{telemetry.Int("step", 1)}},
		{Name: "progress", Attrs: []telemetry.Attr{
			telemetry.Int("step", 1), telemetry.String("state", "done"), telemetry.Bool("extra", true),
		}},
	}}

	requireEvent(recorder, span, "progress", telemetry.Int("step", 1), telemetry.String("state", "done"))

	if recorder.failed {
		t.Fatal("event with every requested attribute failed")
	}
}

// TestRequireEventRejectsAttributesSplitAcrossEvents prevents separate events
// with the same name from collectively satisfying a single event assertion.
func TestRequireEventRejectsAttributesSplitAcrossEvents(t *testing.T) {
	t.Parallel()

	recorder := &eventTB{}
	span := memorytelemetry.Span{Events: []memorytelemetry.Event{
		{Name: "progress", Attrs: []telemetry.Attr{telemetry.Int("step", 1)}},
		{Name: "progress", Attrs: []telemetry.Attr{telemetry.String("state", "done")}},
	}}

	requireEvent(recorder, span, "progress", telemetry.Int("step", 1), telemetry.String("state", "done"))

	if !recorder.failed {
		t.Fatal("attributes split across two events passed")
	}
}

// TestRequireEventRejectsDifferentName ensures an empty attribute request still
// requires an event with the requested name.
func TestRequireEventRejectsDifferentName(t *testing.T) {
	t.Parallel()

	recorder := &eventTB{}
	span := memorytelemetry.Span{Events: []memorytelemetry.Event{{Name: "other"}}}

	requireEvent(recorder, span, "progress")

	if !recorder.failed {
		t.Fatal("event with a different name passed")
	}
}
