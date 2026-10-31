package query

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// ErrUnknownColumn reports that [Targets] found a result column with no
	// field in the destination struct.
	//
	// Example:
	//
	//	var user struct{ ID int }
	//	_, err := query.Targets(&user, []string{"missing"})
	//	_ = errors.Is(err, query.ErrUnknownColumn)
	ErrUnknownColumn = errors.New("column has no field in destination")

	// ErrInvalidTarget reports that [Targets] cannot scan into its
	// destination: it is not a non-nil pointer, or it is a single value and
	// the result does not have exactly one column.
	//
	// Example:
	//
	//	_, err := query.Targets(7, []string{"id"})
	//	_ = errors.Is(err, query.ErrInvalidTarget)
	ErrInvalidTarget = errors.New("invalid scan destination")
)

var (
	// valuerType is the interface that marks a field as a single column value.
	valuerType = reflect.TypeFor[driver.Valuer]()

	// scannerType is the interface that marks a field as a single column value.
	scannerType = reflect.TypeFor[sql.Scanner]()

	// timeType is the one struct the field rules treat as a column value
	// without it implementing either interface.
	timeType = reflect.TypeFor[time.Time]()

	// fieldCache maps a struct reflect.Type to its *fieldSet.
	fieldCache sync.Map
)

// field is one column of a struct type.
type field struct {
	// column is the column name.
	column string

	// index is the field index path from the root struct, through embedded
	// structs.
	index []int

	// typ is the declared type of the field.
	typ reflect.Type
}

// fieldSet is the cached column map of one struct type, shared by [Values],
// [Targets], and cursor pagination.
type fieldSet struct {
	// fields are the columns in discovery order.
	fields []field

	// byColumn maps a column name to its position in fields.
	byColumn map[string]int
}

// ValuesOption filters columns for one Values call. Obtain options from Only
// and Except; reusable options retain copied names, not struct field values.
// A supplied nil option panics when Values invokes it.
//
// Example:
//
//	options := []query.ValuesOption{query.Only("name"), query.Except("id")}
//	values := query.Values(struct{ Name string }{Name: "Ada"}, options...)
//	_ = values["name"]
type ValuesOption func(*valuesConfig)

// valuesConfig holds the column filters of one [Values] call.
type valuesConfig struct {
	// only lists the columns to keep; empty keeps every column.
	only map[string]bool

	// except lists the columns to drop.
	except map[string]bool
}

// Only keeps just the named columns. Names that are not columns of the struct
// are ignored. Several Only options add up.
//
// With no columns, including an expanded nil or empty slice, Only adds no
// restriction: by itself it keeps every column and does not clear other Only
// options. [Except] still applies after Only.
//
// Only copies the column-name slice when the option is created. Later changes
// to the caller's slice do not affect the option, which can be reused.
//
// Example:
//
//	user := struct {
//		ID   int
//		Name string
//	}{ID: 7, Name: "Ada"}
//	values := query.Values(user, query.Only("name"))
//	_ = values["name"] // "Ada"; "id" is absent.
func Only(columns ...string) ValuesOption {
	columns = slices.Clone(columns)

	return func(config *valuesConfig) {
		if config.only == nil {
			config.only = make(map[string]bool, len(columns))
		}

		for _, column := range columns {
			config.only[column] = true
		}
	}
}

// Except drops the named columns. Names that are not columns of the struct
// are ignored. Several Except options add up, and Except applies after Only.
// With no columns, including an expanded nil or empty slice, it drops nothing.
//
// Except copies the column-name slice when the option is created. Later changes
// to the caller's slice do not affect the option, which can be reused.
//
// Example:
//
//	user := struct {
//		ID   int
//		Name string
//	}{ID: 7, Name: "Ada"}
//	values := query.Values(user, query.Except("id"))
//	_ = values["name"] // "Ada"; "id" is absent.
func Except(columns ...string) ValuesOption {
	columns = slices.Clone(columns)

	return func(config *valuesConfig) {
		if config.except == nil {
			config.except = make(map[string]bool, len(columns))
		}

		for _, column := range columns {
			config.except[column] = true
		}
	}
}

// keeps reports whether column belongs in the result.
func (config valuesConfig) keeps(column string) bool {
	if len(config.only) > 0 && !config.only[column] {
		return false
	}

	return !config.except[column]
}

