package launchdarklyfeature_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/featuretest"
	"arkoren.dev/driver/launchdarkly/launchdarklyfeature"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldtestdata"
)

// TestConformance runs the shared feature flag suite against an SDK client fed
// by ldtestdata. It needs no network and sends no events, so it also runs
// under -short.
func TestConformance(t *testing.T) {
	t.Parallel()

	featuretest.Run(t, newHarness)
}

// newClient builds an SDK client that reads its flags from data and sends no
// events.
func newClient(t *testing.T, data *ldtestdata.TestDataSource) *ld.LDClient {
	t.Helper()

	client, err := ld.MakeCustomClient("sdk-test-key", ld.Config{
		DataSource: data,
		Events:     ldcomponents.NoEvents(),
		Logging:    ldcomponents.NoLogging(),
	}, 0)

	if err != nil {
		t.Fatalf("MakeCustomClient() error = %v", err)
	}

	return client
}

// flagValue converts a fixture value to a JSON variation. A nil value is a
// null variation, the fallthrough of an override-only flag.
func flagValue(value any) ldvalue.Value {
	switch v := value.(type) {
	case nil:
		return ldvalue.Null()
	case bool:
		return ldvalue.Bool(v)
	case string:
		return ldvalue.String(v)
	case int64:
		return ldvalue.Int(int(v))
	case float64:
		return ldvalue.Float64(v)
	default:
		panic("unsupported fixture value")
	}
}

// swappable is a driver that delegates to the driver of the harness's current
// client. ldtestdata cannot remove a flag, so Delete rebuilds the client from
// the remaining flags and swaps it in, as a flag deleted in LaunchDarkly
// disappears from a running client.
type swappable struct {
	// current is the driver evaluations are delegated to.
	current atomic.Pointer[launchdarklyfeature.Driver]
}

// Evaluate delegates each read to the atomically published driver. Rebuilding
// the fixture client therefore does not change the suite's driver handle.
func (swappable *swappable) Evaluate(ctx context.Context, request feature.Request) (feature.Result, error) {
	return swappable.current.Load().Evaluate(ctx, request)
}

// harness implements featuretest.Harness over ldtestdata.
type harness struct {
	// driver is the delegating driver the suite evaluates with.
	driver *swappable

	// mu guards flags and data.
	mu sync.Mutex

	// flags is the current set of fixtures.
	flags map[string]featuretest.Flag

	// data feeds the current client; Set updates it in place.
	data *ldtestdata.TestDataSource
}

// newHarness starts a client over empty test data and closes it when the test
// ends.
func newHarness(t *testing.T) featuretest.Harness {
	t.Helper()

	harness := &harness{
		driver: &swappable{},
		flags:  map[string]featuretest.Flag{},
	}

	harness.rebuild(t)
	t.Cleanup(func() {
		_ = harness.driver.current.Load().Client().Close()
	})

	return harness
}

// Driver returns the stable delegating handle used by the conformance suite.
// Flag deletion can replace its native client without invalidating this handle.
func (harness *harness) Driver() feature.Driver {
	return harness.driver
}

// install writes flag to the current test data. The default value is variation
// 0, overrides follow, and a flag without a default value falls through to a
// null variation. A disabled flag is turned off after installing its values,
// and its off variation is the default so a disabled read is well formed.
func (harness *harness) install(flag featuretest.Flag) {
	variations := []ldvalue.Value{flagValue(flag.Value)}
	index := map[string]int{}

	for subject, value := range flag.Overrides {
		variations = append(variations, flagValue(value))
		index[subject] = len(variations) - 1
	}

	// Variations come first because the builder files each target under an
	// existing variation index.
	flagBuilder := harness.data.Flag(flag.Key).ClearTargets().ClearRules().Variations(variations...)

	for subject, i := range index {
		flagBuilder.VariationIndexForUser(subject, i)
	}

	harness.data.Update(flagBuilder.On(!flag.Disabled).FallthroughVariationIndex(0).OffVariationIndex(0))
}

// Set retains the flag fixture and publishes it to the current SDK data source.
// The lock serializes updates with deletion and client replacement.
func (harness *harness) Set(t *testing.T, flag featuretest.Flag) {
	t.Helper()

	harness.mu.Lock()
	defer harness.mu.Unlock()

	harness.flags[flag.Key] = flag
	harness.install(flag)
}

