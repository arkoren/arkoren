package llm

import (
	"encoding/json/v2"
	"fmt"
)

// Event is one item of a streamed model turn. The set is sealed. A driver
// yields [TextDelta], [ReasoningDelta], [ToolCallStarted], [ToolCallDelta]
// and finally [Done]. [Model.RunStream] adds [ToolStarted], [ToolFinished]
// and [StepCompleted]. An Event encodes to JSON with a "type" tag and decodes
// back with [UnmarshalEvent].
// Stream events can contain model content; forward them only to the intended
// consumer, not automatic telemetry or unrestricted diagnostic capture.
//
//	var item llm.Event = llm.TextDelta{Text: "Hello"}
//	data, err := llm.MarshalEvent(item)
//	if err != nil {
//		return err
//	}
//	_ = data
type Event interface {
	// event seals the interface.
	event()
}

// TextDelta is a fragment of the reply text.
// Fragments are ordered by the stream; Done carries the complete response.
//
//	delta := llm.TextDelta{Text: "Hello"}
//	fmt.Print(delta.Text)
type TextDelta struct {
	// Text is the fragment.
	Text string
}

// ReasoningDelta is a fragment of the reasoning trace.
// It can contain sensitive provider/model content and is not a telemetry label.
//
//	delta := llm.ReasoningDelta{Text: "Consider the order status."}
//	_ = delta.Text // Send only to the intended reasoning-stream consumer.
type ReasoningDelta struct {
	// Text is the fragment.
	Text string
}

// ToolCallStarted announces a tool call whose arguments follow as
// [ToolCallDelta] events.
// Announcement is not execution or authorization; Model.Run separately guards
// the completed call against capability policy and its typed input schema.
//
//	started := llm.ToolCallStarted{ID: "call-1", Name: "lookup-order"}
//	fmt.Println(started.ID, started.Name)
type ToolCallStarted struct {
	// ID is the call's ID, shared with its deltas and its final [ToolCall].
	ID string

	// Name is the called tool.
	Name string
}

// ToolCallDelta is a fragment of a tool call's JSON arguments. Fragments are
// not valid JSON until concatenated.
// Group fragments by ID. Do not execute arguments before the final ToolCall is
// available and validated through the agent's registered Tool.
//
//	delta := llm.ToolCallDelta{ID: "call-1", Arguments: `{"number":`}
//	_ = delta.Arguments // Accumulate with later fragments for the same ID.
type ToolCallDelta struct {
	// ID is the [ToolCallStarted.ID] this fragment belongs to.
	ID string

	// Arguments is the fragment of the JSON text.
	Arguments string
}

// Done ends a successful stream and carries the complete turn, so a consumer
// that ignores the deltas still gets the result.
// A caller must let iteration finish normally to publish the standalone turn's
// response event; breaking at Done remains a consumer stop.
//
//	for item, err := range model.Stream(ctx, request) {
//		if err != nil {
//			return err
//		}
//		if done, ok := item.(llm.Done); ok {
//			_ = done.Response.Usage
//		}
//	}
type Done struct {
	// Response is the complete response, as [Driver.Generate] would return.
	Response Response
}

// ToolStarted reports that [Model.RunStream] is starting a tool call. It
// carries no arguments. It is emitted after concurrency admission, immediately
// before launching the guarded call, not while a call is merely queued. It does
// not imply that capability policy allowed the handler to run.
//
//	started := llm.ToolStarted{ID: "call-1", Name: "lookup-order"}
//	fmt.Println(started.ID, started.Name)
type ToolStarted struct {
	// ID is the [ToolCall.ID].
	ID string

	// Name is the tool's name.
	Name string
}

// ToolFinished reports that a tool call finished. It carries no result.
// Results are emitted in call order after the turn's admitted calls are joined.
// A permanently failed or abandoned turn need not emit finish events.
//
//	finished := llm.ToolFinished{ID: "call-1", Name: "lookup-order", IsError: false}
//	fmt.Println(finished.ID, finished.IsError)
type ToolFinished struct {
	// ID is the [ToolCall.ID].
	ID string

	// Name is the tool's name.
	Name string

	// IsError reports whether the model was sent an error result.
	IsError bool
}

