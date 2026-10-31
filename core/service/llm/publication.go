package llm

import (
	"slices"
)

// cloneAgentTools detaches tool storage and rendered argument schemas without
// copying schema implementations, functions, or their captured application state.
func cloneAgentTools(tools []Tool) []Tool {
	cloned := slices.Clone(tools)

	for index := range cloned {
		cloned[index].schema = slices.Clone(cloned[index].schema)
	}

	return cloned
}

// publish detaches owned definition containers at the publication boundary.
func (agent Agent[Out]) publish() Agent[Out] {
	agent.tools = cloneAgentTools(agent.tools)
	agent.outputSchema = slices.Clone(agent.outputSchema)

	agent.err = agent.Err()

	return agent
}
