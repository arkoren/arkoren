package release

import "strings"

// Title renders a module's human-facing release name, never its Go tag.
// Brand spellings preserve acronyms; other module names use initial capitals.
// Existing schema-3 spellings are immutable so recorded batches stay recoverable.
func Title(directory, version string) string {
	if directory == "core" {
		return "Core " + version
	}
	if directory == "cmd/arkoren" {
		return "CLI " + version
	}
	category, name, _ := strings.Cut(directory, "/")
	brands := map[string]string{
		"aes": "AES", "argon2": "Argon2", "aws": "AWS", "chacha20": "ChaCha20",
		"gcp": "GCP", "launchdarkly": "LaunchDarkly", "mcp": "MCP", "mqtt": "MQTT",
		"mysql": "MySQL", "nats": "NATS", "oauth": "OAuth", "openai": "OpenAI",
		"openfeature": "OpenFeature", "opensearch": "OpenSearch", "otel": "OpenTelemetry",
		"paypal": "PayPal", "rabbitmq": "RabbitMQ", "smtp": "SMTP", "sqlite": "SQLite",
		"webpush": "Web Push",
	}
	label, ok := brands[name]
	if !ok {
		label = capitalized(name)
	}

	return capitalized(category) + ": " + label + " " + version
}

// capitalized changes only the initial ASCII letter of canonical module names.
func capitalized(value string) string {
	if value == "" {
		return value
	}

	return strings.ToUpper(value[:1]) + value[1:]
}
