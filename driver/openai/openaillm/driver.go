package openaillm

import (
	"context"
	"fmt"
	"iter"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/driver/openai/openaillm/internal/inference"
)

// Driver implements [llm.Driver] on OpenAI's Responses and Chat Completions
// APIs, and on any server that speaks them.
//
// A Driver is safe for concurrent use. It translates and does nothing else:
// the [llm.Model] facade supplies defaults, and the SDK's retries are off
// unless [Builder.MaxRetries] turns them on.
//
// # Strict schemas
//
// With strict schemas on (the default for [Responses]), tool and output
// schemas are rewritten before sending: every property becomes required,
// optional properties accept null, and additionalProperties is false. The
// rewrite is described on the usage page; a null the model sends decodes to
// the zero value.
//
// # Usage
//
// InputTokens counts all prompt tokens, including those served from the
// prompt cache (also reported in CacheReadTokens); OutputTokens includes
// ReasoningTokens. Chat Completions servers report only what they send.
//
// Example
//
//	driver, err := openaillm.New(func(driver *openaillm.Builder) {
//		driver.APIKey(os.Getenv("OPENAI_API_KEY"))
//	})
//	if err != nil {
//		return err
//	}
//
//	model := llm.New(driver, "gpt-5.2")
//	response, err := model.Generate(ctx, llm.Request{Messages: []llm.Message{llm.UserText("Hello")}})
type Driver struct {
	// client is the SDK client every call goes through.
	client openai.Client

	// config holds the validated settings.
	config settings

	// strict is the resolved strict-schema setting.
	strict bool
}

var _ llm.Driver = (*Driver)(nil)

// New builds a Driver and its SDK client from construction callbacks. It does no I/O; use
// [Driver.Ping] to check the connection. It fails when a setting is invalid:
// an unknown API, or a negative MaxRetries or EmbedBatchSize.
//
// Without [Builder.APIKey] and [Builder.BaseURL] the SDK reads OPENAI_API_KEY and
// OPENAI_BASE_URL from the environment.
// Callbacks run directly once in order on a fresh Builder before validation.
// Nil entries panic; expanded nil or empty slices omit edits. Retained drafts
// cannot change published settings; native hooks remain deferred until requests.
//
// Example
//
//	driver, err := openaillm.New(func(driver *openaillm.Builder) {
//		driver.BaseURL("http://localhost:11434/v1")
//		driver.API(openaillm.ChatCompletions)
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(configure ...BuilderFunc) (*Driver, error) {
	config := configureDriver(configure...)

	if err := config.validate(); err != nil {
		return nil, err
	}

	clientOpts := []option.RequestOption{option.WithMaxRetries(config.MaxRetries)}

	if config.APIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(config.APIKey))
	}

	if config.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(config.BaseURL))
	}

	if config.HTTPClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(config.HTTPClient))
	}

	if config.Organization != "" {
		clientOpts = append(clientOpts, option.WithOrganization(config.Organization))
	}

	if config.Project != "" {
		clientOpts = append(clientOpts, option.WithProject(config.Project))
	}

	return newDriver(openai.NewClient(clientOpts...), config), nil
}

// NewFrom wraps a caller-owned SDK client. The connection settings of
// [Builder] (APIKey, BaseURL, MaxRetries, HTTPClient, Organization, Project)
// are ignored because the client carries its own; API, StrictSchemas,
// EmbedBatchSize and the hooks apply. It validates like [New].
// Callbacks run directly once in order, before validation; nil entries panic.
//
// Example:
//
//	driver, err := openaillm.NewFrom(openai.NewClient(), func(driver *openaillm.Builder) {
//		driver.API(openaillm.ChatCompletions)
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func NewFrom(client openai.Client, configure ...BuilderFunc) (*Driver, error) {
	config := configureDriver(configure...)

	if err := config.validate(); err != nil {
		return nil, err
	}

	return newDriver(client, config), nil
}

// newDriver resolves the derived settings of an already validated config.
func newDriver(client openai.Client, config settings) *Driver {
	strict := config.StrictSchemas == StrictOn ||
		(config.StrictSchemas == StrictDefault && config.API == Responses)

	return &Driver{
		client: client,
		config: config,
		strict: strict,
	}
}

// Ping checks the connection and credentials by listing the models, one page
// (GET /models). Some compatible servers do not implement that endpoint and
// fail the ping.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err // The configured server may not implement GET /models.
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	if _, err := driver.client.Models.List(ctx); err != nil {
		return fmt.Errorf("ping: %w", inference.MapError(err, nil))
	}

	return nil
}

// Generate performs one model turn with the configured API.
// Vendor failures preserve the SDK error and matching llm sentinel; caller
// request containers are observed without modification or retention.
//
// Example:
//
//	reply, err := driver.Generate(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.UserText("Hello")}})
//	if err != nil {
//		return err
//	}
//	_ = reply.Message
func (driver *Driver) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}

	if driver.config.API == ChatCompletions {
		return driver.inference().GenerateChat(ctx, request)
	}

	return driver.inference().GenerateResponses(ctx, request)
}

// Stream performs one model turn with the configured API and yields its
// events. Breaking out of the loop cancels the HTTP request.
//
// Example:
//
//	for event, err := range driver.Stream(ctx, request) {
//		if err != nil {
//			return err
//		}
//		if done, ok := event.(llm.Done); ok {
//			_ = done.Response.Message
//		}
//	}
func (driver *Driver) Stream(ctx context.Context, request llm.Request) iter.Seq2[llm.Event, error] {
	if driver.config.API == ChatCompletions {
		return driver.inference().StreamChat(ctx, request)
	}

	return driver.inference().StreamResponses(ctx, request)
}
