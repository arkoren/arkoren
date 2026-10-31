package feature_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
)

// TestNamespaceLiteralNames verifies joining does not normalize provider keys.
func TestNamespaceLiteralNames(t *testing.T) {
	t.Parallel()

	names := feature.NewNamespace(" Checkout:/ ", Plan("free"))
	definition := names.Flag(" Enabled:/ ")

	if err := definition.Err(); err != nil {
		t.Fatal(err)
	}

	if names.Name() != " Checkout:/ " || names.Separator() != ":" {
		t.Fatalf("namespace = %q, %q", names.Name(), names.Separator())
	}

	if definition.Name() != " Checkout:/ : Enabled:/ " || definition.Fallback() != Plan("free") {
		t.Fatalf("flag = %q, %q", definition.Name(), definition.Fallback())
	}
}

// TestNamespaceEmptySeparator verifies omission and explicit empty differ.
func TestNamespaceEmptySeparator(t *testing.T) {
	t.Parallel()

	names := feature.NewNamespace("checkout", false, func(names *feature.NamespaceBuilder) {
		if names.Separator("") != names {
			t.Fatal("Separator returned another draft")
		}
	})

	if names.Derive().Subnamespace("Admin").Flag("Enabled").Name() != "checkoutAdminEnabled" {
		t.Fatal("explicit empty separator was replaced")
	}
}

// TestNamespaceChildSeparator verifies a child override affects future joins,
// not the join that created its prefix, including multibyte separators.
func TestNamespaceChildSeparator(t *testing.T) {
	t.Parallel()

	names := feature.NewNamespace("checkout", false, func(names *feature.NamespaceBuilder) {
		names.Separator("界")
	})

	child := names.Subnamespace("admin", func(names *feature.NamespaceBuilder) {
		names.Separator("/")
	})

	if child.Flag("enabled").Name() != "checkout界admin/enabled" {
		t.Fatalf("child = %q", child.Flag("enabled").Name())
	}

	if names.Subnamespace("public").Flag("enabled").Name() != "checkout界public界enabled" {
		t.Fatal("parent separator changed")
	}
}

// TestNamespaceCallbacksPublishDetachedDraft verifies ordered, synchronous
// callback execution on one draft and independent variants after publication.
func TestNamespaceCallbacksPublishDetachedDraft(t *testing.T) {
	t.Parallel()

	var retained *feature.NamespaceBuilder
	calls := 0
	names := feature.NewNamespace("checkout", 25,
		func(names *feature.NamespaceBuilder) {
			retained = names
			calls++
			names.Separator("/")
		},
		func(names *feature.NamespaceBuilder) {
			if retained != names || calls != 1 {
				t.Fatal("callbacks did not share an ordered draft")
			}

			calls++
			names.Separator(".")
		})
	retained.Separator("!")

	variant := names.Derive(func(names *feature.NamespaceBuilder) {
		retained = names
		calls++
		names.Separator("/")
	})
	retained.Separator("?")

	if calls != 3 || names.Flag("limit").Name() != "checkout.limit" {
		t.Fatalf("callbacks = %d, parent = %q", calls, names.Flag("limit").Name())
	}

	if variant.Flag("limit").Name() != "checkout/limit" {
		t.Fatalf("variant = %q", variant.Flag("limit").Name())
	}
}

// TestNamespaceInvalidCallbacksAndStickyErrors verifies invalid declarations
// still run every callback and cannot be repaired by joining nonempty children.
func TestNamespaceInvalidCallbacksAndStickyErrors(t *testing.T) {
	t.Parallel()

	calls := 0
	edit := func(names *feature.NamespaceBuilder) {
		calls++
		names.Separator("/")
	}

	names := feature.NewNamespace("", "fallback", edit)
	derived := names.Derive(edit)
	child := derived.Subnamespace("child", edit)

	if calls != 3 {
		t.Fatalf("callbacks = %d", calls)
	}

	for _, err := range []error{names.Err(), derived.Err(), child.Err(), child.Flag("leaf").Err()} {
		if !errors.Is(err, feature.ErrInvalidFlag) || !errors.Is(err, names.Err()) {
			t.Fatalf("inherited error = %v", err)
		}
	}

	if child.Flag("leaf").Name() != "/child/leaf" {
		t.Fatal("invalid descendants lost literal composition")
	}
}

