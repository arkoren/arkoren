package router

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"

	"arkoren.dev/core/http/router/internal/snapshot"
	"arkoren.dev/foundation/problem"
)

// OperationBuilderFunc configures descriptive metadata once per public route
// registration. Callbacks run synchronously in order; nil entries panic.
// An expanded nil slice omits metadata. Neither metadata nor its errors affect
// route execution. Retained drafts cannot change the published description.
//
// Example:
//
//	var describe router.OperationBuilderFunc = func(operation *router.OperationBuilder) {
//		operation.ID("listUsers").Summary("List users")
//	}
type OperationBuilderFunc = func(*OperationBuilder)

// OperationBuilder describes one registration, shared by its explicit methods.
// Errors are sticky and reported by OpenAPI, not by request execution. Schemas
// and examples are captured when supplied, and containers detach at publication.
//
// Example:
//
//	routes.Get("/users", list, func(operation *router.OperationBuilder) {
//		operation.ID("listUsers").JSONResponse(200, router.RawSchema(`{"type":"array"}`))
//	})
type OperationBuilder struct {
	// value contains only JSON-compatible descriptive members.
	value map[string]any
	// hidden omits the registration from export, including metadata errors.
	hidden bool
	// err preserves the first declaration failure.
	err error
}

// routeRecord is the root's single source for diagnostic and API projections.
// A nil operation marks an unannotated registration.
type routeRecord struct {
	// Route retains the existing diagnostic wire shape.
	Route
	// operation is a detached metadata declaration, absent for ordinary routes.
	operation *OperationBuilder
}

// newOperation invokes user policies directly, then publishes independent JSON
// containers. Functions and schema implementations are never retained.
func newOperation(configure []OperationBuilderFunc) *OperationBuilder {
	if len(configure) == 0 {
		return nil
	}

	operation := &OperationBuilder{value: map[string]any{"responses": map[string]any{}}}

	for _, configure := range configure {
		configure(operation)
	}

	published := &OperationBuilder{hidden: operation.hidden, err: operation.err}
	published.value = snapshot.Clone(operation.value).(map[string]any)

	return published
}

// fail keeps the first invalid declaration even if a later setter replaces it.
func (operation *OperationBuilder) fail(err error) {
	if operation.err == nil {
		operation.err = err
	}
}

// ID sets the optional globally unique operation identifier. A registration
// expanded to multiple methods must omit ID or export reports duplicates.
//
// Example:
//
//	operation.ID("getUser")
func (operation *OperationBuilder) ID(id string) *OperationBuilder {
	operation.value["operationId"] = id

	return operation
}

// Summary sets a short human-facing label; it never changes handler behavior.
// Later calls replace the previous text.
//
// Example:
//
//	operation.Summary("Get a user")
func (operation *OperationBuilder) Summary(text string) *OperationBuilder {
	operation.value["summary"] = text

	return operation
}

// Description replaces the longer operation documentation. CommonMark is
// passed through without interpreting or sanitizing it.
//
// Example:
//
//	operation.Description("Returns the current user's public profile.")
func (operation *OperationBuilder) Description(text string) *OperationBuilder {
	operation.value["description"] = text

	return operation
}

// Tags replaces the classification labels, copying the caller's slice.
// Empty input clears the labels and does not affect registration.
//
// Example:
//
//	operation.Tags("users", "public")
func (operation *OperationBuilder) Tags(tags ...string) *OperationBuilder {
	operation.value["tags"] = append([]string{}, tags...)

	return operation
}

// Deprecated marks the operation as deprecated in the document only. The
// route remains callable and no warning header is added.
//
// Example:
//
//	operation.Deprecated(true)
func (operation *OperationBuilder) Deprecated(deprecated bool) *OperationBuilder {
	operation.value["deprecated"] = deprecated

	return operation
}

