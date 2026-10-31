package llm

import (
	"cmp"
	"errors"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/llm/internal/genai"
	"arkoren.dev/core/service/telemetry"
)

// telemetryScope is the instrumentation scope of llm telemetry.
const telemetryScope = "arkoren.dev/core/service/llm"

// errInterrupted records an unfinished callback boundary without retaining its
// panic value, concrete type, or text. Panics still propagate without recovery.
var errInterrupted = errors.New("llm observation interrupted")

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{
		Err:  ErrToolAvailability,
		Name: "llm.ErrToolAvailability",
	},
	{
		Err:  ErrToolUnavailable,
		Name: "llm.ErrToolUnavailable",
	},
	{
		Err:  ErrInvalidRequest,
		Name: "llm.ErrInvalidRequest",
	},
	{
		Err:  ErrUnsupported,
		Name: "llm.ErrUnsupported",
	},
	{
		Err:  ErrNotFound,
		Name: "llm.ErrNotFound",
	},
	{
		Err:  ErrRateLimited,
		Name: "llm.ErrRateLimited",
	},
	{
		Err:  ErrUnavailable,
		Name: "llm.ErrUnavailable",
	},
	{
		Err:  ErrContextLength,
		Name: "llm.ErrContextLength",
	},
	{
		Err:  ErrRefused,
		Name: "llm.ErrRefused",
	},
	{
		Err:  ErrTruncated,
		Name: "llm.ErrTruncated",
	},
	{
		Err:  ErrMaxSteps,
		Name: "llm.ErrMaxSteps",
	},
	{
		Err:  ErrInvalidOutput,
		Name: "llm.ErrInvalidOutput",
	},
	{
		Err:  ErrUnknownTool,
		Name: "llm.ErrUnknownTool",
	},
	{
		Err:  ErrInvalidName,
		Name: "llm.ErrInvalidName",
	},
	{
		Err:  ErrPermanent,
		Name: "llm.ErrPermanent",
	},
}

// newInstruments returns the lazily built instruments of one facade.
func newInstruments() *telemetry.Instruments[genai.Instruments] {
	return telemetry.NewInstruments(genai.Build(telemetryScope))
}

// requestAttrs returns the span and metric attributes known before the call:
// the provider and the requested model.
func requestAttrs(config Config, model string) []telemetry.Attr {
	return []telemetry.Attr{
		telemetry.String(semconv.GenAIProviderName, cmp.Or(config.Provider, semconv.GenAIProviderOther)),
		telemetry.String(semconv.GenAIRequestModel, model),
	}
}

// recordUsage reports usage on op: the gen-ai token counts, and the counts
// gen-ai has no name for under arkoren.llm.
func recordUsage(op *genai.Op, usage Usage) {
	op.Usage(usage.InputTokens, usage.OutputTokens)
	op.SetAttrs(
		telemetry.Int(semconv.GenAIUsageCacheReadInputTokens, usage.CacheReadTokens),
		telemetry.Int(semconv.GenAIUsageCacheCreationInputTokens, usage.CacheWriteTokens),
		telemetry.Int(semconv.LLMUsageReasoningTokens, usage.ReasoningTokens))
}

// recordResponse reports a complete response on op.
func recordResponse(op *genai.Op, response Response) {
	op.Label(telemetry.String(semconv.GenAIResponseModel, response.Model))
	op.SetAttrs(
		telemetry.String(semconv.GenAIResponseID, response.ID),
		telemetry.Strings(semconv.GenAIResponseFinishReasons, []string{string(response.StopReason)}))
	recordUsage(op, response.Usage)
}
