package schedule_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
)

// noop supplies a successful entry handler without performing any work.
func noop(context.Context) error {
	return nil
}

// TestEntryValid checks that publication retains the declared identity, timing,
// and execution policy and computes the next matching run.
func TestEntryValid(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
		entry.WithoutOverlapping()
		entry.Timeout(time.Hour)
		entry.Description("nightly report")
	})

	if err := entry.Err(); err != nil {
		t.Fatal(err)
	}

	if entry.Name() != "report" || entry.DescriptionValue() != "nightly report" {
		t.Fatalf("unexpected identity %+v", entry)
	}

	if entry.Overlapping() || entry.TimeoutValue() != time.Hour {
		t.Fatalf("unexpected execution policy %+v", entry)
	}

	if entry.Spec().Expression() != "0 3 * * *" {
		t.Fatalf("unexpected spec %+v", entry.Spec())
	}

	want := time.Date(2025, 6, 2, 3, 0, 0, 0, time.UTC)

	if got := entry.Next(time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestEntryOverlappingByDefault protects the default policy that permits a new
// invocation while the previous invocation is still running.
func TestEntryOverlappingByDefault(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("a", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	})

	if err := entry.Err(); err != nil {
		t.Fatal(err)
	}

	if !entry.Overlapping() {
		t.Fatal("overlap should be allowed by default")
	}
}

// checkEntryInvalid fails the test unless entry reports an error matching want
// and has no next run.
func checkEntryInvalid(t *testing.T, entry schedule.Entry, want error) {
	t.Helper()

	if err := entry.Err(); !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}

	if !entry.Next(time.Now()).IsZero() {
		t.Error("next of invalid entry should be zero")
	}
}

// TestEntryInvalidEmptyName requires a declaration error and no next run when
// the caller omits the entry identity.
func TestEntryInvalidEmptyName(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidName)
}

// TestEntryInvalidUppercase checks that entry names follow the lowercase async
// naming contract rather than silently normalizing caller input.
func TestEntryInvalidUppercase(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("Report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidName)
}

// TestEntryInvalidUnderscore checks that an underscore cannot replace the
// hyphen separator permitted in entry names.
func TestEntryInvalidUnderscore(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("my_job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidName)
}

// TestEntryInvalidLeadingHyphen rejects a separator without a preceding name
// segment and checks that the invalid entry has no next run.
func TestEntryInvalidLeadingHyphen(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("-job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidName)
}

// TestEntryInvalidTrailingHyphen rejects an unfinished final name segment and
// checks that the invalid entry has no next run.
func TestEntryInvalidTrailingHyphen(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("job-", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidName)
}

// TestEntryInvalidSpace checks that names containing whitespace are rejected
// rather than trimmed or interpreted as multiple words.
func TestEntryInvalidSpace(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("my job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidName)
}

// TestEntryInvalidTooLong checks the first length beyond the 64-byte limit,
// keeping driver-visible identities within the shared naming contract.
func TestEntryInvalidTooLong(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry(strings.Repeat("a", 65), noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidName)
}

// TestEntryInvalidNilFunction verifies that a missing execution body remains a
// declaration error instead of a construction-time panic.
func TestEntryInvalidNilFunction(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("job", nil, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}), schedule.ErrInvalidEntry)
}

// TestEntryInvalidNoSpec checks that construction does not invent a timing
// policy when the caller supplies no entry callbacks.
func TestEntryInvalidNoSpec(t *testing.T) {
	t.Parallel()

	checkEntryInvalid(t, schedule.NewEntry("job", noop), schedule.ErrInvalidEntry)
}

// TestEntryInvalidZeroSpec checks that explicitly supplying the zero Spec does
// not satisfy the requirement for a valid timing declaration.
func TestEntryInvalidZeroSpec(t *testing.T) {
	t.Parallel()

	checkEntryInvalid(t, schedule.NewEntry("job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.Spec{})
	}), schedule.ErrInvalidEntry)
}

// TestEntryInvalidInvalidSpec requires the entry to retain an invalid timing
// declaration and remain unschedulable.
func TestEntryInvalidInvalidSpec(t *testing.T) {
	t.Parallel()

	checkEntryInvalid(t, schedule.NewEntry("job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(30))
	}), schedule.ErrInvalidEntry)
}

