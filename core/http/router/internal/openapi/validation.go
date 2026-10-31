package openapi

import (
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"mime"
	"slices"
	"strings"
)

// completePath rejects opaque patterns and fills undocumented single-segment
// wildcards with required string parameters. Explicit declarations win.
func completePath(pattern string, value map[string]any) error {
	if !strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "...}") {
		return fmt.Errorf("opaque route pattern cannot be represented as an OpenAPI path")
	}

	parameters, _ := value["parameters"].([]any)

	declared, err := parameterNames(parameters, pattern)

	if err != nil {
		return err
	}

	for _, segment := range strings.Split(pattern, "/") {
		if !strings.HasPrefix(segment, "{") {
			continue
		}

		name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")

		if !declared["path:"+name] {
			parameters = append(parameters, map[string]any{"name": name, "in": "path",
				"required": true, "schema": jsontext.Value(`{"type":"string"}`)})
		}
	}

	if len(parameters) != 0 {
		value["parameters"] = parameters
	}

	return nil
}

// parameterNames checks parameter locations, uniqueness and final-path
// membership before inferred parameters are appended.
func parameterNames(parameters []any, pattern string) (map[string]bool, error) {
	names := map[string]bool{}

	for _, value := range parameters {
		parameter := value.(map[string]any)
		name := parameter["name"].(string)
		location := parameter["in"].(string)

		if err := validateParameter(name, location, pattern); err != nil {
			return nil, err
		}

		key := location + ":" + name

		if location == "header" {
			key = strings.ToLower(key)
		}

		if names[key] {
			return nil, fmt.Errorf("duplicate parameter %q in %s", name, location)
		}

		names[key] = true
	}

	return names, nil
}

// validateParameter limits the supported locations and verifies path membership.
func validateParameter(name, location, pattern string) error {
	if name == "" {
		return fmt.Errorf("parameter name must not be empty")
	}

	switch location {
	case "path":
		if !strings.Contains(pattern, "{"+name+"}") {
			return fmt.Errorf("path parameter %q is absent from final path", name)
		}
	case "query", "header", "cookie":
	default:
		return fmt.Errorf("unsupported parameter location %q", location)
	}

	return nil
}

// pathShape erases wildcard names to detect equivalent OpenAPI templates.
func pathShape(pattern string) string {
	segments := strings.Split(pattern, "/")

	for index, segment := range segments {
		if strings.HasPrefix(segment, "{") {
			segments[index] = "{}"
		}
	}

	return strings.Join(segments, "/")
}

// validateOperation checks only the supported OpenAPI envelope, never schemas
// or examples against schemas. User schemas remain opaque JSON.
func validateOperation(value, schemes map[string]any) error {
	if body, ok := value["requestBody"].(map[string]any); ok {
		if err := validateContent(body); err != nil {
			return fmt.Errorf("request body: %w", err)
		}
	}

	responses := value["responses"].(map[string]any)

	for _, status := range slices.Sorted(maps.Keys(responses)) {
		if err := validateResponse(responses[status].(map[string]any)); err != nil {
			return fmt.Errorf("response %s: %w", status, err)
		}
	}

	return validateRequirements(value, schemes)
}

// validateResponse checks header names and each representation's media type.
func validateResponse(response map[string]any) error {
	headers, _ := response["headers"].(map[string]any)

	for _, name := range slices.Sorted(maps.Keys(headers)) {
		if !validToken(name) {
			return fmt.Errorf("invalid response header %q", name)
		}
	}

	if _, ok := response["content"]; !ok {
		return nil
	}

	return validateContent(response)
}

// validateContent requires at least one valid media representation when a
// request body or response content object is present.
func validateContent(parent map[string]any) error {
	content, _ := parent["content"].(map[string]any)

	if len(content) == 0 {
		return fmt.Errorf("content must contain at least one media type")
	}

	for _, media := range slices.Sorted(maps.Keys(content)) {
		kind, _, err := mime.ParseMediaType(media)

		if err != nil {
			return fmt.Errorf("invalid media type %q: %w", media, err)
		}

		if !strings.Contains(kind, "/") {
			return fmt.Errorf("media type %q requires type/subtype", media)
		}
	}

	return nil
}

// validToken checks HTTP token syntax for header names and HTTP schemes.
func validToken(value string) bool {
	if value == "" {
		return false
	}

	return strings.IndexFunc(value, func(character rune) bool {
		return !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", character)
	}) == -1
}

// ValidateSecurityScheme supports a deliberately bounded subset with explicit
// required fields. Unsupported kinds are errors rather than guessed policies.
func ValidateSecurityScheme(name string, scheme map[string]any) error {
	if name == "" || strings.IndexFunc(name, invalidComponentCharacter) != -1 {
		return fmt.Errorf("invalid component name")
	}

	kind, _ := scheme["type"].(string)

	if err := validateSecurityFields(scheme); err != nil {
		return err
	}

	switch kind {
	case "http":
		method, _ := scheme["scheme"].(string)

		if !validToken(method) {
			return fmt.Errorf("http scheme requires a valid scheme token")
		}
	case "apiKey":
		return validateAPIKey(scheme)
	case "mutualTLS":
	default:
		return fmt.Errorf("unsupported security scheme type %q", kind)
	}

	return nil
}

// validateSecurityFields rejects unsupported members instead of passing
// malformed OpenAPI through a loosely typed security-scheme escape hatch.
func validateSecurityFields(scheme map[string]any) error {
	fields := map[string]string{
		"http":      " type scheme bearerFormat description ",
		"apiKey":    " type name in description ",
		"mutualTLS": " type description ",
	}
	kind, _ := scheme["type"].(string)

	for _, name := range slices.Sorted(maps.Keys(scheme)) {
		if strings.HasPrefix(name, "x-") {
			continue
		}

		if !strings.Contains(fields[kind], " "+name+" ") {
			return fmt.Errorf("unsupported security scheme field %q", name)
		}

		if _, ok := scheme[name].(string); !ok {
			return fmt.Errorf("security scheme field %q must be a string", name)
		}
	}

	return nil
}

// validateAPIKey verifies a concrete key name and supported transport location.
func validateAPIKey(scheme map[string]any) error {
	name, _ := scheme["name"].(string)
	location, _ := scheme["in"].(string)

	if name == "" {
		return fmt.Errorf("apiKey requires a name")
	}

	if location != "query" && location != "header" && location != "cookie" {
		return fmt.Errorf("apiKey requires query, header, or cookie location")
	}

	if location == "header" && !validToken(name) {
		return fmt.Errorf("invalid apiKey header name")
	}

	return nil
}

// invalidComponentCharacter restricts component keys to the OpenAPI alphabet.
func invalidComponentCharacter(character rune) bool {
	return !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ._-", character)
}

// validateRequirements requires explicit schemes and disallows OAuth scopes on
// the non-OAuth schemes supported by this bounded API.
func validateRequirements(value, schemes map[string]any) error {
	requirements, _ := value["security"].([]any)

	for _, value := range requirements {
		if err := validateRequirement(value.(map[string]any), schemes); err != nil {
			return err
		}
	}

	return nil
}

// validateRequirement checks the names and scopes of one security alternative.
func validateRequirement(requirement, schemes map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(requirement)) {
		if _, ok := schemes[name]; !ok {
			return fmt.Errorf("undefined security scheme %q", name)
		}

		if len(requirement[name].([]string)) != 0 {
			return fmt.Errorf("security scheme %q does not accept scopes", name)
		}
	}

	return nil
}
