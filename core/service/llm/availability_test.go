package llm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

// observedSchema exposes rendering and parsing boundaries without replacing the
// real schema implementation. Counters belong to the test and are synchronous.
type observedSchema struct {
	// Schema supplies the sealed implementation and normal validation behavior.
	validate.Schema[struct{}]
	// renders counts JSONSchema calls to detect repeated normalization.
	renders *int
	// parses counts Parse calls before the typed handler.
	parses *int
	// failure injects a schema-rendering failure at construction.
	failure error
}

// JSONSchema counts rendering and returns the injected failure, if any.
// Example: document, err := schema.JSONSchema(); _ = document; _ = err
func (schema observedSchema) JSONSchema() ([]byte, error) {
	*schema.renders++

	if schema.failure != nil {
		return nil, schema.failure
	}

	return schema.Schema.JSONSchema()
}

// Parse counts validation calls and delegates defaults and checking unchanged.
// Example: value, err := schema.Parse(ctx, struct{}{}); _ = value; _ = err
func (schema observedSchema) Parse(ctx context.Context, in struct{}) (struct{}, error) {
	*schema.parses++

	return schema.Schema.Parse(ctx, in)
}

// TestToolAvailabilityDefaultsAndSharedCaptures verifies no policy means allow,
// explicit policies are uncached, and retained drafts cannot edit publication.
func TestToolAvailabilityDefaultsAndSharedCaptures(t *testing.T) {
	t.Parallel()

	plain := NewTool("plain", "Plain.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) { return "ok", nil })

	if offered, err := plain.Available(t.Context()); !offered || err != nil {
		t.Fatalf("default = %v, %v", offered, err)
	}

	allowed, calls := false, 0
	var retained *ToolBuilder
	tool := NewTool("guarded", "Guarded.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) { return "ok", nil },
		func(action *ToolBuilder) {
			retained = action
			action.Available(func(context.Context) (bool, error) {
				calls++

				return allowed, nil
			})
		})
	retained.Available(nil)

	if offered, err := tool.Available(t.Context()); offered || err != nil {
		t.Fatalf("denied = %v, %v", offered, err)
	}

	allowed = true

	if offered, err := tool.Available(t.Context()); !offered || err != nil {
		t.Fatalf("allowed = %v, %v", offered, err)
	}

	if calls != 2 || tool.Err() != nil {
		t.Fatalf("calls = %d, err = %v", calls, tool.Err())
	}
}

// TestToolAvailabilityDeclarationOrdering checks callbacks share one draft,
// run once in order, and replace policy without evaluating it at construction.
func TestToolAvailabilityDeclarationOrdering(t *testing.T) {
	t.Parallel()

	var order []int
	var first *ToolBuilder
	tool := NewTool("guarded", "Guarded.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) { return "ok", nil },
		func(action *ToolBuilder) {
			first = action
			order = append(order, 1)
			action.Available(func(context.Context) (bool, error) { panic("replaced") })
		},
		func(action *ToolBuilder) {
			if action != first {
				t.Fatal("different drafts")
			}

			order = append(order, 2)

			if action.Available(func(context.Context) (bool, error) { return true, nil }) != action {
				t.Fatal("setting returned another draft")
			}
		})

	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("order = %v", order)
	}

	if offered, err := tool.Available(t.Context()); !offered || err != nil {
		t.Fatalf("available = %v, %v", offered, err)
	}
}

// TestToolAvailabilityInvalidPositionalsStillConfigure preserves positional
// error precedence while invoking all callbacks on an invalid declaration.
func TestToolAvailabilityInvalidPositionalsStillConfigure(t *testing.T) {
	t.Parallel()

	calls := 0
	tool := NewTool[struct{}, string]("INVALID", "Bad.", nil, nil,
		func(action *ToolBuilder) { calls++; action.Available(nil) },
		func(action *ToolBuilder) { calls++; action.Available(nil) })

	if calls != 2 || !errors.Is(tool.Err(), ErrInvalidName) {
		t.Fatalf("callbacks = %d, error = %v", calls, tool.Err())
	}

	if _, err := tool.Available(t.Context()); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("available error = %v", err)
	}
}