// TestEntryInvalidEmptySpec checks that selecting UTC alone does not define a
// runnable calendar schedule.
func TestEntryInvalidEmptySpec(t *testing.T) {
	t.Parallel()

	checkEntryInvalid(t, schedule.NewEntry("job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC())
	}), schedule.ErrInvalidEntry)
}

// TestEntryInvalidNegativeTimeout rejects negative execution limits while
// leaving zero available to mean an unlimited invocation.
func TestEntryInvalidNegativeTimeout(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)

	checkEntryInvalid(t, schedule.NewEntry("job", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
		entry.Timeout(-time.Second)
	}), schedule.ErrInvalidEntry)
}

// TestEntryValidNames exercises permitted name shapes and the exact length
// boundary, with the same successful validation expected for every input.
func TestEntryValidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"a", "0", "a-b", "a--b", "job-1", strings.Repeat("a", 64)} {
		entry := schedule.NewEntry(name, noop, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Hour(1))
		})

		if err := entry.Err(); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

// TestEntryKeepsFirstError protects declaration diagnostics: a later invalid
// schedule must not replace the earlier invalid-name error.
func TestEntryKeepsFirstError(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("Bad", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(30))
	})

	if err := entry.Err(); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("got %v, want the name error", err)
	}
}

// TestEntryRetainedDraftCannotChangePublishedEntry checks that later edits to a
// captured construction draft leave all published settings unchanged.
func TestEntryRetainedDraftCannotChangePublishedEntry(t *testing.T) {
	t.Parallel()

	var retained *schedule.EntryBuilder
	base := schedule.NewEntry("job", noop, func(entry *schedule.EntryBuilder) {
		retained = entry
		entry.When(schedule.UTC().Hour(1))
		entry.Description("original")
	})
	retained.When(schedule.UTC().Hour(2))
	retained.WithoutOverlapping()
	retained.Timeout(time.Minute)
	retained.Description("changed")

	if !base.Overlapping() || base.TimeoutValue() != 0 {
		t.Fatal("base entry changed")
	}

	if base.DescriptionValue() != "original" || base.Spec().Expression() != "0 1 * * *" {
		t.Fatal("published description or spec changed")
	}
}

// TestEntryCallbacksRunOnceInOrderOnOneDraft protects the construction boundary:
// each supplied callback edits the same draft before required-spec validation.
func TestEntryCallbacksRunOnceInOrderOnOneDraft(t *testing.T) {
	t.Parallel()

	var first *schedule.EntryBuilder
	var calls []int
	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		first = entry
		calls = append(calls, 1)
		entry.Description("nightly report")
	}, func(entry *schedule.EntryBuilder) {
		if entry != first {
			t.Fatal("callbacks received different drafts")
		}

		calls = append(calls, 2)
		entry.When(schedule.UTC().Hour(3))
	})

	if err := entry.Err(); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(calls, []int{1, 2}) || entry.DescriptionValue() != "nightly report" {
		t.Fatalf("callbacks = %v, description = %q", calls, entry.DescriptionValue())
	}
}

// TestEntryConstructorsUseFreshDrafts ensures a retained draft is never reused
// for another declaration, even when the same policy callback is supplied.
func TestEntryConstructorsUseFreshDrafts(t *testing.T) {
	t.Parallel()

	var drafts []*schedule.EntryBuilder
	policy := func(entry *schedule.EntryBuilder) {
		drafts = append(drafts, entry)
		entry.When(schedule.UTC().Hour(3))
	}

	first := schedule.NewEntry("first", noop, policy)
	second := schedule.NewEntry("second", noop, policy)

	if drafts[0] == drafts[1] || first.Name() == second.Name() {
		t.Fatal("constructors shared a draft")
	}
}

// TestEntrySettingsReturnSameDraft covers the mutable receiver contract without
// depending on chaining; published Entry values expose only read operations.
func TestEntrySettingsReturnSameDraft(t *testing.T) {
	t.Parallel()

	schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		if entry.When(schedule.UTC().Hour(3)) != entry {
			t.Fatal("When returned a different draft")
		}

		if entry.WithoutOverlapping() != entry {
			t.Fatal("WithoutOverlapping returned a different draft")
		}

		if entry.Timeout(time.Minute) != entry {
			t.Fatal("Timeout returned a different draft")
		}

		if entry.Description("report") != entry {
			t.Fatal("Description returned a different draft")
		}
	})
}

