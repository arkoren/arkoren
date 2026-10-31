package asyncapi

import "fmt"

// ServerBuilderFunc configures one server before its detached publication.
// Retaining the draft cannot change a published document.
//
// Example:
//
//	var configure asyncapi.ServerBuilderFunc = func(listener *asyncapi.ServerBuilder) {
//		listener.Description("Production broker")
//	}
//	_ = configure
type ServerBuilderFunc = func(*ServerBuilder)

// ServerBuilder describes a broker endpoint, not a live broker connection.
// Security references and bindings document requirements without enforcing them.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Server("main", "broker.example:9092", "kafka", func(listener *asyncapi.ServerBuilder) {
//			listener.ProtocolVersion("1.0")
//		})
//	})
type ServerBuilder struct {
	// draft owns the endpoint metadata and accumulated declaration error.
	draft
}

// Server declares a uniquely named endpoint. Host is a host or host:port,
// including bracketed IPv6; credentials, URI schemes, paths and queries are
// rejected at export. Callbacks still run once for invalid declarations.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Server("main", "broker.example:9092", "kafka")
//	})
func (definition *Builder) Server(id, host, protocol string, configure ...ServerBuilderFunc) *Builder {
	listener := &ServerBuilder{
		draft: draft{
			object: map[string]any{
				"host":     host,
				"protocol": protocol,
				"bindings": map[string]any{},
			},
		},
	}

	for _, configure := range configure {
		configure(listener)
	}

	definition.insert("servers", id, listener.draft)

	return definition
}

// Description replaces the endpoint's human-readable explanation. Empty text
// is allowed and no connection is made when this setting is applied.
//
// Example:
//
//	var configure asyncapi.ServerBuilderFunc = func(listener *asyncapi.ServerBuilder) {
//		listener.Description("Primary event broker")
//	}
//	_ = configure
func (listener *ServerBuilder) Description(text string) *ServerBuilder {
	listener.object["description"] = text

	return listener
}

// ProtocolVersion replaces the documented protocol version. It is metadata,
// not a negotiation setting, and an empty string is accepted.
//
// Example:
//
//	var configure asyncapi.ServerBuilderFunc = func(listener *asyncapi.ServerBuilder) {
//		listener.ProtocolVersion("1.0")
//	}
//	_ = configure
func (listener *ServerBuilder) ProtocolVersion(version string) *ServerBuilder {
	listener.object["protocolVersion"] = version

	return listener
}

// Pathname replaces the endpoint path independently of its host. This value
// documents the protocol endpoint and performs no routing or I/O.
//
// Example:
//
//	var configure asyncapi.ServerBuilderFunc = func(listener *asyncapi.ServerBuilder) {
//		listener.Pathname("/events")
//	}
//	_ = configure
func (listener *ServerBuilder) Pathname(path string) *ServerBuilder {
	listener.object["pathname"] = path

	return listener
}

// Security appends alternative security-scheme references (OR). Server and
// operation requirements apply independently (AND across these two levels).
// Referenced scheme IDs may be declared later; duplicates are errors.
//
// Example:
//
//	var configure asyncapi.ServerBuilderFunc = func(listener *asyncapi.ServerBuilder) {
//		listener.Security("broker-login")
//	}
//	_ = configure
func (listener *ServerBuilder) Security(ids ...string) *ServerBuilder {
	listener.references("security", "#/components/securitySchemes/", ids)

	return listener
}

// Binding captures a protocol-specific server binding object. Duplicate
// protocols and malformed raw objects become deferred document errors.
//
// Example:
//
//	var configure asyncapi.ServerBuilderFunc = func(listener *asyncapi.ServerBuilder) {
//		listener.Binding("kafka", asyncapi.RawObject(`{"bindingVersion":"0.5.0"}`))
//	}
//	_ = configure
func (listener *ServerBuilder) Binding(protocol string, object RawObject) *ServerBuilder {
	listener.binding(protocol, object)

	return listener
}

// SecurityScheme captures a raw AsyncAPI security scheme under a unique ID.
// JSON syntax, supported type names, and local references are checked; this is
// not a full protocol validator and does not configure authentication.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.SecurityScheme("login", asyncapi.RawObject(`{"type":"userPassword"}`))
//	})
func (definition *Builder) SecurityScheme(id string, object RawObject) *Builder {
	value, err := captureObject(object)
	definition.fail(err)

	components := draft{
		object: definition.object["components"].(map[string]any),
	}
	components.fail(validateID(id))

	items := components.object["securitySchemes"].(map[string]any)

	if _, exists := items[id]; exists {
		components.fail(fmt.Errorf("duplicate security scheme id %q", id))
	} else {
		items[id] = value
	}

	definition.fail(components.err)

	return definition
}
