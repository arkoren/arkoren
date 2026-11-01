package remote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Schema is the part of a JSON Schema this command reads: enough to know
// which flags an operation takes and how to convert their text. The server is
// the only validator, so rules such as minimum or pattern are not read.
type Schema struct {
	// Type is the JSON Schema Type: a string, or a list such as
	// ["string","null"] (the first non-null entry counts).
	Type any `json:"type"`

	// Description is the human-readable text shown in help.
	Description string `json:"description"`

	// Properties are the members of an object schema.
	Properties map[string]*Schema `json:"properties"`

	// Required lists the members an object must have.
	Required []string `json:"required"`

	// Items is the element schema of an array.
	Items *Schema `json:"items"`

	// AdditionalProperties is false, true, or the value schema of a map.
	AdditionalProperties jsontext.Value `json:"additionalProperties"`

	// Enum lists the allowed values, each as JSON text.
	Enum []jsontext.Value `json:"enum"`

	// Default is the value the server applies when the input omits it, as
	// JSON text; empty when there is none.
	Default jsontext.Value `json:"default"`
}

// ParseSchema decodes a schema document. An unreadable document yields the
// empty (untyped) schema, so every value is then treated as JSON or text. Null
// property fragments receive that same fallback before help or flag traversal;
// optional external hints must never introduce nil nodes into those walks.
func ParseSchema(doc []byte) *Schema {
	root := &Schema{}

	if err := json.Unmarshal(doc, root); err != nil {
		return &Schema{}
	}

	root.normalizeProperties()

	return root
}

// normalizeProperties replaces malformed null property schemas with untyped
// nodes in this freshly decoded tree. Missing array items remain optional and
// are handled by step; non-null properties and items are normalized recursively.
func (node *Schema) normalizeProperties() {
	for name, child := range node.Properties {
		if child == nil {
			child = &Schema{}
			node.Properties[name] = child
		}

		child.normalizeProperties()
	}

	if node.Items != nil {
		node.Items.normalizeProperties()
	}
}

// kind returns the JSON Schema type name, "" when the schema is untyped.
func (node *Schema) kind() string {
	switch typed := node.Type.(type) {
	case string:
		return typed
	case []any:
		for _, entry := range typed {
			if name, ok := entry.(string); ok && name != "null" {
				return name
			}
		}
	}

	if node.Type == nil && len(node.Properties) > 0 {
		return "object"
	}

	return ""
}

// values returns the schema of a map's values: the additionalProperties
// Schema, an untyped schema for true, and nil when entries are not allowed.
func (node *Schema) values() *Schema {
	switch string(node.AdditionalProperties) {
	case "", "false":
		return nil
	case "true":
		return &Schema{}
	}

	return ParseSchema(node.AdditionalProperties)
}

// isScalar reports whether a value of this schema is a string, number,
// integer, or boolean.
func (node *Schema) isScalar() bool {
	return slices.Contains([]string{"string", "integer", "number", "boolean"}, node.kind())
}

// typeName describes the schema for help: its type, with "array of T" and
// "map of T" spelled out.
func (node *Schema) typeName() string {
	switch node.kind() {
	case "array":
		if node.Items != nil && node.Items.kind() != "" {
			return "array of " + node.Items.typeName()
		}

		return "array"
	case "object":
		if values := node.values(); values != nil && len(node.Properties) == 0 {
			if name := values.typeName(); name != "any" {
				return "map of " + name
			}

			return "map"
		}

		return "object"
	case "":
		return "any"
	}

	return node.kind()
}

// pathError is returned by [schema.step] when a path segment names nothing.
type pathError struct {
	// segment is the offending path segment.
	segment string

	// reason explains why it was refused when it is not simply unknown.
	reason string
}

// Error implements error.
func (failure *pathError) Error() string {
	if failure.reason != "" {
		return failure.reason
	}

	return "unknown property " + strconv.Quote(failure.segment)
}

