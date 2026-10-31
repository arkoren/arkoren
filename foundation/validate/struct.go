package validate

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"arkoren.dev/foundation/validate/internal/fields"
)

// StructBuilder registers typed schemas for Go field names during [Struct]
// construction. Methods mutate this temporary draft, not published schemas.
// Retaining it is unnecessary; later registrations cannot change the result.
// A builder must not be mutated concurrently.
// The type parameter selects the exact validated struct type. Typed Field
// callbacks select a concrete child draft without reflection allocation.
//
// Example:
//
//	type Person struct {
//		Name string
//	}
//	schema := validate.Struct[Person](func(object *validate.StructBuilder[Person]) {
//		object.Field("Name", func(name *validate.StringBuilder[string]) {
//			name.Required()
//		})
//	})
//	err := schema.Validate(context.Background(), Person{
//		Name: "Ada",
//	})
//	if err != nil {
//		panic(err)
//	}
type StructBuilder[T any] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[T]
	// registrations preserves declaration order, including invalid duplicates.
	// Struct snapshots this slice before sorting and compiling it.
	registrations []objectRegistration
}

// objectRegistration retains a Go field name and its immutable schema value.
// A nil schema is retained so construction can report it with other problems.
type objectRegistration struct {
	// name selects a Go field, never its JSON name.
	name string
	// schema retains the schema's exact validated type through anySchema.
	schema anySchema
}

// Field publishes a concrete typed child draft for name and returns the same
// receiver. Callbacks run once, synchronously in order on one fresh child.
// It preserves invalid registrations for the enclosing construction report.
// Unregistered fields are not validated. The
// schema must validate the field's exact Go type; named types need their
// corresponding typed drafts. Invalid shared schemas, duplicate names, and invalid
// fields are reported together by Struct after all callbacks finish.
//
// Example:
//
//	type Person struct {
//		Name string
//	}
//	schema := validate.Struct[Person](func(object *validate.StructBuilder[Person]) {
//		object.Field("Name", func(name *validate.StringBuilder[string]) {
//			name.Required()
//		})
//	})
//	err := schema.Validate(context.Background(), Person{
//		Name: "Ada",
//	})
//	if err != nil {
//		panic(err)
//	}
func (object *StructBuilder[T]) Field[D any, P draftPointer[D]](name string, configure ...ChildBuilderFunc[P]) *StructBuilder[T] {
	schema := buildChild[D](configure)

	object.registrations = append(object.registrations, objectRegistration{
		name:   name,
		schema: schema,
	})

	return object
}

// objField is a field of an object schema with its compiled node.
type objField struct {
	// info retains the selected field metadata used for extraction and
	// diagnostics.
	info *fields.Field
	// node retains the compiled child validator for this registration.
	node *node
	// schema retains the immutable child schema used for inspection and
	// composition.
	schema anySchema
}

// StructSchema validates a struct. Create one with [Struct].
//
// Errors are keyed by the JSON Pointer of each field, using the names the
// encoding/json/v2 package gives it. An object always checks its fields;
// wrap it in [Ptr] to make the whole object optional.
// A zero StructSchema is uninitialized: JSONSchema returns an error wrapping
// ErrInvalidSchema, and Parse and Validate panic with an error wrapping that
// sentinel through compilation. Use Struct[T]() for a valid schema without
// registered fields.
//
// Example:
//
//	schema := validate.Struct[struct {
//		Name string
//	}]()
//	value, err := schema.Parse(context.Background(), struct {
//		Name string
//	}{
//		Name: "Ada",
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value.Name)
type StructSchema[T any] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[T]

	// env retains the enclosing struct type and resolved field metadata.
	env *env
	// fields lists compiled registrations in Go declaration order.
	fields []objField
	// top retains the compiled whole-object rules shared between execution calls.
	top *node
}

// Struct returns a schema for the struct type T. Field registrations name
// Go fields of T, and every schema must validate exactly the type of its
// field; there is no conversion, so a named string field needs
// StringBuilder instantiated with that named string type.
//
// Callbacks run once, synchronously, in argument order on the same builder.
// Zero callbacks, including an expanded nil or empty slice, register no fields;
// T must still be a supported struct. Supplied nil callbacks and callback
// panics propagate directly. Registrations are detached before publication.
// Compiled fields execute in struct order, not registration order.
//
// Struct panics with an error wrapping [ErrInvalidSchema] when registration
// is wrong. The message lists every problem: unknown names with
// suggestions, unexported or JSON-excluded fields, type mismatches, bad
// condition fields and values, Required combined with Default, and
// defaults that fail their own schema.
// Invalid string lengths, numeric factors and non-finite numeric bounds are
// recorded without stopping callbacks and included in the same report.
// Diagnostics are sorted by Go name, preserving registration order for ties.
//
// Example:
//
//	type Signup struct {
//		Email string
//	}
//	schema := validate.Struct[Signup](func(object *validate.StructBuilder[Signup]) {
//		object.Field("Email", func(email *validate.StringBuilder[string]) {
//			email.Trim().Email().Required()
//		})
//	})
//	value, err := schema.Parse(context.Background(), Signup{
//		Email: " ada@example.com ",
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value.Email)
func Struct[T any](define ...StructBuilderFunc[T]) StructSchema[T] {
	object := &StructBuilder[T]{}

	for _, callback := range define {
		callback(object)
	}

	return object.snapshot()
}

