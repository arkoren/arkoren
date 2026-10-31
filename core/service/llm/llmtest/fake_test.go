package llmtest_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
)

// ExampleFake scripts a reply and inspects the request that produced it.
func ExampleFake() {
	fake := llmtest.New()
	fake.Reply("Hello!")

	model := llm.New(fake, "test-model")

	response, err := model.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{llm.UserText("Say hello.")},
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(err, response.Message.Parts[0], len(fake.Generated()))
	// Output: <nil> {Hello!} 1
}

// scripted wraps a [llmtest.Fake] and queues an answer for every turn, so the
// conformance suite, which only has a driver factory, gets sensible replies
// from the fake's real code paths. Leftover steps are harmless: they are all
// the same function.
type scripted struct {
	// Fake records calls while the overrides produce prompt-dependent replies.
	*llmtest.Fake
}

// Generate queues the answer, then delegates to the fake.
func (driver scripted) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	driver.ReplyWith(answer)

	return driver.Fake.Generate(ctx, request)
}

// Stream queues the answer, then delegates to the fake.
func (driver scripted) Stream(ctx context.Context, request llm.Request) iter.Seq2[llm.Event, error] {
	driver.ReplyWith(answer)

	return driver.Fake.Stream(ctx, request)
}

// Embed rejects the suite's unknown model, then delegates to the fake.
func (driver scripted) Embed(ctx context.Context, request llm.EmbedRequest) (llm.Embeddings, error) {
	if request.Model == "arkoren-no-such-model" {
		return llm.Embeddings{}, llm.ErrNotFound
	}

	return driver.Fake.Embed(ctx, request)
}

// answer plays a model that behaves as the conformance suite expects.
func answer(request llm.Request) (llm.Response, error) {
	reply := func(parts ...llm.Part) (llm.Response, error) {
		return llm.Response{
			Message: llm.Message{
				Role:  llm.RoleAssistant,
				Parts: parts,
			},
			StopReason: llm.StopEndTurn,
		}, nil
	}

	last := request.Messages[len(request.Messages)-1]
	prompt := ""

	for _, part := range last.Parts {
		switch part := part.(type) {
		case llm.Text:
			prompt += part.Text
		case llm.ToolResult:
			if part.IsError {
				return reply(llm.Text{Text: "The lookup failed."})
			}

			return reply(llm.Text{Text: part.Content})
		case llm.Image:
			return reply(llm.Text{Text: "Red."})
		case llm.Document:
			return reply(llm.Text{Text: "Tangerine."})
		}
	}

	switch {
	case request.Model == "arkoren-no-such-model":
		return llm.Response{}, llm.ErrNotFound
	case request.Output != nil:
		return reply(llm.Text{Text: `{"name":"Ada Lovelace","age":36}`})
	case request.ToolChoice.Kind == llm.ToolChoiceNamed:
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Parts: []llm.Part{
					llm.ToolCall{
						ID:        "call-1",
						Name:      request.ToolChoice.Name,
						Arguments: []byte(`{"city":"Paris"}`),
					},
				},
			},
			StopReason: llm.StopToolUse,
		}, nil
	case request.Reasoning == llm.EffortLow && len(request.Messages) == 1:
		return reply(llm.Reasoning{
			Text:      "17 * 3 = 51",
			Signature: "sig-1",
		}, llm.Text{Text: "51"})
	case request.Reasoning == llm.EffortLow:
		// The earlier assistant turn must come back with its signature.
		for _, message := range request.Messages {
			for _, part := range message.Parts {
				if reasoning, ok := part.(llm.Reasoning); ok && reasoning.Signature != "sig-1" {
					return llm.Response{}, llm.ErrInvalidRequest
				}
			}
		}

		return reply(llm.Text{Text: "52"})
	case strings.HasPrefix(prompt, "Count"):
		return reply(llm.Text{Text: strings.Repeat("1 2 3 4 5 ", 10)})
	}

	return reply(llm.Text{Text: "Pineapple"})
}

// TestFakeConformance verifies Fake passes the driver conformance suite.
func TestFakeConformance(t *testing.T) {
	llmtest.Run(t, func(*testing.T) llm.Driver {
		return scripted{llmtest.New()}
	})
}

// TestFakeConformanceWithoutOptionalChecks verifies the Without options skip
// their checks: a driver that supports none of them still passes.
func TestFakeConformanceWithoutOptionalChecks(t *testing.T) {
	llmtest.Run(t, func(*testing.T) llm.Driver {
		return noOptional{scripted{llmtest.New()}}
	},
		llmtest.WithModel("m"), llmtest.WithEmbeddingModel("e"), llmtest.WithoutEmbeddings(),
		llmtest.WithoutImages(), llmtest.WithoutDocuments(), llmtest.WithoutReasoning(), llmtest.WithoutStructuredOutput())
}

