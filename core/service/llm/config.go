package llm

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// Config reports the settings of a [Model] or an [Embedder]. It is not a
// constructor input. Fields belonging to the other role remain zero.
// Service pointers are shared; changing the returned record changes no facade.
//
//	config := llm.New(driver, "gpt-5.2").Config()
//	fmt.Println(config.MaxTokens)
type Config struct {
	// Telemetry receives the spans and metrics of every call. Nil uses
	// [telemetry.Default], resolved each time a call runs, so calling
	// [telemetry.SetDefault] after [New] still takes effect.
	//
	//	service := model.Config().Telemetry
	Telemetry *telemetry.Telemetry

	// Events receives [ResponseGenerated], [ResponseFailed],
	// [EmbeddingsGenerated], [EmbeddingsFailed], [AgentCompleted],
	// [AgentFailed], [ToolInvoked] and [ToolFailed]. Nil, the default,
	// publishes nothing.
	//
	//	bus := model.Config().Events
	Events *event.Bus

	// Provider names the vendor in the gen_ai.provider.name telemetry
	// attribute, such as "openai". Empty reports "_OTHER".
	//
	//	provider := model.Config().Provider
	Provider string

	// MaxTokens is the output cap of a [Model] for requests that set none.
	// Zero leaves the vendor's default.
	//
	//	limit := model.Config().MaxTokens
	MaxTokens int

	// MaxSteps is the most model turns one [Model.Run] may take for agents
	// and runs that set none. Zero or less uses 10.
	//
	//	limit := model.Config().MaxSteps
	MaxSteps int

	// Dimensions is the vector size of an [Embedder]. Zero uses the vendor's
	// default.
	//
	//	size := embedder.Config().Dimensions
	Dimensions int

	// Purpose is what an [Embedder]'s vectors are for. Empty input selects
	// [PurposeUnspecified], leaving the choice to the vendor.
	//
	//	purpose := embedder.Config().Purpose
	Purpose Purpose
}

// ModelBuilder edits one model's construction draft. New copies its completed
// settings, so retaining the draft cannot reconfigure the published model.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.MaxTokens(4096)
//	})
type ModelBuilder struct {
	// config stores scalar settings and shared services until publication.
	config Config
}

// EmbedderBuilder edits embedding-only defaults and shared instrumentation.
// NewEmbedder copies its completed settings; services remain shared.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(embedder *llm.EmbedderBuilder) {
//		embedder.Dimensions(1536)
//	})
type EmbedderBuilder struct {
	// config stores scalar settings and shared services until publication.
	config Config
}

// Telemetry selects model instrumentation. Nil follows telemetry.Default on
// each operation, including after construction. It returns the same draft.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.Telemetry(app.Telemetry())
//	})
func (model *ModelBuilder) Telemetry(value *telemetry.Telemetry) *ModelBuilder {
	model.config.Telemetry = value

	return model
}

// Events selects the shared bus for model and agent outcomes. Nil disables
// publication, not execution. It returns the same draft.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.Events(app.Events())
//	})
func (model *ModelBuilder) Events(value *event.Bus) *ModelBuilder {
	model.config.Events = value

	return model
}

// Provider labels model telemetry, not routing. Empty reports _OTHER.
// It replaces the previous label and returns the same draft.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.Provider("openai")
//	})
func (model *ModelBuilder) Provider(value string) *ModelBuilder {
	model.config.Provider = value

	return model
}

// MaxTokens sets the fallback output cap. Zero leaves the vendor default;
// negative caps fail request validation at use. Request and run caps win.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.MaxTokens(4096)
//	})
func (model *ModelBuilder) MaxTokens(value int) *ModelBuilder {
	model.config.MaxTokens = value

	return model
}

// MaxSteps sets the agent fallback turn limit. Zero or less uses ten;
// agent and per-run positive limits take precedence. It returns the draft.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.MaxSteps(5)
//	})
func (model *ModelBuilder) MaxSteps(value int) *ModelBuilder {
	model.config.MaxSteps = value

	return model
}

// Telemetry selects embedding instrumentation. Nil follows telemetry.Default
// per operation; it does not capture the current default at construction.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(embedder *llm.EmbedderBuilder) {
//		embedder.Telemetry(app.Telemetry())
//	})
func (embedder *EmbedderBuilder) Telemetry(value *telemetry.Telemetry) *EmbedderBuilder {
	embedder.config.Telemetry = value

	return embedder
}

// Events selects the shared bus for embedding outcomes. Nil publishes nothing.
// Repeated settings replace the previous bus and return the same draft.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(embedder *llm.EmbedderBuilder) {
//		embedder.Events(app.Events())
//	})
func (embedder *EmbedderBuilder) Events(value *event.Bus) *EmbedderBuilder {
	embedder.config.Events = value

	return embedder
}

// Provider labels embedding telemetry, not routing. Empty reports _OTHER.
// It replaces the previous label and returns the same draft.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(embedder *llm.EmbedderBuilder) {
//		embedder.Provider("openai")
//	})
func (embedder *EmbedderBuilder) Provider(value string) *EmbedderBuilder {
	embedder.config.Provider = value

	return embedder
}

// Dimensions sets the requested vector size. Zero uses the vendor default;
// negative values fail at Embed, before driver I/O. It returns the draft.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(embedder *llm.EmbedderBuilder) {
//		embedder.Dimensions(1536)
//	})
func (embedder *EmbedderBuilder) Dimensions(value int) *EmbedderBuilder {
	embedder.config.Dimensions = value

	return embedder
}

// Purpose sets the embedding use case. PurposeUnspecified delegates to the
// vendor; other values are passed to the driver unchanged. It returns the draft.
//
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(embedder *llm.EmbedderBuilder) {
//		embedder.Purpose(llm.PurposeQuery)
//	})
func (embedder *EmbedderBuilder) Purpose(value Purpose) *EmbedderBuilder {
	embedder.config.Purpose = value

	return embedder
}

// ModelBuilderFunc edits the fresh model construction draft synchronously.
// Callbacks run once in order; nil entries panic. Services remain shared,
// while later edits to a retained draft cannot reconfigure the model.
//
//	var policy llm.ModelBuilderFunc = func(model *llm.ModelBuilder) {
//		model.MaxTokens(4096)
//	}
//	model := llm.New(driver, "gpt-5.2", policy)
//	_ = model.Config().MaxTokens
type ModelBuilderFunc = func(*ModelBuilder)

// EmbedderBuilderFunc edits one fresh embedding construction draft.
// Ordered callbacks run once without performing I/O; nil entries panic and
// the completed scalar settings are copied before publication.
//
//	var policy llm.EmbedderBuilderFunc = func(embedder *llm.EmbedderBuilder) {
//		embedder.Dimensions(1536)
//	}
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small", policy)
//	_ = embedder.Config().Dimensions
type EmbedderBuilderFunc = func(*EmbedderBuilder)