// Hidden excludes this registration from export, including its metadata
// errors. It does not hide the route from Routes or prevent requests.
//
// Example:
//
//	operation.Hidden(true)
func (operation *OperationBuilder) Hidden(hidden bool) *OperationBuilder {
	operation.hidden = hidden

	return operation
}

// Body adds or replaces one request representation. Schema bytes are rendered
// immediately; errors remain sticky until export. No body parsing is added.
//
// Example:
//
//	operation.Body("application/xml", router.RawSchema(`{"type":"string"}`))
func (operation *OperationBuilder) Body(media string, schema Schema) *OperationBuilder {
	body := operation.object("requestBody")
	operation.media(body, media, schema)

	return operation
}

// JSONBody describes an application/json request body without requiring it.
// Use BodyRequired to mark presence as required in the document.
//
// Example:
//
//	operation.JSONBody(router.RawSchema(`{"type":"object"}`)).BodyRequired(true)
func (operation *OperationBuilder) JSONBody(schema Schema) *OperationBuilder {
	return operation.Body("application/json", schema)
}

// BodyRequired describes whether the request body is required. This setting
// does not reject empty requests; handlers remain responsible for validation.
//
// Example:
//
//	operation.BodyRequired(true)
func (operation *OperationBuilder) BodyRequired(required bool) *OperationBuilder {
	operation.object("requestBody")["required"] = required

	return operation
}

// Response declares a response status and description without adding a body.
// Existing representations and headers for that status are preserved. Status
// must be 100–599; an empty description uses the standard HTTP status text.
//
// Example:
//
//	operation.Response(204, "Deleted")
func (operation *OperationBuilder) Response(status int, description string) *OperationBuilder {
	response := operation.response(status)

	if description == "" {
		description = http.StatusText(status)
	}

	response["description"] = description

	return operation
}

// MediaResponse adds a response representation without choosing an encoder.
// Repeated calls with different media types describe content negotiation.
//
// Example:
//
//	operation.MediaResponse(200, "text/plain", router.RawSchema(`{"type":"string"}`))
func (operation *OperationBuilder) MediaResponse(status int, media string, schema Schema) *OperationBuilder {
	operation.media(operation.response(status), media, schema)

	return operation
}

// JSONResponse describes application/json output at status. It never marshals
// handler values, validates output, or adds automatic error responses.
//
// Example:
//
//	operation.JSONResponse(200, router.RawSchema(`{"type":"object"}`))
func (operation *OperationBuilder) JSONResponse(status int, schema Schema) *OperationBuilder {
	return operation.MediaResponse(status, "application/json", schema)
}

// object obtains an operation member with object shape, creating it when absent.
func (operation *OperationBuilder) object(name string) map[string]any {
	value, ok := operation.value[name].(map[string]any)

	if !ok {
		value = map[string]any{}
		operation.value[name] = value
	}

	return value
}

// response obtains one status record and records invalid status numbers.
func (operation *OperationBuilder) response(status int) map[string]any {
	if status < 100 || status > 599 {
		operation.fail(fmt.Errorf("response status %d must be 100–599", status))
	}

	responses := operation.object("responses")
	key := strconv.Itoa(status)
	value, ok := responses[key].(map[string]any)

	if !ok {
		value = map[string]any{"description": http.StatusText(status)}
		responses[key] = value
	}

	return value
}

// media captures one rendered schema under a request or response content map.
func (operation *OperationBuilder) media(parent map[string]any, media string, schema Schema) {
	if media == "" {
		operation.fail(fmt.Errorf("media type must not be empty"))
	}

	value, err := renderSchema(schema)
	operation.fail(err)

	content, ok := parent["content"].(map[string]any)

	if !ok {
		content = map[string]any{}
		parent["content"] = content
	}

	content[media] = map[string]any{"schema": value}
}