// draftSelf preserves the concrete pointer identity of a struct draft.
func (object *StructBuilder[T]) draftSelf() *StructBuilder[T] {
	return object
}

// publish compiles the completed struct and erases only its schema type.
func (object *StructBuilder[T]) publish() anySchema {
	return object.snapshot()
}

// snapshot detaches registrations, checks exact field types and compiles a
// reusable immutable execution plan. Retained drafts share no owned slices.
func (object *StructBuilder[T]) snapshot() StructSchema[T] {
	typ := reflect.TypeFor[T]()

	if typ.Kind() != reflect.Struct {
		invalidf("Struct[%s]: the type must be a struct", typ)
	}

	info := fields.Inspect(typ)
	current := &env{
		typ:  typ,
		info: info,
	}

	rep := &report{
		defaults: true,
	}

	for _, problem := range object.problems {
		rep.addf("", "%s", problem)
	}

	registrations := slices.Clone(object.registrations)
	slices.SortStableFunc(registrations, func(left, right objectRegistration) int {
		return cmp.Compare(left.name, right.name)
	})

	fields := compileObjectFields(registrations, current, rep)

	rep.check("Struct[" + typ.String() + "]")

	slices.SortFunc(fields, func(left, right objField) int {
		return slices.Compare(left.info.Index, right.info.Index)
	})

	schema := StructSchema[T]{
		base:   object.detached(),
		env:    current,
		fields: fields,
	}

	return schema.sealed()
}

// ruleBase exposes immutable object-wide rules to a compatible draft.
func (schema StructSchema[T]) ruleBase() base[T] {
	rules := schema.base

	if schema.env == nil {
		rules.problems = append(slices.Clip(rules.problems), "shared StructSchema is uninitialized")
	}

	return rules
}

// Use adds a published schema's object-wide rules and registered fields.
// Later local Field declarations remain checked for duplicate registrations.
// The source must be StructSchema[T] (value or nonnil pointer), or InferSchema[T]
// for type-only documentation. Other roles with the same T are rejected instead
// of losing their validation plan. A typed nil pointer wraps ErrInvalidSchema.
//
// Example:
//
//	type Person struct {
//		Name string
//	}
//	shared := validate.Struct[Person]()
//	schema := validate.Struct(func(object *validate.StructBuilder[Person]) {
//		object.Use(shared)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (object *StructBuilder[T]) Use(schema Schema[T]) *StructBuilder[T] {
	source := schemaValue(schema)
	reused := reusedDefinition[T, StructSchema[T]](source)

	object.base = object.inherit(source)

	for _, field := range reused.fields {
		object.registrations = append(object.registrations, objectRegistration{
			name:   field.info.GoName,
			schema: field.schema,
		})
	}

	return object
}

// compileObjectFields checks sorted registrations against the struct and
// compiles each valid schema through the existing validation engine. Duplicate
// registrations remain errors rather than silently replacing earlier schemas.
func compileObjectFields(registrations []objectRegistration, current *env, rep *report) []objField {
	var fields []objField

	for index, registration := range registrations {
		name := registration.name

		if index > 0 && registrations[index-1].name == name {
			rep.addf(name, "duplicate field registration")
		}

		field, ok := current.info.ByGo[name]

		if !ok {
			rep.addf(name, "%s", unknownFieldProblem(current.info, name))

			continue
		}

		schema := registration.schema

		if schema == nil {
			rep.addf(name, "the schema is nil")

			continue
		}

		if schema.rtype() != field.Type {
			rep.addf(name, "the schema validates %s but the field has type %s", schema.rtype(), field.Type)

			continue
		}

		fields = append(fields, objField{
			info:   field,
			node:   schema.compile(current, rep, name),
			schema: schema,
		})
	}

	return fields
}

// sealed caches the node used for standalone validation, when the schema
// can be compiled outside another object.
func (schema StructSchema[T]) sealed() StructSchema[T] {
	rep := &report{}

	if node := schema.compile(nil, rep, ""); len(rep.lines) == 0 {
		schema.top = node
	} else {
		schema.top = nil
	}

	return schema
}

