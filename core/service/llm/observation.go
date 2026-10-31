package llm

import (
	"context"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/service/llm/internal/genai"
)

// endInterrupted is deferred when an operation starts. completed becomes true
// immediately before normal finalization; otherwise this closes the span with
// one bounded class, including on panic or Goexit. It does not inspect recover.
func endInterrupted(ctx context.Context, operation *genai.Op, completed *bool) {
	if !*completed {
		operation.End(ctx, errInterrupted, errtype.Sentinel{
			Err: errInterrupted, Name: "llm.interrupted",
		})
	}
}
