package openfeaturefeature_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/featuretest"
	"arkoren.dev/driver/openfeature/openfeaturefeature"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/isolated"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
)

// TestConformance runs the shared feature flag suite against the SDK's
// in-memory provider. It needs no services, so it also runs under -short.
func TestConformance(t *testing.T) {
	t.Parallel()

	featuretest.Run(t, newHarness)
}

// swappable is a FeatureProvider that delegates to an in-memory provider that
// can be replaced at any time. The SDK's memprovider reads a flag map handed
// to it at construction without locking and has no update API, so changing
// flags after registration means building a new provider over a copy of the
// map and swapping it in atomically.
type swappable struct {
	// current is the provider evaluations are delegated to.
	current atomic.Pointer[memprovider.InMemoryProvider]
}

// Metadata identifies the suite provider independently of the current flag map.
// Its fixed name lets SDK diagnostics distinguish it from global providers.
func (swappable *swappable) Metadata() openfeature.Metadata {
	return openfeature.Metadata{
		Name: "swappable-in-memory",
	}
}

// Hooks returns nil so the fixture adds no evaluation side effects. The driver
// is tested through the provider result rather than SDK hook behavior.
func (swappable *swappable) Hooks() []openfeature.Hook {
	return nil
}

// BooleanEvaluation reads the current immutable provider snapshot. Defaults
// and flattened subject attributes pass through unchanged to the SDK provider.
func (swappable *swappable) BooleanEvaluation(ctx context.Context, flag string, fallback bool, flat openfeature.FlattenedContext) openfeature.BoolResolutionDetail {
	return swappable.current.Load().BooleanEvaluation(ctx, flag, fallback, flat)
}

// StringEvaluation delegates to the current flag snapshot, preserving the
// SDK's default handling and resolution details for the conformance suite.
func (swappable *swappable) StringEvaluation(ctx context.Context, flag string, fallback string, flat openfeature.FlattenedContext) openfeature.StringResolutionDetail {
	return swappable.current.Load().StringEvaluation(ctx, flag, fallback, flat)
}

// FloatEvaluation delegates numeric reads to the atomically published provider.
// Replacing fixtures does not require replacing the suite's driver handle.
func (swappable *swappable) FloatEvaluation(ctx context.Context, flag string, fallback float64, flat openfeature.FlattenedContext) openfeature.FloatResolutionDetail {
	return swappable.current.Load().FloatEvaluation(ctx, flag, fallback, flat)
}

// IntEvaluation uses the current provider's integer conversion and details.
// It does not normalize the default or subject context before delegation.
func (swappable *swappable) IntEvaluation(ctx context.Context, flag string, fallback int64, flat openfeature.FlattenedContext) openfeature.IntResolutionDetail {
	return swappable.current.Load().IntEvaluation(ctx, flag, fallback, flat)
}

// ObjectEvaluation preserves arbitrary SDK object values when delegating.
// The provider snapshot owns the fixture map, not arbitrary nested values.
func (swappable *swappable) ObjectEvaluation(ctx context.Context, flag string, fallback any, flat openfeature.FlattenedContext) openfeature.InterfaceResolutionDetail {
	return swappable.current.Load().ObjectEvaluation(ctx, flag, fallback, flat)
}

// harness implements featuretest.Harness over a swappable provider.
type harness struct {
	// driver is the started driver under test.
	driver *openfeaturefeature.Driver

	// provider receives every flag change.
	provider *swappable

	// mu guards flags.
	mu sync.Mutex

	// flags is the current set of flag definitions.
	flags map[string]memprovider.InMemoryFlag
}

// newHarness starts a driver over an empty swappable provider and stops it
// when the test ends.
func newHarness(t *testing.T) featuretest.Harness {
	t.Helper()

	harness := &harness{
		provider: &swappable{},
		flags:    map[string]memprovider.InMemoryFlag{},
	}
	harness.publish()
	harness.driver = openfeaturefeature.New(harness.provider)

	if err := harness.driver.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	t.Cleanup(func() {
		if err := harness.driver.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})

	return harness
}

// Driver returns the started handle shared by the suite's flag operations.
// Provider snapshots can change without replacing this driver.
func (harness *harness) Driver() feature.Driver {
	return harness.driver
}

