package inference

import (
	"context"
	"fmt"
	"io"
	"iter"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"arkoren.dev/core/service/llm"
)

// outputSchemaName is the name sent with every structured output schema.
const outputSchemaName = "output"

// reasoningSeparator joins the item ID and the encrypted content of a
// reasoning item inside [llm.Reasoning.Signature]. Item IDs never contain it
// and the encrypted content may, so the signature is split at the first one.
const reasoningSeparator = ":"

// responsesParams translates request into Responses API parameters, always
// stateless by default (store=false) and requesting encrypted reasoning content.
// The native hook runs last and can deliberately override either setting.
func (driver *Client) responsesParams(request llm.Request) (responses.ResponseNewParams, error) {
	if err := checkRequest(request); err != nil {
		return responses.ResponseNewParams{}, err
	}

	params := responses.ResponseNewParams{
		Model:      request.Model,
		Store:      openai.Bool(false),
		Include:    []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		ToolChoice: responsesToolChoice(request.ToolChoice),
	}

	if request.Instructions != "" {
		params.Instructions = openai.String(request.Instructions)
	}

	if request.MaxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(request.MaxTokens))
	}

	params.Reasoning.Effort = effort(request.Reasoning)

	input := make(responses.ResponseInputParam, 0, len(request.Messages))

	for _, message := range request.Messages {
		items, err := responsesItems(message)

		if err != nil {
			return responses.ResponseNewParams{}, err
		}

		input = append(input, items...)
	}

	params.Input.OfInputItemList = input

	tools, err := driver.responsesTools(request.Tools)

	if err != nil {
		return responses.ResponseNewParams{}, err
	}

	params.Tools = tools

	if len(request.Output) > 0 {
		schema, err := parseSchema(request.Output, driver.Strict)

		if err != nil {
			return responses.ResponseNewParams{}, err
		}

		params.Text.Format.OfJSONSchema = &responses.ResponseFormatTextJSONSchemaConfigParam{
			Name:   outputSchemaName,
			Schema: schema,
			Strict: openai.Bool(driver.Strict),
		}
	}

	if driver.ResponsesHook != nil {
		driver.ResponsesHook(&params)
	}

	return params, nil
}

// responsesTools converts ordered tool definitions using the driver's schema
// policy. The first malformed schema fails before the native request hook runs.
func (driver *Client) responsesTools(tools []llm.ToolSpec) ([]responses.ToolUnionParam, error) {
	var converted []responses.ToolUnionParam

	for _, tool := range tools {
		schema, err := toolSchema(tool, driver.Strict)

		if err != nil {
			return nil, err
		}

		function := responses.FunctionToolParam{
			Name: tool.Name, Parameters: schema, Strict: openai.Bool(driver.Strict),
		}

		if tool.Description != "" {
			function.Description = openai.String(tool.Description)
		}

		converted = append(converted, responses.ToolUnionParam{OfFunction: &function})
	}

	return converted, nil
}

// responsesToolChoice encodes a portable selection policy in the native union.
// Unspecified and unknown choices retain the endpoint's default behavior.
func responsesToolChoice(choice llm.ToolChoice) responses.ResponseNewParamsToolChoiceUnion {
	var converted responses.ResponseNewParamsToolChoiceUnion

	switch choice.Kind {
	case llm.ToolChoiceNone:
		converted.OfToolChoiceMode = openai.Opt(responses.ToolChoiceOptionsNone)
	case llm.ToolChoiceRequired:
		converted.OfToolChoiceMode = openai.Opt(responses.ToolChoiceOptionsRequired)
	case llm.ToolChoiceNamed:
		converted.OfFunctionTool = &responses.ToolChoiceFunctionParam{Name: choice.Name}
	}

	return converted
}

// toolSchema returns the parameters schema of tool; a tool without a schema
// takes no arguments.
func toolSchema(tool llm.ToolSpec, strict bool) (map[string]any, error) {
	if len(tool.InputSchema) == 0 {
		return parseSchema([]byte(`{"type":"object","properties":{}}`), strict)
	}

	return parseSchema(tool.InputSchema, strict)
}