// Parameter appends a named path, query, header, or cookie parameter. Path
// parameters are always required; others are optional. The final group path
// is checked during export. Duplicate names in one location are errors.
//
// Example:
//
//	operation.Parameter("id", "path", router.RawSchema(`{"type":"string"}`))
func (operation *OperationBuilder) Parameter(name, location string, schema Schema) *OperationBuilder {
	value, err := renderSchema(schema)
	operation.fail(err)

	parameter := map[string]any{"name": name, "in": location, "schema": value}

	if location == "path" {
		parameter["required"] = true
	}

	parameters, _ := operation.value["parameters"].([]any)
	operation.value["parameters"] = append(parameters, parameter)

	return operation
}

// ResponseHeader describes a response header's value without adding the header
// at runtime. Names match case-insensitively, as HTTP headers do. Repeated
// declarations replace both the schema and spelling of the earlier name for
// this response status. Invalid names or schemas remain export errors.
//
// Example:
//
//	operation.ResponseHeader(200, "X-Request-ID", router.RawSchema(`{"type":"string"}`))
func (operation *OperationBuilder) ResponseHeader(status int, name string, schema Schema) *OperationBuilder {
	value, err := renderSchema(schema)
	operation.fail(err)

	response := operation.response(status)
	headers, ok := response["headers"].(map[string]any)

	if !ok {
		headers = map[string]any{}
		response["headers"] = headers
	}

	canonical := http.CanonicalHeaderKey(name)

	for existing := range headers {
		if http.CanonicalHeaderKey(existing) == canonical {
			delete(headers, existing)
		}
	}

	headers[name] = map[string]any{"schema": value}

	return operation
}

// Security adds an alternative security requirement. Each call is OR with
// previous calls. Every named scheme must be explicitly declared on
// OpenAPIBuilder. The supported non-OAuth schemes require empty scopes;
// nonempty scopes are reported as an export error.
//
// Example:
//
//	operation.Security("bearer")
func (operation *OperationBuilder) Security(name string, scopes ...string) *OperationBuilder {
	requirements, _ := operation.value["security"].([]any)
	operation.value["security"] = append(requirements, map[string]any{name: append([]string{}, scopes...)})

	return operation
}

// Example adds a named request-body example for an existing representation.
// The JSON encoding is captured immediately; encoding failures are sticky.
// Examples are descriptive and are not validated against schemas.
//
// Example:
//
//	operation.Example("application/json", "newUser", map[string]string{"name": "Ada"})
func (operation *OperationBuilder) Example(media, name string, value any) *OperationBuilder {
	body := operation.object("requestBody")
	content, _ := body["content"].(map[string]any)
	representation, _ := content[media].(map[string]any)

	if representation == nil {
		operation.fail(fmt.Errorf("example %q requires body media type %q", name, media))

		return operation
	}

	data, err := json.Marshal(value)
	operation.fail(err)

	examples, ok := representation["examples"].(map[string]any)

	if !ok {
		examples = map[string]any{}
		representation["examples"] = examples
	}

	examples[name] = map[string]any{"value": jsontext.Value(data)}

	return operation
}

// Problem explicitly describes the router's negotiated RFC 9457 wire model
// at the supplied status. It advertises both JSON media types and plain text,
// not Details' private Go fields or its error cause. It performs no automatic
// mapping and does not imply that every route error has this shape.
//
// Example:
//
//	operation.Problem(problem.NotFound())
func (operation *OperationBuilder) Problem(details problem.Details) *OperationBuilder {
	status := details.Status()

	if status == 0 {
		status = http.StatusInternalServerError
	}

	schema := RawSchema(`{"type":"object","properties":{"type":{"type":"string"},"title":{"type":"string"},"status":{"type":"integer"},"detail":{"type":"string"},"instance":{"type":"string"}},"additionalProperties":true}`)
	operation.Response(status, details.Title())
	operation.MediaResponse(status, "application/problem+json", schema)
	operation.MediaResponse(status, "application/json", schema)
	operation.MediaResponse(status, "text/plain", RawSchema(`{"type":"string"}`))

	return operation
}