// Set installs flag. Overrides are a context evaluator that matches the
// targeting key; the default value is the flag's only variant. A disabled
// flag keeps its definition and uses the provider's DISABLED state.
func (harness *harness) Set(_ *testing.T, flag featuretest.Flag) {
	state := memprovider.Enabled

	if flag.Disabled {
		state = memprovider.Disabled
	}

	memFlag := memprovider.InMemoryFlag{
		Key:            flag.Key,
		State:          state,
		DefaultVariant: "default",
		Variants:       map[string]any{"default": flag.Value},
	}

	if len(flag.Overrides) > 0 {
		overrides := maps.Clone(flag.Overrides)
		evaluate := func(definition memprovider.InMemoryFlag, flat openfeature.FlattenedContext) (any, openfeature.ProviderResolutionDetail) {
			if key, _ := flat[openfeature.TargetingKey].(string); key != "" {
				if value, ok := overrides[key]; ok {
					return value, openfeature.ProviderResolutionDetail{
						Reason:  openfeature.TargetingMatchReason,
						Variant: "override",
					}
				}
			}

			return definition.Variants["default"], openfeature.ProviderResolutionDetail{
				Reason:  openfeature.StaticReason,
				Variant: "default",
			}
		}

		memFlag.ContextEvaluator = &evaluate
	}

	harness.mu.Lock()
	defer harness.mu.Unlock()

	harness.flags[flag.Key] = memFlag
	harness.publish()
}

// Delete publishes a snapshot without key. Deleting an absent fixture is safe
// and preserves all remaining flag definitions and targeting overrides.
func (harness *harness) Delete(_ *testing.T, key string) {
	harness.mu.Lock()
	defer harness.mu.Unlock()

	delete(harness.flags, key)
	harness.publish()
}

// publish swaps in a provider over a copy of the current flags. Callers hold
// harness.mu, except the constructor.
func (harness *harness) publish() {
	provider := memprovider.NewInMemoryProvider(maps.Clone(harness.flags))
	harness.provider.current.Store(provider)
}

// acquiringProvider exercises a legitimate native initialization that acquires
// its resources only after the test releases an application-controlled barrier.
type acquiringProvider struct {
	// stub supplies ordinary local evaluation and shutdown implementations.
	stub

	// entered signals that initialization has reached the acquisition boundary.
	entered chan struct{}

	// release allows initialization to complete without a real-time wait.
	release chan struct{}
}

// Init holds native initialization at a deterministic boundary. It performs no
// I/O and completes only when the application-controlled release channel closes.
func (acquiringProvider *acquiringProvider) Init(openfeature.EvaluationContext) error {
	close(acquiringProvider.entered)
	<-acquiringProvider.release

	return nil
}

// TestStopWaitsForProviderAcquisition verifies owned shutdown cannot return
// while native initialization is still able to acquire resources afterwards.
func TestStopWaitsForProviderAcquisition(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		provider := &acquiringProvider{entered: make(chan struct{}), release: make(chan struct{})}
		driver := openfeaturefeature.New(provider)
		started, stopped := make(chan error, 1), make(chan error, 1)

		go func() {
			started <- driver.Start(context.Background())
		}()
		<-provider.entered

		go func() {
			stopped <- driver.Stop(context.Background())
		}()
		synctest.Wait()

		select {
		case err := <-stopped:
			t.Errorf("Stop returned before native initialization finished: %v", err)
			stopped <- err
		default:
		}

		close(provider.release)

		if err := <-started; err != nil {
			t.Fatal(err)
		}

		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
	})
}

// TestStopBeforeStartIsTerminal verifies the documented terminal transition
// through public lifecycle calls without inspecting driver state or providers.
func TestStopBeforeStartIsTerminal(t *testing.T) {
	t.Parallel()

	driver := openfeaturefeature.New(&stub{})

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err == nil {
		t.Fatal("Start succeeded after terminal Stop")
	}
}

// TestInvalidKindsPrecedeReadiness rejects empty and unknown kinds before an
// unstarted provider can hide malformed raw requests behind ErrNotReady.
func TestInvalidKindsPrecedeReadiness(t *testing.T) {
	t.Parallel()

	driver := openfeaturefeature.New(&stub{})

	for _, kind := range []feature.Kind{"", "future"} {
		_, err := driver.Evaluate(t.Context(), feature.Request{Key: "missing", Kind: kind})

		if !errors.Is(err, feature.ErrTypeMismatch) {
			t.Fatalf("kind %q error=%v; want ErrTypeMismatch", kind, err)
		}
	}
}

