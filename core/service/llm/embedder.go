package llm

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/llm/internal/genai"
	"arkoren.dev/core/service/telemetry"
)

// Embedder turns texts into vectors with an embedding model, adding
// validation, telemetry and events to a [Driver]'s Embed. It is safe for
// concurrent use when its driver is.
// Construction does no I/O; the acquiring owner starts and stops the shared
// driver separately. Embed does not retry failed provider calls.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small")
//	vectors, err := embedder.Embed(ctx, "First document.", "Second document.")
//	if err != nil {
//		return err
//	}
//	fmt.Println(len(vectors))
type Embedder struct {
	// driver performs the requests.
	driver Driver

	// model is the embedding model ID.
	model string

	// config holds the defaults, telemetry and events.
	config Config

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[genai.Instruments]
}

// NewEmbedder returns an Embedder that sends requests to driver using the
// embedding model, such as "text-embedding-3-small". It does no I/O and
// cannot fail; a nil driver or empty model is reported as [ErrInvalidRequest]
// by each call. Callbacks run directly once in order on one fresh draft.
// Nil entries panic; expanded nil/empty slices omit edits. Completed settings
// are copied independently of retained drafts; services remain shared.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(embedder *llm.EmbedderBuilder) {
//		embedder.Dimensions(1536)
//	})
func NewEmbedder(driver Driver, model string, configure ...EmbedderBuilderFunc) *Embedder {
	draft := &EmbedderBuilder{}

	for _, configure := range configure {
		configure(draft)
	}

	draft.config.Purpose = cmp.Or(draft.config.Purpose, PurposeUnspecified)

	return &Embedder{
		driver:      driver,
		model:       model,
		config:      draft.config,
		instruments: newInstruments(),
	}
}

// Driver returns the underlying Driver.
// Raw embedding calls bypass facade defaults, validation, and instrumentation.
//
//	backend := embedder.Driver()
//	_ = backend // Its lifecycle remains with the composition owner.
func (embedder *Embedder) Driver() Driver {
	return embedder.driver
}

// DriverAs returns the first driver of type T in the chain of the underlying
// driver. A driver that wraps another exposes it with an Unwrap() Driver
// method, so wrappers do not hide the concrete driver.
// Lookup transfers no ownership and returns false when no matching driver exists.
//
//	fake, ok := embedder.DriverAs[*llmtest.Fake]()
//	if ok {
//		_ = fake.Embedded()
//	}
func (embedder *Embedder) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](embedder.Driver())
}

// Config returns an independent reporting record. Shared telemetry and event
// services are not cloned; assigning fields cannot reconfigure the embedder.
//
//	config := llm.NewEmbedder(driver, "text-embedding-3-small").Config()
//	fmt.Println(config.Dimensions)
func (embedder *Embedder) Config() Config {
	return embedder.config
}

// Embed returns one vector per text, in order. The driver batches large
// inputs. A nil ctx, nil driver, empty model or empty texts fails with
// [ErrInvalidRequest] before any I/O; a driver that returns a different
// number of vectors than texts is an error too. When [EmbedderBuilder.Events] is set,
// Embed publishes [EmbeddingsGenerated] or [EmbeddingsFailed]. The usage is
// recorded in telemetry and in the event.
//
//	vectors, err := embedder.Embed(ctx, "first text", "second text")
func (embedder *Embedder) Embed(ctx context.Context, texts ...string) ([][]float32, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context cannot be nil", ErrInvalidRequest)
	}

	ctx, op := genai.Begin(ctx, embedder.config.Telemetry, embedder.instruments, telemetry.Client,
		semconv.GenAIOperationEmbeddings, embedder.model, requestAttrs(embedder.config, embedder.model)...)
	completed := false
	defer endInterrupted(ctx, op, &completed)
	op.SetAttrs(telemetry.Int(semconv.LLMEmbeddingInputs, len(texts)))

	start := time.Now()

	var embeddings Embeddings

	err := embedder.check(texts)

	if err == nil {
		embeddings, err = embedder.driver.Embed(ctx, EmbedRequest{
			Model:      embedder.model,
			Inputs:     texts,
			Dimensions: embedder.config.Dimensions,
			Purpose:    embedder.config.Purpose,
		})
	}

	if err == nil && len(embeddings.Vectors) != len(texts) {
		err = fmt.Errorf("llm: driver returned %d vectors for %d texts", len(embeddings.Vectors), len(texts))
	}

	if err == nil {
		recordUsage(op, embeddings.Usage)
	}

	completed = true
	op.End(ctx, err, errorSentinels...)
	embedder.emit(ctx, len(texts), embeddings.Usage, err, time.Since(start))

	if err != nil {
		return nil, err
	}

	return embeddings.Vectors, nil
}

// check validates the parts of an Embed call that need no I/O.
func (embedder *Embedder) check(texts []string) error {
	switch {
	case embedder.driver == nil:
		return fmt.Errorf("%w: driver cannot be nil", ErrInvalidRequest)
	case embedder.model == "":
		return fmt.Errorf("%w: model cannot be empty", ErrInvalidRequest)
	case len(texts) == 0:
		return fmt.Errorf("%w: at least one text is required", ErrInvalidRequest)
	case embedder.config.Dimensions < 0:
		return fmt.Errorf("%w: dimensions cannot be negative", ErrInvalidRequest)
	}

	return nil
}

// emit publishes the outcome of one Embed call.
func (embedder *Embedder) emit(ctx context.Context, count int, usage Usage, err error, duration time.Duration) {
	bus := embedder.config.Events

	if err != nil {
		bus.Emit(ctx, EmbeddingsFailed, EmbeddingsFailedEvent{
			Model:     embedder.model,
			Count:     count,
			ErrorType: eventErrorType(err),
			Duration:  duration,
		})

		return
	}

	bus.Emit(ctx, EmbeddingsGenerated, EmbeddingsGeneratedEvent{
		Model:    embedder.model,
		Count:    count,
		Usage:    usage,
		Duration: duration,
	})
}