// noOptional fails the requests the Without options promise are never made.
type noOptional struct {
	// scripted supplies required capabilities while overrides reject optional ones.
	scripted
}

// Generate fails requests that use an optional feature.
func (driver noOptional) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	if request.Output != nil || request.Reasoning == llm.EffortLow {
		return llm.Response{}, errors.New("optional feature used")
	}

	for _, part := range request.Messages[0].Parts {
		switch part.(type) {
		case llm.Image, llm.Document:
			return llm.Response{}, errors.New("optional feature used")
		}
	}

	return driver.scripted.Generate(ctx, request)
}

// Embed fails: the suite must not embed.
func (noOptional) Embed(context.Context, llm.EmbedRequest) (llm.Embeddings, error) {
	return llm.Embeddings{}, errors.New("embeddings used")
}

// userRequest returns a request with one user message.
func userRequest(text string) llm.Request {
	return llm.Request{
		Model:    "m",
		Messages: []llm.Message{llm.UserText(text)},
	}
}

// TestFakeRepliesInOrder verifies each turn consumes one scripted reply and
// fills the response ID and model.
func TestFakeRepliesInOrder(t *testing.T) {
	fake := llmtest.New()
	fake.Reply("one")
	fake.ReplyParts(llm.Text{Text: "two"})

	first, err := fake.Generate(t.Context(), userRequest("a"))

	if err != nil {
		t.Fatal(err)
	}

	second, err := fake.Generate(t.Context(), userRequest("b"))

	if err != nil {
		t.Fatal(err)
	}

	if first.Message.Parts[0] != (llm.Text{Text: "one"}) || second.Message.Parts[0] != (llm.Text{Text: "two"}) {
		t.Errorf("replies = %v, %v", first.Message.Parts, second.Message.Parts)
	}

	if first.ID == "" || first.ID == second.ID || first.Model != "m" || first.StopReason != llm.StopEndTurn {
		t.Errorf("first response = %+v, second ID = %q", first, second.ID)
	}
}