// TestInvalidKindsPrecedeDisabledFlag exercises a ready native disabled provider;
// no invalid kind is permitted to return the ordinary no-value disabled result.
func TestInvalidKindsPrecedeDisabledFlag(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{detail: openfeature.ProviderResolutionDetail{Reason: openfeature.DisabledReason}})

	for _, kind := range []feature.Kind{"", "future"} {
		_, err := driver.Evaluate(t.Context(), feature.Request{Key: "off", Kind: kind})

		if !errors.Is(err, feature.ErrTypeMismatch) {
			t.Fatalf("kind %q error=%v; want ErrTypeMismatch", kind, err)
		}
	}
}

// TestCancellationPrecedesInvalidKind protects context-first validation without
// initializing the owned native provider or requiring any shared SDK registry.
func TestCancellationPrecedesInvalidKind(t *testing.T) {
	t.Parallel()

	driver := openfeaturefeature.New(&stub{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.Evaluate(ctx, feature.Request{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate error=%v; want context.Canceled", err)
	}
}

// stub is a provider that resolves every flag to a fixed detail and records
// what it was asked and how its lifecycle was driven.
type stub struct {
	// detail is returned for every evaluation.
	detail openfeature.ProviderResolutionDetail

	// initErr is returned by Init.
	initErr error

	// calls counts evaluations.
	calls atomic.Int32

	// inits and shutdowns count lifecycle calls.
	inits, shutdowns atomic.Int32

	// last is the flattened context of the latest evaluation.
	last atomic.Pointer[openfeature.FlattenedContext]
}

// Metadata gives the fixture a recognizable name. Tests use it to detect
// accidental registration in the SDK's global provider registry.
func (stub *stub) Metadata() openfeature.Metadata {
	return openfeature.Metadata{
		Name: "stub",
	}
}

// Hooks returns nil so evaluation tests observe only the configured resolution.
// No SDK hook changes the subject or result behind the driver's back.
func (stub *stub) Hooks() []openfeature.Hook {
	return nil
}

// Init records an initialization attempt and returns the fixture's error.
// This lets lifecycle tests distinguish construction from provider startup.
func (stub *stub) Init(openfeature.EvaluationContext) error {
	stub.inits.Add(1)

	return stub.initErr
}

// Shutdown records provider release without changing evaluation fixtures.
// Ownership tests use the count to distinguish owned and borrowed providers.
func (stub *stub) Shutdown() {
	stub.shutdowns.Add(1)
}

// record notes an evaluation and returns the configured detail.
func (stub *stub) record(flat openfeature.FlattenedContext) openfeature.ProviderResolutionDetail {
	stub.calls.Add(1)
	stub.last.Store(&flat)

	return stub.detail
}

// BooleanEvaluation records the received context and returns true with the
// configured detail, separating value conversion from reason/error mapping.
func (stub *stub) BooleanEvaluation(_ context.Context, _ string, _ bool, flat openfeature.FlattenedContext) openfeature.BoolResolutionDetail {
	return openfeature.BoolResolutionDetail{
		Value:                    true,
		ProviderResolutionDetail: stub.record(flat),
	}
}

// StringEvaluation returns a fixed string with the configured resolution detail.
// Recording the context lets tests inspect subject conversion independently.
func (stub *stub) StringEvaluation(_ context.Context, _ string, _ string, flat openfeature.FlattenedContext) openfeature.StringResolutionDetail {
	return openfeature.StringResolutionDetail{
		Value:                    "text",
		ProviderResolutionDetail: stub.record(flat),
	}
}

// FloatEvaluation returns a fractional value so tests can distinguish float
// resolution from integer resolution while sharing the configured detail.
func (stub *stub) FloatEvaluation(_ context.Context, _ string, _ float64, flat openfeature.FlattenedContext) openfeature.FloatResolutionDetail {
	return openfeature.FloatResolutionDetail{
		Value:                    2.5,
		ProviderResolutionDetail: stub.record(flat),
	}
}

// IntEvaluation supplies an integer fixture with the configured reason or error.
// The recorded context is the one the SDK passed to the provider.
func (stub *stub) IntEvaluation(_ context.Context, _ string, _ int64, flat openfeature.FlattenedContext) openfeature.IntResolutionDetail {
	return openfeature.IntResolutionDetail{
		Value:                    7,
		ProviderResolutionDetail: stub.record(flat),
	}
}

// ObjectEvaluation echoes the supplied default and records the evaluation.
// It satisfies the native provider contract without inventing object fixtures.
func (stub *stub) ObjectEvaluation(_ context.Context, _ string, fallback any, flat openfeature.FlattenedContext) openfeature.InterfaceResolutionDetail {
	return openfeature.InterfaceResolutionDetail{
		Value:                    fallback,
		ProviderResolutionDetail: stub.record(flat),
	}
}

// started returns a driver over provider that is already started.
func started(t *testing.T, provider *stub, configure ...func(*openfeaturefeature.Builder)) *openfeaturefeature.Driver {
	t.Helper()

	driver := openfeaturefeature.New(provider, configure...)

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	return driver
}

// evaluate evaluates a bool flag for an anonymous subject.
func evaluate(driver *openfeaturefeature.Driver) (feature.Result, error) {
	return driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindBool,
	})
}

// wantError fails the test unless a provider resolution with err's code makes
// Evaluate fail with an error wrapping want.
func wantError(t *testing.T, resolution openfeature.ResolutionError, want error) {
	t.Helper()

	driver := started(t, &stub{
		detail: openfeature.ProviderResolutionDetail{
			ResolutionError: resolution,
			Reason:          openfeature.ErrorReason,
		},
	})

	if _, err := evaluate(driver); !errors.Is(err, want) {
		t.Errorf("Evaluate() error = %v, want %v", err, want)
	}
}

// wantReason fails the test unless a provider reason maps to want.
func wantReason(t *testing.T, reason openfeature.Reason, want feature.Reason) {
	t.Helper()

	driver := started(t, &stub{
		detail: openfeature.ProviderResolutionDetail{
			Reason: reason,
		},
	})

	result, err := evaluate(driver)

	if err != nil || result.Reason != want {
		t.Errorf("Evaluate() = %#v, %v, want reason %s", result, err, want)
	}
}

// TestErrorFlagNotFound checks that an SDK missing-flag resolution matches
// the contract sentinel rather than leaking the native error alone.
func TestErrorFlagNotFound(t *testing.T) {
	t.Parallel()

	wantError(t, openfeature.NewFlagNotFoundResolutionError("x"), feature.ErrNotFound)
}

// TestErrorTypeMismatch protects the contract mapping for a provider value of
// the wrong kind, allowing callers to use errors.Is across providers.
func TestErrorTypeMismatch(t *testing.T) {
	t.Parallel()

	wantError(t, openfeature.NewTypeMismatchResolutionError("x"), feature.ErrTypeMismatch)
}

// TestErrorParseError checks that malformed provider values share the contract's
// type-mismatch category instead of becoming generic backend failures.
func TestErrorParseError(t *testing.T) {
	t.Parallel()

	wantError(t, openfeature.NewParseErrorResolutionError("x"), feature.ErrTypeMismatch)
}

// TestErrorProviderNotReady maps the SDK's startup state to ErrNotReady.
// Callers can distinguish provider availability from missing flag definitions.
func TestErrorProviderNotReady(t *testing.T) {
	t.Parallel()

	wantError(t, openfeature.NewProviderNotReadyResolutionError("x"), feature.ErrNotReady)
}

// TestErrorTargetingKeyMissing classifies a provider's required identity failure
// as an invalid subject, not an unavailable flag backend.
func TestErrorTargetingKeyMissing(t *testing.T) {
	t.Parallel()

	wantError(t, openfeature.NewTargetingKeyMissingResolutionError("x"), feature.ErrInvalidSubject)
}

// TestErrorInvalidContext verifies that rejected evaluation attributes match
// ErrInvalidSubject so callers can correct the supplied subject.
func TestErrorInvalidContext(t *testing.T) {
	t.Parallel()

	wantError(t, openfeature.NewInvalidContextResolutionError("x"), feature.ErrInvalidSubject)
}

// TestErrorGeneralNamesTheCode retains the native code and message for errors
// without a contract category, without matching unrelated sentinels.
func TestErrorGeneralNamesTheCode(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{
		detail: openfeature.ProviderResolutionDetail{
			ResolutionError: openfeature.NewGeneralResolutionError("boom"),
			Reason:          openfeature.ErrorReason,
		},
	})

	_, err := evaluate(driver)

	if err == nil {
		t.Fatal("Evaluate() error = nil, want an error")
	}

	for _, sentinel := range []error{feature.ErrNotFound, feature.ErrTypeMismatch, feature.ErrNotReady, feature.ErrInvalidSubject} {
		if errors.Is(err, sentinel) {
			t.Errorf("Evaluate() error matches %v, want a plain error", sentinel)
		}
	}

	if got := err.Error(); !strings.Contains(got, "GENERAL") || !strings.Contains(got, "boom") {
		t.Errorf("Evaluate() error = %q, want the code and message", got)
	}
}

