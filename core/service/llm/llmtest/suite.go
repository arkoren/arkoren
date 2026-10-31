package llmtest

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/core/service/llm"
)

// Option changes one conformance suite setting.
type Option func(*suite)

// WithModel sets the model ID the suite sends requests to. The default is
// "test-model".
func WithModel(id string) Option {
	return func(s *suite) {
		s.model = id
	}
}

// WithEmbeddingModel sets the model ID the suite embeds with. The default is
// "test-embedding-model".
func WithEmbeddingModel(id string) Option {
	return func(s *suite) {
		s.embeddingModel = id
	}
}

// WithoutEmbeddings skips the embedding checks, for a vendor or model that
// returns [llm.ErrUnsupported] from Embed.
func WithoutEmbeddings() Option {
	return func(s *suite) {
		s.embeddings = false
	}
}

// WithoutImages skips the [llm.Image] input check, for a model that cannot
// look at pictures.
func WithoutImages() Option {
	return func(s *suite) {
		s.images = false
	}
}

// WithoutDocuments skips the [llm.Document] input check, for a vendor or
// model that cannot read files.
func WithoutDocuments() Option {
	return func(s *suite) {
		s.documents = false
	}
}

// WithoutReasoning skips the reasoning round-trip check, for a model that
// does not reason.
func WithoutReasoning() Option {
	return func(s *suite) {
		s.reasoning = false
	}
}

// WithoutStructuredOutput skips the [llm.Request.Output] check, for a vendor
// or model that cannot enforce a JSON schema.
func WithoutStructuredOutput() Option {
	return func(s *suite) {
		s.structured = false
	}
}

// suite holds the settings and driver factory of one [Run].
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) llm.Driver

	// model and embeddingModel are the IDs requests are sent to.
	model, embeddingModel string

	// embeddings, images, documents, reasoning and structured say which
	// optional checks run.
	embeddings, images, documents, reasoning, structured bool
}

// unknownModel is a model ID no vendor serves.
const unknownModel = "arkoren-no-such-model"

// Prompts and expected answers. Each expected answer is a distinctive word
// that is looked up case-insensitively in the reply, so wording around it does
// not matter.
const (
	textPrompt     = "Reply with exactly the single word: pineapple"
	longPrompt     = "Count from 1 to 100, separated by spaces, and write nothing else."
	toolPrompt     = "What is the secret code for the city Paris? You must call the lookup_code tool."
	toolCode       = "7421"
	imagePrompt    = "What color is this image? Answer with one word."
	documentPrompt = "What is the secret word in the attached document? Answer with one word."
	documentWord   = "tangerine"
	reasonPrompt   = "What is 17 * 3? Answer with the number only."
	reasonFollowUp = "Add 1 to that. Answer with the number only."
)

