package llm

import (
	"context"
	"testing"

	"arkoren.dev/foundation/validate"
)

// borrowedSchema preserves a real schema's sealed validation implementation
// while returning shared rendering storage. It exposes missing ownership copies
// at capture separately from copies at publication.
type borrowedSchema[T any] struct {
	// Schema supplies validation and parsing; only rendering is overridden.
	validate.Schema[T]

	// document is caller-owned and deliberately returned without a copy.
	document []byte
}

// JSONSchema returns the caller-owned buffer so tests can mutate it after an
// Output setting. Production declarations must detach this rendering storage.
//
// Example:
//
//	schema := borrowedSchema[string]{
//		Schema:   validate.String[string](),
//		document: []byte(`{"type":"string"}`),
//	}
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema borrowedSchema[T]) JSONSchema() ([]byte, error) {
	return schema.document, nil
}

// TestAgentPublicationOwnsOutputBytes distinguishes schema-buffer capture from
// retained-draft publication. Parsing still uses the supplied schema instance.
func TestAgentPublicationOwnsOutputBytes(t *testing.T) {
	t.Parallel()

	document := []byte(`{"type":"string"}`)
	var retained *AgentBuilder[string]
	agent := NewAgent[string]("summary", func(agent *AgentBuilder[string]) {
		retained = agent
		agent.Output(borrowedSchema[string]{
			Schema:   validate.String[string](),
			document: document,
		})

		document[0] = '!'
	})

	retained.definition.outputSchema[0] = '!'

	if string(agent.outputSchema) != `{"type":"string"}` {
		t.Fatalf("output schema = %s", agent.outputSchema)
	}

	output, err := agent.parse(t.Context(), `"summary"`)

	if err != nil || output != "summary" {
		t.Fatalf("parse = %q, %v", output, err)
	}
}

// TestAgentPublicationOwnsToolBytes checks both copies of tool schema storage,
// in addition to the caller's tool list and the retained draft's list.
func TestAgentPublicationOwnsToolBytes(t *testing.T) {
	t.Parallel()

	tool := NewTool("lookup", "Find a record.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			return "found", nil
		})

	tools := []Tool{tool}
	want := string(tool.schema)
	var retained *AgentBuilder[string]
	agent := NewAgent[string]("support", func(agent *AgentBuilder[string]) {
		retained = agent
		agent.Tools(tools...)

		tool.schema[0] = '!'
		tools[0] = Tool{}
	})

	retained.definition.tools[0].schema[0] = '!'
	retained.definition.tools[0] = Tool{}

	if agent.tools[0].Name() != "lookup" || string(agent.tools[0].schema) != want {
		t.Fatalf("published tool = %+v", agent.tools[0])
	}
}