// TestReasonStatic preserves a provider's constant-value explanation.
// This distinguishes untargeted flags from fallback/default resolutions.
func TestReasonStatic(t *testing.T) {
	t.Parallel()

	wantReason(t, openfeature.StaticReason, feature.ReasonStatic)
}

// TestReasonDefault preserves the provider's default-resolution explanation.
// The driver must not relabel a successful fallback as an error.
func TestReasonDefault(t *testing.T) {
	t.Parallel()

	wantReason(t, openfeature.DefaultReason, feature.ReasonDefault)
}

// TestReasonTargetingMatch retains the explanation for a subject-specific rule.
// Consumers can distinguish targeting from static or split allocation.
func TestReasonTargetingMatch(t *testing.T) {
	t.Parallel()

	wantReason(t, openfeature.TargetingMatchReason, feature.ReasonTargetingMatch)
}

// TestReasonSplit checks the contract reason for percentage allocation.
// It must remain distinct from a deterministic targeting match.
func TestReasonSplit(t *testing.T) {
	t.Parallel()

	wantReason(t, openfeature.SplitReason, feature.ReasonSplit)
}

// TestReasonCached preserves cache provenance reported by the SDK provider.
// A cached resolution is still a successful evaluation.
func TestReasonCached(t *testing.T) {
	t.Parallel()

	wantReason(t, openfeature.CachedReason, feature.ReasonCached)
}