// lookupTool is the tool every tool check offers.
var lookupTool = llm.ToolSpec{
	Name:        "lookup_code",
	Description: "Looks up the secret code of a city.",
	InputSchema: []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`),
}

// personSchema is the output schema of the structured output check.
var personSchema = []byte(`{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"],"additionalProperties":false}`)

// Run checks that the drivers created by newDriver follow the [llm.Driver]
// contract: they generate and stream text, round-trip tool calls, enforce
// structured output, return and accept reasoning traces unchanged, look at
// images and documents, embed in order and in batches, reject unknown parts
// with [llm.ErrUnsupported] and unknown models with [llm.ErrNotFound], and
// honor context cancellation.
//
// newDriver is called once per subtest. The suite sends real requests, so a
// vendor driver needs a model that can pass the checks; the Without options
// turn off the checks a vendor cannot pass. The assertions ask for fixed
// words and compare case-insensitively, because real models are not
// deterministic. The embedding check also asks for 64 dimensions, which the
// embedding model must support.
//
// Example
//
//	func TestDriver(t *testing.T) {
//		llmtest.Run(t, func(t *testing.T) llm.Driver {
//			return openaillm.New(os.Getenv("OPENAI_API_KEY"))
//		}, llmtest.WithModel("gpt-5.2"), llmtest.WithEmbeddingModel("text-embedding-3-small"))
//	}
func Run(t *testing.T, newDriver func(t *testing.T) llm.Driver, opts ...Option) {
	s := &suite{
		newDriver:      newDriver,
		model:          "test-model",
		embeddingModel: "test-embedding-model",
		embeddings:     true,
		images:         true,
		documents:      true,
		reasoning:      true,
		structured:     true,
	}

	for _, opt := range opts {
		opt(s)
	}

	t.Run("GeneratesText", s.testGeneratesText)
	t.Run("StreamMatchesDone", s.testStreamMatchesDone)
	t.Run("StreamBreakStopsCleanly", s.testStreamBreakStopsCleanly)
	t.Run("ToolCallRoundTrip", s.testToolCallRoundTrip)
	t.Run("ToolResultError", s.testToolResultError)
	t.Run("StreamsToolCall", s.testStreamsToolCall)
	t.Run("RejectsUnknownPart", s.testRejectsUnknownPart)
	t.Run("RejectsUnknownModel", s.testRejectsUnknownModel)
	t.Run("RejectsCanceledContext", s.testRejectsCanceledContext)

	if s.structured {
		t.Run("StructuredOutput", s.testStructuredOutput)
	}

	if s.reasoning {
		t.Run("ReasoningRoundTrip", s.testReasoningRoundTrip)
	}

	if s.images {
		t.Run("ImageInput", s.testImageInput)
	}

	if s.documents {
		t.Run("DocumentInput", s.testDocumentInput)
	}

	if s.embeddings {
		t.Run("EmbedsInOrder", s.testEmbedsInOrder)
		t.Run("EmbedsDimensions", s.testEmbedsDimensions)
		t.Run("EmbedsBatches", s.testEmbedsBatches)
		t.Run("EmbedRejectsUnknownModel", s.testEmbedRejectsUnknownModel)
		t.Run("EmbedRejectsCanceledContext", s.testEmbedRejectsCanceledContext)
	}
}

// request returns a request to the suite's model asking prompt.
func (suite *suite) request(prompt string, parts ...llm.Part) llm.Request {
	return llm.Request{
		Model:    suite.model,
		Messages: []llm.Message{llm.User(append([]llm.Part{llm.Text{Text: prompt}}, parts...)...)},
	}
}

// generate sends request with a live context and fails the test on error.
func (suite *suite) generate(t *testing.T, driver llm.Driver, request llm.Request) llm.Response {
	t.Helper()

	response, err := driver.Generate(t.Context(), request)

	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if response.Message.Role != llm.RoleAssistant {
		t.Fatalf("Generate() role = %q, want %q", response.Message.Role, llm.RoleAssistant)
	}

	return response
}

// text joins the text parts of message.
func text(message llm.Message) string {
	var out strings.Builder

	for _, part := range message.Parts {
		if part, ok := part.(llm.Text); ok {
			out.WriteString(part.Text)
		}
	}

	return out.String()
}

// mentions reports whether got contains word, ignoring case.
func mentions(got, word string) bool {
	return strings.Contains(strings.ToLower(got), word)
}

// toolCalls returns the tool calls of message in order.
func toolCalls(message llm.Message) []llm.ToolCall {
	var calls []llm.ToolCall

	for _, part := range message.Parts {
		if call, ok := part.(llm.ToolCall); ok {
			calls = append(calls, call)
		}
	}

	return calls
}

// collect drains a stream, failing the test on a yielded error, and returns
// its events.
func collect(t *testing.T, driver llm.Driver, request llm.Request) []llm.Event {
	t.Helper()

	var events []llm.Event

	for event, err := range driver.Stream(t.Context(), request) {
		if err != nil {
			t.Fatalf("Stream() error = %v", err)
		}

		events = append(events, event)
	}

	return events
}

// testGeneratesText verifies a plain request returns an assistant text reply
// that stops normally and reports a model.
func (suite *suite) testGeneratesText(t *testing.T) {
	request := suite.request(textPrompt)
	request.Instructions = "You are a terse assistant."

	response := suite.generate(t, suite.newDriver(t), request)

	if got := text(response.Message); !mentions(got, "pineapple") {
		t.Errorf("reply = %q, want it to contain %q", got, "pineapple")
	}

	if response.StopReason != llm.StopEndTurn {
		t.Errorf("StopReason = %q, want %q", response.StopReason, llm.StopEndTurn)
	}
}

// testStreamMatchesDone verifies a stream ends with exactly one Done, nothing
// after it, and text deltas that add up to the text of the Done response.
func (suite *suite) testStreamMatchesDone(t *testing.T) {
	events := collect(t, suite.newDriver(t), suite.request(textPrompt))

	if len(events) == 0 {
		t.Fatal("Stream() yielded no events")
	}

	done, ok := events[len(events)-1].(llm.Done)

	if !ok {
		t.Fatalf("last event = %T, want llm.Done", events[len(events)-1])
	}

	var streamed strings.Builder

	for _, event := range events[:len(events)-1] {
		switch event := event.(type) {
		case llm.Done:
			t.Fatal("Stream() yielded more than one llm.Done")
		case llm.TextDelta:
			streamed.WriteString(event.Text)
		}
	}

	if got, want := streamed.String(), text(done.Response.Message); got != want {
		t.Errorf("text deltas = %q, Done text = %q", got, want)
	}

	if !mentions(streamed.String(), "pineapple") {
		t.Errorf("streamed text = %q, want it to contain %q", streamed.String(), "pineapple")
	}
}

// testStreamBreakStopsCleanly verifies leaving the loop after the first event
// ends the stream without a panic, a hang or an error, and leaves the driver
// usable.
func (suite *suite) testStreamBreakStopsCleanly(t *testing.T) {
	driver := suite.newDriver(t)

	for _, err := range driver.Stream(t.Context(), suite.request(longPrompt)) {
		if err != nil {
			t.Fatalf("Stream() error = %v", err)
		}

		break
	}

	suite.generate(t, driver, suite.request(textPrompt))
}

// toolRequest returns a request that forces a call to the lookup tool.
func (suite *suite) toolRequest() llm.Request {
	request := suite.request(toolPrompt)
	request.Tools = []llm.ToolSpec{lookupTool}
	request.ToolChoice = llm.ToolChoice{
		Kind: llm.ToolChoiceNamed,
		Name: lookupTool.Name,
	}

	return request
}

// testToolCallRoundTrip verifies a forced tool call comes back as a tool-use
// stop with a named call and JSON arguments, and that the model answers from
// the tool result sent back.
func (suite *suite) testToolCallRoundTrip(t *testing.T) {
	driver := suite.newDriver(t)
	request := suite.toolRequest()

	first := suite.generate(t, driver, request)

	calls := toolCalls(first.Message)

	if len(calls) == 0 {
		t.Fatalf("no tool call in %+v", first.Message)
	}

	call := calls[0]

	if call.ID == "" || call.Name != lookupTool.Name {
		t.Fatalf("tool call = %+v, want an ID and name %q", call, lookupTool.Name)
	}

	var arguments map[string]any

	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		t.Fatalf("tool arguments %q are not a JSON object: %v", call.Arguments, err)
	}

	if first.StopReason != llm.StopToolUse {
		t.Errorf("StopReason = %q, want %q", first.StopReason, llm.StopToolUse)
	}

	request.ToolChoice = llm.ToolChoice{Kind: llm.ToolChoiceNone}
	request.Messages = append(request.Messages, first.Message,
		llm.User(llm.ToolResult{
			CallID:  call.ID,
			Content: "The secret code is " + toolCode + ".",
		}))

	second := suite.generate(t, driver, request)

	if got := text(second.Message); !mentions(got, toolCode) {
		t.Errorf("reply = %q, want it to contain %q", got, toolCode)
	}
}

// testToolResultError verifies a tool result marked as an error is accepted
// and the model replies in text.
func (suite *suite) testToolResultError(t *testing.T) {
	driver := suite.newDriver(t)
	request := suite.toolRequest()

	first := suite.generate(t, driver, request)

	calls := toolCalls(first.Message)

	if len(calls) == 0 {
		t.Fatalf("no tool call in %+v", first.Message)
	}

	request.ToolChoice = llm.ToolChoice{Kind: llm.ToolChoiceNone}
	request.Messages = append(request.Messages, first.Message,
		llm.User(llm.ToolResult{
			CallID:  calls[0].ID,
			Content: "error: city not found",
			IsError: true,
		}))

	second := suite.generate(t, driver, request)

	if got := text(second.Message); strings.TrimSpace(got) == "" {
		t.Errorf("reply after a tool error has no text: %+v", second.Message)
	}
}

// testStreamsToolCall verifies a streamed tool call announces the same ID and
// name as the Done response and that the argument fragments add up to its
// arguments.
func (suite *suite) testStreamsToolCall(t *testing.T) {
	events := collect(t, suite.newDriver(t), suite.toolRequest())

	done, ok := events[len(events)-1].(llm.Done)

	if !ok {
		t.Fatalf("last event = %T, want llm.Done", events[len(events)-1])
	}

	calls := toolCalls(done.Response.Message)

	if len(calls) == 0 {
		t.Fatalf("no tool call in %+v", done.Response.Message)
	}

	var (
		started   bool
		arguments strings.Builder
	)

	for _, event := range events {
		switch event := event.(type) {
		case llm.ToolCallStarted:
			if event.ID == calls[0].ID && event.Name == calls[0].Name {
				started = true
			}
		case llm.ToolCallDelta:
			if event.ID == calls[0].ID {
				arguments.WriteString(event.Arguments)
			}
		}
	}

	if !started {
		t.Errorf("no ToolCallStarted for %+v in %d events", calls[0], len(events))
	}

	var streamed, final any

	if err := json.Unmarshal([]byte(arguments.String()), &streamed); err != nil {
		t.Fatalf("streamed arguments %q are not JSON: %v", arguments.String(), err)
	}

	if err := json.Unmarshal(calls[0].Arguments, &final); err != nil {
		t.Fatalf("final arguments %q are not JSON: %v", calls[0].Arguments, err)
	}

	if !reflect.DeepEqual(streamed, final) {
		t.Errorf("streamed arguments = %s, Done arguments = %s", arguments.String(), calls[0].Arguments)
	}
}

// testStructuredOutput verifies a request with an output schema returns text
// that decodes into the schema's shape and carries the requested facts.
func (suite *suite) testStructuredOutput(t *testing.T) {
	request := suite.request("Extract the person: Ada Lovelace is 36 years old.")
	request.Output = personSchema

	response := suite.generate(t, suite.newDriver(t), request)

	var person struct {
		// Name is the person's name extracted from the prompt.
		Name string `json:"name"`
		// Age is the person's age extracted from the same prompt.
		Age int `json:"age"`
	}

	if err := json.Unmarshal([]byte(text(response.Message)), &person); err != nil {
		t.Fatalf("reply %q does not match the schema: %v", text(response.Message), err)
	}

	if !mentions(person.Name, "ada") || person.Age != 36 {
		t.Errorf("person = %+v, want Ada aged 36", person)
	}
}

// testReasoningRoundTrip verifies a reasoning trace returned by the model can
// be sent back unchanged, signature included, and that the next turn
// succeeds. A model that returns no trace skips the check.
func (suite *suite) testReasoningRoundTrip(t *testing.T) {
	driver := suite.newDriver(t)
	request := suite.request(reasonPrompt)
	request.Reasoning = llm.EffortLow

	first := suite.generate(t, driver, request)

	hasReasoning := false

	for _, part := range first.Message.Parts {
		_, ok := part.(llm.Reasoning)
		hasReasoning = hasReasoning || ok
	}

	if !hasReasoning {
		t.Skipf("the model returned no reasoning part: %+v", first.Message)
	}

	request.Messages = append(request.Messages, first.Message, llm.UserText(reasonFollowUp))

	second := suite.generate(t, driver, request)

	if got := text(second.Message); !mentions(got, "52") {
		t.Errorf("reply = %q, want it to contain %q", got, "52")
	}
}

// testImageInput verifies the model can see an image sent inline.
func (suite *suite) testImageInput(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 64, 64))

	for x := range 64 {
		for y := range 64 {
			picture.Set(x, y, color.RGBA{
				R: 255,
				A: 255,
			})
		}
	}

	var encoded bytes.Buffer

	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}

	response := suite.generate(t, suite.newDriver(t), suite.request(imagePrompt,
		llm.Image{
			Data:      encoded.Bytes(),
			MediaType: "image/png",
		}))

	if got := text(response.Message); !mentions(got, "red") {
		t.Errorf("reply = %q, want it to contain %q", got, "red")
	}
}