// Delete removes an existing fixture by rebuilding the SDK client. Missing keys
// leave the current client untouched because ldtestdata has no deletion API.
func (harness *harness) Delete(t *testing.T, key string) {
	t.Helper()

	harness.mu.Lock()
	defer harness.mu.Unlock()

	if _, ok := harness.flags[key]; !ok {
		return
	}

	delete(harness.flags, key)
	harness.rebuild(t)
}

// rebuild replaces the client with one over fresh test data holding the
// current fixtures, and closes the previous client. Callers hold harness.mu, except
// the constructor.
func (harness *harness) rebuild(t *testing.T) {
	t.Helper()

	old := harness.driver.current.Load()
	harness.data = ldtestdata.DataSource()

	for flag := range maps.Values(harness.flags) {
		harness.install(flag)
	}

	harness.driver.current.Store(launchdarklyfeature.NewFrom(newClient(t, harness.data)))

	if old != nil {
		_ = old.Client().Close()
	}
}

// TestInvalidKindsPrecedeDisabledFlag rejects empty and unknown raw kinds even
// when the native flag is off and would otherwise take the no-value shortcut.
func TestInvalidKindsPrecedeDisabledFlag(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("off").On(false))
	driver := driverFor(t, data)

	for _, kind := range []feature.Kind{"", "future"} {
		_, err := driver.Evaluate(t.Context(), feature.Request{Key: "off", Kind: kind})

		if !errors.Is(err, feature.ErrTypeMismatch) {
			t.Fatalf("kind %q error=%v; want ErrTypeMismatch", kind, err)
		}
	}
}

// TestInvalidKindsPrecedeReadiness rejects invalid request kinds before native
// client startup or flag lookup, without requiring a provider or SDK credentials.
func TestInvalidKindsPrecedeReadiness(t *testing.T) {
	t.Parallel()

	driver, err := launchdarklyfeature.New("local-unused-sdk-key")

	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []feature.Kind{"", "future"} {
		_, err := driver.Evaluate(t.Context(), feature.Request{Key: "missing", Kind: kind})

		if !errors.Is(err, feature.ErrTypeMismatch) {
			t.Fatalf("kind %q error=%v; want ErrTypeMismatch", kind, err)
		}
	}
}

// TestCancellationPrecedesInvalidKind preserves context-first error precedence
// for raw invalid requests, even when the native client has never been started.
func TestCancellationPrecedesInvalidKind(t *testing.T) {
	t.Parallel()

	driver, err := launchdarklyfeature.New("local-unused-sdk-key")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = driver.Evaluate(ctx, feature.Request{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate error=%v; want context.Canceled", err)
	}
}

// TestStopWaitsForOwnedStartup uses the public native hook to suspend acquisition
// and checks shutdown cannot return while that acquisition can still publish.
func TestStopWaitsForOwnedStartup(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		driver, err := launchdarklyfeature.New("local-sdk-key", func(features *launchdarklyfeature.Builder) {
			features.SDK(func(config *ld.Config) {
				close(entered)
				<-release

				config.DataSource = ldtestdata.DataSource()
				config.Events = ldcomponents.NoEvents()
				config.Logging = ldcomponents.NoLogging()
			})
		})

		if err != nil {
			t.Fatal(err)
		}

		started, stopped := make(chan error, 1), make(chan error, 1)

		go func() {
			started <- driver.Start(context.Background())
		}()

		<-entered

		go func() {
			stopped <- driver.Stop(context.Background())
		}()

		synctest.Wait()

		select {
		case err := <-stopped:
			t.Errorf("Stop returned during acquisition: %v", err)
			stopped <- err
		default:
		}

		close(release)

		if err := <-started; err != nil {
			t.Fatal(err)
		}

		if err := <-stopped; err != nil {
			t.Fatal(err)
		}

		if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
			t.Fatalf("Ping after Stop=%v; want ErrNotReady", err)
		}
	})
}

// neverReady is a data source that never receives flags, so a client built on
// it stays uninitialized.
type neverReady struct{}

// Build supplies a data source that deliberately never becomes ready.
// It allows startup failure tests to avoid a real streaming connection.
func (neverReady) Build(subsystems.ClientContext) (subsystems.DataSource, error) {
	return neverReady{}, nil
}

// Close has no resources to release because this fixture starts no transport.
// Returning nil lets startup tests focus on initialization failure.
func (neverReady) Close() error {
	return nil
}

