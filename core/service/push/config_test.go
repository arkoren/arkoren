package push_test

import (
	"testing"

	"arkoren.dev/core/service/push"
	"arkoren.dev/core/service/push/pushtest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

func TestSenderRetainedDraftCannotReplaceTelemetry(t *testing.T) {
	t.Parallel()

	traces := telemetrytest.New()
	instrumentation := traces.Telemetry(t)
	var retained *push.Builder
	sender := push.New(pushtest.New(), func(delivery *push.Builder) {
		retained = delivery
		delivery.Telemetry(instrumentation)
	})
	retained.Telemetry(telemetry.New(nil))

	if _, err := sender.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready")); err != nil {
		t.Fatal(err)
	}

	traces.AssertSpanCount(t, 1)
}