// StepCompleted closes one model turn of [Model.RunStream], after its tool
// calls finished.
// Step counts model turns, not individual tool calls or validation retries
// inside a tool. Invalid final-output retries consume another model turn.
//
//	completed := llm.StepCompleted{Step: 1, StopReason: llm.StopEndTurn}
//	fmt.Println(completed.Step, completed.StopReason)
type StepCompleted struct {
	// Step is the 1-based number of the turn.
	Step int

	// StopReason is why the turn ended.
	StopReason StopReason

	// Usage counts the tokens of the turn.
	Usage Usage
}

// event marks text deltas as members of the sealed streaming event family.
func (TextDelta) event() {
}

// event marks reasoning deltas as members of the sealed streaming event family.
func (ReasoningDelta) event() {
}

// event marks tool-call starts as members of the sealed streaming event family.
func (ToolCallStarted) event() {
}

// event marks argument deltas as members of the sealed streaming event family.
func (ToolCallDelta) event() {
}

// event marks final responses as members of the sealed streaming event family.
func (Done) event() {
}

// event marks tool execution starts as members of the sealed event family.
func (ToolStarted) event() {
}

// event marks tool execution results as members of the sealed event family.
func (ToolFinished) event() {
}

// event marks completed agent steps as members of the sealed event family.
func (StepCompleted) event() {
}

// MarshalJSON encodes the event like [MarshalEvent], so json.Marshal on any
// event includes the "type" tag.
// Encoding does not redact text; select the receiving consumer explicitly.
//
//	data, err := (llm.TextDelta{Text: "Hello"}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event TextDelta) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// MarshalJSON encodes the event like [MarshalEvent].
// Its tagged JSON preserves reasoning text rather than classifying or logging it.
//
//	data, err := (llm.ReasoningDelta{Text: "Considering the order."}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event ReasoningDelta) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// MarshalJSON encodes the event like [MarshalEvent].
// The call identity is retained so the consumer can associate later fragments.
//
//	data, err := (llm.ToolCallStarted{ID: "call-1", Name: "lookup-order"}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event ToolCallStarted) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// MarshalJSON encodes the event like [MarshalEvent].
// Arguments is encoded as fragment text, not parsed as a complete JSON object.
//
//	data, err := (llm.ToolCallDelta{ID: "call-1", Arguments: `{"number":`}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event ToolCallDelta) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// MarshalJSON encodes the event like [MarshalEvent].
// A malformed raw part inside Response propagates its JSON encoding error.
//
//	data, err := (llm.Done{Response: response}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event Done) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// MarshalJSON encodes the event like [MarshalEvent].
// The wire event contains identity only, never the tool's argument payload.
//
//	data, err := (llm.ToolStarted{ID: "call-1", Name: "lookup-order"}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event ToolStarted) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// MarshalJSON encodes the event like [MarshalEvent].
// Only the failure flag is included; model-facing result text stays separate.
//
//	data, err := (llm.ToolFinished{ID: "call-1", Name: "lookup-order", IsError: true}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event ToolFinished) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// MarshalJSON encodes the event like [MarshalEvent].
// Turn usage and the raw stop-reason value are preserved without model content.
//
//	data, err := (llm.StepCompleted{Step: 1, StopReason: llm.StopEndTurn}).MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (event StepCompleted) MarshalJSON() ([]byte, error) {
	return MarshalEvent(event)
}

// eventJSON is the wire form of every [Event] variant.
type eventJSON struct {
	// Type selects the concrete event variant during decoding.
	Type string `json:"type"`
	// Text carries the incremental text or a tool's textual result.
	Text string `json:"text,omitzero"`
	// ID identifies the tool call associated with this event.
	ID string `json:"id,omitzero"`
	// Name names the tool being requested or executed.
	Name string `json:"name,omitzero"`
	// Arguments carries the incremental JSON argument text.
	Arguments string `json:"arguments,omitzero"`
	// Response is present only for the final model response.
	Response *Response `json:"response,omitzero"`
	// IsError distinguishes a failed tool result from a successful result.
	IsError bool `json:"is_error,omitzero"`
	// Step identifies the completed agent turn.
	Step int `json:"step,omitzero"`
	// Stop carries the model's reason for ending the turn.
	Stop StopReason `json:"stop_reason,omitzero"`
	// Usage reports the completed turn's token counts.
	Usage Usage `json:"usage,omitzero"`
}