// IsInitialized always reports false so the SDK cannot finish initialization.
// The fixture exercises the driver's readiness and startup error paths.
func (neverReady) IsInitialized() bool {
	return false
}

// Start deliberately leaves the readiness channel unsignaled. No goroutine or
// network request is created, keeping failure scenarios local to the test.
func (neverReady) Start(chan<- struct{}) {
}

// driverFor returns a NewFrom driver over a client fed by data, closed when
// the test ends.
func driverFor(t *testing.T, data *ldtestdata.TestDataSource) *launchdarklyfeature.Driver {
	t.Helper()

	client := newClient(t, data)
	t.Cleanup(func() {
		_ = client.Close()
	})

	return launchdarklyfeature.NewFrom(client)
}

// evaluate evaluates key for subject as kind with a background context.
func evaluate(t *testing.T, driver *launchdarklyfeature.Driver, key string, kind feature.Kind, subject feature.Subject) (feature.Result, error) {
	t.Helper()

	return driver.Evaluate(t.Context(), feature.Request{
		Key:     key,
		Kind:    kind,
		Subject: subject,
	})
}

// stringFlag returns a builder for a string flag whose fallthrough is "off"
// (variation 0) and whose variation 1 is "on".
func stringFlag(data *ldtestdata.TestDataSource, key string) *ldtestdata.FlagBuilder {
	return data.Flag(key).Variations(ldvalue.String("off"), ldvalue.String("on")).
		FallthroughVariationIndex(0).OffVariationIndex(0)
}

// TestEvaluateOffReturnsNoValueAndDisabled discards the SDK's off variation.
// The contract reports disabled flags with a nil value and disabled reason.
func TestEvaluateOffReturnsNoValueAndDisabled(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(stringFlag(data, "f").On(false))

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{
		Key: "u1",
	})

	if err != nil || result.Value != nil || result.Reason != feature.ReasonDisabled {
		t.Fatalf("Evaluate() = %#v, %v, want nil value, disabled, nil", result, err)
	}
}

// TestEvaluateOffWithoutOffVariationIsDisabled covers an off flag without a
// configured off value; missing variations must not become type errors.
func TestEvaluateOffWithoutOffVariationIsDisabled(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.UsePreconfiguredFlag(ldbuilders.NewFlagBuilder("f").On(false).
		Variations(ldvalue.String("a")).FallthroughVariation(0).Build())

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{})

	if err != nil || result.Value != nil || result.Reason != feature.ReasonDisabled {
		t.Fatalf("Evaluate() = %#v, %v, want nil value, disabled, nil", result, err)
	}
}

// TestEvaluateTargetMatchIsTargetingMatchWithVariantIndex preserves both the
// successful subject-targeted value and the SDK's numeric variation identity.
func TestEvaluateTargetMatchIsTargetingMatchWithVariantIndex(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(stringFlag(data, "f").VariationIndexForUser("u1", 1))

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{
		Key: "u1",
	})

	if err != nil || result.Value != "on" || result.Reason != feature.ReasonTargetingMatch || result.Variant != "1" {
		t.Fatalf("Evaluate() = %#v, %v, want on, targeting_match, variant 1", result, err)
	}
}

// TestEvaluateRuleMatchIsTargetingMatch checks attribute-based rules rather
// than explicit user targets, preserving their common contract explanation.
func TestEvaluateRuleMatchIsTargetingMatch(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(stringFlag(data, "f").IfMatch("plan", ldvalue.String("pro")).ThenReturnIndex(1))

	subject := feature.Subject{
		Key:        "u1",
		Attributes: map[string]any{"plan": "pro"},
	}

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, subject)

	if err != nil || result.Value != "on" || result.Reason != feature.ReasonTargetingMatch {
		t.Fatalf("Evaluate() = %#v, %v, want on, targeting_match", result, err)
	}
}

// TestEvaluateFallthroughIsDefaultWithVariantIndex retains the selected value
// and variation index when no targeting rule matches the subject.
func TestEvaluateFallthroughIsDefaultWithVariantIndex(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(stringFlag(data, "f"))

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{
		Key: "u1",
	})

	if err != nil || result.Value != "off" || result.Reason != feature.ReasonDefault || result.Variant != "0" {
		t.Fatalf("Evaluate() = %#v, %v, want off, default, variant 0", result, err)
	}
}