// Values returns the column values of a struct as a map from column name to
// value, ready for [Builder.Insert], [Builder.Update], or [RawNamed]. v is a
// struct or a non-nil pointer to one; Values panics on anything else, which is
// a programming error.
//
// The columns follow one set of rules, shared with [Targets] and with cursor
// pagination:
//
//   - The column is the db tag name, the text before the first comma. An empty
//     name uses the lowercased field name, so an untagged exported field
//     "CreatedAt" is the column "createdat". db:"-" skips the field.
//   - Unexported fields are skipped.
//   - An untagged embedded struct is flattened into the columns of its
//     parent. A field of the outer struct wins over one with the same column
//     in an embedded struct; the first discovered field wins at equal depth.
//   - A field whose type implements [database/sql/driver.Valuer] or, through
//     a pointer, [database/sql.Scanner], and [time.Time], is one column and is
//     never flattened. The value is returned as is, so the database driver
//     converts it.
//     Valuer detection uses the declared field type: if only *T implements
//     Valuer, embed T to map its fields or use *T to bind the pointer itself.
//     Scanner and time.Time detection still take precedence over flattening.
//
// A nil embedded struct pointer yields a typed nil pointer for each of its
// columns, which a database driver writes as NULL. Values are not
// converted: a pointer field is returned as the pointer.
//
// Example:
//
//	user := struct {
//		ID   int
//		Name string
//	}{ID: 7, Name: "Ada"}
//	values := query.Values(user, query.Except("id"))
//	statement, err := query.Table("users", func(statement *query.Builder) {
//		statement.Where("id", user.ID)
//		statement.Update(values)
//	}).Build()
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func Values(v any, opts ...ValuesOption) map[string]any {
	root := reflect.ValueOf(v)

	if root.Kind() == reflect.Pointer && !root.IsNil() {
		root = root.Elem()
	}

	if root.Kind() != reflect.Struct {
		panic(fmt.Sprintf("query: Values needs a struct or a non-nil pointer to a struct, got %T", v))
	}

	var config valuesConfig

	for _, opt := range opts {
		opt(&config)
	}

	set := fieldsOf(root.Type())
	values := make(map[string]any, len(set.fields))

	for _, field := range set.fields {
		if config.keeps(field.column) {
			values[field.column] = field.read(root)
		}
	}

	return values
}

// Targets returns the scan destinations for one result row: one pointer per
// entry of columns, in order, for use with [database/sql.Rows.Scan].
//
// dest is a non-nil pointer to a struct, whose fields follow the rules of
// [Values], or to a single value (a number, a string, [time.Time], a
// [database/sql.Scanner], or a struct without mapped fields), which needs
// exactly one column. A column without a field fails with [ErrUnknownColumn];
// fields without a column are left untouched. Nil embedded struct pointers on
// the way to a field are allocated. Any other destination fails with
// [ErrInvalidTarget].
//
// Example:
//
//	var user struct {
//		ID   int
//		Name string
//	}
//	targets, err := query.Targets(&user, []string{"id", "name"})
//	if err != nil {
//		return err
//	}
//	if err := rows.Scan(targets...); err != nil {
//		return err
//	}
//	_ = user.Name
func Targets(dest any, columns []string) ([]any, error) {
	pointer := reflect.ValueOf(dest)

	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return nil, fmt.Errorf("%w: %T is not a non-nil pointer", ErrInvalidTarget, dest)
	}

	target := pointer.Elem()

	if target.Kind() != reflect.Struct || isValue(target.Type()) {
		return scalarTarget(dest, columns)
	}

	set := fieldsOf(target.Type())

	if len(set.fields) == 0 {
		return scalarTarget(dest, columns)
	}

	targets := make([]any, len(columns))

	for index, column := range columns {
		position, ok := set.byColumn[column]

		if !ok {
			return nil, fmt.Errorf("%w: %q in %s", ErrUnknownColumn, column, target.Type())
		}

		targets[index] = set.fields[position].address(target).Interface()
	}

	return targets, nil
}

// scalarTarget returns dest as the one destination of a single-column result.
func scalarTarget(dest any, columns []string) ([]any, error) {
	if len(columns) != 1 {
		return nil, fmt.Errorf("%w: %T holds one value but the result has %d columns", ErrInvalidTarget, dest, len(columns))
	}

	return []any{dest}, nil
}

