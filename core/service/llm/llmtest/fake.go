package llmtest

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"iter"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/service/llm"
)

// ErrNoReply is returned by [Fake.Generate] and [Fake.Stream] when the script
// has no reply left for the turn. It makes an unexpected model call fail the
// test instead of getting an invented answer.
var ErrNoReply = errors.New("llmtest: no scripted reply left")

// defaultDimensions is the size of the vectors [Fake.Embed] returns when the
// request does not ask for one.
const defaultDimensions = 8

// Fake is a scripted [llm.Driver]. Each call to [Fake.Generate] or
// [Fake.Stream] consumes the next scripted reply; [Fake.Embed] answers
// without a script. Requests that pass validation are recorded and available
// from [Fake.Generated] and [Fake.Embedded].
//
// Fake rejects a context that is already done and parts that are not one of
// the [llm.Part] types with [llm.ErrUnsupported], like a real driver, before
// it records or consumes anything. It is safe for concurrent use.
//
// # Example
//
//	fake := llmtest.New()
//	fake.ReplyToolCall("lookup", `{"city":"Paris"}`)
//	fake.Reply("It is sunny.")
type Fake struct {
	// mutex guards every field below.
	mutex sync.Mutex

	// script holds the replies not yet used, in order.
	script []func(llm.Request) (llm.Response, error)

	// generated holds copies of the recorded generate and stream requests.
	generated []llm.Request

	// embedded holds copies of the recorded embedding requests.
	embedded []llm.EmbedRequest

	// embedFn replaces the deterministic embeddings when not nil.
	embedFn func(llm.EmbedRequest) (llm.Embeddings, error)

	// turns counts the responses the fake built, for their IDs.
	turns int

	// failWith fails every call until cleared.
	failWith error

	// failNext fails the next call only.
	failNext error
}

var _ llm.Driver = (*Fake)(nil)

// New returns a [Fake] with an empty script.
func New() *Fake {
	return &Fake{}
}

// queue appends one step to the script.
func (fake *Fake) queue(step func(llm.Request) (llm.Response, error)) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.script = append(fake.script, step)
}

// Reply queues a turn that answers with text and stops with
// [llm.StopEndTurn].
func (fake *Fake) Reply(text string) {
	fake.ReplyParts(llm.Text{Text: text})
}

// ReplyParts queues a turn that answers with an assistant message made of
// parts. The turn stops with [llm.StopToolUse] when parts contains an
// [llm.ToolCall] and with [llm.StopEndTurn] otherwise. The parts are used as
// given and shared with the caller.
func (fake *Fake) ReplyParts(parts ...llm.Part) {
	stop := llm.StopEndTurn

	if slices.ContainsFunc(parts, func(part llm.Part) bool {
		_, ok := part.(llm.ToolCall)

		return ok
	}) {
		stop = llm.StopToolUse
	}

	fake.ReplyResponse(llm.Response{
		Message: llm.Message{
			Role:  llm.RoleAssistant,
			Parts: parts,
		},
		StopReason: stop,
	})
}

// ReplyToolCall queues a turn in which the model calls the tool name with
// the JSON object args, stopping with [llm.StopToolUse]. The call ID is
// "call-" and the number of requests recorded so far, so the first call is
// "call-1".
func (fake *Fake) ReplyToolCall(name, args string) {
	fake.ReplyWith(func(llm.Request) (llm.Response, error) {
		fake.mutex.Lock()

		id := fmt.Sprintf("call-%d", len(fake.generated))
		fake.mutex.Unlock()

		call := llm.ToolCall{
			ID:        id,
			Name:      name,
			Arguments: []byte(args),
		}

		return llm.Response{
			Message: llm.Message{
				Role:  llm.RoleAssistant,
				Parts: []llm.Part{call},
			},
			StopReason: llm.StopToolUse,
		}, nil
	})
}

// ReplyResponse queues a turn that returns response as is, except that an
// empty ID becomes "fake-" and the turn number and an empty Model becomes the
// requested model.
func (fake *Fake) ReplyResponse(response llm.Response) {
	fake.ReplyWith(func(request llm.Request) (llm.Response, error) {
		return response, nil
	})
}

