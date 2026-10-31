package openaillm

import (
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// BuilderFunc is an ordinary callback alias for reusable construction policies.
// New and NewFrom invoke policies once in order; nil entries panic.
//
// Example:
//
//	var local openaillm.BuilderFunc = func(driver *openaillm.Builder) {
//		driver.API(openaillm.ChatCompletions)
//	}
//	driver, err := openaillm.New(local)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// API selects the OpenAI endpoint family a [Driver] speaks. Its string value
// can be stored in configuration; an empty value selects [Responses].
//
// Example:
//
//	api := openaillm.API("chat_completions")
//	_ = api == openaillm.ChatCompletions
type API string

const (
	// Responses uses the Responses API (POST /responses). It is the default.
	// Requests default to stateless: before a native hook, the driver sends
	// store=false and asks for encrypted reasoning content so reasoning
	// survives a round trip through the caller's own history. A ResponsesHook can
	// override these settings; store=false does not prevent provider retention.
	//
	// Example:
	//
	//	api := openaillm.Responses
	//	_ = string(api) // "responses"
	Responses API = "responses"

	// ChatCompletions uses the Chat Completions API (POST
	// /chat/completions), which most OpenAI-compatible servers implement.
	//
	// Example:
	//
	//	api := openaillm.ChatCompletions
	//	_ = string(api) // "chat_completions"
	ChatCompletions API = "chat_completions"
)

// StrictMode says whether the driver sends tool and output schemas in OpenAI's
// strict mode. Empty settings select [StrictDefault]; string values are suitable
// for configuration and JSON without an ordinal-to-name translation.
//
// Example:
//
//	mode := openaillm.StrictOn
//	_ = string(mode) // "on"
type StrictMode string

const (
	// StrictDefault enables strict schemas for [Responses] and disables them
	// for [ChatCompletions], because OpenAI-compatible servers usually reject
	// the strict-mode keywords. Empty settings also select this behavior.
	//
	// Example:
	//
	//	mode := openaillm.StrictDefault
	//	_ = string(mode) // "default"
	StrictDefault StrictMode = "default"

	// StrictOn always sends strict schemas, rewriting them as described on
	// [Driver].
	//
	// Example:
	//
	//	mode := openaillm.StrictOn
	//	_ = string(mode) // "on"
	StrictOn StrictMode = "on"

	// StrictOff never rewrites schemas and sends them with strict=false.
	//
	// Example:
	//
	//	mode := openaillm.StrictOff
	//	_ = string(mode) // "off"
	StrictOff StrictMode = "off"
)

// settings holds the private connection and request policy of a [Driver].
//
// Fields marked as connection settings configure the SDK client built by
// [New] and are ignored by [NewFrom], whose client the caller already
// configured. Every other field applies to both constructors.
type settings struct {
	// APIKey is the bearer token (connection setting). The default, empty,
	// lets the SDK read the OPENAI_API_KEY environment variable; servers
	// without authentication need none.
	APIKey string

	// BaseURL is the API root, such as "http://localhost:11434/v1" for Ollama
	// (connection setting). The default, empty, lets the SDK read
	// OPENAI_BASE_URL and otherwise use https://api.openai.com/v1.
	BaseURL string

	// API selects the endpoint family. The default is [Responses].
	API API

	// MaxRetries is how many times the SDK retries a failed request
	// (connection setting). The default, 0, turns the SDK's retries off,
	// because the llm contract leaves retrying to the caller. Negative values
	// are rejected by the constructors.
	MaxRetries int

	// HTTPClient is the client used for requests (connection setting). The
	// default, nil, uses the SDK's client.
	HTTPClient *http.Client

	// Organization is sent as the OpenAI-Organization header (connection
	// setting). The default, empty, falls back to OPENAI_ORG_ID.
	Organization string

	// Project is sent as the OpenAI-Project header (connection setting). The
	// default, empty, falls back to OPENAI_PROJECT_ID.
	Project string

	// StrictSchemas selects strict-mode schemas. The default is
	// [StrictDefault].
	StrictSchemas StrictMode

	// EmbedBatchSize is the largest number of inputs sent in one embeddings
	// request. The default, 0, is 2048, OpenAI's limit. Negative values are
	// rejected by the constructors.
	EmbedBatchSize int

	// ResponsesHook, when non-nil, receives every [responses.ResponseNewParams]
	// just before it is sent in [Responses] mode, to set sampling parameters,
	// hosted tools, service tiers, and other native settings. It may modify
	// the parameters; it must not retain them.
	ResponsesHook func(*responses.ResponseNewParams)

	// ChatHook, when non-nil, receives every [openai.ChatCompletionNewParams]
	// just before it is sent in [ChatCompletions] mode. The same rules as
	// ResponsesHook apply.
	ChatHook func(*openai.ChatCompletionNewParams)
}

// defaultEmbedBatchSize is OpenAI's limit on inputs per embeddings request.
const defaultEmbedBatchSize = 2048

// Builder configures one driver construction. Callbacks run directly once in
// order on one fresh draft, before validation. Nil entries panic; expanded nil
// or empty slices omit edits. Retained drafts cannot change published settings.
// SDK clients and function captures remain shared, not recursively cloned.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.API(openaillm.ChatCompletions)
//	}
//	_, err := openaillm.New(configure)
//	if err != nil {
//		return err
//	}
type Builder struct {
	// config is the mutable root detached when the constructor publishes.
	config settings
}

// configureDriver runs policies on one draft and returns its settings by value.
// Native request hooks are stored here and are invoked only when sending requests.
func configureDriver(configure ...BuilderFunc) settings {
	driver := &Builder{
		config: settings{
			EmbedBatchSize: defaultEmbedBatchSize,
		},
	}

	for _, configure := range configure {
		configure(driver)
	}

	if driver.config.API == "" {
		driver.config.API = Responses
	}

	if driver.config.StrictSchemas == "" {
		driver.config.StrictSchemas = StrictDefault
	}

	return driver.config
}

// APIKey sets the SDK bearer token and returns the same draft. Empty preserves
// OPENAI_API_KEY fallback. NewFrom ignores it, retaining its caller-owned client.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.APIKey("test-key")
//	}
//	_ = configure
func (driver *Builder) APIKey(key string) *Builder {
	driver.config.APIKey = key

	return driver
}

// BaseURL sets the SDK API root and returns the same draft. Empty preserves
// OPENAI_BASE_URL fallback, then the SDK default. NewFrom ignores this setting.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.BaseURL("http://localhost:11434/v1")
//	}
//	_ = configure
func (driver *Builder) BaseURL(url string) *Builder {
	driver.config.BaseURL = url

	return driver
}

// API selects the endpoint family and returns the same draft. Responses is
// the default; both constructors reject unknown values after all callbacks.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.API(openaillm.ChatCompletions)
//	}
//	_ = configure
func (driver *Builder) API(api API) *Builder {
	driver.config.API = api

	return driver
}

// MaxRetries sets SDK retries and returns the same draft. Zero disables retries
// and is the default. Both constructors reject negatives; NewFrom otherwise
// ignores it because its client already owns its connection settings.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.MaxRetries(0)
//	}
//	_ = configure
func (driver *Builder) MaxRetries(n int) *Builder {
	driver.config.MaxRetries = n

	return driver
}

// HTTPClient sets the shared HTTP client and returns the same draft. Nil uses
// the SDK default; NewFrom ignores this setting. Client internals are not copied.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.HTTPClient(http.DefaultClient)
//	}
//	_ = configure
func (driver *Builder) HTTPClient(client *http.Client) *Builder {
	driver.config.HTTPClient = client

	return driver
}

// Organization sets the organization header and returns the same draft. Empty
// preserves OPENAI_ORG_ID fallback. NewFrom ignores this connection setting.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.Organization("org")
//	}
//	_ = configure
func (driver *Builder) Organization(organization string) *Builder {
	driver.config.Organization = organization

	return driver
}

// Project sets the project header and returns the same draft. Empty preserves
// OPENAI_PROJECT_ID fallback. NewFrom ignores this connection setting.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.Project("project")
//	}
//	_ = configure
func (driver *Builder) Project(project string) *Builder {
	driver.config.Project = project

	return driver
}

// StrictSchemas overrides endpoint-specific schema defaults and returns the
// same draft. Without an override only Responses is strict. False explicitly
// disables rewriting, including for Responses; both constructors apply it.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.StrictSchemas(false)
//	}
//	_ = configure
func (driver *Builder) StrictSchemas(strict bool) *Builder {
	driver.config.StrictSchemas = StrictOff

	if strict {
		driver.config.StrictSchemas = StrictOn
	}

	return driver
}

// EmbedBatchSize sets the maximum inputs per embeddings request and returns
// the same draft. Zero uses 2048; both constructors reject negative sizes.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.EmbedBatchSize(32)
//	}
//	_ = configure
func (driver *Builder) EmbedBatchSize(n int) *Builder {
	driver.config.EmbedBatchSize = n

	return driver
}

// ResponsesHook stores a native hook and returns the same draft. A non-nil hook
// runs just before each Responses request, never during construction. It may
// modify parameters but must not retain them. Nil clears the hook.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.ResponsesHook(nil)
//	}
//	_ = configure
func (driver *Builder) ResponsesHook(hook func(*responses.ResponseNewParams)) *Builder {
	driver.config.ResponsesHook = hook

	return driver
}

// ChatHook stores a native hook and returns the same draft. A non-nil hook runs
// just before each Chat Completions request, never during construction. It may
// modify parameters but must not retain them. Nil clears the hook.
//
// Example:
//
//	configure := func(driver *openaillm.Builder) {
//		driver.ChatHook(nil)
//	}
//	_ = configure
func (driver *Builder) ChatHook(hook func(*openai.ChatCompletionNewParams)) *Builder {
	driver.config.ChatHook = hook

	return driver
}

// validate reports the first invalid setting.
func (settings settings) validate() error {
	switch {
	case settings.API != Responses && settings.API != ChatCompletions:
		return fmt.Errorf("openaillm: unknown API %q", settings.API)
	case settings.MaxRetries < 0:
		return fmt.Errorf("openaillm: MaxRetries %d is negative", settings.MaxRetries)
	case settings.EmbedBatchSize < 0:
		return fmt.Errorf("openaillm: EmbedBatchSize %d is negative", settings.EmbedBatchSize)
	}

	return nil
}