// TestToolAvailabilityNilPolicySticky distinguishes invalid explicit nil from
// an omitted setting; later policies cannot clear the first error.
func TestToolAvailabilityNilPolicySticky(t *testing.T) {
	t.Parallel()

	tool := NewTool("guarded", "Guarded.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) { return "ok", nil },
		func(action *ToolBuilder) {
			action.Available(nil)
			action.Available(func(context.Context) (bool, error) { panic("invalid") })
		})

	if _, err := tool.Available(t.Context()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}

	if _, err := tool.call(t.Context(), []byte(`invalid`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("direct call error = %v", err)
	}
}

// TestToolAvailabilityNilCallbackPanics keeps construction misuse visible even
// when the required positional arguments already make a declaration invalid.
func TestToolAvailabilityNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("nil callback did not panic")
		}
	}()
	NewTool[struct{}, string]("INVALID", "Bad.", nil, nil, nil)
}

// TestToolAvailabilityEmptyCallbackSlicesKeepDefaults verifies the matching
// callback alias accepts ordinary callback slices, with nil and empty expansion
// both behaving like omission rather than a nil callback entry.
func TestToolAvailabilityEmptyCallbackSlicesKeepDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*ToolBuilder)
	empty := []ToolBuilderFunc{}
	first := NewTool("first", "First.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			return "ok", nil
		}, omitted...)

	second := NewTool("second", "Second.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			return "ok", nil
		}, empty...)

	if offered, err := first.Available(t.Context()); !offered || err != nil {
		t.Fatalf("nil expansion = %v, %v", offered, err)
	}

	if offered, err := second.Available(t.Context()); !offered || err != nil {
		t.Fatalf("empty expansion = %v, %v", offered, err)
	}
}

// TestToolAvailabilityConstructionPanicPropagates leaves application panics
// untouched rather than converting construction misuse into declaration errors.
func TestToolAvailabilityConstructionPanicPropagates(t *testing.T) {
	t.Parallel()

	cause := "construction panic"
	defer func() {
		if recovered := recover(); recovered != cause {
			t.Errorf("panic = %v", recovered)
		}
	}()
	NewTool[struct{}, string]("INVALID", "Bad.", nil, nil,
		func(action *ToolBuilder) {
			panic(cause)
		})
}

// TestToolAvailabilityMissingSchemaPrecedesHandler preserves the historical
// schema-before-handler validation when both positional values are absent.
func TestToolAvailabilityMissingSchemaPrecedesHandler(t *testing.T) {
	t.Parallel()

	callbacks := 0
	tool := NewTool[struct{}, string]("guarded", "Guarded.", nil, nil,
		func(action *ToolBuilder) {
			callbacks++
			action.Available(nil)
		})

	if !errors.Is(tool.Err(), ErrInvalidRequest) || !strings.Contains(tool.Err().Error(), "needs a schema") {
		t.Fatalf("error = %v", tool.Err())
	}

	if callbacks != 1 {
		t.Fatalf("callbacks = %d", callbacks)
	}
}

// TestToolAvailabilityMissingHandlerSkipsRendering checks nil handlers retain
// their positional error without invoking schema rendering or skipping callbacks.
func TestToolAvailabilityMissingHandlerSkipsRendering(t *testing.T) {
	t.Parallel()

	renders, parses, callbacks := 0, 0, 0
	schema := observedSchema{Schema: validate.Infer[struct{}](), renders: &renders, parses: &parses}
	tool := NewTool[struct{}, string]("guarded", "Guarded.", schema, nil,
		func(action *ToolBuilder) {
			callbacks++
			action.Available(nil)
		})

	if !errors.Is(tool.Err(), ErrInvalidRequest) || !strings.Contains(tool.Err().Error(), "needs a function") {
		t.Fatalf("error = %v", tool.Err())
	}

	if renders != 0 || callbacks != 1 {
		t.Fatalf("renders = %d, callbacks = %d", renders, callbacks)
	}
}

// TestToolAvailabilityCanceledBeforePolicy avoids invoking application policy
// when the calling context is already canceled.
func TestToolAvailabilityCanceledBeforePolicy(t *testing.T) {
	t.Parallel()

	tool := NewTool("guarded", "Guarded.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) { panic("handler") },
		func(action *ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { panic("policy") })
		})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := tool.Available(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	if _, err := tool.call(ctx, []byte(`invalid`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("call error = %v", err)
	}
}

// TestToolAvailabilityCancellationAfterPolicy prevents decode and execution
// when a successful policy cancels its calling context before returning.
func TestToolAvailabilityCancellationAfterPolicy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	tool := NewTool("guarded", "Guarded.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) { panic("handler") },
		func(action *ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { cancel(); return true, nil })
		})

	if _, err := tool.call(ctx, []byte(`invalid`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("call error = %v", err)
	}
}

