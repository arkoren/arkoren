package mcp_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

func TestWithAvailabilityCannotBypassExistingPolicy(t *testing.T) {
	t.Parallel()

	calls := 0
	tool := mcp.NewTool("guarded", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		t.Fatal("handler invoked")

		return mcp.NoInput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
		action.Available(func(context.Context) (bool, error) {
			calls++

			return false, nil
		})
	})

	guarded := tool.WithAvailability(func(context.Context) (bool, error) {
		t.Fatal("second policy invoked after denial")

		return true, nil
	})

	if calls != 0 {
		t.Fatal("construction evaluated policy")
	}

	_, err := guarded.Call(t.Context(), jsontext.Value(`not json`))

	if !errors.Is(err, mcp.ErrUnavailable) || calls != 1 {
		t.Fatalf("guard failed: %v, calls %d", err, calls)
	}

	if !errors.Is(tool.WithAvailability(nil).Err(), mcp.ErrInvalidTool) {
		t.Fatal("nil policy accepted")
	}
}

func TestAvailabilityDefaultsAndInvalidDefinitions(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo)

	if offered, err := tool.Available(t.Context()); !offered || err != nil {
		t.Fatalf("default = %v, %v", offered, err)
	}

	if offered, err := (mcp.Tool{}).Available(t.Context()); offered || !errors.Is(err, mcp.ErrInvalidTool) {
		t.Fatalf("zero = %v, %v", offered, err)
	}
}

func TestAvailabilityPublicationAndSharedCaptures(t *testing.T) {
	t.Parallel()

	offered := false
	var retained *mcp.ToolBuilder[request, request]
	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		retained = action
		action.Available(func(context.Context) (bool, error) {
			return true, nil
		}).Available(func(context.Context) (bool, error) {
			return offered, nil
		})
	})
	retained.Available(func(context.Context) (bool, error) {
		return true, nil
	})

	if allowed, err := tool.Available(t.Context()); allowed || err != nil {
		t.Fatalf("retained draft changed publication: %v, %v", allowed, err)
	}

	offered = true

	if allowed, err := tool.Available(t.Context()); !allowed || err != nil {
		t.Fatalf("closure was cloned: %v, %v", allowed, err)
	}
}

func TestAvailabilityNilPolicyErrorSticks(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Available(nil).Available(func(context.Context) (bool, error) {
			return true, nil
		})
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatal(tool.Err())
	}

	if _, err := tool.Available(t.Context()); !errors.Is(err, mcp.ErrInvalidTool) {
		t.Fatal(err)
	}
}

func TestUnavailableCallSkipsDecodeParseAndHandler(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[request](func(object *validate.StructBuilder[request]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Normalize(func(string) string {
				t.Fatal("unavailable input normalized")

				return ""
			})
		})
	})

	tool := mcp.NewTool("echo", func(context.Context, request) (request, error) {
		t.Fatal("unavailable handler called")

		return request{}, nil
	}, func(action *mcp.ToolBuilder[request, request]) {
		action.Input(schema).Available(func(context.Context) (bool, error) {
			return false, nil
		})
	})

	for _, arguments := range []jsontext.Value{jsontext.Value(`null`), jsontext.Value(`{"name":"x"}`)} {
		if _, err := tool.Call(t.Context(), arguments); !errors.Is(err, mcp.ErrUnavailable) {
			t.Fatal(err)
		}
	}
}

func TestAvailabilityErrorRetainsCauseWithoutDenial(t *testing.T) {
	t.Parallel()

	cause := mcp.ToolError("internal policy diagnostic")
	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Available(func(context.Context) (bool, error) {
			return true, cause
		})
	})

	_, err := tool.Call(t.Context(), nil)

	if !errors.Is(err, cause) || !errors.Is(err, mcp.ErrAvailability) {
		t.Fatalf("policy cause lost: %v", err)
	}

	if errors.Is(err, mcp.ErrUnavailable) {
		t.Fatal("policy failure classified as denial")
	}
}

func TestAvailabilityPreCanceledSkipsPolicy(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Available(func(context.Context) (bool, error) {
			t.Fatal("canceled policy called")

			return true, nil
		})
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := tool.Available(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if _, err := tool.Call(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAvailabilityPanicPropagates(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Available(func(context.Context) (bool, error) {
			panic("policy panic")
		})
	})
	defer func() {
		if recover() != "policy panic" {
			t.Fatal("policy panic did not propagate")
		}
	}()
	_, _ = tool.Call(t.Context(), nil)
}

func TestAvailabilityCancellationBeforeDecode(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Available(func(context.Context) (bool, error) {
			cancel()

			return true, nil
		})
	})

	if _, err := tool.Call(ctx, jsontext.Value(`null`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("policy cancellation did not precede decoding: %v", err)
	}
}
