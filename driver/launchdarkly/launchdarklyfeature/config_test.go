package launchdarklyfeature

import (
	"context"
	"testing"
	"time"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldtestdata"
)

// TestBuilderDefersNativeHookAndDetachesRoot checks construction callbacks run
// directly once while the native hook remains stored until SDK startup.
func TestBuilderDefersNativeHookAndDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	hooks := 0
	driver, err := New("sdk-test-key", func(features *Builder) {
		retained = features
		calls++
		features.StartWait(time.Second)
		features.SDK(func(config *ld.Config) {
			hooks++
			config.DataSource = ldtestdata.DataSource()
			config.Events = ldcomponents.NoEvents()
			config.Logging = ldcomponents.NoLogging()
		})
	}, func(features *Builder) {
		calls++

		if features != retained || calls != 2 {
			t.Fatal("callbacks were replayed or reordered")
		}
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.StartWait(-1).SDK(nil)

	if hooks != 0 || driver.startWait != time.Second {
		t.Fatal("native hook ran early or draft escaped publication")
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if hooks != 1 || calls != 2 {
		t.Fatal("startup did not invoke only the native hook")
	}
}

// TestBuilderMethodsReturnDraft checks receiver identity and accepts native nil
// as clearing a runtime hook, unlike nil construction-callback misuse.
func TestBuilderMethodsReturnDraft(t *testing.T) {
	t.Parallel()

	features := &Builder{}

	if features.StartWait(0) != features || features.SDK(nil) != features {
		t.Fatal("method returned another draft")
	}
}

// TestBuilderCallbacksPrecedeRequiredKeyValidation checks policies still run for
// invalid required inputs before the existing domain error is returned.
func TestBuilderCallbacksPrecedeRequiredKeyValidation(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := New("", func(features *Builder) {
		calls++
		features.SDK(nil)
	})

	if err == nil || calls != 1 {
		t.Fatal("required-key validation skipped a callback")
	}
}

// TestBuilderNilCallbackPanics checks nil is directly called even with an
// invalid required key; it is not converted into an ordinary validation error.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New("", nil)
}

// TestBuilderEmptySlicesPreserveDefault checks nil and empty expansions supply
// no policies and leave default startup normalization intact.
func TestBuilderEmptySlicesPreserveDefault(t *testing.T) {
	t.Parallel()

	var configure []func(*Builder)
	driver, err := New("sdk-test-key", configure...)

	if err != nil {
		t.Fatal(err)
	}

	if driver.startWait != DefaultStartWait {
		t.Fatal("nil expansion changed default")
	}

	driver, err = New("sdk-test-key", []func(*Builder){}...)

	if err != nil {
		t.Fatal(err)
	}

	if driver.startWait != DefaultStartWait {
		t.Fatal("empty expansion changed default")
	}
}

// TestBuilderApplicationPanicPropagates checks policy panics propagate before
// domain validation, even when the supplied SDK key is invalid.
func TestBuilderApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	_, _ = New("", func(features *Builder) {
		panic("application panic")
	})
}
