package application_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"arkoren.dev/core/application"
	"arkoren.dev/core/event"
)

// must returns unsubscribe, and panics with err when the registration
// failed, so tests with fixed valid names stay linear.
func must(unsubscribe event.Unsubscribe, err error) event.Unsubscribe {
	if err != nil {
		panic(err)
	}

	return unsubscribe
}

// TestLifecycleEventNamesAreValid checks the published lifecycle identifiers.
// Every signal and failure topic must satisfy event registration rules.
func TestLifecycleEventNamesAreValid(t *testing.T) {
	t.Parallel()

	bus := event.New()

	for _, name := range []string{application.Starting.Name(), application.Started.Name(), application.Stopping.Name(), application.Stopped.Name(), application.Failed.Name()} {
		if _, err := bus.ListenEvent(name, func(context.Context, any) error {
			return nil
		}); err != nil {
			t.Errorf("lifecycle event %q: %v", name, err)
		}
	}
}

// TestEventsListenRegexObservesEveryEvent checks lifecycle fan-out by pattern.
// The observer receives the event name together with its payload type.
func TestEventsListenRegexObservesEveryEvent(t *testing.T) {
	app := application.New()

	var seen []string

	_, err := app.Events().ListenRegex(regexp.MustCompile(``), func(_ context.Context, name string, payload any) error {
		seen = append(seen, fmt.Sprintf("%s %T", name, payload))

		return nil
	})

	mustNil(t, err)

	mustNil(t, app.Start(context.Background()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	if len(seen) == 0 || !strings.HasPrefix(seen[0], "app.starting ") {
		t.Fatalf("seen = %v, want lifecycle events with payload type names", seen)
	}
}