// TestReasonStaleIsCached checks compatibility with providers reporting STALE.
// The contract represents this cached provenance with ReasonCached.
func TestReasonStaleIsCached(t *testing.T) {
	t.Parallel()

	wantReason(t, "STALE", feature.ReasonCached)
}

// TestReasonUnknown keeps explicitly unknown SDK provenance unknown.
// The driver must not infer a targeting decision from an absent explanation.
func TestReasonUnknown(t *testing.T) {
	t.Parallel()

	wantReason(t, openfeature.UnknownReason, feature.ReasonUnknown)
}

// TestReasonUnrecognizedIsUnknown accepts future provider reason strings.
// Unrecognized explanations degrade to the contract's unknown reason.
func TestReasonUnrecognizedIsUnknown(t *testing.T) {
	t.Parallel()

	wantReason(t, "SOMETHING_NEW", feature.ReasonUnknown)
}

// TestReasonEmptyIsUnknown covers providers that omit an explanation.
// An empty reason must not accidentally imply a static or default decision.
func TestReasonEmptyIsUnknown(t *testing.T) {
	t.Parallel()

	wantReason(t, "", feature.ReasonUnknown)
}

// TestReasonDisabledReturnsNoValue checks that disabled resolutions discard the
// provider's placeholder value while retaining the disabled explanation.
func TestReasonDisabledReturnsNoValue(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{
		detail: openfeature.ProviderResolutionDetail{
			Reason: openfeature.DisabledReason,
		},
	})

	result, err := evaluate(driver)

	if err != nil || result.Value != nil || result.Reason != feature.ReasonDisabled {
		t.Errorf("Evaluate() = %#v, %v, want nil value, disabled", result, err)
	}
}

// TestReasonDefaultKeepsValueAndVariant ensures default provenance does not
// discard a successful provider value or its variant identifier.
func TestReasonDefaultKeepsValueAndVariant(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{
		detail: openfeature.ProviderResolutionDetail{
			Reason:  openfeature.DefaultReason,
			Variant: "off",
		},
	})

	result, err := evaluate(driver)

	if err != nil || result.Value != true || result.Variant != "off" {
		t.Errorf("Evaluate() = %#v, %v, want value true, variant off", result, err)
	}
}