// TestEvaluateExperimentFallthroughIsSplit distinguishes a percentage rollout
// from ordinary fallthrough, even though both use the default rule position.
func TestEvaluateExperimentFallthroughIsSplit(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.UsePreconfiguredFlag(ldbuilders.NewFlagBuilder("f").On(true).
		Variations(ldvalue.String("a"), ldvalue.String("b")).
		Fallthrough(ldbuilders.Experiment(ldvalue.OptionalInt{}, ldbuilders.Bucket(0, 50000), ldbuilders.Bucket(1, 50000))).
		Build())

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{
		Key: "u1",
	})

	if err != nil || result.Reason != feature.ReasonSplit || result.Value == nil {
		t.Fatalf("Evaluate() = %#v, %v, want a value with split", result, err)
	}
}

// TestEvaluateExperimentRuleMatchIsSplit prioritizes experiment provenance over
// the matching rule, so rollout decisions remain visible as split allocation.
func TestEvaluateExperimentRuleMatchIsSplit(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	rule := ldbuilders.NewRuleBuilder().ID("r").
		Clauses(ldbuilders.Clause("plan", ldmodel.OperatorIn, ldvalue.String("pro"))).
		VariationOrRollout(ldbuilders.Experiment(ldvalue.OptionalInt{}, ldbuilders.Bucket(0, 50000), ldbuilders.Bucket(1, 50000)))
	data.UsePreconfiguredFlag(ldbuilders.NewFlagBuilder("f").On(true).
		Variations(ldvalue.String("a"), ldvalue.String("b")).FallthroughVariation(0).AddRule(rule).Build())

	subject := feature.Subject{
		Key:        "u1",
		Attributes: map[string]any{"plan": "pro"},
	}

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, subject)

	if err != nil || result.Reason != feature.ReasonSplit {
		t.Fatalf("Evaluate() = %#v, %v, want split", result, err)
	}
}

// TestEvaluatePrerequisiteFailedIsDisabledWithNoValue suppresses the fallback
// value when a flag's gate fails, preserving the contract's disabled semantics.
func TestEvaluatePrerequisiteFailedIsDisabledWithNoValue(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("gate").VariationForAll(false))
	data.UsePreconfiguredFlag(ldbuilders.NewFlagBuilder("f").On(true).
		Variations(ldvalue.String("a")).FallthroughVariation(0).OffVariation(0).
		AddPrerequisite("gate", 0).Build())

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{
		Key: "u1",
	})

	if err != nil || result.Value != nil || result.Reason != feature.ReasonDisabled {
		t.Fatalf("Evaluate() = %#v, %v, want nil value, disabled, nil", result, err)
	}
}

// TestEvaluateNullVariationReturnsNoValueWithMappedReason accepts a JSON null
// variation without losing its default-selection explanation.
func TestEvaluateNullVariationReturnsNoValueWithMappedReason(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("f").Variations(ldvalue.Null(), ldvalue.String("x")).
		FallthroughVariationIndex(0).OffVariationIndex(1).VariationIndexForUser("u1", 1))

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{
		Key: "u2",
	})

	if err != nil || result.Value != nil || result.Reason != feature.ReasonDefault {
		t.Fatalf("Evaluate() = %#v, %v, want nil value, default, nil", result, err)
	}
}

// TestEvaluateUnknownKeyIsNotFound checks the missing-flag sentinel against a
// ready local SDK client, separating absence from provider readiness.
func TestEvaluateUnknownKeyIsNotFound(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()

	_, err := evaluate(t, driverFor(t, data), "missing", feature.KindBool, feature.Subject{})

	if !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("Evaluate() error = %v, want ErrNotFound", err)
	}
}

// TestEvaluateWrongTypeIsTypeMismatch requests a scalar kind incompatible with
// the stored variation, protecting errors.Is classification across backends.
func TestEvaluateWrongTypeIsTypeMismatch(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("f").ValueForAll(ldvalue.String("text")))

	for _, kind := range []feature.Kind{feature.KindBool, feature.KindInt, feature.KindFloat} {
		_, err := evaluate(t, driverFor(t, data), "f", kind, feature.Subject{})

		if !errors.Is(err, feature.ErrTypeMismatch) {
			t.Errorf("Evaluate(%s) error = %v, want ErrTypeMismatch", kind, err)
		}
	}
}

