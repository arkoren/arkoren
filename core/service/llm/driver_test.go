package llm_test

import (
	"encoding/json/v2"
	"testing"

	"arkoren.dev/core/service/llm"
)

// TestToolChoicesJSONUsesStrings keeps per-request policy legible and stable
// across declaration reordering, including the unset default sentinel.
func TestToolChoicesJSONUsesStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]llm.ToolChoiceKind{"", llm.ToolChoiceAuto, llm.ToolChoiceNone,
		llm.ToolChoiceRequired, llm.ToolChoiceNamed})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","auto","none","required","named"]` {
		t.Fatalf("tool choices JSON = %s", data)
	}
}

// TestEffortsJSONUsesStrings preserves the portable reasoning vocabulary rather
// than exposing the old enum ordinals in serialized requests.
func TestEffortsJSONUsesStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]llm.Effort{"", llm.EffortDefault, llm.EffortOff,
		llm.EffortLow, llm.EffortMedium, llm.EffortHigh})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","default","off","low","medium","high"]` {
		t.Fatalf("efforts JSON = %s", data)
	}
}

// TestPurposesJSONUsesStrings keeps query and document embedding policy usable
// directly from configuration and preserves the unset default sentinel.
func TestPurposesJSONUsesStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]llm.Purpose{"", llm.PurposeUnspecified, llm.PurposeQuery, llm.PurposeDocument})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","unspecified","query","document"]` {
		t.Fatalf("purposes JSON = %s", data)
	}
}
