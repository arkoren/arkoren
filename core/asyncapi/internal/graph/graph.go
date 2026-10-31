// Package graph validates references in completed raw AsyncAPI documents.
package graph

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// graph is a validation-only view of the immutable JSON snapshot. Raw nodes
// avoid numeric conversion and keep protocol-specific metadata opaque.
type graph struct {
	// Info contains the required human-readable title and contract version.
	Info map[string]string `json:"info"`

	// Servers maps stable endpoint IDs to raw server objects.
	Servers map[string]jsontext.Value `json:"servers"`

	// Channels maps stable IDs to raw channel objects.
	Channels map[string]jsontext.Value `json:"channels"`

	// Operations contains the application's send and receive declarations.
	Operations map[string]jsontext.Value `json:"operations"`

	// Components contains reusable schemas and security scheme objects.
	Components map[string]map[string]jsontext.Value `json:"components"`
}

// Validate checks graph constraints against the completed snapshot, so
// forward references work independently of declaration order. It is not a full
// AsyncAPI or protocol-specific validator.
func Validate(data []byte) error {
	var document graph

	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}

	if document.Info["title"] == "" || document.Info["version"] == "" {
		return fmt.Errorf("title and version must not be empty")
	}

	for _, validate := range []func(graph) error{validateServers, validateChannels, validateOperations, validateSchemes} {
		if err := validate(document); err != nil {
			return err
		}
	}

	return nil
}

// validateServers checks host-only endpoints and nonempty protocol names.
// Host parsing rejects credentials rather than attempting to redact secrets.
func validateServers(document graph) error {
	schemes := document.Components["securitySchemes"]

	for _, id := range sortedKeys(document.Servers) {
		var server map[string]jsontext.Value

		if err := json.Unmarshal(document.Servers[id], &server); err != nil {
			return err
		}

		if err := validateHost(rawString(server["host"])); err != nil {
			return fmt.Errorf("server %q: %w", id, err)
		}

		if strings.TrimSpace(rawString(server["protocol"])) == "" {
			return fmt.Errorf("server %q: protocol must not be empty", id)
		}

		if err := validateReferenceSelection(server["security"], "#/components/securitySchemes/", schemes); err != nil {
			return fmt.Errorf("server %q: %w", id, err)
		}
	}

	return nil
}

// validateHost accepts URI authority host syntax, including bracketed IPv6,
// while rejecting URI schemes, secrets, paths and queries from public metadata.
func validateHost(host string) error {
	if strings.TrimSpace(host) == "" || strings.ContainsAny(host, "/?#@{}\\") {
		return fmt.Errorf("host must be a nonempty credential-free host or host:port")
	}

	endpoint, err := url.Parse("//" + host)

	if err != nil {
		return fmt.Errorf("invalid host: %w", err)
	}

	if endpoint.Hostname() == "" || endpoint.User != nil {
		return fmt.Errorf("host must not contain credentials")
	}

	return nil
}

// validateChannels enforces explicit server selection when endpoints exist,
// and rejects address templates until a parameter declaration API exists.
func validateChannels(document graph) error {
	for _, id := range sortedKeys(document.Channels) {
		var channel map[string]jsontext.Value

		if err := json.Unmarshal(document.Channels[id], &channel); err != nil {
			return err
		}

		if strings.ContainsAny(rawString(channel["address"]), "{}") {
			return fmt.Errorf("channel %q: address templates are not supported", id)
		}

		if len(document.Servers) != 0 && len(rawArray(channel["servers"])) == 0 {
			return fmt.Errorf("channel %q: explicit server selection is required", id)
		}

		if err := validateReferenceSelection(channel["servers"], "#/servers/", document.Servers); err != nil {
			return fmt.Errorf("channel %q: %w", id, err)
		}

		if err := validateMessages(channel["messages"]); err != nil {
			return fmt.Errorf("channel %q: %w", id, err)
		}
	}

	return nil
}

// validateMessages checks the content type boundary without attempting schema
// execution. Schema syntax and capture errors belong to the caller.
func validateMessages(value jsontext.Value) error {
	var messages map[string]map[string]jsontext.Value

	if err := json.Unmarshal(value, &messages); err != nil {
		return err
	}

	for _, id := range sortedKeys(messages) {
		if strings.TrimSpace(rawString(messages[id]["contentType"])) == "" {
			return fmt.Errorf("message %q: content type must not be empty", id)
		}
	}

	return nil
}

// validateOperations visits every operation ID, including names that resemble
// annotation fields. References resolve against the completed document graph.
func validateOperations(document graph) error {
	for _, id := range sortedKeys(document.Operations) {
		var operation map[string]jsontext.Value

		if err := json.Unmarshal(document.Operations[id], &operation); err != nil {
			return err
		}

		if err := validateOperation(operation, document); err != nil {
			return fmt.Errorf("operation %q: %w", id, err)
		}
	}

	return nil
}

// validateOperation requires a channel and an explicit nonempty selection from
// that channel's messages. Security selections use the root scheme collection;
// schemas, examples, bindings and extension values are not graph edges.
func validateOperation(operation map[string]jsontext.Value, document graph) error {
	if len(rawArray(operation["messages"])) == 0 {
		return fmt.Errorf("messages must select at least one message")
	}

	id, err := referencedID(operation["channel"], "#/channels/", document.Channels)

	if err != nil {
		return err
	}

	var channel map[string]jsontext.Value

	if err := json.Unmarshal(document.Channels[id], &channel); err != nil {
		return err
	}

	var messages map[string]jsontext.Value

	if err := json.Unmarshal(channel["messages"], &messages); err != nil {
		return err
	}

	if err := validateReferenceSelection(operation["messages"], "#/channels/"+id+"/messages/", messages); err != nil {
		return err
	}

	schemes := document.Components["securitySchemes"]

	return validateReferenceSelection(operation["security"], "#/components/securitySchemes/", schemes)
}

