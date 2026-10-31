// Package llmtest provides test support for [llm.Driver] implementations and
// for code that talks to a model.
//
// [Fake] is a scripted driver: the test queues the replies it wants
// ([Fake.Reply], [Fake.ReplyToolCall], [Fake.ReplyWith], ...) and the code
// under test receives them in order, one per model turn. Every request that
// reaches the fake is recorded and can be inspected or asserted on. A turn
// with no scripted reply fails with [ErrNoReply], so code that calls the model
// more often than the test expects is caught instead of silently answered.
// Embeddings need no script: the fake derives deterministic unit vectors from
// the input text.
//
// [Run] is a conformance suite that every driver must pass. It sends real
// requests, so a driver backed by a vendor needs credentials and a model; the
// suite's options name the model and switch off the checks a vendor cannot
// pass. The checks are written for nondeterministic models: they ask for a
// fixed word and look for it case-insensitively, and they force a tool call
// with a named tool choice.
//
// There is no memory driver for the llm contract, so [Fake] is also the
// in-process driver for tests of code built on [llm.Model] and
// [llm.Embedder].
//
// # Example
//
//	fake := llmtest.New()
//	fake.Reply("Hello!")
//	model := llm.New(fake, "test-model")
//
//	response, err := model.Generate(ctx, llm.Request{
//		Messages: []llm.Message{llm.UserText("Say hello.")},
//	})
//
//	fake.AssertGenerated(t, func(request llm.Request) bool {
//		return request.Model == "test-model"
//	})
//
// A driver test file calls the suite:
//
//	func TestDriver(t *testing.T) {
//		llmtest.Run(t, func(t *testing.T) llm.Driver {
//			return newDriver(t)
//		}, llmtest.WithModel("gpt-5.2"), llmtest.WithoutDocuments())
//	}
package llmtest