// TestEntrySettingsReturnSameInvalidDraft ensures settings never replace the
// draft even after an earlier invalid name has made the declaration unusable.
func TestEntrySettingsReturnSameInvalidDraft(t *testing.T) {
	t.Parallel()

	schedule.NewEntry("Bad", noop, func(entry *schedule.EntryBuilder) {
		if entry.When(schedule.UTC().Hour(3)) != entry {
			t.Fatal("When replaced invalid draft")
		}

		if entry.WithoutOverlapping() != entry {
			t.Fatal("WithoutOverlapping replaced invalid draft")
		}

		if entry.Timeout(time.Minute) != entry {
			t.Fatal("Timeout replaced invalid draft")
		}

		if entry.Description("report") != entry {
			t.Fatal("Description replaced invalid draft")
		}
	})
}

// TestEntryTimeoutDefaultIsUnlimited protects the declaration default without
// depending on timeout replacement or an invalid entry's read-only getters.
func TestEntryTimeoutDefaultIsUnlimited(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	})

	if err := entry.Err(); err != nil {
		t.Fatal(err)
	}

	if entry.TimeoutValue() != 0 {
		t.Fatalf("default timeout = %v", entry.TimeoutValue())
	}
}

// TestEntryNilCallbackPanics distinguishes construction misuse from nil handler
// domain validation. The constructor must not silently omit a supplied entry.
func TestEntryNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()
	schedule.NewEntry("report", noop, nil)
}

// TestEntryNilCallbackPanicsForInvalidDeclaration ensures early declaration
// errors do not suppress callback invocation or callback misuse panics.
func TestEntryNilCallbackPanicsForInvalidDeclaration(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("invalid declaration suppressed nil callback panic")
		}
	}()
	schedule.NewEntry("Bad", nil, nil)
}

// TestEntryCallbackPanicPropagates requires the original panic value to escape,
// rather than converting application-code panics into declaration errors.
func TestEntryCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "callback panic" {
			t.Fatalf("panic = %v", got)
		}
	}()
	schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		panic("callback panic")
	})
}

// TestEntryNilCallbackExpansionOmitsEdits treats an expanded nil slice as zero
// callbacks; the missing required schedule remains an ordinary domain error.
func TestEntryNilCallbackExpansionOmitsEdits(t *testing.T) {
	t.Parallel()

	var configure []func(*schedule.EntryBuilder)

	checkEntryInvalid(t, schedule.NewEntry("report", noop, configure...), schedule.ErrInvalidEntry)
}

// TestEntryEmptyCallbackExpansionOmitsEdits preserves the distinction between
// an empty callback list and a list containing one nil function.
func TestEntryEmptyCallbackExpansionOmitsEdits(t *testing.T) {
	t.Parallel()

	configure := []func(*schedule.EntryBuilder){}

	checkEntryInvalid(t, schedule.NewEntry("report", noop, configure...), schedule.ErrInvalidEntry)
}

// TestEntryEmptyCallbackDoesNotInferSpec checks final required validation even
// when the caller supplies a nonnil callback that makes no settings.
func TestEntryEmptyCallbackDoesNotInferSpec(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
	})

	checkEntryInvalid(t, entry, schedule.ErrInvalidEntry)
}

// TestEntryInvalidNameStillRunsCallbacks preserves first-error precedence while
// guaranteeing that construction policies are not conditionally skipped.
func TestEntryInvalidNameStillRunsCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	entry := schedule.NewEntry("Bad", noop, func(entry *schedule.EntryBuilder) {
		calls++
		entry.Timeout(-time.Second)
	}, func(entry *schedule.EntryBuilder) {
		calls++
		entry.Description("invalid name")
	})

	checkEntryInvalid(t, entry, schedule.ErrInvalidName)

	if calls != 2 || entry.DescriptionValue() != "invalid name" {
		t.Fatalf("callbacks = %d, description = %q", calls, entry.DescriptionValue())
	}
}

// TestEntryNilHandlerStillRunsCallbacks keeps nil execution functions as domain
// errors without invoking them or suppressing otherwise valid policy edits.
func TestEntryNilHandlerStillRunsCallbacks(t *testing.T) {
	t.Parallel()

	called := false
	entry := schedule.NewEntry("report", nil, func(entry *schedule.EntryBuilder) {
		called = true
		entry.When(schedule.UTC().Hour(3))
	})

	checkEntryInvalid(t, entry, schedule.ErrInvalidEntry)

	if !called || !strings.Contains(entry.Err().Error(), "nil function") {
		t.Fatalf("callback = %v, error = %v", called, entry.Err())
	}
}