// ReplyWith queues a turn computed from the request. The error, if any, is
// returned by the driver call as is, so a test can script [llm.ErrRateLimited]
// or [llm.ErrNotFound] for one turn. fn runs without the fake's lock held and
// may call the fake.
func (fake *Fake) ReplyWith(fn func(llm.Request) (llm.Response, error)) {
	fake.queue(func(request llm.Request) (llm.Response, error) {
		response, err := fn(request)

		if err != nil {
			return llm.Response{}, err
		}

		if response.ID == "" {
			response.ID = fmt.Sprintf("fake-%d", fake.nextTurn())
		}

		if response.Model == "" {
			response.Model = request.Model
		}

		return response, nil
	})
}

// nextTurn returns the number of the response being built, starting at one.
func (fake *Fake) nextTurn() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.turns++

	return fake.turns
}

// EmbedWith replaces the deterministic embeddings with fn for every later
// [Fake.Embed] call, until EmbedWith(nil). Requests are still recorded and
// failures still injected.
func (fake *Fake) EmbedWith(fn func(llm.EmbedRequest) (llm.Embeddings, error)) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.embedFn = fn
}

// begin runs the checks every call shares: a done context, then an injected
// failure. It returns the error to give the caller.
func (fake *Fake) begin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failWith
}

// next validates request, records it, and pops the next script step.
func (fake *Fake) next(request llm.Request) (func(llm.Request) (llm.Response, error), error) {
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			switch part.(type) {
			case llm.Text, llm.Image, llm.Document, llm.ToolCall, llm.ToolResult, llm.Reasoning:
			default:
				return nil, fmt.Errorf("%w: part type %T", llm.ErrUnsupported, part)
			}
		}
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.generated = append(fake.generated, cloneRequest(request))

	if len(fake.script) == 0 {
		return nil, fmt.Errorf("%w (request %d)", ErrNoReply, len(fake.generated))
	}

	step := fake.script[0]
	fake.script = fake.script[1:]

	return step, nil
}

// Generate consumes the next scripted reply and returns it. It fails with the
// context's error when ctx is done, with an injected error from
// [Fake.FailNext] or [Fake.FailWith], with [llm.ErrUnsupported] for an
// unknown part type, and with [ErrNoReply] when the script is empty.
func (fake *Fake) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	if err := fake.begin(ctx); err != nil {
		return llm.Response{}, err
	}

	step, err := fake.next(request)

	if err != nil {
		return llm.Response{}, err
	}

	return step(request)
}

// Stream consumes the next scripted reply like [Fake.Generate] and yields it
// as events: the text split into word-sized [llm.TextDelta] values, each
// reasoning trace as one [llm.ReasoningDelta], each tool call as an
// [llm.ToolCallStarted] and one [llm.ToolCallDelta] with the whole arguments,
// and last [llm.Done] with the response. Failures are yielded as the second
// value and end the sequence.
func (fake *Fake) Stream(ctx context.Context, request llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		response, err := fake.Generate(ctx, request)

		if err != nil {
			yield(nil, err)

			return
		}

		for _, part := range response.Message.Parts {
			var events []llm.Event

			switch part := part.(type) {
			case llm.Text:
				for chunk := range strings.SplitAfterSeq(part.Text, " ") {
					if chunk != "" {
						events = append(events, llm.TextDelta{Text: chunk})
					}
				}
			case llm.Reasoning:
				if part.Text != "" {
					events = append(events, llm.ReasoningDelta{Text: part.Text})
				}
			case llm.ToolCall:
				events = append(events,
					llm.ToolCallStarted{
						ID:   part.ID,
						Name: part.Name,
					},
					llm.ToolCallDelta{
						ID:        part.ID,
						Arguments: string(part.Arguments),
					})
			}

			for _, event := range events {
				if ctx.Err() != nil || !yield(event, nil) {
					return
				}
			}
		}

		yield(llm.Done{Response: response}, nil)
	}
}

// Embed records the request and returns one vector per input. Without
// [Fake.EmbedWith] each vector is a unit vector of request.Dimensions
// components (8 when zero) derived from a hash of its input, so equal inputs
// give equal vectors and different inputs differ. It fails with
// [llm.ErrInvalidRequest] for negative dimensions, plus the failures of
// [Fake.Generate] other than the script ones.
func (fake *Fake) Embed(ctx context.Context, request llm.EmbedRequest) (llm.Embeddings, error) {
	if err := fake.begin(ctx); err != nil {
		return llm.Embeddings{}, err
	}

	if request.Dimensions < 0 {
		return llm.Embeddings{}, fmt.Errorf("%w: dimensions cannot be negative", llm.ErrInvalidRequest)
	}

	fake.mutex.Lock()

	request.Inputs = slices.Clone(request.Inputs)
	fake.embedded = append(fake.embedded, request)
	embedFn := fake.embedFn
	fake.mutex.Unlock()

	if embedFn != nil {
		return embedFn(request)
	}

	dimensions := request.Dimensions

	if dimensions == 0 {
		dimensions = defaultDimensions
	}

	vectors := make([][]float32, len(request.Inputs))

	for i, input := range request.Inputs {
		vectors[i] = hashVector(input, dimensions)
	}

	return llm.Embeddings{Vectors: vectors}, nil
}