// TestNamespaceEmptyChildrenRejected verifies empty child components cannot
// hide behind otherwise nonempty assembled provider names.
func TestNamespaceEmptyChildrenRejected(t *testing.T) {
	t.Parallel()

	names := feature.NewNamespace("checkout", false)
	child := names.Subnamespace("")

	for _, err := range []error{child.Err(), child.Derive().Err(), child.Flag("leaf").Err(), names.Flag("").Err()} {
		if !errors.Is(err, feature.ErrInvalidFlag) {
			t.Fatalf("empty child error = %v", err)
		}
	}

	if names.Err() != nil {
		t.Fatal("child error changed parent")
	}
}

// TestNamespaceZeroRemainsInvalid verifies derivation cannot make zero values
// valid, even when a descendant's name becomes nonempty.
func TestNamespaceZeroRemainsInvalid(t *testing.T) {
	t.Parallel()

	names := feature.Namespace[bool]{}

	for _, err := range []error{names.Err(), names.Derive().Err(), names.Subnamespace("child").Err(), names.Flag("leaf").Err()} {
		if !errors.Is(err, feature.ErrInvalidFlag) {
			t.Fatalf("zero namespace error = %v", err)
		}
	}
}

// TestNamespaceNilCallbackPanics verifies nil entries remain programmer misuse.
func TestNamespaceNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	feature.NewNamespace("", false, nil)
}

// TestNamespaceExpandedNilOmitsEdits verifies aliases remain ordinary function
// types and expanding an absent callback slice does not reset inherited joins.
func TestNamespaceExpandedNilOmitsEdits(t *testing.T) {
	t.Parallel()

	var configure []func(*feature.NamespaceBuilder)
	names := feature.NewNamespace("checkout", false, configure...)

	if names.Derive(configure...).Subnamespace("admin", configure...).Flag("enabled").Name() != "checkout:admin:enabled" {
		t.Fatal("expanded nil callbacks changed joins")
	}
}

// TestNamespaceFallbackRetainsNamedInteger verifies all descendant definitions
// inherit an exact named fallback, including integers above float64 precision.
func TestNamespaceFallbackRetainsNamedInteger(t *testing.T) {
	t.Parallel()

	// limit represents an application-owned exact integer quantity.
	type limit int64
	const fallback limit = 9007199254740993
	names := feature.NewNamespace("uploads", fallback)
	child := names.Derive().Subnamespace("admin")

	if names.Fallback() != fallback || child.Fallback() != fallback {
		t.Fatal("namespace fallback changed")
	}

	for _, definition := range []feature.Flag[limit]{names.Flag("bytes"), child.Flag("bytes")} {
		if definition.Fallback() != fallback {
			t.Fatalf("fallback = %d", definition.Fallback())
		}
	}
}

// TestNamespaceMemorySetupPreservesNamedInteger verifies namespaced leaves
// retain large exact integers in both default and per-subject memory setup.
func TestNamespaceMemorySetupPreservesNamedInteger(t *testing.T) {
	t.Parallel()

	// limit represents an application-owned exact integer quantity.
	type limit int64
	const fallback limit = 9007199254740993
	definition := feature.NewNamespace("uploads", fallback).Subnamespace("admin").Flag("bytes")
	driver := memoryfeature.New()
	const stored limit = fallback + 2
	const targeted limit = fallback + 4

	if err := driver.Set(definition, stored); err != nil {
		t.Fatal(err)
	}

	if err := driver.SetFor(definition, "u1", targeted); err != nil {
		t.Fatal(err)
	}

	flags := feature.New(driver)

	value, err := flags.Value(t.Context(), definition, feature.Subject{})

	if err != nil || value != stored {
		t.Fatalf("stored = %d, %v", value, err)
	}

	value, err = flags.Value(t.Context(), definition, feature.Subject{Key: "u1"})

	if err != nil || value != targeted {
		t.Fatalf("targeted = %d, %v", value, err)
	}
}