// MarshalEvent encodes event as JSON with a "type" tag, for forwarding a
// stream to a client. It fails on an Event type defined outside this package.
//
//	data, err := llm.MarshalEvent(llm.TextDelta{Text: "Hel"})
func MarshalEvent(event Event) ([]byte, error) {
	var wire eventJSON

	switch event := event.(type) {
	case TextDelta:
		wire = eventJSON{
			Type: "text_delta",
			Text: event.Text,
		}
	case ReasoningDelta:
		wire = eventJSON{
			Type: "reasoning_delta",
			Text: event.Text,
		}
	case ToolCallStarted:
		wire = eventJSON{
			Type: "tool_call_started",
			ID:   event.ID,
			Name: event.Name,
		}
	case ToolCallDelta:
		wire = eventJSON{
			Type:      "tool_call_delta",
			ID:        event.ID,
			Arguments: event.Arguments,
		}
	case Done:
		wire = eventJSON{
			Type:     "done",
			Response: &event.Response,
		}
	case ToolStarted:
		wire = eventJSON{
			Type: "tool_started",
			ID:   event.ID,
			Name: event.Name,
		}
	case ToolFinished:
		wire = eventJSON{
			Type:    "tool_finished",
			ID:      event.ID,
			Name:    event.Name,
			IsError: event.IsError,
		}
	case StepCompleted:
		wire = eventJSON{
			Type:  "step_completed",
			Step:  event.Step,
			Stop:  event.StopReason,
			Usage: event.Usage,
		}
	default:
		return nil, fmt.Errorf("llm: cannot encode event of type %T", event)
	}

	return json.Marshal(wire)
}

// decodeEventJSON parses every typed wire field before event dispatch, preserving
// JSON error identity even when the tag is unknown. A Done event requires a
// nonnull response object; an empty object is valid, and incidental null response
// fields on other tags are allowed. Unknown members follow json.Unmarshal's
// default extension behavior. The returned wire value is ready for conversion.
func decodeEventJSON(data []byte) (eventJSON, error) {
	var wire eventJSON

	if err := json.Unmarshal(data, &wire); err != nil {
		return eventJSON{}, err
	}

	if wire.Type == "done" && wire.Response == nil {
		return eventJSON{}, fmt.Errorf("llm: done event has no response")
	}

	return wire, nil
}

// UnmarshalEvent decodes an event encoded by [MarshalEvent] into its concrete
// event type, preserving argument fragments, raw stop reasons, and usage fields.
// It returns nil with an error for malformed typed fields, an unknown type tag,
// or a Done event whose response is missing or null. An empty response object
// is valid. Unknown extension members are ignored; JSON errors remain available
// through errors.As rather than being replaced with event-type errors.
//
//	event, err := llm.UnmarshalEvent([]byte(`{"type":"text_delta","text":"Hello"}`))
//	if err != nil {
//		return err
//	}
//	if delta, ok := event.(llm.TextDelta); ok {
//		fmt.Print(delta.Text)
//	}
func UnmarshalEvent(data []byte) (Event, error) {
	wire, err := decodeEventJSON(data)

	if err != nil {
		return nil, err
	}

	switch wire.Type {
	case "text_delta":
		return TextDelta{Text: wire.Text}, nil
	case "reasoning_delta":
		return ReasoningDelta{Text: wire.Text}, nil
	case "tool_call_started":
		return ToolCallStarted{
			ID:   wire.ID,
			Name: wire.Name,
		}, nil
	case "tool_call_delta":
		return ToolCallDelta{
			ID:        wire.ID,
			Arguments: wire.Arguments,
		}, nil
	case "done":
		return Done{Response: *wire.Response}, nil
	case "tool_started":
		return ToolStarted{
			ID:   wire.ID,
			Name: wire.Name,
		}, nil
	case "tool_finished":
		return ToolFinished{
			ID:      wire.ID,
			Name:    wire.Name,
			IsError: wire.IsError,
		}, nil
	case "step_completed":
		return StepCompleted{
			Step:       wire.Step,
			StopReason: wire.Stop,
			Usage:      wire.Usage,
		}, nil
	default:
		return nil, fmt.Errorf("llm: unknown event type %q", wire.Type)
	}
}