// responsesItems translates one message into Responses input items, keeping
// the order of its parts. Text and media of a user message share one message
// item; a tool result ends it and becomes its own item.
func responsesItems(message llm.Message) ([]responses.ResponseInputItemUnionParam, error) {
	switch message.Role {
	case llm.RoleUser, llm.RoleAssistant:
	default:
		return nil, fmt.Errorf("openaillm: unknown role %q: %w", message.Role, llm.ErrInvalidRequest)
	}

	input := responsesInput{}

	for _, part := range message.Parts {
		if err := input.appendPart(part, message.Role); err != nil {
			return nil, err
		}
	}

	input.flush()

	return input.items, nil
}

// responsesInput coalesces content within one history message. Standalone
// items flush pending user media and assistant text before being appended, so
// native input boundaries preserve the original tool and reasoning order.
type responsesInput struct {
	// items holds already flushed messages and standalone protocol items.
	items []responses.ResponseInputItemUnionParam

	// content buffers text/media that the protocol represents as user content.
	content responses.ResponseInputMessageContentListParam

	// text buffers adjacent assistant text; the accumulator must not be copied.
	text strings.Builder
}

// flush publishes pending user content before pending assistant text, matching
// the established media conversion order. Repeated flushes emit no empty items.
func (input *responsesInput) flush() {
	if len(input.content) > 0 {
		input.items = append(input.items, responses.ResponseInputItemParamOfMessage(
			input.content, responses.EasyInputMessageRoleUser))
		input.content = nil
	}

	if input.text.Len() > 0 {
		input.items = append(input.items, responses.ResponseInputItemParamOfMessage(
			input.text.String(), responses.EasyInputMessageRoleAssistant))
		input.text.Reset()
	}
}

// appendPart converts one history part, with role already validated by
// responsesItems. Unsupported parts and invalid media fail locally; foreign
// reasoning still ends the previous text item even though it is not replayed.
func (input *responsesInput) appendPart(part llm.Part, role llm.Role) error {
	switch part := part.(type) {
	case llm.Text:
		if role == llm.RoleAssistant {
			input.text.WriteString(part.Text)

			return nil
		}

		input.content = append(input.content, responses.ResponseInputContentParamOfInputText(part.Text))
	case llm.Image, llm.Document:
		content, err := responsesMedia(part)

		if err != nil {
			return err
		}

		input.content = append(input.content, content)
	case llm.ToolResult, llm.ToolCall, llm.Reasoning:
		input.flush()
		input.items = append(input.items, responsesStandaloneItems(part)...)
	default:
		return fmt.Errorf("openaillm: part %T: %w", part, llm.ErrUnsupported)
	}

	return nil
}

// responsesMedia encodes an image or document as native input content. Inline
// files require bytes and a media type; PDFs retain the .pdf filename suffix.
// Callers route only image/document parts here; other types are unsupported.
func responsesMedia(part llm.Part) (responses.ResponseInputContentUnionParam, error) {
	switch part := part.(type) {
	case llm.Image:
		url, err := imageURL(part)

		if err != nil {
			return responses.ResponseInputContentUnionParam{}, err
		}

		return responses.ResponseInputContentUnionParam{OfInputImage: &responses.ResponseInputImageParam{
			Detail: responses.ResponseInputImageDetailAuto, ImageURL: openai.String(url),
		}}, nil
	case llm.Document:
		if len(part.Data) == 0 || part.MediaType == "" {
			return responses.ResponseInputContentUnionParam{}, fmt.Errorf(
				"openaillm: document needs data and a media type: %w", llm.ErrInvalidRequest)
		}

		filename := "document"

		if part.MediaType == "application/pdf" {
			filename = "document.pdf"
		}

		return responses.ResponseInputContentUnionParam{OfInputFile: &responses.ResponseInputFileParam{
			FileData: openai.String(dataURL(part.MediaType, part.Data)), Filename: openai.String(filename),
		}}, nil
	}

	return responses.ResponseInputContentUnionParam{}, fmt.Errorf("openaillm: part %T: %w", part, llm.ErrUnsupported)
}