// validateSchemes recognizes AsyncAPI security types while leaving vendor and
// OAuth flow semantics to callers and external validators.
func validateSchemes(document graph) error {
	for _, id := range sortedKeys(document.Components["securitySchemes"]) {
		var scheme map[string]jsontext.Value

		if err := json.Unmarshal(document.Components["securitySchemes"][id], &scheme); err != nil {
			return err
		}

		if err := validateScheme(scheme, document.Components["securitySchemes"]); err != nil {
			return fmt.Errorf("security scheme %q: %w", id, err)
		}
	}

	return nil
}

// validateScheme checks the known type enum and required shape for common
// scheme families. Local aliases must resolve within the security collection;
// external references remain opaque and never cause network access.
func validateScheme(scheme map[string]jsontext.Value, schemes map[string]jsontext.Value) error {
	if _, reference := scheme["$ref"]; reference {
		return validateSchemeReference(scheme, schemes)
	}

	kind := rawString(scheme["type"])
	known := strings.Fields("userPassword apiKey X509 symmetricEncryption asymmetricEncryption " +
		"httpApiKey http oauth2 openIdConnect plain scramSha256 scramSha512 gssapi")

	if !slices.Contains(known, kind) {
		return fmt.Errorf("unsupported security scheme type %q", kind)
	}

	return validateSchemeFields(kind, scheme)
}

// validateSchemeFields checks required scalar members, not protocol behavior.
// Unknown members are preserved as caller-owned protocol documentation.
func validateSchemeFields(kind string, scheme map[string]jsontext.Value) error {
	required := map[string][]string{
		"http": {"scheme"}, "httpApiKey": {"name", "in"},
		"apiKey": {"in"}, "openIdConnect": {"openIdConnectUrl"}, "oauth2": {"flows"},
	}

	for _, member := range required[kind] {
		if err := validateSchemeMember(member, scheme[member]); err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
	}

	allowed := map[string][]string{"apiKey": {"user", "password"}, "httpApiKey": {"header", "query", "cookie"}}

	if locations, ok := allowed[kind]; ok && !slices.Contains(locations, rawString(scheme["in"])) {
		return fmt.Errorf("invalid %s security location", kind)
	}

	return nil
}

// validateSchemeMember checks the small structural boundary of required scheme
// members. OAuth flow semantics remain opaque, but flows must be an object.
func validateSchemeMember(member string, value jsontext.Value) error {
	if member == "flows" {
		if value.Kind() != '{' {
			return fmt.Errorf("flows must be an object")
		}

		return nil
	}

	if rawString(value) == "" {
		return fmt.Errorf("%s must be a nonempty string", member)
	}

	return nil
}

// validateSchemeReference keeps local security aliases within their component
// collection. External references remain caller-owned and are never fetched.
func validateSchemeReference(scheme map[string]jsontext.Value, schemes map[string]jsontext.Value) error {
	reference := rawString(scheme["$ref"])

	if reference == "" {
		return fmt.Errorf("security scheme reference must not be empty")
	}

	if !strings.HasPrefix(reference, "#") {
		return nil
	}

	_, err := referenceTarget(reference, "#/components/securitySchemes/", schemes)

	return err
}

// rawString reads a string member from an internally encoded object. Non-string
// and null values return empty, which the calling validation treats as absent.
func rawString(value jsontext.Value) string {
	var text string

	if err := json.Unmarshal(value, &text); err != nil {
		return ""
	}

	return text
}

// rawArray reads a reference array from an internally encoded object. Missing
// and non-array members are treated as empty required selections.
func rawArray(value jsontext.Value) []jsontext.Value {
	var items []jsontext.Value

	if err := json.Unmarshal(value, &items); err != nil {
		return nil
	}

	return items
}

// sortedKeys makes first-error selection stable independently of map iteration.
// It copies only keys; values remain owned by the immutable snapshot.
func sortedKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))

	for key := range items {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// validateReferenceSelection checks only a declared graph-edge array. Each
// reference must target an existing ID in the specified collection, regardless
// of whether that ID resembles an annotation or extension member name.
func validateReferenceSelection(value jsontext.Value, prefix string, targets map[string]jsontext.Value) error {
	for _, child := range rawArray(value) {
		if _, err := referencedID(child, prefix, targets); err != nil {
			return err
		}
	}

	return nil
}

// referencedID resolves a reference object within one known graph collection.
// Prefix matching also prevents operation messages from selecting another
// channel's messages. This never walks arbitrary JSON or fetches external data.
func referencedID(value jsontext.Value, prefix string, targets map[string]jsontext.Value) (string, error) {
	var object map[string]jsontext.Value

	if err := json.Unmarshal(value, &object); err != nil {
		return "", err
	}

	reference := rawString(object["$ref"])

	return referenceTarget(reference, prefix, targets)
}

// referenceTarget resolves a known graph edge by collection prefix and ID.
// Both captured reference objects and raw security aliases share this boundary;
// a matching prefix never exempts a missing target from validation.
func referenceTarget(reference, prefix string, targets map[string]jsontext.Value) (string, error) {
	id, local := strings.CutPrefix(reference, prefix)

	if !local {
		return "", fmt.Errorf("unresolved reference %q: expected prefix %q", reference, prefix)
	}

	if _, exists := targets[id]; !exists {
		return "", fmt.Errorf("unresolved reference %q", reference)
	}

	return id, nil
}