// TestEvaluateMalformedFlagIsPlainErrorNamingKind preserves SDK diagnostics for
// invalid flag data without misclassifying it as a contract sentinel.
func TestEvaluateMalformedFlagIsPlainErrorNamingKind(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.UsePreconfiguredFlag(ldbuilders.NewFlagBuilder("f").On(true).
		Variations(ldvalue.String("a")).FallthroughVariation(7).Build())

	_, err := evaluate(t, driverFor(t, data), "f", feature.KindString, feature.Subject{})

	if err == nil || !strings.Contains(err.Error(), "MALFORMED_FLAG") {
		t.Fatalf("Evaluate() error = %v, want an error naming MALFORMED_FLAG", err)
	}

	for _, sentinel := range []error{feature.ErrNotFound, feature.ErrTypeMismatch, feature.ErrNotReady, feature.ErrInvalidSubject} {
		if errors.Is(err, sentinel) {
			t.Errorf("Evaluate() error matches %v, want a plain error", sentinel)
		}
	}
}

// TestEvaluateUninitializedClientIsNotReady checks borrowed-client readiness.
// An SDK client with no initial flag data must not evaluate as a missing flag.
func TestEvaluateUninitializedClientIsNotReady(t *testing.T) {
	t.Parallel()

	client, _ := ld.MakeCustomClient("sdk-test-key", ld.Config{
		DataSource: neverReady{},
		Events:     ldcomponents.NoEvents(),
		Logging:    ldcomponents.NoLogging(),
	}, 0)
	t.Cleanup(func() {
		_ = client.Close()
	})

	driver := launchdarklyfeature.NewFrom(client)

	_, err := evaluate(t, driver, "f", feature.KindBool, feature.Subject{})

	if !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Evaluate() error = %v, want ErrNotReady", err)
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() error = %v, want ErrNotReady", err)
	}
}

// TestEvaluateRejectedAttributeIsInvalidSubject checks SDK context validation.
// Invalid native attribute types must identify the request subject as invalid.
func TestEvaluateRejectedAttributeIsInvalidSubject(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("f").VariationForAll(true))
	driver := driverFor(t, data)

	for _, name := range []string{"name", "key", "kind"} {
		subject := feature.Subject{
			Key:        "u1",
			Attributes: map[string]any{name: int64(5)},
		}

		_, err := evaluate(t, driver, "f", feature.KindBool, subject)

		if !errors.Is(err, feature.ErrInvalidSubject) {
			t.Errorf("Evaluate(attribute %q) error = %v, want ErrInvalidSubject", name, err)
		}
	}
}

// TestEvaluateEmptyKeyIsOneSharedAnonymousContext protects stable bucketing for
// anonymous reads by using the same documented SDK context key each time.
func TestEvaluateEmptyKeyIsOneSharedAnonymousContext(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(stringFlag(data, "by-key").IfMatch("key", ldvalue.String(launchdarklyfeature.AnonymousKey)).ThenReturnIndex(1))
	data.Update(stringFlag(data, "by-flag").IfMatch("anonymous", ldvalue.Bool(true)).ThenReturnIndex(1))
	driver := driverFor(t, data)

	for _, key := range []string{"by-key", "by-flag"} {
		result, err := evaluate(t, driver, key, feature.KindString, feature.Subject{})

		if err != nil || result.Value != "on" {
			t.Errorf("Evaluate(%q, anonymous) = %#v, %v, want on", key, result, err)
		}

		result, err = evaluate(t, driver, key, feature.KindString, feature.Subject{
			Key: "u1",
		})

		if err != nil || result.Value != "off" {
			t.Errorf("Evaluate(%q, u1) = %#v, %v, want off", key, result, err)
		}
	}
}