// testDocumentInput verifies the model can read a PDF sent inline.
func (suite *suite) testDocumentInput(t *testing.T) {
	response := suite.generate(t, suite.newDriver(t), suite.request(documentPrompt,
		llm.Document{
			Data:      pdf("The secret word is " + documentWord + "."),
			MediaType: "application/pdf",
		}))

	if got := text(response.Message); !mentions(got, documentWord) {
		t.Errorf("reply = %q, want it to contain %q", got, documentWord)
	}
}

// pdf returns a one-page PDF that shows line. Object offsets are computed, so
// the cross-reference table is valid and strict readers accept the file.
func pdf(line string) []byte {
	stream := "BT /F1 18 Tf 72 700 Td (" + line + ") Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var out bytes.Buffer

	out.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objects))

	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}

	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)

	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}

	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)

	return out.Bytes()
}

// unknownPart is a part type outside the sealed set. Embedding [llm.Text]
// promotes its unexported method, which is how it satisfies [llm.Part].
type unknownPart struct {
	// Text provides the marker method while preserving an unknown concrete type.
	llm.Text
}

// testRejectsUnknownPart verifies a part type the driver does not know fails
// with llm.ErrUnsupported from both Generate and Stream.
func (suite *suite) testRejectsUnknownPart(t *testing.T) {
	driver := suite.newDriver(t)
	request := suite.request(textPrompt, unknownPart{llm.Text{Text: "extra"}})

	if _, err := driver.Generate(t.Context(), request); !errors.Is(err, llm.ErrUnsupported) {
		t.Errorf("Generate() error = %v, want llm.ErrUnsupported", err)
	}

	for _, err := range driver.Stream(t.Context(), request) {
		if !errors.Is(err, llm.ErrUnsupported) {
			t.Errorf("Stream() error = %v, want llm.ErrUnsupported", err)
		}

		break
	}
}