// TestNamespaceInvalidLeafSkipsDriver verifies definition errors precede
// subject validation and cancellation, and evaluation preserves the fallback.
func TestNamespaceInvalidLeafSkipsDriver(t *testing.T) {
	t.Parallel()

	driver := &stub{}
	definition := feature.NewNamespace("", Plan("free")).Flag("plan")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	subject := feature.Subject{Attributes: map[string]any{"bad": struct{}{}}}

	detail, err := feature.New(driver).ValueDetail(ctx, definition, subject)

	if !errors.Is(err, feature.ErrInvalidFlag) || detail.Value != Plan("free") || detail.Reason != feature.ReasonError {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}

	value, err := feature.New(driver).Value(ctx, definition, subject)

	if !errors.Is(err, feature.ErrInvalidFlag) || value != Plan("free") {
		t.Fatalf("Value = %q, %v", value, err)
	}

	if driver.calls.Load() != 0 {
		t.Fatalf("driver calls = %d", driver.calls.Load())
	}
}

// TestNamespaceInvalidLeafDoesNotMutateMemory verifies both setup operations
// reject inherited errors before creating the assembled nonempty provider key.
func TestNamespaceInvalidLeafDoesNotMutateMemory(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewNamespace("", false).Flag("enabled")

	if err := driver.Set(definition, true); !errors.Is(err, feature.ErrInvalidFlag) {
		t.Fatalf("Set = %v", err)
	}

	if err := driver.SetFor(definition, "u1", true); !errors.Is(err, feature.ErrInvalidFlag) {
		t.Fatalf("SetFor = %v", err)
	}

	_, err := driver.Evaluate(t.Context(), feature.Request{Key: definition.Name(), Kind: feature.KindBool})

	if !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("Evaluate = %v", err)
	}
}

// TestNamespaceMissingFlagUsesFallback verifies namespacing does not register
// provider state and a missing flag retains the exact named fallback and reason.
func TestNamespaceMissingFlagUsesFallback(t *testing.T) {
	t.Parallel()

	definition := feature.NewNamespace("checkout", Plan("free")).Flag("plan")

	detail, err := feature.New(memoryfeature.New()).ValueDetail(t.Context(), definition, feature.Subject{})

	if !errors.Is(err, feature.ErrNotFound) || detail.Value != Plan("free") || detail.Reason != feature.ReasonError {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}
}

// TestNamespaceDisabledFlagUsesFallback verifies a disabled provider result
// remains successful and carries its disabled reason instead of ReasonError.
func TestNamespaceDisabledFlagUsesFallback(t *testing.T) {
	t.Parallel()

	definition := feature.NewNamespace("checkout", Plan("free")).Flag("plan")
	driver := memoryfeature.New()

	if err := driver.Set(definition, Plan("pro")); err != nil {
		t.Fatal(err)
	}

	driver.Disable(definition.Name())

	detail, err := feature.New(driver).ValueDetail(t.Context(), definition, feature.Subject{})

	if err != nil || detail.Value != Plan("free") || detail.Reason != feature.ReasonDisabled {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}
}

// TestNamespaceOverrideOnlyUsesFallback verifies unmatched subjects obtain
// the inherited fallback rather than the targeted value or a missing-flag error.
func TestNamespaceOverrideOnlyUsesFallback(t *testing.T) {
	t.Parallel()

	definition := feature.NewNamespace("checkout", Plan("free")).Subnamespace("admin").Flag("plan")
	driver := memoryfeature.New()

	if err := driver.SetFor(definition, "u1", Plan("pro")); err != nil {
		t.Fatal(err)
	}

	detail, err := feature.New(driver).ValueDetail(t.Context(), definition, feature.Subject{Key: "u2"})

	if err != nil || detail.Value != Plan("free") || detail.Reason != feature.ReasonDefault {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}
}