// TestEntryConstructionDoesNotRunHandler verifies that policy callbacks and
// the required execution function have separate roles during declaration.
func TestEntryConstructionDoesNotRunHandler(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", func(context.Context) error {
		t.Fatal("construction invoked handler")

		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	})

	if err := entry.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestEntryLaterCallbackReplacesSpec permits common timing policy followed by
// a local override; missing-spec validation must occur after both callbacks.
func TestEntryLaterCallbackReplacesSpec(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(4))
	})

	if err := entry.Err(); err != nil {
		t.Fatal(err)
	}

	if entry.Spec().Expression() != "0 4 * * *" {
		t.Fatalf("spec = %q", entry.Spec().Expression())
	}
}

// TestEntryInvalidSpecStaysInvalidAcrossCallbacks protects immediate setting
// validation: later replacement cannot erase the first invalid timing input.
func TestEntryInvalidSpecStaysInvalidAcrossCallbacks(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(30))
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
		entry.Timeout(-time.Second)
	})

	checkEntryInvalid(t, entry, schedule.ErrInvalidEntry)

	if !strings.Contains(entry.Err().Error(), "hour") {
		t.Fatalf("first spec error lost: %v", entry.Err())
	}
}

// TestEntryNegativeTimeoutStaysInvalidAcrossCallbacks keeps the first timeout
// error even when another callback supplies a valid timeout and timing policy.
func TestEntryNegativeTimeoutStaysInvalidAcrossCallbacks(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.Timeout(-time.Second)
	}, func(entry *schedule.EntryBuilder) {
		entry.Timeout(time.Minute)
		entry.When(schedule.UTC().Hour(3))
	})

	checkEntryInvalid(t, entry, schedule.ErrInvalidEntry)

	if !strings.Contains(entry.Err().Error(), "negative timeout") {
		t.Fatalf("first timeout error lost: %v", entry.Err())
	}
}

// TestEntryZeroTimeoutClearsLimit checks explicit unlimited execution after a
// shared policy supplies a positive limit. Zero must not reapply a default.
func TestEntryZeroTimeoutClearsLimit(t *testing.T) {
	t.Parallel()

	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
		entry.Timeout(time.Minute)
	}, func(entry *schedule.EntryBuilder) {
		entry.Timeout(0)
	})

	if err := entry.Err(); err != nil {
		t.Fatal(err)
	}

	if entry.TimeoutValue() != 0 {
		t.Fatalf("timeout = %v", entry.TimeoutValue())
	}
}

// TestEntrySpecTransformationsRemainIndependent verifies that changing a local
// immutable Spec value cannot alter an entry that already captured it.
func TestEntrySpecTransformationsRemainIndependent(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(3)
	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	})

	spec = spec.Minute(30)

	if entry.Spec().Expression() != "0 3 * * *" || spec.Expression() != "30 3 * * *" {
		t.Fatalf("entry = %q, input = %q", entry.Spec().Expression(), spec.Expression())
	}
}

// TestEntrySpecFieldInputRemainsIndependent verifies caller-owned field slices
// are already captured by immutable Spec values before entry publication.
func TestEntrySpecFieldInputRemainsIndependent(t *testing.T) {
	t.Parallel()

	hours := []int{3, 4}
	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(hours...))
	})

	hours[0] = 12

	if entry.Spec().Expression() != "0 3,4 * * *" {
		t.Fatalf("entry captured mutable input: %q", entry.Spec().Expression())
	}
}

// TestEntryRetainedInvalidDraftCannotRepairPublishedEntry ensures final required
// validation is frozen even if a retained draft is edited after construction.
func TestEntryRetainedInvalidDraftCannotRepairPublishedEntry(t *testing.T) {
	t.Parallel()

	var retained *schedule.EntryBuilder
	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		retained = entry
	})
	retained.When(schedule.UTC().Hour(3))

	checkEntryInvalid(t, entry, schedule.ErrInvalidEntry)
}

// ExampleNewEntry checks declaration errors and observes the completed policy.
// The execution function is stored, not invoked by construction.
func ExampleNewEntry() {
	entry := schedule.NewEntry("report", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
		entry.Timeout(10 * time.Second)
	})

	if err := entry.Err(); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(entry.Name(), entry.Spec().Expression(), entry.TimeoutValue())
	// Output: report 0 3 * * * 10s
}