// testRejectsUnknownModel verifies an unknown model fails with
// llm.ErrNotFound from both Generate and Stream.
func (suite *suite) testRejectsUnknownModel(t *testing.T) {
	driver := suite.newDriver(t)
	request := suite.request(textPrompt)
	request.Model = unknownModel

	if _, err := driver.Generate(t.Context(), request); !errors.Is(err, llm.ErrNotFound) {
		t.Errorf("Generate() error = %v, want llm.ErrNotFound", err)
	}

	for _, err := range driver.Stream(t.Context(), request) {
		if !errors.Is(err, llm.ErrNotFound) {
			t.Errorf("Stream() error = %v, want llm.ErrNotFound", err)
		}

		break
	}
}

// canceled returns a context that is already canceled.
func canceled(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

// testRejectsCanceledContext verifies Generate and Stream fail with the
// context's error when the context is already canceled.
func (suite *suite) testRejectsCanceledContext(t *testing.T) {
	driver := suite.newDriver(t)
	request := suite.request(textPrompt)

	if _, err := driver.Generate(canceled(t), request); !errors.Is(err, context.Canceled) {
		t.Errorf("Generate() error = %v, want context.Canceled", err)
	}

	for _, err := range driver.Stream(canceled(t), request) {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Stream() error = %v, want context.Canceled", err)
		}

		break
	}
}

