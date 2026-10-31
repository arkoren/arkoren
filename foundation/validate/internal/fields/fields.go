// Package fields resolves JSON-v2 field visibility and reflection paths.
// It does not compile validation rules or mutate application data.
package fields

import (
	"cmp"
	"encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// Field describes one JSON-visible Go field selected by discovery.
// Its Go name selects registrations; its JSON name determines error pointers.
type Field struct {
	// GoName is the Go field selector accepted during registration.
	GoName string
	// JSONName is the encoded field name used in JSON Pointers and schema
	// properties.
	JSONName string
	// Index is the reflection path through promoted fields to the selected value.
	Index []int
	// Type is the exact selected field type, including named-type identity.
	Type reflect.Type
	// depth counts embedding levels for dominant-field selection.
	depth int
	// tagged marks an explicit JSON name, which breaks same-depth promotion ties.
	tagged bool
}

// Struct is the resolved field table of a struct type.
type Struct struct {
	// Fields lists selected JSON-visible fields in Go declaration order.
	Fields []*Field
	// ByGo indexes selected fields by their Go selector for registration lookup.
	ByGo map[string]*Field
	// Rejected maps unavailable selectors to the reason registration must reject
	// them.
	Rejected map[string]string
}

var (
	// marshalerType identifies the JSON Marshaler interface during field
	// discovery. Types with custom encodings are leaves rather than promoted
	// objects.
	marshalerType = reflect.TypeFor[json.Marshaler]()
	// marshalerToType identifies the streaming JSON MarshalerTo interface during
	// field discovery. It has the same leaf treatment as Marshaler.
	marshalerToType = reflect.TypeFor[json.MarshalerTo]()
)

// Marshals reports whether the type, or a pointer to it, customizes its
// JSON encoding. Such types are leaves: their fields are never promoted.
func Marshals(typ reflect.Type) bool {
	pointer := reflect.PointerTo(typ)

	return pointer.Implements(marshalerType) || pointer.Implements(marshalerToType)
}

// Inspect resolves the JSON names of the fields of a struct type
// following the encoding/json/v2 rules. The caller caches the result.
func Inspect(typ reflect.Type) *Struct {
	info := &Struct{
		ByGo:     make(map[string]*Field),
		Rejected: make(map[string]string),
	}

	info.collect(typ, nil, make(map[reflect.Type]bool))

	groups := make(map[string][]*Field)

	for _, candidate := range info.Fields {
		groups[candidate.JSONName] = append(groups[candidate.JSONName], candidate)
	}

	info.Fields = nil

	for name, group := range groups {
		winner := dominant(group)

		if winner == nil {
			for _, loser := range group {
				if _, ok := info.Rejected[loser.GoName]; !ok {
					info.Rejected[loser.GoName] = "its JSON name " + quote(name) + " conflicts with another field"
				}
			}

			continue
		}

		info.Fields = append(info.Fields, winner)
	}

	slices.SortFunc(info.Fields, func(left, right *Field) int {
		return slices.Compare(left.Index, right.Index)
	})

	for _, field := range info.Fields {
		selected, ok := typ.FieldByName(field.GoName)

		if !ok {
			info.Rejected[field.GoName] = "its Go selector is ambiguous"
			continue
		}

		if slices.Equal(selected.Index, field.Index) {
			info.ByGo[field.GoName] = field
		}
	}

	return info
}

// Lookup resolves a Go selector that is either a JSON-visible terminal field or
// an untagged embedded struct on an explicit path. Embedded containers have no
// JSON segment: their selected children retain their flattened JSON names.
// FieldByName enforces Go ambiguity and shortest-depth selection independently
// of JSON dominance, including selectors through unexported value embeddings.
func (info *Struct) Lookup(typ reflect.Type, name string) (*Field, bool) {
	if field, ok := info.ByGo[name]; ok {
		return field, true
	}

	field, ok := typ.FieldByName(name)

	if !ok || !embeddedPath(field) {
		return nil, false
	}

	return &Field{
		GoName: name,
		Index:  field.Index,
		Type:   field.Type,
	}, true
}

// embeddedPath reports whether a selector names a flattened embedded struct.
// Tagged, excluded, custom-marshaled and unsupported inline fields cannot be
// traversed this way because doing so would invent a JSON path the engine does
// not expose. Unexported pointer embeddings retain collect's existing rejection.
func embeddedPath(field reflect.StructField) bool {
	name, reason := jsonFieldName(field)

	return reason == "" && promotedType(field, name) != nil
}

// promotedType returns the struct whose fields replace an embedded container.
// Explicit names, custom encoders and unexported pointer owners are leaves or
// unavailable fields, not promotion candidates. Tag validity is checked by the
// caller so discovery and explicit-path lookup use the same promotion rule.
func promotedType(field reflect.StructField, name string) reflect.Type {
	if !field.Anonymous || name != "" {
		return nil
	}

	typ := field.Type

	if typ.Kind() == reflect.Pointer {
		if !field.IsExported() {
			return nil
		}

		typ = typ.Elem()
	}

	if typ.Kind() != reflect.Struct || Marshals(typ) {
		return nil
	}

	return typ
}

// quote wraps text in double quotes.
func quote(text string) string {
	return `"` + text + `"`
}

// dominant picks the field that wins a JSON name conflict: the shallowest,
// then the explicitly tagged one. It returns nil when the winner is
// ambiguous.
func dominant(group []*Field) *Field {
	shallowest := slices.MinFunc(group, func(left, right *Field) int {
		return cmp.Compare(left.depth, right.depth)
	}).depth

	var best []*Field

	for _, field := range group {
		if field.depth == shallowest {
			best = append(best, field)
		}
	}

	if len(best) > 1 {
		var tagged []*Field

		for _, field := range best {
			if field.tagged {
				tagged = append(tagged, field)
			}
		}

		best = tagged
	}

	if len(best) != 1 {
		return nil
	}

	return best[0]
}

// collect discovers candidates before Inspect resolves JSON dominance.
// The active type path suppresses recursive embedding instead of inventing a
// named property for a field that JSON-v2 promotes. Sibling paths can revisit
// the same type; depth is derived from the reflection prefix.
func (info *Struct) collect(typ reflect.Type, prefix []int, visited map[reflect.Type]bool) {
	if visited[typ] {
		return
	}

	visited[typ] = true
	defer delete(visited, typ)

	for index := range typ.NumField() {
		field := typ.Field(index)
		path := append(slices.Clone(prefix), index)
		name, reason := jsonFieldName(field)

		if reason != "" {
			info.Rejected[field.Name] = reason

			continue
		}

		if embedded := promotedType(field, name); embedded != nil {
			info.collect(embedded, path, visited)

			continue
		}

		if !field.IsExported() {
			info.Rejected[field.Name] = "it is unexported"
			continue
		}

		info.Fields = append(info.Fields, &Field{
			GoName:   field.Name,
			JSONName: cmp.Or(name, field.Name),
			Index:    path,
			Type:     field.Type,
			depth:    len(prefix),
			tagged:   name != "",
		})
	}
}

// jsonFieldName resolves a field's explicit JSON name and rejects names that
// Go 1.27's JSON-v2 encoder cannot represent. An empty name retains the Go name
// or implicit embedding; a nonempty reason prevents schema registration.
// Unsupported flattening options are rejected before any path is invented.
func jsonFieldName(field reflect.StructField) (name, reason string) {
	tag := field.Tag.Get("json")

	if tag == "-" {
		return "", `it is excluded with json:"-"`
	}

	name, options, _ := strings.Cut(tag, ",")

	if strings.ContainsAny(name, "\\'\"`") || !utf8.ValidString(name) {
		return name, "its JSON name is invalid for encoding/json/v2"
	}

	return name, rejectedOption(options)
}

// rejectedOption returns why a json tag option makes a field unusable in
// a schema, or the empty string.
func rejectedOption(options string) string {
	for _, option := range Options(options) {
		switch option {
		case "inline":
			return "it uses the json inline option"
		case "embed":
			return "it uses the unsupported json embed option"
		case "unknown":
			return "it uses the json unknown option"
		}
	}

	return ""
}

// Options splits the option suffix of a JSON-v2 field tag without
// interpreting commas inside single-quoted format strings as option boundaries.
// Backslash escapes protect the next character inside a quoted string. This
// only locates boundaries; the JSON encoder still owns tag validity and format
// interpretation. An empty suffix produces one empty, harmless option.
func Options(options string) []string {
	var result []string

	start := 0
	quoted := false
	escaped := false

	for index, character := range options {
		switch {
		case escaped:
			escaped = false
		case quoted && character == '\\':
			escaped = true
		case character == '\'':
			quoted = !quoted
		case !quoted && character == ',':
			result = append(result, options[start:index])
			start = index + 1
		}
	}

	return append(result, options[start:])
}

// Value resolves the compiled field path without allocating. A nil embedded
// or explicit intermediate pointer returns an invalid Value, preserving the
// distinction between an unreachable target and a real scalar zero.
func (field *Field) Value(object reflect.Value) reflect.Value {
	value, err := object.FieldByIndexErr(field.Index)

	if err != nil {
		return reflect.Value{}
	}

	return value
}