// TestEvaluateAttributesKeepTheirTypes uses matching SDK rules to verify scalar,
// list, and timestamp conversion without stringifying subject attributes.
func TestEvaluateAttributesKeepTheirTypes(t *testing.T) {
	t.Parallel()

	moment := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	data := ldtestdata.DataSource()
	attributes := map[string]struct {
		// value is the subject attribute passed through the driver conversion.
		value any
		// match is the native rule operand expected to match the converted value.
		match ldvalue.Value
	}{
		"string": {
			value: "pro",
			match: ldvalue.String("pro"),
		},
		"bool": {
			value: true,
			match: ldvalue.Bool(true),
		},
		"int64": {
			value: int64(7),
			match: ldvalue.Int(7),
		},
		"float64": {
			value: 2.5,
			match: ldvalue.Float64(2.5),
		},
		"strings": {
			value: []string{"a", "b"},
			match: ldvalue.String("b"),
		},
		"time": {
			value: moment,
			match: ldvalue.Float64(float64(moment.UnixMilli())),
		},
	}

	subject := feature.Subject{
		Key:        "u1",
		Attributes: map[string]any{},
	}

	for name, attribute := range attributes {
		subject.Attributes[name] = attribute.value
		data.Update(data.Flag(name).IfMatch(name, attribute.match).ThenReturn(true).FallthroughVariation(false))
	}

	driver := driverFor(t, data)

	for name := range attributes {
		result, err := evaluate(t, driver, name, feature.KindBool, subject)

		if err != nil || result.Value != true {
			t.Errorf("attribute %q: Evaluate() = %#v, %v, want true", name, result, err)
		}
	}
}

// TestEvaluateAttributeCannotReplaceSubjectKey protects subject identity from
// an attribute named key, which must not redirect targeting to another user.
func TestEvaluateAttributeCannotReplaceSubjectKey(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(stringFlag(data, "f").VariationIndexForUser("u1", 1))

	subject := feature.Subject{
		Key:        "u1",
		Attributes: map[string]any{"key": "u2", "kind": "device"},
	}

	result, err := evaluate(t, driverFor(t, data), "f", feature.KindString, subject)

	if err != nil || result.Value != "on" {
		t.Fatalf("Evaluate() = %#v, %v, want on for user u1", result, err)
	}
}

// TestEvaluateIntKindReturnsInt64ForWholeNumbers checks the contract's integer
// representation when the SDK stores numeric variations as JSON numbers.
func TestEvaluateIntKindReturnsInt64ForWholeNumbers(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("whole").ValueForAll(ldvalue.Float64(3)))
	data.Update(data.Flag("fraction").ValueForAll(ldvalue.Float64(3.5)))
	driver := driverFor(t, data)

	result, err := evaluate(t, driver, "whole", feature.KindInt, feature.Subject{})

	if err != nil || result.Value != int64(3) {
		t.Errorf("Evaluate(whole) = %#v, %v, want int64(3)", result, err)
	}

	result, err = evaluate(t, driver, "fraction", feature.KindInt, feature.Subject{})

	if err != nil || result.Value != 3.5 {
		t.Errorf("Evaluate(fraction) = %#v, %v, want float64(3.5)", result, err)
	}
}

// TestFacadeRejectsFractionalFlagAsInt verifies facade validation rejects a
// fractional SDK number rather than silently truncating it to an integer.
func TestFacadeRejectsFractionalFlagAsInt(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("whole").ValueForAll(ldvalue.Int(3)))
	data.Update(data.Flag("fraction").ValueForAll(ldvalue.Float64(3.5)))
	flags := feature.New(driverFor(t, data))

	if got, err := flags.Value(t.Context(), feature.NewFlag("whole", -1), feature.Subject{}); err != nil || got != 3 {
		t.Errorf("Value[int](whole) = %d, %v, want 3, nil", got, err)
	}

	if got, err := flags.Value(t.Context(), feature.NewFlag("fraction", -1), feature.Subject{}); !errors.Is(err, feature.ErrTypeMismatch) || got != -1 {
		t.Errorf("Value[int](fraction) = %d, %v, want -1, ErrTypeMismatch", got, err)
	}
}

// TestEvaluateCanceledContextFailsBeforeTheSDK protects cancellation ordering.
// Even a valid local flag must not produce a successful canceled evaluation.
func TestEvaluateCanceledContextFailsBeforeTheSDK(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("f").VariationForAll(true))
	driver := driverFor(t, data)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.Evaluate(ctx, feature.Request{
		Key:  "f",
		Kind: feature.KindBool,
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate() error = %v, want context.Canceled", err)
	}
}

// TestNewRejectsEmptySDKKey rejects missing required credentials before startup.
// The constructor must not rely on a later SDK connection to validate the key.
func TestNewRejectsEmptySDKKey(t *testing.T) {
	t.Parallel()

	if _, err := launchdarklyfeature.New(""); err == nil {
		t.Fatal("New(\"\") error = nil, want an error")
	}
}