// embed embeds inputs with the suite's embedding model and fails the test on
// error.
func (suite *suite) embed(t *testing.T, driver llm.Driver, dimensions int, inputs ...string) [][]float32 {
	t.Helper()

	embeddings, err := driver.Embed(t.Context(), llm.EmbedRequest{
		Model:      suite.embeddingModel,
		Inputs:     inputs,
		Dimensions: dimensions,
	})

	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if len(embeddings.Vectors) != len(inputs) {
		t.Fatalf("Embed() returned %d vectors for %d inputs", len(embeddings.Vectors), len(inputs))
	}

	for i, vector := range embeddings.Vectors {
		if len(vector) == 0 || len(vector) != len(embeddings.Vectors[0]) {
			t.Fatalf("vector %d has %d components, vector 0 has %d", i, len(vector), len(embeddings.Vectors[0]))
		}
	}

	return embeddings.Vectors
}

// cosine returns the cosine similarity of a and b.
func cosine(a, b []float32) float64 {
	var dot, normA, normB float64

	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// testEmbedsInOrder verifies a batch returns one vector per input in input
// order: each vector is closer to the vector of the same text embedded alone
// than the other texts' vectors are.
func (suite *suite) testEmbedsInOrder(t *testing.T) {
	driver := suite.newDriver(t)
	inputs := []string{"The cat sat on the mat.", "Quarterly revenue grew by twelve percent.", "Photosynthesis converts light into sugar."}

	batch := suite.embed(t, driver, 0, inputs...)

	for i, input := range inputs {
		alone := suite.embed(t, driver, 0, input)[0]
		same := cosine(batch[i], alone)

		for j := range inputs {
			if j != i && cosine(batch[j], alone) >= same {
				t.Errorf("vector %d is not the closest to %q embedded alone", i, input)
			}
		}
	}
}

// testEmbedsDimensions verifies the requested vector size is honored.
func (suite *suite) testEmbedsDimensions(t *testing.T) {
	const dimensions = 64

	for i, vector := range suite.embed(t, suite.newDriver(t), dimensions, "first", "second") {
		if len(vector) != dimensions {
			t.Errorf("vector %d has %d components, want %d", i, len(vector), dimensions)
		}
	}
}

// testEmbedsBatches verifies more inputs than any vendor accepts in one
// request still return one vector each, so the driver must split the batch.
func (suite *suite) testEmbedsBatches(t *testing.T) {
	inputs := make([]string, 2100)

	for i := range inputs {
		inputs[i] = fmt.Sprintf("batch item %d", i)
	}

	suite.embed(t, suite.newDriver(t), 0, inputs...)
}

// testEmbedRejectsUnknownModel verifies an unknown embedding model fails with
// llm.ErrNotFound.
func (suite *suite) testEmbedRejectsUnknownModel(t *testing.T) {
	_, err := suite.newDriver(t).Embed(t.Context(), llm.EmbedRequest{
		Model:  unknownModel,
		Inputs: []string{"hello"},
	})

	if !errors.Is(err, llm.ErrNotFound) {
		t.Errorf("Embed() error = %v, want llm.ErrNotFound", err)
	}
}

// testEmbedRejectsCanceledContext verifies Embed fails with the context's
// error when the context is already canceled.
func (suite *suite) testEmbedRejectsCanceledContext(t *testing.T) {
	_, err := suite.newDriver(t).Embed(canceled(t), llm.EmbedRequest{
		Model:  suite.embeddingModel,
		Inputs: []string{"hello"},
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Embed() error = %v, want context.Canceled", err)
	}
}