// responsesStandaloneItems converts tool results, calls, and signed reasoning
// after pending message content has been flushed. Unsigned reasoning produces
// no item because it cannot be replayed in a stateless native conversation.
func responsesStandaloneItems(part llm.Part) []responses.ResponseInputItemUnionParam {
	switch part := part.(type) {
	case llm.ToolResult:
		item := responses.ResponseInputItemParamOfFunctionCallOutput(part.Content)
		item.OfFunctionCallOutput.CallID = openai.String(part.CallID)

		return []responses.ResponseInputItemUnionParam{item}
	case llm.ToolCall:
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfFunctionCall(toolArguments(part), part.ID, part.Name),
		}
	case llm.Reasoning:
		id, encrypted, ok := strings.Cut(part.Signature, reasoningSeparator)

		if !ok {
			return nil
		}

		item := responses.ResponseInputItemParamOfReasoning(id, []responses.ResponseReasoningItemSummaryParam{})
		item.OfReasoning.EncryptedContent = openai.String(encrypted)

		return []responses.ResponseInputItemUnionParam{item}
	}

	return nil
}

// fromResponse translates a Responses API reply into an [llm.Response]. A
// reply whose status is failed becomes an error.
func fromResponse(reply *responses.Response) (llm.Response, error) {
	if reply.Status == responses.ResponseStatusFailed {
		return llm.Response{}, mapFailure(string(reply.Error.Code), reply.Error.Message)
	}

	response := llm.Response{
		ID:         reply.ID,
		Model:      reply.Model,
		StopReason: llm.StopEndTurn,
		Message: llm.Message{
			Role: llm.RoleAssistant,
		},
		Usage: llm.Usage{
			InputTokens:      int(reply.Usage.InputTokens),
			OutputTokens:     int(reply.Usage.OutputTokens),
			CacheReadTokens:  int(reply.Usage.InputTokensDetails.CachedTokens),
			CacheWriteTokens: int(reply.Usage.InputTokensDetails.CacheWriteTokens),
			ReasoningTokens:  int(reply.Usage.OutputTokensDetails.ReasoningTokens),
		},
	}

	for _, item := range reply.Output {
		appendResponseOutput(&response, item)
	}

	switch reply.IncompleteDetails.Reason {
	case "content_filter":
		response.StopReason = llm.StopContentFilter
	case "max_output_tokens":
		response.StopReason = llm.StopMaxTokens
	}

	return response, nil
}

// appendResponseOutput appends one supported native output item and updates
// the content-derived stop reason. Refusal outranks tool use regardless of
// item order; fromResponse subsequently applies incomplete-response reasons.
func appendResponseOutput(response *llm.Response, item responses.ResponseOutputItemUnion) {
	switch item.Type {
	case "message":
		parts, stop := responsesText(item.Content)
		response.Message.Parts = append(response.Message.Parts, parts...)

		if stop == llm.StopRefusal {
			response.StopReason = stop
		}
	case "reasoning":
		response.Message.Parts = append(response.Message.Parts, responsesReasoning(item)...)
	case "function_call":
		if response.StopReason != llm.StopRefusal {
			response.StopReason = llm.StopToolUse
		}

		response.Message.Parts = append(response.Message.Parts, llm.ToolCall{
			ID: item.CallID, Name: item.Name, Arguments: callArguments(item.Arguments.OfString),
		})
	}
}

// responsesText converts message content in wire order and identifies a
// refusal independently of its text, which may be empty. Unknown native
// content variants are ignored, as they have no portable representation.
func responsesText(content []responses.ResponseOutputMessageContentUnion) ([]llm.Part, llm.StopReason) {
	var parts []llm.Part
	stop := llm.StopEndTurn

	for _, item := range content {
		switch item.Type {
		case "output_text":
			parts = append(parts, llm.Text{Text: item.Text})
		case "refusal":
			stop = llm.StopRefusal
			parts = append(parts, llm.Text{Text: item.Refusal})
		}
	}

	return parts, stop
}

