package migrate

import (
	"encoding/json/v2"
	"testing"
)

// TestOutOfOrderJSONUsesStrings makes migration configuration independent of
// the declaration order of the available policies.
func TestOutOfOrderJSONUsesStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]OutOfOrder{"", OutOfOrderWarn, OutOfOrderAllow, OutOfOrderError})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","warn","allow","error"]` {
		t.Fatalf("policies JSON = %s", data)
	}
}

// TestEmptyOutOfOrderDefaultsToWarn retains the old zero-value warning policy
// when settings are omitted or explicitly reset from an earlier callback.
func TestEmptyOutOfOrderDefaultsToWarn(t *testing.T) {
	t.Parallel()

	config := newSettings(func(migrator *Builder) {
		migrator.OutOfOrder(OutOfOrderAllow)
		migrator.OutOfOrder("")
	})

	if config.OutOfOrder != OutOfOrderWarn || newSettings().OutOfOrder != OutOfOrderWarn {
		t.Fatalf("default policy = %q", config.OutOfOrder)
	}
}

// TestUnknownOutOfOrderPreserved retains the previous constructor behavior for
// unrecognized policies instead of silently substituting a known policy.
func TestUnknownOutOfOrderPreserved(t *testing.T) {
	t.Parallel()

	config := newSettings(func(migrator *Builder) {
		migrator.OutOfOrder("unknown")
	})

	if config.OutOfOrder != "unknown" {
		t.Fatalf("unknown policy = %q", config.OutOfOrder)
	}
}
