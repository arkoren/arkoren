package asyncapi_test

import (
	"bytes"
	"strings"
	"testing"

	"arkoren.dev/core/asyncapi"
)

// TestServerCredentialsRejected prevents credentials from appearing in public
// documentation. Diagnostics do not echo the credential-bearing host.
func TestServerCredentialsRejected(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Server("main", "user:secret@broker.example:9092", "kafka")
	})

	_, err := document.JSON()

	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected credential-free diagnostic, got %v", err)
	}
}

// TestServerIPv6Host accepts bracketed IPv6 authorities with a port.
func TestServerIPv6Host(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Server("main", "[::1]:9092", "kafka")
	})

	if _, err := document.JSON(); err != nil {
		t.Fatal(err)
	}
}

// TestSecurityReferencesUseAsyncAPIShape verifies OR reference arrays rather
// than OpenAPI requirement maps, including forward declarations.
func TestSecurityReferencesUseAsyncAPIShape(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Server("main", "broker.example:9092", "kafka", func(listener *asyncapi.ServerBuilder) {
			listener.Security("login")
		})
		definition.SecurityScheme("login", asyncapi.RawObject(`{"type":"userPassword"}`))
	})

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(data, []byte(`"security":[{"$ref":"#/components/securitySchemes/login"}]`)) {
		t.Fatalf("incorrect security shape: %s", data)
	}
}

// TestUnknownSecurityType rejects misspelled AsyncAPI security type names.
func TestUnknownSecurityType(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.SecurityScheme("login", asyncapi.RawObject(`{"type":"unknown"}`))
	})

	if _, err := document.JSON(); err == nil {
		t.Fatal("unknown security type accepted")
	}
}

// TestAnnotationLikeServerIDsRejectMissingSecurity ensures server IDs cannot
// hide their authentication requirements from graph validation.
func TestAnnotationLikeServerIDsRejectMissingSecurity(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"example", "default", "schema", "x-send"} {
		document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
			definition.Server(id, "broker.example:9092", "kafka", func(listener *asyncapi.ServerBuilder) {
				listener.Security(id)
			})
		})

		if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "#/components/securitySchemes/"+id) {
			t.Fatalf("server %q: expected missing security scheme, got %v", id, err)
		}
	}
}

// TestAnnotationLikeSecurityAliasesRejectMissingTargets preserves local scheme
// alias resolution without inspecting opaque raw binding or schema contents.
func TestAnnotationLikeSecurityAliasesRejectMissingTargets(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"example", "default", "schema", "x-send"} {
		document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
			definition.SecurityScheme(id, asyncapi.RawObject(`{"$ref":"#/components/securitySchemes/missing"}`))
		})

		if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "#/components/securitySchemes/missing") {
			t.Fatalf("security scheme %q: expected missing target, got %v", id, err)
		}
	}
}
