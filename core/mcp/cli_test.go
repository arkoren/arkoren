package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// ExampleToolBuilder_Role publishes diagnostic meaning without adding a command
// alias or granting access. The independent availability policy still denies use.
func ExampleToolBuilder_Role() {
	tool := mcp.NewTool("service.health", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
		action.CLI("service", "observe").Role("health")
		action.Available(func(context.Context) (bool, error) {
			return false, nil
		})
	})

	if err := tool.Err(); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(tool.Describe().CLI.Role)

	_, err := tool.Call(context.Background(), nil)
	fmt.Println(errors.Is(err, mcp.ErrUnavailable))
	// Output:
	// health
	// true
}

// cliInput supplies one positional property for CLI publication tests.
type cliInput struct {
	// ID is the resource identifier carried in example objects.
	ID string `json:"id"`
}

// hookedCLIInput rejects even a structurally compatible empty object through
// its JSON method, which construction must honor when checking CLI examples.
type hookedCLIInput struct{}

// errExampleDecoding makes a custom JSON hook observable through NewTool.Err,
// without mutable package state or a test of the private example decoder.
var errExampleDecoding = errors.New("custom example decoding failed")

// UnmarshalJSON returns the hook's original cause so the public declaration
// error can demonstrate that strict typed decoding executes application code.
func (hookedcliinput *hookedCLIInput) UnmarshalJSON([]byte) error {
	return errExampleDecoding
}

func TestCLIMetadataDetached(t *testing.T) {
	t.Parallel()

	var retained *mcp.ToolBuilder[cliInput, cliInput]
	names := []string{"id"}
	tool := mcp.NewTool("exact.name", func(_ context.Context, input cliInput) (cliInput, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[cliInput, cliInput]) {
		retained = action
		action.CLI("resources", "show").GroupSummary("Resources").Role("trace")
		action.Positional(names...).Example("One", `{"id":"one"}`).Cursor(true)
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	names[0] = "wrong"
	retained.CLI("changed", "changed").Positional("wrong").Example("Two", `{}`)

	info := tool.Describe()
	info.CLI.Positional[0] = "changed"
	info.CLI.Examples[0].Input[0] = '['
	info.CLI.Group = "changed"
	info = tool.Describe()

	if info.Name != "exact.name" || info.CLI.Group != "resources" {
		t.Fatalf("metadata changed: %+v", info)
	}

	if info.CLI.Positional[0] != "id" || string(info.CLI.Examples[0].Input) != `{"id":"one"}` {
		t.Fatalf("owned storage changed: %+v", info.CLI)
	}
}

func TestCLIRejectsUnknownPositional(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("sample", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
		action.CLI("test", "sample").Positional("missing")
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatalf("invalid positional accepted: %v", tool.Err())
	}
}

// TestCLIRejectsEmptyGroup preserves declaration-error classification when
// structural metadata has no namespace, without executing its handler.
func TestCLIRejectsEmptyGroup(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("sample", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		t.Fatal("invalid declaration invoked handler")

		return mcp.NoInput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
		action.CLI("", "show")
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatalf("empty CLI group accepted: %v", tool.Err())
	}
}

// TestCLIRejectsNonobjectExample keeps the documented argument-object contract
// separate from typed parsing, policies, and application handler execution.
func TestCLIRejectsNonobjectExample(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("sample", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		t.Fatal("invalid example invoked handler")

		return mcp.NoInput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
		action.CLI("sample", "show").Example("Array", `[]`)
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatalf("nonobject CLI example accepted: %v", tool.Err())
	}
}

func TestCLIRejectsWrongExampleType(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("sample", func(_ context.Context, input cliInput) (cliInput, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[cliInput, cliInput]) {
		action.CLI("test", "sample").Example("Wrong", `{"id":42}`)
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatalf("invalid example accepted: %v", tool.Err())
	}
}

func TestCLIExamplesNeverExecuteContextualRules(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("sample", func(_ context.Context, input cliInput) (cliInput, error) {
		t.Fatal("example invoked handler")

		return input, nil
	}, func(action *mcp.ToolBuilder[cliInput, cliInput]) {
		action.CLI("test", "sample").Example("One", `{"id":"one"}`)
		action.Input(validate.Struct(func(object *validate.StructBuilder[cliInput]) {
			object.Check(func(context.Context, cliInput) error {
				t.Fatal("example executed contextual rule")

				return nil
			})
		}))
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestCLIExampleInvokesJSONHookButNotPolicyOrHandler distinguishes JSON input
// methods from schema Parse and request-time application execution.
func TestCLIExampleInvokesJSONHookButNotPolicyOrHandler(t *testing.T) {
	t.Parallel()

	policyCalls, handlerCalls := 0, 0
	tool := mcp.NewTool("hooked", func(context.Context, hookedCLIInput) (mcp.NoInput, error) {
		handlerCalls++

		return mcp.NoInput{}, nil
	}, func(action *mcp.ToolBuilder[hookedCLIInput, mcp.NoInput]) {
		action.Input(validate.Struct[hookedCLIInput]())
		action.CLI("sample", "decode").Example("Empty arguments", `{}`)
		action.Available(func(context.Context) (bool, error) {
			policyCalls++

			return true, nil
		})
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) || !errors.Is(tool.Err(), errExampleDecoding) {
		t.Fatalf("JSON decoding hook cause lost: %v", tool.Err())
	}

	if policyCalls+handlerCalls != 0 {
		t.Fatalf("construction ran policy %d or handler %d times", policyCalls, handlerCalls)
	}
}