// TestEvaluateKinds checks boolean dispatch uses the provider's scalar value.
// The other scalar evaluators have separate fixtures to isolate their paths.
func TestEvaluateKinds(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{})

	result, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindBool,
	})

	if err != nil || result.Value != true {
		t.Errorf("Evaluate(%s) = %#v, %v, want %#v", feature.KindBool, result, err, true)
	}
}

// TestEvaluateStringKind checks native string dispatch without converting the
// provider value into another scalar representation.
func TestEvaluateStringKind(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{})

	result, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindString,
	})

	if err != nil || result.Value != "text" {
		t.Errorf("Evaluate(%s) = %#v, %v, want %#v", feature.KindString, result, err, "text")
	}
}

// TestEvaluateIntKind retains the provider's int64 representation. Integer
// dispatch must not route through the floating-point evaluator.
func TestEvaluateIntKind(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{})

	result, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindInt,
	})

	if err != nil || result.Value != int64(7) {
		t.Errorf("Evaluate(%s) = %#v, %v, want %#v", feature.KindInt, result, err, int64(7))
	}
}

// TestEvaluateFloatKind retains fractional provider values. A float request
// must not truncate its value or use the integer evaluator.
func TestEvaluateFloatKind(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{})

	result, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindFloat,
	})

	if err != nil || result.Value != 2.5 {
		t.Errorf("Evaluate(%s) = %#v, %v, want %#v", feature.KindFloat, result, err, 2.5)
	}
}

// TestEvaluateUnknownKindFails rejects kinds outside the scalar contract.
// The failure matches ErrTypeMismatch instead of guessing an SDK evaluator.
func TestEvaluateUnknownKindFails(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{})

	_, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.Kind("unknown"),
	})

	if !errors.Is(err, feature.ErrTypeMismatch) {
		t.Errorf("Evaluate() error = %v, want ErrTypeMismatch", err)
	}
}

// TestEvaluateConvertsSubjectToContext checks targeting-key translation and
// preservation of string, integer, slice, and timestamp attribute values.
func TestEvaluateConvertsSubjectToContext(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := started(t, provider)
	at := time.Unix(1_700_000_000, 0).UTC()
	attributes := map[string]any{"plan": "pro", "seats": int64(5), "tags": []string{"a", "b"}, "since": at}

	_, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindBool,
		Subject: feature.Subject{
			Key:        "u1",
			Attributes: attributes,
		},
	})

	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	flat := *provider.last.Load()

	if flat[openfeature.TargetingKey] != "u1" || flat["plan"] != "pro" || flat["seats"] != int64(5) {
		t.Errorf("context = %#v, want targeting key u1 and the attributes", flat)
	}

	if tags, _ := flat["tags"].([]string); !slices.Equal(tags, []string{"a", "b"}) {
		t.Errorf("tags = %#v, want [a b]", flat["tags"])
	}

	if since, _ := flat["since"].(time.Time); !since.Equal(at) {
		t.Errorf("since = %#v, want %v", flat["since"], at)
	}
}

// TestEvaluateAnonymousSubjectHasNoTargetingKey omits the SDK identity field for
// an empty subject key, rather than publishing an empty targeting identity.
func TestEvaluateAnonymousSubjectHasNoTargetingKey(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := started(t, provider)

	if _, err := evaluate(driver); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if _, ok := (*provider.last.Load())[openfeature.TargetingKey]; ok {
		t.Error("anonymous subject set a targeting key")
	}
}

// TestEvaluateDoesNotModifyAttributes protects caller ownership when ambient
// evaluation attributes are merged with the request's subject attributes.
func TestEvaluateDoesNotModifyAttributes(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{}, func(features *openfeaturefeature.Builder) {
		features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(map[string]any{"env": "prod"}))
	})
	attributes := map[string]any{"plan": "pro"}

	_, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindBool,
		Subject: feature.Subject{
			Key:        "u1",
			Attributes: attributes,
		},
	})

	if err != nil || len(attributes) != 1 {
		t.Errorf("Evaluate() error = %v, attributes = %v, want unchanged", err, attributes)
	}
}