// rtype returns the exact validated Go type, including for a zero schema that
// has no resolved environment yet. Composition can check type compatibility
// before compile reports that the definition is uninitialized.
func (schema StructSchema[T]) rtype() reflect.Type {
	return reflect.TypeFor[T]()
}

// resolve maps a Go field name to its JSON name for [At].
func (schema StructSchema[T]) resolve(name string) (string, bool) {
	field, ok := schema.env.info.ByGo[name]

	if !ok {
		return "", false
	}

	return field.JSONName, true
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication; a zero
// schema contributes a diagnostic without dereferencing its missing environment.
func (schema StructSchema[T]) compile(e *env, rep *report, where string) *node {
	if schema.env == nil {
		rep.addf(where, "Struct schema is uninitialized; use Struct")

		return &node{}
	}

	n := schema.build(e, rep, where, leafHooks[T]{
		typ: schema.env.typ,
		get: func(v reflect.Value) T {
			object, _ := v.Interface().(T)

			return object
		},
		resolve: schema.resolve,
	})

	n.always = true

	for _, field := range schema.fields {
		n.mut = n.mut || field.node.mut
	}

	fields := schema.fields
	embedded := mutableEmbeddedPointers(schema.env.typ, fields)

	n.pre = func(st *state, v reflect.Value) {
		prepareFields(st, v, fields, embedded)
	}

	n.child = func(st *state, v reflect.Value) {
		runFields(st, v, fields)
	}

	schema.verifyDefault(n, rep, where)

	return n
}

// prepareFields detaches mutable embedded pointers before preparing fields,
// so sibling conditions observe defaults without changing caller-owned data.
func prepareFields(st *state, v reflect.Value, fields []objField, embedded [][]int) {
	detachEmbeddedPointers(st, v, embedded)

	for _, field := range fields {
		if st.stopped() {
			return
		}

		if value, err := v.FieldByIndexErr(field.info.Index); err == nil {
			field.node.applyDefault(st, value)
		}
	}
}

// runFields validates already prepared fields in struct order. An unreachable
// promoted field is checked as zero without allocating an embedded pointer.
func runFields(st *state, v reflect.Value, fields []objField) {
	// A field behind a nil embedded pointer is checked as its zero value:
	// non-addressable, so nothing is defaulted or normalized and Parse
	// never allocates the pointer.
	for _, field := range fields {
		if st.stopped() {
			return
		}

		value, err := v.FieldByIndexErr(field.info.Index)

		if err != nil {
			value = reflect.Zero(field.info.Type)
		}

		st.push(field.info.JSONName)
		field.node.runPrepared(st, value, v, false)
		st.pop()
	}
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Struct[struct{}]()
//	if err := schema.Validate(context.Background(), struct{}{}); err != nil {
//		panic(err)
//	}
func (schema StructSchema[T]) Validate(ctx context.Context, v T) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Struct[struct{}]()
//	value, err := schema.Parse(context.Background(), struct{}{})
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema StructSchema[T]) Parse(ctx context.Context, v T) (T, error) {
	n := schema.top

	if n == nil {
		n = compileStandalone[T](schema)
	}

	return execute(ctx, n, v)
}

// unknownFieldProblem explains why name cannot be used as a field
// reference, with a suggestion when one is close.
func unknownFieldProblem(info *fields.Struct, name string) string {
	if reason, ok := info.Rejected[name]; ok {
		return fmt.Sprintf("field %q cannot be validated: %s", name, reason)
	}

	for _, field := range info.Fields {
		if field.JSONName == name && field.GoName != name {
			return fmt.Sprintf("no field named %q; use the Go field name %q, not its JSON name", name, field.GoName)
		}
	}

	best, bestDistance := "", len(name)/3+2

	for _, candidate := range slices.Sorted(maps.Keys(info.ByGo)) {
		if distance := editDistance(strings.ToLower(name), strings.ToLower(candidate)); distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}

	if best != "" {
		return fmt.Sprintf("unknown field %q (did you mean %q?)", name, best)
	}

	return fmt.Sprintf("unknown field %q", name)
}

// editDistance returns the Levenshtein distance between two strings.
func editDistance(left, right string) int {
	previous := make([]int, len(right)+1)

	for index := range previous {
		previous[index] = index
	}

	for row := 1; row <= len(left); row++ {
		current := make([]int, len(right)+1)
		current[0] = row

		for column := 1; column <= len(right); column++ {
			cost := 1

			if left[row-1] == right[column-1] {
				cost = 0
			}

			current[column] = min(previous[column]+1, current[column-1]+1, previous[column-1]+cost)
		}

		previous = current
	}

	return previous[len(right)]
}
