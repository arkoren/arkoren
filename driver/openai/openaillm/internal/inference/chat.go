package inference

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"arkoren.dev/core/service/llm"
)

// chatParams translates request into Chat Completions parameters and runs the
// native hook last. Reasoning parts of the history are dropped because the API
// has no place for them, and documents are not supported.
func (driver *Client) chatParams(request llm.Request) (openai.ChatCompletionNewParams, error) {
	if err := checkRequest(request); err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	params := openai.ChatCompletionNewParams{
		Model:           request.Model,
		ReasoningEffort: effort(request.Reasoning),
		ToolChoice:      chatToolChoice(request.ToolChoice),
	}

	if request.Instructions != "" {
		params.Messages = append(params.Messages, openai.SystemMessage(request.Instructions))
	}

	if request.MaxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(request.MaxTokens))
	}

	for _, message := range request.Messages {
		messages, err := chatMessages(message)

		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}

		params.Messages = append(params.Messages, messages...)
	}

	tools, err := driver.chatTools(request.Tools)

	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	params.Tools = tools

	if len(request.Output) > 0 {
		schema, err := parseSchema(request.Output, driver.Strict)

		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}

		params.ResponseFormat.OfJSONSchema = &shared.ResponseFormatJSONSchemaParam{
			JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
				Name:   outputSchemaName,
				Schema: schema,
				Strict: openai.Bool(driver.Strict),
			},
		}
	}

	if driver.ChatHook != nil {
		driver.ChatHook(&params)
	}

	return params, nil
}

// chatTools converts tool definitions with this driver's schema policy. It
// preserves declaration order and stops at the first invalid input schema.
func (driver *Client) chatTools(tools []llm.ToolSpec) ([]openai.ChatCompletionToolUnionParam, error) {
	var converted []openai.ChatCompletionToolUnionParam

	for _, tool := range tools {
		schema, err := toolSchema(tool, driver.Strict)

		if err != nil {
			return nil, err
		}

		function := shared.FunctionDefinitionParam{
			Name:       tool.Name,
			Parameters: schema,
			Strict:     openai.Bool(driver.Strict),
		}

		if tool.Description != "" {
			function.Description = openai.String(tool.Description)
		}

		converted = append(converted, openai.ChatCompletionFunctionTool(function))
	}

	return converted, nil
}

// chatToolChoice encodes the portable selection policy. Unspecified or unknown
// kinds leave the native union empty, retaining the endpoint's default choice.
func chatToolChoice(choice llm.ToolChoice) openai.ChatCompletionToolChoiceOptionUnionParam {
	var converted openai.ChatCompletionToolChoiceOptionUnionParam

	switch choice.Kind {
	case llm.ToolChoiceNone:
		converted.OfAuto = openai.String("none")
	case llm.ToolChoiceRequired:
		converted.OfAuto = openai.String("required")
	case llm.ToolChoiceNamed:
		converted.OfFunctionToolChoice = &openai.ChatCompletionNamedToolChoiceParam{
			Function: openai.ChatCompletionNamedToolChoiceFunctionParam{
				Name: choice.Name,
			},
		}
	}

	return converted
}

// chatMessages translates one message into Chat Completions messages. A user
// message becomes a user message plus one tool message per tool result, in
// part order; an assistant message becomes one message holding its text and
// tool calls.
func chatMessages(message llm.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	switch message.Role {
	case llm.RoleUser:
		return chatUserMessages(message)
	case llm.RoleAssistant:
		return chatAssistantMessage(message)
	}

	return nil, fmt.Errorf("openaillm: unknown role %q: %w", message.Role, llm.ErrInvalidRequest)
}

// chatUserMessages is the user half of [chatMessages]. A user message of a
// single text part is sent as a plain string, which older compatible servers
// require.
func chatUserMessages(message llm.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	var (
		messages []openai.ChatCompletionMessageParamUnion
		content  []openai.ChatCompletionContentPartUnionParam
	)

	flush := func() {
		switch {
		case len(content) == 0:
		case len(content) == 1 && content[0].OfText != nil:
			messages = append(messages, openai.UserMessage(content[0].OfText.Text))
		default:
			messages = append(messages, openai.UserMessage(content))
		}

		content = nil
	}

	for _, part := range message.Parts {
		switch part := part.(type) {
		case llm.Text:
			content = append(content, openai.TextContentPart(part.Text))
		case llm.Image:
			url, err := imageURL(part)

			if err != nil {
				return nil, err
			}

			content = append(content, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL: url,
			}))
		case llm.ToolResult:
			flush()

			messages = append(messages, openai.ToolMessage(part.Content, part.CallID))
		case llm.Document:
			return nil, fmt.Errorf("openaillm: documents are not supported in chat completions mode: %w", llm.ErrUnsupported)
		default:
			return nil, fmt.Errorf("openaillm: part %T: %w", part, llm.ErrUnsupported)
		}
	}

	flush()

	return messages, nil
}