// TestEvaluationContextIsMergedUnderSubject verifies request-specific values
// and identity override ambient defaults while unrelated defaults survive.
func TestEvaluationContextIsMergedUnderSubject(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := started(t, provider, func(features *openfeaturefeature.Builder) {
		features.EvaluationContext(openfeature.NewEvaluationContext("service", map[string]any{"env": "prod", "plan": "free"}))
	})

	_, err := driver.Evaluate(context.Background(), feature.Request{
		Key:  "flag",
		Kind: feature.KindBool,
		Subject: feature.Subject{
			Key:        "u1",
			Attributes: map[string]any{"plan": "pro"},
		},
	})

	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	flat := *provider.last.Load()

	if flat["env"] != "prod" || flat["plan"] != "pro" || flat[openfeature.TargetingKey] != "u1" {
		t.Errorf("context = %#v, want env from the config and plan and key from the subject", flat)
	}
}

// TestEvaluateCanceledSkipsProvider protects the cancellation boundary: an
// already canceled request fails before any native evaluation is recorded.
func TestEvaluateCanceledSkipsProvider(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := started(t, provider)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.Evaluate(ctx, feature.Request{
		Key:  "flag",
		Kind: feature.KindBool,
	})

	if !errors.Is(err, context.Canceled) || provider.calls.Load() != 0 {
		t.Errorf("Evaluate() error = %v, provider calls = %d, want canceled and no calls", err, provider.calls.Load())
	}
}

// TestEvaluateBeforeStartIsNotReady rejects reads before provider startup.
// The fixture call count also proves rejection happens before SDK dispatch.
func TestEvaluateBeforeStartIsNotReady(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := openfeaturefeature.New(provider)

	if _, err := evaluate(driver); !errors.Is(err, feature.ErrNotReady) || provider.calls.Load() != 0 {
		t.Errorf("Evaluate() error = %v, provider calls = %d, want ErrNotReady and no calls", err, provider.calls.Load())
	}
}

// TestNewDoesNoIO protects deferred initialization. Construction must retain
// the provider without invoking its lifecycle methods.
func TestNewDoesNoIO(t *testing.T) {
	t.Parallel()

	provider := &stub{}

	_ = openfeaturefeature.New(provider)

	if provider.inits.Load() != 0 {
		t.Error("New initialized the provider")
	}
}

// TestPingBeforeAndAfterStart follows readiness across start and stop.
// Only a running provider may produce a successful readiness check.
func TestPingBeforeAndAfterStart(t *testing.T) {
	t.Parallel()

	driver := openfeaturefeature.New(&stub{})

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() before Start error = %v, want ErrNotReady", err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := driver.Ping(t.Context()); err != nil {
		t.Errorf("Ping() after Start error = %v, want nil", err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() after Stop error = %v, want ErrNotReady", err)
	}
}

// TestStartAndStopManageTheProvider checks one initialization and one shutdown
// for a provider owned by a newly constructed driver.
func TestStartAndStopManageTheProvider(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := openfeaturefeature.New(provider)

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if provider.inits.Load() != 1 {
		t.Errorf("Init calls = %d, want 1", provider.inits.Load())
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if provider.shutdowns.Load() != 1 {
		t.Errorf("Shutdown calls = %d, want 1", provider.shutdowns.Load())
	}

	if _, err := evaluate(driver); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Evaluate() after Stop error = %v, want ErrNotReady", err)
	}
}

// TestStartTwiceFails rejects duplicate lifecycle initialization.
// A running driver's provider must not be initialized a second time.
func TestStartTwiceFails(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{})

	if err := driver.Start(t.Context()); err == nil {
		t.Error("second Start() error = nil, want an error")
	}
}

// TestStartReportsInitFailure checks startup diagnostics and cleanup after a
// provider rejects initialization, leaving the driver not ready.
func TestStartReportsInitFailure(t *testing.T) {
	t.Parallel()

	provider := &stub{
		initErr: errors.New("no connection"),
	}
	driver := openfeaturefeature.New(provider)

	if err := driver.Start(t.Context()); !errors.Is(err, provider.initErr) {
		t.Errorf("Start() error = %v, want the init error", err)
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() after failed Start error = %v, want ErrNotReady", err)
	}

	if provider.shutdowns.Load() != 1 {
		t.Errorf("Shutdown calls = %d, want 1 after failed Start", provider.shutdowns.Load())
	}
}

