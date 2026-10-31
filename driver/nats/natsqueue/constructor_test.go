package natsqueue_test

import (
	"errors"
	"testing"
	"time"

	"arkoren.dev/driver/nats/natsqueue"
)

// TestNewRequiresPositionalTopology keeps required settings out of optional
// policies and reports missing topology without performing connection I/O.
func TestNewRequiresPositionalTopology(t *testing.T) {
	t.Parallel()

	_, err := natsqueue.New("JOBS", nil)

	if !errors.Is(err, natsqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want invalid topology", err)
	}
}

// TestNewRejectsFailureSubjectInWorkTopology prevents terminal envelopes from
// being consumed again as jobs under the same durable consumer.
func TestNewRejectsFailureSubjectInWorkTopology(t *testing.T) {
	t.Parallel()

	_, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.FailureSubject("jobs.work")
	})

	if !errors.Is(err, natsqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want invalid topology", err)
	}
}

// TestNewRejectsInvalidNativeStreamName validates native names without dialing.
func TestNewRejectsInvalidNativeStreamName(t *testing.T) {
	t.Parallel()

	_, err := natsqueue.New("invalid.stream", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	})

	if !errors.Is(err, natsqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want invalid stream", err)
	}
}

// TestNewRejectsInvalidNativeDurableName rejects the native forbidden dot.
func TestNewRejectsInvalidNativeDurableName(t *testing.T) {
	t.Parallel()

	_, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "invalid.durable"},
	})

	if !errors.Is(err, natsqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want invalid durable", err)
	}
}

// TestNewRejectsWildcardWorkSubject excludes subjects that cannot be used as
// the literal publication destination promised by the topology map.
func TestNewRejectsWildcardWorkSubject(t *testing.T) {
	t.Parallel()

	_, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.>", Durable: "work"},
	})

	if !errors.Is(err, natsqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want invalid subject", err)
	}
}

// TestNewRejectsZeroHeartbeatInterval excludes the positive AckWait that would
// truncate to a zero ticker interval when divided by two.
func TestNewRejectsZeroHeartbeatInterval(t *testing.T) {
	t.Parallel()

	_, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.AckWait(time.Nanosecond)
	})

	if !errors.Is(err, natsqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want invalid AckWait", err)
	}
}

// TestNewRejectsUnsupportedURLScheme reports ordinary URL errors at construction
// and never needs an unavailable remote server to discover the invalid scheme.
func TestNewRejectsUnsupportedURLScheme(t *testing.T) {
	t.Parallel()

	_, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs("https://localhost:4222")
	})

	if !errors.Is(err, natsqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want invalid URLs", err)
	}
}
