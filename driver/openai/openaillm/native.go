package openaillm

import (
	"github.com/openai/openai-go/v3"
)

// Client returns the underlying OpenAI client, for calls the llm contract does
// not cover (files, moderation, batches). The client is a value; its options
// are those the driver was built with.
// Native operations bypass llm facade validation and telemetry; the caller
// controls the shared HTTP client and its transport lifecycle.
//
// Example:
//
//	client := driver.Client()
//	_ = client.Files // Use the native Files service for explicit file operations.
func (driver *Driver) Client() openai.Client {
	return driver.client
}
