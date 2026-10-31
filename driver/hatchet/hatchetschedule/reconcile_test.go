package hatchetschedule

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
)

// TestExpressionPassesUTCThrough verifies that UTC cron expressions reach
// Hatchet without an added zone prefix.
func TestExpressionPassesUTCThrough(t *testing.T) {
	t.Parallel()

	driver, _, _ := newTestDriver(t)

	expression, err := driver.expression(entryEvery("a", schedule.UTC().Hour(3)))

	if err != nil || expression != "0 3 * * *" {
		t.Fatalf("expression = %q, %v", expression, err)
	}
}

// TestExpressionRejectsNonUTC verifies that non-UTC entries fail with
// ErrUnsupported unless CronTZ support is enabled.
func TestExpressionRejectsNonUTC(t *testing.T) {
	t.Parallel()

	driver, _, _ := newTestDriver(t)
	location := time.FixedZone("Test", 3600)

	_, err := driver.expression(entryEvery("a", schedule.In(location).Hour(3)))

	if !errors.Is(err, schedule.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

// TestExpressionAddsCronTZWhenAllowed verifies that an enabled CronTZ policy
// includes the entry's explicit zone in the engine expression.
func TestExpressionAddsCronTZWhenAllowed(t *testing.T) {
	t.Parallel()

	driver, _, _ := newTestDriver(t, func(scheduler *Builder) {
		scheduler.AllowCronTZ(true)
	})
	location := time.FixedZone("Test", 3600)

	expression, err := driver.expression(entryEvery("a", schedule.In(location).Hour(3)))

	if err != nil || expression != "CRON_TZ=Test 0 3 * * *" {
		t.Fatalf("expression = %q, %v", expression, err)
	}
}

// TestHashCronDiffersByWorkflowAndExpression verifies that the stable short
// digest changes when either workflow identity or cron expression changes.
func TestHashCronDiffersByWorkflowAndExpression(t *testing.T) {
	t.Parallel()

	base := hashCron("w", "* * * * *")

	if base != hashCron("w", "* * * * *") || base == hashCron("x", "* * * * *") || base == hashCron("w", "0 * * * *") {
		t.Fatal("hash is not stable and distinct")
	}

	if len(base) != 16 {
		t.Fatalf("len(hash) = %d, want 16", len(base))
	}
}

// TestReconcileCreatesMissingCrons verifies that missing entries create native
// triggers with the expected ownership metadata and workflow names.
func TestReconcileCreatesMissingCrons(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	entries := []schedule.Entry{
		entryEvery("a", schedule.UTC().Hour(3)),
		entryEvery("b", schedule.UTC().Hour(4), func(entry *schedule.EntryBuilder) {
			entry.WithoutOverlapping()
		}),
	}

	if err := driver.reconcile(t.Context(), crons, entries); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if len(crons.created) != 2 || crons.createdFor[0] != "arkoren-run" || crons.createdFor[1] != "arkoren-run-exclusive" {
		t.Fatalf("created = %v for %v", crons.created, crons.createdFor)
	}

	first := crons.created[0]

	if first.Name != "a" || first.Input["name"] != "a" || first.AdditionalMetadata[metadataPrefix] != "arkoren" {
		t.Fatalf("created[0] = %+v", first)
	}
}

// TestReconcileKeepsUnchangedCrons verifies that matching ownership and
// definition hashes avoid unnecessary native creates or deletes.
func TestReconcileKeepsUnchangedCrons(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	crons.rows = []fakeCron{{
		id:       "1",
		workflow: "arkoren-run",
		name:     "a",
		metadata: managed("arkoren", "arkoren-run", "0 3 * * *"),
	}}

	if err := driver.reconcile(t.Context(), crons, []schedule.Entry{entryEvery("a", schedule.UTC().Hour(3))}); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if len(crons.created) != 0 || len(crons.deleted) != 0 {
		t.Fatalf("created = %v, deleted = %v", crons.created, crons.deleted)
	}
}

// TestReconcileReplacesStaleCrons verifies that changed definitions replace
// their old native triggers instead of retaining stale expressions.
func TestReconcileReplacesStaleCrons(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	crons.rows = []fakeCron{{
		id:       "1",
		workflow: "arkoren-run",
		name:     "a",
		metadata: managed("arkoren", "arkoren-run", "0 1 * * *"),
	}}

	if err := driver.reconcile(t.Context(), crons, []schedule.Entry{entryEvery("a", schedule.UTC().Hour(3))}); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if len(crons.deleted) != 1 || crons.deleted[0] != "1" || len(crons.created) != 1 {
		t.Fatalf("created = %v, deleted = %v", crons.created, crons.deleted)
	}
}

// TestReconcileDeletesUndeclaredCrons verifies that owned triggers absent from
// the declaration set are removed.
func TestReconcileDeletesUndeclaredCrons(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	crons.rows = []fakeCron{{
		id:       "9",
		workflow: "arkoren-run-exclusive",
		name:     "gone",
		metadata: managed("arkoren", "arkoren-run-exclusive", "0 1 * * *"),
	}}

	if err := driver.reconcile(t.Context(), crons, nil); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if len(crons.deleted) != 1 || crons.deleted[0] != "9" {
		t.Fatalf("deleted = %v", crons.deleted)
	}
}

// TestReconcileIgnoresForeignCrons verifies that reconciliation leaves triggers
// belonging to another prefix untouched.
func TestReconcileIgnoresForeignCrons(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	crons.rows = []fakeCron{
		{
			id:       "1",
			workflow: "arkoren-run",
			name:     "other",
			metadata: managed("different", "arkoren-run", "x"),
		},
		{
			id:       "2",
			workflow: "arkoren-run",
			name:     "bare",
		},
	}

	if err := driver.reconcile(t.Context(), crons, nil); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if len(crons.deleted) != 0 {
		t.Fatalf("deleted = %v", crons.deleted)
	}
}

// TestReconcilePagesThroughAllCrons verifies that reconciliation reaches owned
// triggers beyond the first native listing page.
func TestReconcilePagesThroughAllCrons(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)

	for index := range int(pageSize) + 5 {
		crons.rows = append(crons.rows, fakeCron{
			id:       fmt.Sprint(index),
			workflow: "arkoren-run",
			name:     fmt.Sprint("n", index),
			metadata: managed("arkoren", "arkoren-run", "x"),
		})
	}

	if err := driver.reconcile(t.Context(), crons, nil); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if len(crons.deleted) != int(pageSize)+5 {
		t.Fatalf("deleted %d crons", len(crons.deleted))
	}
}

// TestReconcileMovesCronBetweenWorkflows verifies that changing overlap policy
// moves the trigger between ordinary and exclusive workflow types.
func TestReconcileMovesCronBetweenWorkflows(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	crons.rows = []fakeCron{{
		id:       "1",
		workflow: "arkoren-run",
		name:     "a",
		metadata: managed("arkoren", "arkoren-run", "0 3 * * *"),
	}}
	entries := []schedule.Entry{entryEvery("a", schedule.UTC().Hour(3), func(entry *schedule.EntryBuilder) {
		entry.WithoutOverlapping()
	})}

	if err := driver.reconcile(t.Context(), crons, entries); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if len(crons.deleted) != 1 || len(crons.createdFor) != 1 || crons.createdFor[0] != "arkoren-run-exclusive" {
		t.Fatalf("deleted = %v, createdFor = %v", crons.deleted, crons.createdFor)
	}
}

// TestReconcileTreatsConflictAsSuccess verifies that a concurrent native create
// conflict does not fail reconciliation.
func TestReconcileTreatsConflictAsSuccess(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	crons.createErr = errors.New("received non-200 response from server. got status 409 with body 'exists'")

	if err := driver.reconcile(t.Context(), crons, []schedule.Entry{entryEvery("a", schedule.UTC().Hour(3))}); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
}

// TestReconcileReturnsCreateErrors verifies that unexpected native creation
// failures remain visible to the caller.
func TestReconcileReturnsCreateErrors(t *testing.T) {
	t.Parallel()

	driver, crons, _ := newTestDriver(t)
	crons.createErr = errors.New("boom")

	if err := driver.reconcile(t.Context(), crons, []schedule.Entry{entryEvery("a", schedule.UTC().Hour(3))}); err == nil {
		t.Fatal("reconcile() error = nil")
	}
}
