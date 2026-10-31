package asyncapi

import (
	"errors"
	"testing"
)

func TestCaptureObjectPreservesAndCopiesOpaqueJSON(t *testing.T) {
	t.Parallel()

	source := RawObject(` {"number":90071992547409931234567890,"$ref":"opaque"} `)

	data, err := captureObject(source)

	if err != nil {
		t.Fatal(err)
	}

	source[1] = '['

	if string(data) != ` {"number":90071992547409931234567890,"$ref":"opaque"} ` {
		t.Fatalf("capture changed or aliased object: %s", data)
	}
}

func TestCaptureObjectRejectsNonobjects(t *testing.T) {
	t.Parallel()

	for _, source := range []RawObject{nil, {}, []byte("null"), []byte("true"), []byte("[]"), []byte("{"), []byte("{} {}"), []byte(`{"a":1,"a":2}`)} {
		if _, err := captureObject(source); err == nil {
			t.Fatalf("accepted invalid object %q", source)
		}
	}
}

// schemaProvider records capture calls and returns caller-owned bytes or an
// injected error, allowing ownership and failure behavior to be checked.
type schemaProvider struct {
	// calls counts provider invocations, including failed captures.
	calls int
	// data is deliberately retained so tests can mutate it after capture.
	data []byte
	// err is the original error whose identity capture must preserve.
	err error
}

// JSONSchema records exactly one invocation and returns the configured result.
// It panics for a nil receiver, exposing accidental calls to typed nil values.
//
// Example:
//
//	provider := &schemaProvider{data: []byte(`true`)}
//	_, _ = provider.JSONSchema()
func (provider *schemaProvider) JSONSchema() ([]byte, error) {
	provider.calls++

	return provider.data, provider.err
}

func TestCaptureSchemaCallsOnceAndCopies(t *testing.T) {
	t.Parallel()

	provider := &schemaProvider{data: []byte(`{"type":"string"}`)}

	data, err := captureSchema(provider)

	if err != nil {
		t.Fatal(err)
	}

	provider.data[0] = '['

	if provider.calls != 1 || string(data) != `{"type":"string"}` {
		t.Fatalf("capture did not detach once: calls=%d, data=%s", provider.calls, data)
	}
}

func TestCaptureSchemaRejectsNilProviders(t *testing.T) {
	t.Parallel()

	var provider *schemaProvider

	for _, schema := range []Schema{nil, provider, RawSchema(nil)} {
		if _, err := captureSchema(schema); err == nil {
			t.Fatal("accepted nil provider")
		}
	}
}

func TestCaptureSchemaPreservesProviderError(t *testing.T) {
	t.Parallel()

	cause := errors.New("provider failed")
	provider := &schemaProvider{err: cause}

	_, err := captureSchema(provider)

	if !errors.Is(err, cause) || provider.calls != 1 {
		t.Fatalf("unexpected error or call count: %v, %d", err, provider.calls)
	}
}

func TestCaptureSchemaValidatesProviderOutput(t *testing.T) {
	t.Parallel()

	provider := &schemaProvider{data: []byte(`{"$ref":"external"}`)}

	if _, err := captureSchema(provider); err == nil {
		t.Fatal("accepted unsupported provider output")
	}

	if provider.calls != 1 {
		t.Fatalf("provider called %d times", provider.calls)
	}
}