// TestStopWithoutStartDoesNothing protects unstarted provider ownership.
// A no-op stop must not invoke shutdown on a provider never initialized.
func TestStopWithoutStartDoesNothing(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := openfeaturefeature.New(provider)

	if err := driver.Stop(t.Context()); err != nil || provider.shutdowns.Load() != 0 {
		t.Errorf("Stop() error = %v, shutdowns = %d, want nil and 0", err, provider.shutdowns.Load())
	}
}

// TestNewDoesNotReplaceTheGlobalProvider verifies isolated SDK registration.
// Driver startup must not change global clients used by other application code.
func TestNewDoesNotReplaceTheGlobalProvider(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	driver := started(t, provider)

	if openfeature.NamedProviderMetadata("").Name == "stub" || openfeature.ProviderMetadata().Name == "stub" {
		t.Error("the stub provider was registered globally")
	}

	if _, err := evaluate(driver); err != nil {
		t.Errorf("Evaluate() error = %v", err)
	}
}

// TestNewFromDoesNotOwnTheProvider checks borrowed-client lifecycle behavior.
// Stopping the wrapper leaves its provider initialized and usable by its owner.
func TestNewFromDoesNotOwnTheProvider(t *testing.T) {
	t.Parallel()

	provider := &stub{}
	api := isolated.NewAPI()
	t.Cleanup(func() {
		_ = api.Shutdown(context.WithoutCancel(t.Context()))
	})

	if err := api.SetProviderAndWait(t.Context(), provider); err != nil {
		t.Fatalf("SetProviderAndWait() error = %v", err)
	}

	driver := openfeaturefeature.NewFrom(api.NewClient())

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := driver.Ping(t.Context()); err != nil {
		t.Errorf("Ping() error = %v, want nil", err)
	}

	if _, err := evaluate(driver); err != nil {
		t.Errorf("Evaluate() error = %v", err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if provider.inits.Load() != 1 || provider.shutdowns.Load() != 0 {
		t.Errorf("Init/Shutdown calls = %d/%d, want 1/0: the caller owns the provider", provider.inits.Load(), provider.shutdowns.Load())
	}

	if _, err := evaluate(driver); err != nil {
		t.Errorf("Evaluate() after Stop error = %v, want nil", err)
	}
}

// TestNewFromPingReportsProviderState observes a borrowed provider's failed
// initialization rather than assuming a wrapped SDK client is always ready.
func TestNewFromPingReportsProviderState(t *testing.T) {
	t.Parallel()

	api := isolated.NewAPI()
	t.Cleanup(func() {
		_ = api.Shutdown(context.WithoutCancel(t.Context()))
	})
	driver := openfeaturefeature.NewFrom(api.NewClient())

	_ = api.SetProvider(t.Context(), &stub{
		initErr: errors.New("down"),
	})
	deadline := time.Now().Add(5 * time.Second)

	for driver.Ping(t.Context()) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() with a failed provider error = %v, want ErrNotReady", err)
	}
}

// TestClientReturnsTheWrappedClient protects native client identity.
// Wrapping must not substitute a new SDK client with different provider state.
func TestClientReturnsTheWrappedClient(t *testing.T) {
	t.Parallel()

	client := isolated.NewAPI().NewClient()

	if got := openfeaturefeature.NewFrom(client).Client(); got != client {
		t.Error("Client() did not return the client passed to NewFrom")
	}
}

// TestFacadeReadsThroughTheDriver checks typed facade integration, including
// preservation of a successful value, split reason, and variant identifier.
func TestFacadeReadsThroughTheDriver(t *testing.T) {
	t.Parallel()

	driver := started(t, &stub{
		detail: openfeature.ProviderResolutionDetail{
			Reason:  openfeature.SplitReason,
			Variant: "b",
		},
	})

	detail, err := feature.New(driver).ValueDetail(t.Context(), feature.NewFlag("flag", 0), feature.Subject{
		Key: "u1",
	})

	if err != nil || detail.Value != 7 || detail.Variant != "b" || detail.Reason != feature.ReasonSplit {
		t.Errorf("ValueDetail() = %#v, %v, want 7, b, split", detail, err)
	}
}