// chatAssistantMessage is the assistant half of [chatMessages].
func chatAssistantMessage(message llm.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	var (
		text      strings.Builder
		toolCalls []openai.ChatCompletionMessageToolCallUnionParam
	)

	for _, part := range message.Parts {
		switch part := part.(type) {
		case llm.Text:
			text.WriteString(part.Text)
		case llm.ToolCall:
			toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: part.ID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      part.Name,
						Arguments: toolArguments(part),
					},
				},
			})
		case llm.Reasoning:
			// The API has no field for reasoning; it is dropped.
		default:
			return nil, fmt.Errorf("openaillm: part %T: %w", part, llm.ErrUnsupported)
		}
	}

	if text.Len() == 0 && len(toolCalls) == 0 {
		return nil, nil
	}

	assistant := openai.ChatCompletionAssistantMessageParam{
		ToolCalls: toolCalls,
	}

	if text.Len() > 0 {
		assistant.Content.OfString = openai.String(text.String())
	}

	return []openai.ChatCompletionMessageParamUnion{{
		OfAssistant: &assistant,
	}}, nil
}

// chatStop maps a Chat Completions finish reason to a stop reason. Servers
// that report "stop" together with tool calls still mean [llm.StopToolUse].
func chatStop(finish string, toolCalls, refused bool) llm.StopReason {
	switch {
	case finish == "content_filter":
		return llm.StopContentFilter
	case finish == "length":
		return llm.StopMaxTokens
	case refused:
		return llm.StopRefusal
	case toolCalls || finish == "tool_calls" || finish == "function_call":
		return llm.StopToolUse
	}

	return llm.StopEndTurn
}

// chatUsage converts the usage object of a completion or final chunk.
func chatUsage(usage openai.CompletionUsage) llm.Usage {
	return llm.Usage{
		InputTokens:      int(usage.PromptTokens),
		OutputTokens:     int(usage.CompletionTokens),
		CacheReadTokens:  int(usage.PromptTokensDetails.CachedTokens),
		CacheWriteTokens: int(usage.PromptTokensDetails.CacheWriteTokens),
		ReasoningTokens:  int(usage.CompletionTokensDetails.ReasoningTokens),
	}
}

// GenerateChat performs one turn on the Chat Completions API.
func (driver *Client) GenerateChat(ctx context.Context, request llm.Request) (llm.Response, error) {
	params, err := driver.chatParams(request)

	if err != nil {
		return llm.Response{}, err
	}

	reply, err := driver.SDK.Chat.Completions.New(ctx, params)

	if err != nil {
		return llm.Response{}, MapError(err, request.Output)
	}

	if len(reply.Choices) == 0 {
		return llm.Response{}, fmt.Errorf("openaillm: reply has no choices: %w", llm.ErrUnavailable)
	}

	choice := reply.Choices[0]
	response := llm.Response{
		ID:    reply.ID,
		Model: reply.Model,
		Message: llm.Message{
			Role: llm.RoleAssistant,
		},
		Usage: chatUsage(reply.Usage),
	}

	if choice.Message.Content != "" {
		response.Message.Parts = append(response.Message.Parts, llm.Text{
			Text: choice.Message.Content,
		})
	}

	if choice.Message.Refusal != "" {
		response.Message.Parts = append(response.Message.Parts, llm.Text{
			Text: choice.Message.Refusal,
		})
	}

	for _, call := range choice.Message.ToolCalls {
		response.Message.Parts = append(response.Message.Parts, llm.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: callArguments(call.Function.Arguments),
		})
	}

	response.StopReason = chatStop(choice.FinishReason, len(choice.Message.ToolCalls) > 0, choice.Message.Refusal != "")

	return response, nil
}

// streamedCall is a tool call being assembled from chunk deltas.
type streamedCall struct {
	// id is the provider's tool-call identifier assembled from stream deltas.
	id string

	// name is the tool selected by the provider for this streamed call.
	name string

	// arguments accumulates JSON argument fragments until the call completes.
	arguments strings.Builder
}

// chatStream accumulates the first choice of one Chat Completions stream.
// Deltas retain arrival order; completed calls are published in wire-index
// order. Builders are mutated in place and the accumulator must not be copied.
type chatStream struct {
	// result holds the latest nonempty identity and last reported token usage.
	result llm.Response

	// text collects content deltas independently from policy refusal content.
	text strings.Builder

	// refusal collects refusal fragments for the final message and stop reason.
	refusal strings.Builder

	// calls retains each call under its stream-local index until completion.
	calls map[int64]*streamedCall

	// finish is the last nonempty finish reason; empty means a truncated stream.
	finish string
}