// read returns the value of the field in root, a struct value of the type the
// field belongs to.
func (field field) read(root reflect.Value) any {
	current := root

	for depth, position := range field.index {
		if depth > 0 && current.Kind() == reflect.Pointer {
			if current.IsNil() {
				return reflect.Zero(reflect.PointerTo(field.typ)).Interface()
			}

			current = current.Elem()
		}

		current = current.Field(position)
	}

	return current.Interface()
}

// address returns a pointer to the field in root, a settable struct value,
// allocating the embedded struct pointers on the way.
func (field field) address(root reflect.Value) reflect.Value {
	current := root

	for depth, position := range field.index {
		if depth > 0 && current.Kind() == reflect.Pointer {
			if current.IsNil() {
				current.Set(reflect.New(current.Type().Elem()))
			}

			current = current.Elem()
		}

		current = current.Field(position)
	}

	return current.Addr()
}

// isValue identifies one-column types without inventing a Valuer pointer for
// a value field. Scanner detection includes the pointer used by Targets;
// Valuer detection uses the declared type because Values returns it as is.
func isValue(typ reflect.Type) bool {
	if typ == timeType {
		return true
	}

	pointer := reflect.PointerTo(typ)

	return typ.Implements(valuerType) || typ.Implements(scannerType) || pointer.Implements(scannerType)
}

// fieldsOf returns the cached columns of typ, which must be a struct type.
func fieldsOf(typ reflect.Type) *fieldSet {
	if cached, ok := fieldCache.Load(typ); ok {
		return cached.(*fieldSet)
	}

	actual, _ := fieldCache.LoadOrStore(typ, buildFieldSet(typ))

	return actual.(*fieldSet)
}

// embeddedStruct is an embedded struct waiting to be flattened.
type embeddedStruct struct {
	// typ is the struct type.
	typ reflect.Type

	// index is the field index path to the embedded field.
	index []int

	// chain lists the struct types on the way from the root, to stop
	// recursive embedding.
	chain []reflect.Type
}

// buildFieldSet walks typ breadth first, so a field of the struct itself wins
// over a field of an embedded struct, and the first of equals wins.
func buildFieldSet(typ reflect.Type) *fieldSet {
	set := &fieldSet{
		byColumn: make(map[string]int),
	}
	level := []embeddedStruct{{
		typ:   typ,
		chain: []reflect.Type{typ},
	}}

	for len(level) > 0 {
		var next []embeddedStruct

		for _, current := range level {
			for position := range current.typ.NumField() {
				structField := current.typ.Field(position)
				column, tagged := columnName(structField)

				if column == "-" {
					continue
				}

				index := append(slices.Clone(current.index), position)

				if embedded, ok := flattened(structField, tagged, current.chain); ok {
					next = append(next, embeddedStruct{
						typ:   embedded,
						index: index,
						chain: append(slices.Clone(current.chain), embedded),
					})

					continue
				}

				if !structField.IsExported() {
					continue
				}

				if _, taken := set.byColumn[column]; !taken {
					set.byColumn[column] = len(set.fields)
					set.fields = append(set.fields, field{
						column: column,
						index:  index,
						typ:    structField.Type,
					})
				}
			}
		}

		level = next
	}

	return set
}

// flattened returns the struct type of an untagged embedded struct field whose
// columns belong to its parent.
func flattened(structField reflect.StructField, tagged bool, chain []reflect.Type) (reflect.Type, bool) {
	if !structField.Anonymous || tagged {
		return nil, false
	}

	if isValue(structField.Type) {
		return nil, false
	}

	typ := structField.Type

	if typ.Kind() == reflect.Pointer {
		// A pointer to an unexported type cannot be allocated through reflection.
		if !structField.IsExported() {
			return nil, false
		}

		typ = typ.Elem()
	}

	// The original pointer type already exposed any Valuer/Scanner methods;
	// time.Time alone needs recognition after dereferencing the field type.
	if typ.Kind() != reflect.Struct || typ == timeType || slices.Contains(chain, typ) {
		return nil, false
	}

	return typ, true
}

// columnName returns the column a struct field maps to and whether a db tag
// named it. An untagged field maps to its lowercased name.
func columnName(structField reflect.StructField) (string, bool) {
	tag, _, _ := strings.Cut(structField.Tag.Get("db"), ",")

	if tag != "" {
		return tag, true
	}

	return strings.ToLower(structField.Name), false
}