// hashVector returns a unit vector of the given size derived from input. Each
// component is the FNV-1a hash of the input and the component index, mapped
// to [-1, 1], and the whole vector is then normalized.
func hashVector(input string, dimensions int) []float32 {
	vector := make([]float32, dimensions)

	var sum float64

	for i := range vector {
		hash := fnv.New64a()
		_, _ = fmt.Fprintf(hash, "%d:%s", i, input) // hash.Hash writes never fail.
		component := float64(hash.Sum64())/float64(math.MaxUint64)*2 - 1
		vector[i] = float32(component)
		sum += component * component
	}

	norm := float32(math.Sqrt(sum))

	if norm == 0 {
		vector[0] = 1

		return vector
	}

	for i := range vector {
		vector[i] /= norm
	}

	return vector
}

// FailWith makes every following call return err until FailWith(nil).
func (fake *Fake) FailWith(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.failWith = err
}

// FailNext makes the next call return err. A call that fails this way is not
// recorded and consumes no scripted reply.
func (fake *Fake) FailNext(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.failNext = err
}

// Reset clears the script, the recorded requests, [Fake.EmbedWith] and the
// injected failures.
func (fake *Fake) Reset() {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.script = nil
	fake.generated = nil
	fake.embedded = nil
	fake.embedFn = nil
	fake.turns = 0
	fake.failWith = nil
	fake.failNext = nil
}

// Generated returns the requests of every [Fake.Generate] and [Fake.Stream]
// call that reached the script, in order, including those that failed with
// [ErrNoReply]. The messages, tools and schema are copies; the parts inside a
// message are shared with the caller's.
func (fake *Fake) Generated() []llm.Request {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	requests := make([]llm.Request, len(fake.generated))

	for i, request := range fake.generated {
		requests[i] = cloneRequest(request)
	}

	return requests
}

// Embedded returns the requests of every [Fake.Embed] call that passed
// validation, in order.
func (fake *Fake) Embedded() []llm.EmbedRequest {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	requests := slices.Clone(fake.embedded)

	for i := range requests {
		requests[i].Inputs = slices.Clone(requests[i].Inputs)
	}

	return requests
}

// AssertGenerated fails t unless at least one recorded request satisfies
// every predicate. Without predicates any request matches.
//
// # Example
//
//	fake.AssertGenerated(t, func(request llm.Request) bool {
//		return len(request.Tools) == 1
//	})
func (fake *Fake) AssertGenerated(t testing.TB, preds ...func(llm.Request) bool) {
	t.Helper()

	if !fake.anyGenerated(preds) {
		t.Errorf("no generated request matches the predicate (%d generated)", len(fake.Generated()))
	}
}

// AssertNotGenerated fails t if any recorded request satisfies every
// predicate. Without predicates any request matches.
func (fake *Fake) AssertNotGenerated(t testing.TB, preds ...func(llm.Request) bool) {
	t.Helper()

	if fake.anyGenerated(preds) {
		t.Errorf("a generated request matches the predicate")
	}
}

// anyGenerated reports whether a recorded request satisfies every predicate.
func (fake *Fake) anyGenerated(preds []func(llm.Request) bool) bool {
	return slices.ContainsFunc(fake.Generated(), func(request llm.Request) bool {
		return !slices.ContainsFunc(preds, func(pred func(llm.Request) bool) bool {
			return !pred(request)
		})
	})
}

// cloneRequest copies the slices of request that a caller could later change:
// the messages (and their part lists), the tools and the output schema.
func cloneRequest(request llm.Request) llm.Request {
	request.Messages = slices.Clone(request.Messages)

	for i := range request.Messages {
		request.Messages[i].Parts = slices.Clone(request.Messages[i].Parts)
	}

	request.Tools = slices.Clone(request.Tools)
	request.Output = slices.Clone(request.Output)

	return request
}