// TestFakeReplyToolCall verifies a scripted tool call stops with tool use.
func TestFakeReplyToolCall(t *testing.T) {
	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"city":"Paris"}`)

	response, err := fake.Generate(t.Context(), userRequest("a"))

	if err != nil {
		t.Fatal(err)
	}

	call, ok := response.Message.Parts[0].(llm.ToolCall)

	if !ok || call.ID != "call-1" || call.Name != "lookup" || string(call.Arguments) != `{"city":"Paris"}` {
		t.Errorf("part = %+v", response.Message.Parts[0])
	}

	if response.StopReason != llm.StopToolUse {
		t.Errorf("StopReason = %q, want %q", response.StopReason, llm.StopToolUse)
	}
}

// TestFakeReplyResponseKeepsFields verifies ReplyResponse returns the given
// response untouched where it set fields.
func TestFakeReplyResponseKeepsFields(t *testing.T) {
	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		ID:         "r1",
		Model:      "other",
		StopReason: llm.StopMaxTokens,
		Usage:      llm.Usage{OutputTokens: 3},
	})

	response, err := fake.Generate(t.Context(), userRequest("a"))

	if err != nil {
		t.Fatal(err)
	}

	if response.ID != "r1" || response.Model != "other" || response.StopReason != llm.StopMaxTokens || response.Usage.OutputTokens != 3 {
		t.Errorf("response = %+v", response)
	}
}

// TestFakeReplyWithSeesRequestAndFails verifies ReplyWith receives the
// request and its error is returned as is.
func TestFakeReplyWithSeesRequestAndFails(t *testing.T) {
	fake := llmtest.New()
	fake.ReplyWith(func(request llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrRateLimited
	})

	if _, err := fake.Generate(t.Context(), userRequest("a")); !errors.Is(err, llm.ErrRateLimited) {
		t.Errorf("error = %v, want llm.ErrRateLimited", err)
	}
}

// TestFakeRunsOutOfScript verifies a turn without a reply fails with
// ErrNoReply and is still recorded.
func TestFakeRunsOutOfScript(t *testing.T) {
	fake := llmtest.New()

	if _, err := fake.Generate(t.Context(), userRequest("a")); !errors.Is(err, llmtest.ErrNoReply) {
		t.Errorf("Generate() error = %v, want ErrNoReply", err)
	}

	for _, err := range fake.Stream(t.Context(), userRequest("a")) {
		if !errors.Is(err, llmtest.ErrNoReply) {
			t.Errorf("Stream() error = %v, want ErrNoReply", err)
		}
	}

	if got := len(fake.Generated()); got != 2 {
		t.Errorf("Generated() = %d requests, want 2", got)
	}
}

// TestFakeStreamSplitsTextAndEndsWithDone verifies the stream events for a
// mixed reply.
func TestFakeStreamSplitsTextAndEndsWithDone(t *testing.T) {
	fake := llmtest.New()
	fake.ReplyParts(
		llm.Reasoning{
			Text:      "thinking",
			Signature: "s",
		},
		llm.Text{Text: "one two three"},
		llm.ToolCall{
			ID:        "c1",
			Name:      "lookup",
			Arguments: []byte(`{}`),
		},
	)

	var events []llm.Event

	for event, err := range fake.Stream(t.Context(), userRequest("a")) {
		if err != nil {
			t.Fatal(err)
		}

		events = append(events, event)
	}

	want := []llm.Event{
		llm.ReasoningDelta{Text: "thinking"},
		llm.TextDelta{Text: "one "}, llm.TextDelta{Text: "two "}, llm.TextDelta{Text: "three"},
		llm.ToolCallStarted{
			ID:   "c1",
			Name: "lookup",
		}, llm.ToolCallDelta{
			ID:        "c1",
			Arguments: `{}`,
		},
	}

	if len(events) != len(want)+1 || !slices.Equal(events[:len(want)], want) {
		t.Fatalf("events = %#v", events)
	}

	done, ok := events[len(events)-1].(llm.Done)

	if !ok || done.Response.StopReason != llm.StopToolUse {
		t.Errorf("last event = %#v, want Done with tool use", events[len(events)-1])
	}
}

// TestFakeRejectsUnknownPart verifies a part outside the sealed set fails
// with ErrUnsupported before anything is recorded or consumed.
func TestFakeRejectsUnknownPart(t *testing.T) {
	fake := llmtest.New()
	fake.Reply("x")

	request := userRequest("a")
	request.Messages[0].Parts = append(request.Messages[0].Parts, customPart{})

	if _, err := fake.Generate(t.Context(), request); !errors.Is(err, llm.ErrUnsupported) {
		t.Errorf("error = %v, want llm.ErrUnsupported", err)
	}

	if len(fake.Generated()) != 0 {
		t.Error("the rejected request was recorded")
	}

	if _, err := fake.Generate(t.Context(), userRequest("a")); err != nil {
		t.Errorf("the reply was consumed by the rejected request: %v", err)
	}
}

// customPart is a part type the fake does not know.
type customPart struct {
	// Text supplies the sealed marker without making this an accepted part type.
	llm.Text
}

// TestFakeRejectsCanceledContext verifies a done context fails the call.
func TestFakeRejectsCanceledContext(t *testing.T) {
	fake := llmtest.New()
	fake.Reply("x")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := fake.Generate(ctx, userRequest("a")); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// TestFakeFailureInjection verifies FailNext, FailWith and Reset.
func TestFakeFailureInjection(t *testing.T) {
	fake := llmtest.New()
	fake.Reply("x")
	fake.FailNext(llm.ErrUnavailable)

	if _, err := fake.Generate(t.Context(), userRequest("a")); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("FailNext error = %v", err)
	}

	if _, err := fake.Generate(t.Context(), userRequest("a")); err != nil {
		t.Fatalf("second call error = %v, want the reply FailNext did not consume", err)
	}

	fake.FailWith(llm.ErrRateLimited)
	fake.Reply("y")

	for range 2 {
		if _, err := fake.Generate(t.Context(), userRequest("a")); !errors.Is(err, llm.ErrRateLimited) {
			t.Fatalf("FailWith error = %v", err)
		}

		if _, err := fake.Embed(t.Context(), llm.EmbedRequest{Inputs: []string{"a"}}); !errors.Is(err, llm.ErrRateLimited) {
			t.Fatalf("FailWith embed error = %v", err)
		}
	}

	fake.Reset()

	if _, err := fake.Generate(t.Context(), userRequest("a")); !errors.Is(err, llmtest.ErrNoReply) {
		t.Errorf("after Reset error = %v, want ErrNoReply", err)
	}

	if len(fake.Generated()) != 1 {
		t.Errorf("Generated() = %d, want only the call after Reset", len(fake.Generated()))
	}
}

// TestFakeAssertions verifies the assertions pass on a match and report on a
// mismatch.
func TestFakeAssertions(t *testing.T) {
	fake := llmtest.New()
	fake.Reply("x")

	request := userRequest("hello")
	request.Instructions = "be brief"

	if _, err := fake.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	fake.AssertGenerated(t)
	fake.AssertGenerated(t, func(request llm.Request) bool {
		return request.Instructions == "be brief"
	}, func(request llm.Request) bool {
		return request.Model == "m"
	})
	fake.AssertNotGenerated(t, func(request llm.Request) bool {
		return request.Instructions == "other"
	})

	recorder := &recordingTB{TB: t}

	fake.AssertNotGenerated(recorder)
	fake.AssertGenerated(recorder, func(llm.Request) bool {
		return false
	})

	if recorder.errors != 2 {
		t.Errorf("failed assertions reported %d errors, want 2", recorder.errors)
	}
}

// recordingTB counts Errorf calls instead of failing the test.
type recordingTB struct {
	// TB supplies the remaining test methods while Errorf counts failures locally.
	testing.TB
	// errors counts assertion failures without failing the enclosing test.
	errors int
}

// Errorf counts the failure.
func (recorder *recordingTB) Errorf(string, ...any) {
	recorder.errors++
}

// TestFakeRecordsCopyOfRequest verifies later changes to the caller's slices
// do not alter the record.
func TestFakeRecordsCopyOfRequest(t *testing.T) {
	fake := llmtest.New()
	fake.Reply("x")

	request := userRequest("hello")
	request.Output = []byte(`{}`)

	if _, err := fake.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	request.Messages[0] = llm.UserText("changed")
	request.Output[0] = 'X'

	fake.Generated()[0].Messages[0] = llm.UserText("also changed")

	fake.AssertGenerated(t, func(r llm.Request) bool {
		return r.Messages[0].Parts[0] == (llm.Text{Text: "hello"}) && string(r.Output) == `{}`
	})
}

// TestFakeEmbedIsDeterministic verifies equal inputs give equal unit vectors
// of the requested size, and different inputs differ.
func TestFakeEmbedIsDeterministic(t *testing.T) {
	fake := llmtest.New()

	first, err := fake.Embed(t.Context(), llm.EmbedRequest{
		Inputs:     []string{"a", "b"},
		Dimensions: 16,
	})

	if err != nil {
		t.Fatal(err)
	}

	second, err := fake.Embed(t.Context(), llm.EmbedRequest{
		Inputs:     []string{"b"},
		Dimensions: 16,
	})

	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(first.Vectors[1], second.Vectors[0]) || slices.Equal(first.Vectors[0], first.Vectors[1]) {
		t.Error("equal inputs must give equal vectors and different inputs different ones")
	}

	var norm float64

	for _, component := range first.Vectors[0] {
		norm += float64(component) * float64(component)
	}

	if len(first.Vectors[0]) != 16 || norm < 0.999 || norm > 1.001 {
		t.Errorf("vector has %d components and squared norm %v, want 16 and 1", len(first.Vectors[0]), norm)
	}

	if len(fake.Embedded()) != 2 {
		t.Errorf("Embedded() = %d requests, want 2", len(fake.Embedded()))
	}
}

// TestFakeEmbedDefaultsAndErrors verifies the default size, EmbedWith and the
// negative dimensions error.
func TestFakeEmbedDefaultsAndErrors(t *testing.T) {
	fake := llmtest.New()

	embeddings, err := fake.Embed(t.Context(), llm.EmbedRequest{Inputs: []string{"a"}})

	if err != nil || len(embeddings.Vectors[0]) != 8 {
		t.Fatalf("default embedding = %v, %v, want 8 components", embeddings, err)
	}

	if _, err := fake.Embed(t.Context(), llm.EmbedRequest{
		Inputs:     []string{"a"},
		Dimensions: -1,
	}); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("negative dimensions error = %v, want llm.ErrInvalidRequest", err)
	}

	fake.EmbedWith(func(request llm.EmbedRequest) (llm.Embeddings, error) {
		return llm.Embeddings{Vectors: [][]float32{{1, 2, 3}}}, nil
	})

	embeddings, err = fake.Embed(t.Context(), llm.EmbedRequest{Inputs: []string{"a"}})

	if err != nil || !slices.Equal(embeddings.Vectors[0], []float32{1, 2, 3}) {
		t.Errorf("EmbedWith result = %v, %v", embeddings, err)
	}
}

// TestFakeWorksBehindModel verifies the fake drives the llm facade and
// streaming through it ends with Done.
func TestFakeWorksBehindModel(t *testing.T) {
	fake := llmtest.New()
	fake.Reply("hi there")
	fake.ReplyToolCall("lookup", `{"a":1}`)

	model := llm.New(fake, "m")

	var streamed string

	for event, err := range model.Stream(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("a")}}) {
		if err != nil {
			t.Fatal(err)
		}

		if delta, ok := event.(llm.TextDelta); ok {
			streamed += delta.Text
		}
	}

	if streamed != "hi there" {
		t.Errorf("streamed = %q", streamed)
	}

	response, err := model.Generate(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("b")}})

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(response.Message)

	if err != nil || !strings.Contains(string(encoded), "lookup") {
		t.Errorf("message = %s, %v", encoded, err)
	}
}