// TestNamespaceProviderConversionFailureUsesFallback verifies provider schemas
// remain runtime constraints even when the declaration fixes an exact Go type.
func TestNamespaceProviderConversionFailureUsesFallback(t *testing.T) {
	t.Parallel()

	// seats is intentionally narrower than the provider's integer value.
	type seats int8
	definition := feature.NewNamespace("checkout", seats(7)).Flag("limit")
	driver := &stub{result: feature.Result{Value: int64(300), Reason: feature.ReasonStatic}}

	detail, err := feature.New(driver).ValueDetail(t.Context(), definition, feature.Subject{})

	if !errors.Is(err, feature.ErrTypeMismatch) || detail.Value != seats(7) || detail.Reason != feature.ReasonError {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}
}

// TestNamespaceCanceledEvaluationUsesFallback verifies valid definitions still
// reach the provider's cancellation boundary and retain the declared fallback.
func TestNamespaceCanceledEvaluationUsesFallback(t *testing.T) {
	t.Parallel()

	definition := feature.NewNamespace("checkout", Plan("free")).Flag("plan")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	detail, err := feature.New(memoryfeature.New()).ValueDetail(ctx, definition, feature.Subject{})

	if !errors.Is(err, context.Canceled) || detail.Value != Plan("free") || detail.Reason != feature.ReasonError {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}
}

// TestNamespaceInvalidSubjectSkipsProvider verifies valid namespace leaves
// retain subject-validation precedence over a canceled provider operation.
func TestNamespaceInvalidSubjectSkipsProvider(t *testing.T) {
	t.Parallel()

	driver := &stub{}
	definition := feature.NewNamespace("checkout", false).Flag("enabled")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	subject := feature.Subject{Attributes: map[string]any{"bad": struct{}{}}}

	detail, err := feature.New(driver).ValueDetail(ctx, definition, subject)

	if !errors.Is(err, feature.ErrInvalidSubject) || detail.Value || detail.Reason != feature.ReasonError {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}

	if driver.calls.Load() != 0 {
		t.Fatalf("driver calls = %d", driver.calls.Load())
	}
}

// TestNamespaceFormattingOmitsFallback verifies diagnostic formats and slog
// never reflect the potentially sensitive inherited fallback.
func TestNamespaceFormattingOmitsFallback(t *testing.T) {
	t.Parallel()

	names := feature.NewNamespace("checkout", "private-fallback")
	var output bytes.Buffer
	slog.New(slog.NewJSONHandler(&output, nil)).InfoContext(t.Context(), "configured", "namespace", names)

	for _, text := range []string{
		fmt.Sprint(names), fmt.Sprintf("%+v", names), fmt.Sprintf("%#v", names),
		fmt.Sprintf("%d", names), fmt.Sprintf("%x", names), output.String(),
	} {
		if strings.Contains(text, "private-fallback") || strings.Contains(text, "checkout") {
			t.Fatalf("unsafe formatting = %s", text)
		}
	}
}

// TestNamespaceRetypingRejected verifies namespace and leaf types cannot be
// independently reassigned or converted and unsupported fallbacks fail to build.
func TestNamespaceRetypingRejected(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "-gcflags=-e", "./testdata/namespace")

	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("namespace retyping compiled")
	}

	for _, diagnostic := range []string{
		"cannot convert feature.NewNamespace",
		"cannot use feature.NewNamespace",
		"cannot convert names.Flag",
		"cannot use names.Flag",
		"does not satisfy feature.Primitive",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Fatalf("missing %q diagnostic: %s", diagnostic, output)
		}
	}
}