// appendChunk records metadata and first-choice deltas, stopping immediately
// when yield declines an event. Usage-only chunks update metadata without
// changing the finish reason or producing an event.
func (stream *chatStream) appendChunk(chunk openai.ChatCompletionChunk, yield func(llm.Event, error) bool) bool {
	stream.result.ID = cmp.Or(chunk.ID, stream.result.ID)
	stream.result.Model = cmp.Or(chunk.Model, stream.result.Model)

	if chunk.JSON.Usage.Valid() {
		stream.result.Usage = chatUsage(chunk.Usage)
	}

	if len(chunk.Choices) == 0 {
		return true
	}

	choice := chunk.Choices[0]
	stream.finish = cmp.Or(choice.FinishReason, stream.finish)

	if choice.Delta.Content != "" {
		stream.text.WriteString(choice.Delta.Content)

		if !yield(llm.TextDelta{Text: choice.Delta.Content}, nil) {
			return false
		}
	}

	stream.refusal.WriteString(choice.Delta.Refusal)

	for _, delta := range choice.Delta.ToolCalls {
		if !stream.appendToolDelta(delta, yield) {
			return false
		}
	}

	return true
}

// appendToolDelta initializes a call from its first fragment and then appends
// nonempty arguments. A rejected start event prevents the argument delta from
// being consumed or yielded, matching early iterator exit within a chunk.
func (stream *chatStream) appendToolDelta(
	delta openai.ChatCompletionChunkChoiceDeltaToolCall,
	yield func(llm.Event, error) bool,
) bool {
	call := stream.calls[delta.Index]

	if call == nil {
		call = &streamedCall{id: delta.ID, name: delta.Function.Name}
		stream.calls[delta.Index] = call

		if !yield(llm.ToolCallStarted{ID: call.id, Name: call.name}, nil) {
			return false
		}
	}

	if delta.Function.Arguments == "" {
		return true
	}

	call.arguments.WriteString(delta.Function.Arguments)

	return yield(llm.ToolCallDelta{ID: call.id, Arguments: delta.Function.Arguments}, nil)
}

// response publishes the accumulated message after a finish reason arrives.
// Premature EOF wraps both unavailable and unexpected EOF; otherwise text,
// refusal, and index-sorted calls retain their established final-part order.
func (stream *chatStream) response() (llm.Response, error) {
	if stream.finish == "" {
		return llm.Response{}, fmt.Errorf("openaillm: stream ended before the response completed: %w: %w",
			llm.ErrUnavailable, io.ErrUnexpectedEOF)
	}

	response := stream.result

	if stream.text.Len() > 0 {
		response.Message.Parts = append(response.Message.Parts, llm.Text{Text: stream.text.String()})
	}

	if stream.refusal.Len() > 0 {
		response.Message.Parts = append(response.Message.Parts, llm.Text{Text: stream.refusal.String()})
	}

	for _, index := range slices.Sorted(maps.Keys(stream.calls)) {
		call := stream.calls[index]
		response.Message.Parts = append(response.Message.Parts, llm.ToolCall{
			ID: call.id, Name: call.name, Arguments: callArguments(call.arguments.String()),
		})
	}

	response.StopReason = chatStop(stream.finish, len(stream.calls) > 0, stream.refusal.Len() > 0)

	return response, nil
}

// StreamChat performs one turn on the Chat Completions API with
// stream_options.include_usage, yielding deltas as chunks arrive and the
// assembled response at the end. Tool call deltas are keyed by index on the
// wire; the first delta of an index carries the call's ID and name.
func (driver *Client) StreamChat(parent context.Context, request llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		params, err := driver.chatParams(request)

		if err != nil {
			yield(nil, err)

			return
		}

		if err := parent.Err(); err != nil {
			yield(nil, err)

			return
		}

		params.StreamOptions.IncludeUsage = openai.Bool(true)

		ctx, cancel := context.WithCancel(parent)
		defer cancel()

		stream := driver.SDK.Chat.Completions.NewStreaming(ctx, params)
		defer func() {
			// Stream/parent errors determine the result; Close only retires the reader.
			_ = stream.Close()
		}()

		assembled := chatStream{
			result: llm.Response{
				Message: llm.Message{
					Role: llm.RoleAssistant,
				},
			},
			calls: map[int64]*streamedCall{},
		}

		for stream.Next() {
			if !assembled.appendChunk(stream.Current(), yield) {
				return
			}
		}

		if err := parent.Err(); err != nil {
			yield(nil, err)

			return
		}

		if err := stream.Err(); err != nil {
			yield(nil, MapError(err, request.Output))

			return
		}

		response, err := assembled.response()

		if err != nil {
			yield(nil, err)

			return
		}

		yield(llm.Done{
			Response: response,
		}, nil)
	}
}