// TestToolAvailabilityDirectCallDeniesBeforeParse verifies the private executor
// cannot bypass capability policy even with malformed JSON.
func TestToolAvailabilityDirectCallDeniesBeforeParse(t *testing.T) {
	t.Parallel()

	renders, parses, policies := 0, 0, 0
	schema := observedSchema{Schema: validate.Infer[struct{}](), renders: &renders, parses: &parses}
	tool := NewTool("guarded", "Guarded.", schema,
		func(context.Context, struct{}) (string, error) { panic("handler") },
		func(action *ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { policies++; return false, nil })
		})

	if _, err := tool.call(t.Context(), []byte(`invalid`)); !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("error = %v", err)
	}

	if renders != 1 || parses != 0 || policies != 1 {
		t.Fatalf("renders = %d, parses = %d, policies = %d", renders, parses, policies)
	}
}

// TestToolAvailabilityDirectCallParsesOnce confirms the guard does not duplicate
// schema normalization, parsing, or handler execution on the allowed path.
func TestToolAvailabilityDirectCallParsesOnce(t *testing.T) {
	t.Parallel()

	renders, parses, handlers, policies := 0, 0, 0, 0
	schema := observedSchema{Schema: validate.Infer[struct{}](), renders: &renders, parses: &parses}
	tool := NewTool("guarded", "Guarded.", schema,
		func(context.Context, struct{}) (string, error) { handlers++; return "ok", nil },
		func(action *ToolBuilder) {
			if renders != 1 {
				t.Fatalf("render timing = %d", renders)
			}

			action.Available(func(context.Context) (bool, error) { policies++; return true, nil })
		})

	if text, err := tool.call(t.Context(), nil); text != "ok" || err != nil {
		t.Fatalf("call = %q, %v", text, err)
	}

	if !reflect.DeepEqual([]int{renders, parses, handlers, policies}, []int{1, 1, 1, 1}) {
		t.Fatalf("counts = %d, %d, %d, %d", renders, parses, handlers, policies)
	}
}

// TestToolAvailabilityFailureIsPrivate preserves nested causes for the caller
// without allowing a policy-wrapped ToolError to become model-facing content.
func TestToolAvailabilityFailureIsPrivate(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("private policy: %w", ToolError("secret"))
	tool := NewTool("guarded", "Guarded.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) { panic("handler") },
		func(action *ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { return false, cause })
		})

	_, err := tool.call(t.Context(), []byte(`invalid`))

	if !errors.Is(err, cause) || !errors.Is(err, ErrToolAvailability) {
		t.Fatalf("cause lost: %v", err)
	}

	var visible toolError

	if !errors.As(err, &visible) || !errors.Is(err, ErrPermanent) {
		t.Fatalf("classification lost: %v", err)
	}

	if modelText(err) != genericToolFailure {
		t.Fatalf("leaked: %s", modelText(err))
	}
}

// TestToolAvailabilitySchemaFailurePrecedesCallbacks preserves rendering errors
// and still invokes configuration exactly once, without retrying the schema.
func TestToolAvailabilitySchemaFailurePrecedesCallbacks(t *testing.T) {
	t.Parallel()

	renders, parses, callbacks := 0, 0, 0
	cause := errors.New("cannot render")
	schema := observedSchema{Schema: validate.Infer[struct{}](), renders: &renders, parses: &parses, failure: cause}
	tool := NewTool("guarded", "Guarded.", schema,
		func(context.Context, struct{}) (string, error) { return "ok", nil },
		func(action *ToolBuilder) { callbacks++; action.Available(nil) })

	if !errors.Is(tool.Err(), cause) || renders != 1 || callbacks != 1 {
		t.Fatalf("error = %v, renders = %d, callbacks = %d", tool.Err(), renders, callbacks)
	}
}

// TestToolPublicationOwnsSchemaBeforeCallbacks mutates borrowed rendering both
// inside tool configuration and before constructing an agent from the tool.
func TestToolPublicationOwnsSchemaBeforeCallbacks(t *testing.T) {
	t.Parallel()

	document := []byte(`{"type":"object"}`)
	tool := NewTool("guarded", "Guarded.", borrowedSchema[struct{}]{
		Schema: validate.Infer[struct{}](), document: document,
	}, func(context.Context, struct{}) (string, error) { return "ok", nil },
		func(action *ToolBuilder) { document[0] = '!' })

	document[1] = '!'
	agent := NewAgent[string]("agent", func(agent *AgentBuilder[string]) { agent.Tools(tool) })

	if string(agent.tools[0].schema) != `{"type":"object"}` {
		t.Fatalf("schema = %s", agent.tools[0].schema)
	}
}