// responsesReasoning preserves paragraph-separated summaries and opaque
// encrypted state. Empty reasoning items produce no part; encrypted state is
// joined with its item ID so responsesStandaloneItems can replay it later.
func responsesReasoning(item responses.ResponseOutputItemUnion) []llm.Part {
	var summary []string

	for _, part := range item.Summary {
		summary = append(summary, part.Text)
	}

	reasoning := llm.Reasoning{Text: strings.Join(summary, "\n\n")}

	if item.EncryptedContent != "" {
		reasoning.Signature = item.ID + reasoningSeparator + item.EncryptedContent
	}

	if reasoning.Text == "" && reasoning.Signature == "" {
		return nil
	}

	return []llm.Part{reasoning}
}

// GenerateResponses performs one turn on the Responses API.
func (driver *Client) GenerateResponses(ctx context.Context, request llm.Request) (llm.Response, error) {
	params, err := driver.responsesParams(request)

	if err != nil {
		return llm.Response{}, err
	}

	reply, err := driver.SDK.Responses.New(ctx, params)

	if err != nil {
		return llm.Response{}, MapError(err, request.Output)
	}

	return fromResponse(reply)
}

// StreamResponses performs one turn on the Responses API, yielding deltas as
// the server sends them and the complete response from the terminal event.
// Tool call deltas are keyed by item ID on the wire; they are re-keyed by call
// ID here, the ID the final [llm.ToolCall] carries.
func (driver *Client) StreamResponses(parent context.Context, request llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		params, err := driver.responsesParams(request)

		if err != nil {
			yield(nil, err)

			return
		}

		if err := parent.Err(); err != nil {
			yield(nil, err)

			return
		}

		ctx, cancel := context.WithCancel(parent)
		defer cancel()

		stream := driver.SDK.Responses.NewStreaming(ctx, params)
		defer func() {
			// Stream/parent errors determine the result; Close only retires the reader.
			_ = stream.Close()
		}()

		callIDs := map[string]string{}

		for stream.Next() {
			out, err := responsesStreamEvent(stream.Current(), callIDs)

			if err != nil {
				yield(nil, err)

				return
			}

			if out == nil {
				continue
			}

			_, done := out.(llm.Done)

			if !yield(out, nil) || done {
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

		yield(nil, fmt.Errorf("openaillm: stream ended before the response completed: %w: %w",
			llm.ErrUnavailable, io.ErrUnexpectedEOF))
	}
}

// responsesStreamEvent converts terminal snapshots and failures separately
// from incremental deltas. A successful terminal snapshot yields Done; nil
// means an unrepresented native event and does not complete the stream.
func responsesStreamEvent(event responses.ResponseStreamEventUnion, callIDs map[string]string) (llm.Event, error) {
	switch event.Type {
	case "response.completed", "response.incomplete":
		response, err := fromResponse(&event.Response)

		if err != nil {
			return nil, err
		}

		return llm.Done{Response: response}, nil
	case "response.failed":
		return nil, mapFailure(string(event.Response.Error.Code), event.Response.Error.Message)
	case "error":
		return nil, mapFailure(event.Code, event.Message)
	}

	return responsesStreamDelta(event, callIDs), nil
}

// responsesStreamDelta maps incremental native events and records item-to-call
// IDs as starts arrive. Argument deltas without a known start retain their
// native item ID; known empty call IDs are not replaced by the fallback.
func responsesStreamDelta(event responses.ResponseStreamEventUnion, callIDs map[string]string) llm.Event {
	switch event.Type {
	case "response.output_text.delta":
		return llm.TextDelta{Text: event.Delta}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return llm.ReasoningDelta{Text: event.Delta}
	case "response.output_item.added":
		if event.Item.Type != "function_call" {
			return nil
		}

		callIDs[event.Item.ID] = event.Item.CallID

		return llm.ToolCallStarted{ID: event.Item.CallID, Name: event.Item.Name}
	case "response.function_call_arguments.delta":
		id, ok := callIDs[event.ItemID]

		if !ok {
			id = event.ItemID
		}

		return llm.ToolCallDelta{ID: id, Arguments: event.Delta}
	}

	return nil
}