// step returns the schema reached from node by one path segment: an object
// property, a map entry, or an array index.
func (node *Schema) step(segment string) (*Schema, error) {
	switch node.kind() {
	case "array":
		if index, err := strconv.Atoi(segment); err != nil || index < 0 {
			return nil, &pathError{
				segment: segment,
				reason:  fmt.Sprintf("%q is not an array index", segment),
			}
		}

		if node.Items == nil {
			return &Schema{}, nil
		}

		return node.Items, nil
	case "object":
		if child, ok := node.Properties[segment]; ok {
			return child, nil
		}

		if values := node.values(); values != nil {
			return values, nil
		}
	}

	return nil, &pathError{
		segment: segment,
	}
}

// flagInfo is one flag an operation accepts, as listed by help and offered
// as a suggestion.
type flagInfo struct {
	// path is the dotted path, with <key> for a map entry and <n> for an
	// array index.
	path string

	// schema is the schema of the value the flag sets.
	schema *Schema

	// required reports that the enclosing object requires the property.
	required bool
}

// maxFlagDepth bounds how deep [schema.flags] descends, which keeps help
// readable for deeply nested inputs.
const maxFlagDepth = 4

// flags lists every flag path of the schema, parents before their children
// and properties in name order.
func (node *Schema) flags(prefix string, depth int) []flagInfo {
	var list []flagInfo

	for _, name := range slices.Sorted(maps.Keys(node.Properties)) {
		child := node.Properties[name]
		list = append(list, flagInfo{
			path:     prefix + name,
			schema:   child,
			required: slices.Contains(node.Required, name),
		})
		list = append(list, child.nested(prefix+name, depth+1)...)
	}

	return list
}

// nested lists the flags below the value at path: the members of an object,
// the entries of a map, and the elements of an array of objects.
func (node *Schema) nested(path string, depth int) []flagInfo {
	if depth >= maxFlagDepth {
		return nil
	}

	switch node.kind() {
	case "object":
		list := node.flags(path+".", depth)

		if values := node.values(); values != nil && len(node.Properties) == 0 {
			list = append(list, flagInfo{
				path:     path + ".<key>",
				schema:   values,
				required: false,
			})
			list = append(list, values.nested(path+".<key>", depth+1)...)
		}

		return list
	case "array":
		if node.Items != nil && !node.Items.isScalar() && node.Items.kind() != "" {
			return append([]flagInfo{{
				path:     path + ".<n>",
				schema:   node.Items,
				required: false,
			}}, node.Items.nested(path+".<n>", depth+1)...)
		}
	}

	return nil
}

// closest returns up to limit names nearest to word by edit distance, nearest
// first, keeping only those within a distance that suggests a typo rather
// than a different word. The order among equals is the order of names.
func closest(word string, names []string, limit int) []string {
	type scored struct {
		// name retains the candidate spelling for the final suggestion list.
		name string

		// distance orders candidates stably by case-insensitive edit distance.
		distance int
	}

	var near []scored

	for _, name := range names {
		if distance := editDistance(word, name); distance <= max(2, len(word)/3) {
			near = append(near, scored{
				name:     name,
				distance: distance,
			})
		}
	}

	slices.SortStableFunc(near, func(a, b scored) int {
		return a.distance - b.distance
	})

	var out []string

	for _, entry := range near[:min(limit, len(near))] {
		out = append(out, entry.name)
	}

	return out
}

// editDistance is the Levenshtein distance between two strings, compared
// case-insensitively.
func editDistance(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	previous := make([]int, len(b)+1)

	for column := range previous {
		previous[column] = column
	}

	for row := 1; row <= len(a); row++ {
		current := make([]int, len(b)+1)
		current[0] = row

		for column := 1; column <= len(b); column++ {
			cost := 1

			if a[row-1] == b[column-1] {
				cost = 0
			}

			current[column] = min(previous[column]+1, current[column-1]+1, previous[column-1]+cost)
		}

		previous = current
	}

	return previous[len(b)]
}