// startable returns a New driver whose SDK reads data and sends no events.
func startable(
	t *testing.T,
	data *ldtestdata.TestDataSource,
	configure ...func(*launchdarklyfeature.Builder),
) *launchdarklyfeature.Driver {
	t.Helper()

	policies := append([]func(*launchdarklyfeature.Builder){func(features *launchdarklyfeature.Builder) {
		features.SDK(func(config *ld.Config) {
			config.DataSource = data
			config.Events = ldcomponents.NoEvents()
			config.Logging = ldcomponents.NoLogging()
		})
	}}, configure...)

	driver, err := launchdarklyfeature.New("sdk-test-key", policies...)

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return driver
}

// TestLifecycleOfOwnedClient follows construction, startup, readiness, and stop.
// The SDK client is created only at Start and released by its owning driver.
func TestLifecycleOfOwnedClient(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("f").VariationForAll(true))
	driver := startable(t, data)

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() before Start error = %v, want ErrNotReady", err)
	}

	if _, err := evaluate(t, driver, "f", feature.KindBool, feature.Subject{}); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Evaluate() before Start error = %v, want ErrNotReady", err)
	}

	if driver.Client() != nil {
		t.Error("Client() before Start != nil, want nil")
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := driver.Start(t.Context()); err == nil {
		t.Error("second Start() error = nil, want an error")
	}

	if err := driver.Ping(t.Context()); err != nil {
		t.Errorf("Ping() after Start error = %v", err)
	}

	client := driver.Client()

	if client == nil || !client.Initialized() {
		t.Fatal("Client() after Start is nil or uninitialized")
	}

	if result, err := evaluate(t, driver, "f", feature.KindBool, feature.Subject{}); err != nil || result.Value != true {
		t.Errorf("Evaluate() = %#v, %v, want true", result, err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() after Stop error = %v, want ErrNotReady", err)
	}

	if _, err := evaluate(t, driver, "f", feature.KindBool, feature.Subject{}); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Evaluate() after Stop error = %v, want ErrNotReady", err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Errorf("second Stop() error = %v, want nil", err)
	}
}

// TestStartFailsWhenClientDoesNotInitialize exercises the local never-ready
// source, ensuring startup does not report success without initial flag data.
func TestStartFailsWhenClientDoesNotInitialize(t *testing.T) {
	t.Parallel()

	driver, err := launchdarklyfeature.New("sdk-test-key",
		func(features *launchdarklyfeature.Builder) {
			features.StartWait(-1)
			features.SDK(func(config *ld.Config) {
				config.DataSource = neverReady{}
				config.Events = ldcomponents.NoEvents()
				config.Logging = ldcomponents.NoLogging()
			})
		},
	)

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := driver.Start(t.Context()); err == nil {
		t.Fatal("Start() error = nil, want an initialization error")
	}

	if driver.Client() != nil {
		t.Error("Client() after failed Start != nil, want nil")
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, feature.ErrNotReady) {
		t.Errorf("Ping() error = %v, want ErrNotReady", err)
	}
}

// TestStartFailsWithCanceledContext prevents an already canceled startup from
// creating a usable owned client or reporting successful initialization.
func TestStartFailsWithCanceledContext(t *testing.T) {
	t.Parallel()

	driver := startable(t, ldtestdata.DataSource())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
}

// TestNewFromDoesNotOwnTheClient protects borrowed native client ownership.
// Stopping the wrapper leaves the caller's SDK client initialized and usable.
func TestNewFromDoesNotOwnTheClient(t *testing.T) {
	t.Parallel()

	data := ldtestdata.DataSource()
	data.Update(data.Flag("f").VariationForAll(true))
	client := newClient(t, data)
	t.Cleanup(func() {
		_ = client.Close()
	})

	driver := launchdarklyfeature.NewFrom(client)

	if err := driver.Start(t.Context()); err != nil {
		t.Errorf("Start() error = %v, want nil", err)
	}

	if driver.Client() != client {
		t.Error("Client() is not the client passed to NewFrom")
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Errorf("Stop() error = %v, want nil", err)
	}

	if !client.Initialized() {
		t.Error("client closed by Stop, want it left to the caller")
	}

	if err := driver.Ping(t.Context()); err != nil {
		t.Errorf("Ping() after Stop error = %v, want nil", err)
	}

	if result, err := evaluate(t, driver, "f", feature.KindBool, feature.Subject{}); err != nil || result.Value != true {
		t.Errorf("Evaluate() after Stop = %#v, %v, want true", result, err)
	}
}
