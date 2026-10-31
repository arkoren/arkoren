package database

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"arkoren.dev/core/pagination"
	"arkoren.dev/foundation/query"
)

// fingerprintLength is the number of hash bytes kept in a cursor
// fingerprint. Eight bytes detect accidental reuse across orderings; the
// cursor is not a security token.
const fingerprintLength = 8

// cursorPayload is the content of an opaque cursor.
type cursorPayload struct {
	// Values holds the ordering key values of the boundary row, one per
	// order, in JSON form.
	Values []jsontext.Value `json:"v"`

	// Fingerprint identifies the columns and directions the cursor was
	// made for.
	Fingerprint string `json:"k"`

	// Backward reports that the cursor pages toward the start.
	Backward bool `json:"b,omitempty"`
}

// keyset reads and writes ordering keys for rows of one result type.
type keyset struct {
	// orders are the published query orderings the keys follow.
	orders []query.Order

	// columns are the result type columns the orders read, one per order.
	columns []string

	// types are the dereferenced field types, one per order.
	types []reflect.Type

	// fingerprint identifies orders.
	fingerprint string

	// codec signs and verifies cursors; nil leaves them unsigned.
	codec *pagination.CursorCodec
}

// newKeyset resolves the orders of query against the columns of T, which follow
// the field rules of query.Values. Mutation rejection precedes order and type
// checks so the first window cannot execute a RETURNING write via Select.
func newKeyset[T any](statement query.Query) (*keyset, error) {
	if !statement.IsSelect() {
		return nil, fmt.Errorf("keyset pagination: %w: %w", query.ErrInvalidQuery, errNotSelect)
	}

	orders := statement.Inspect().Orders

	if len(orders) == 0 {
		return nil, ErrNoOrder
	}

	typ := reflect.TypeFor[T]()

	root := derefType(typ)

	if root.Kind() != reflect.Struct {
		return nil, fmt.Errorf("result type %s is not a struct: %w", typ, ErrCursorColumn)
	}

	keys := &keyset{
		orders:      orders,
		columns:     make([]string, 0, len(orders)),
		types:       make([]reflect.Type, 0, len(orders)),
		fingerprint: fingerprint(orders),
	}

	for _, order := range orders {
		keys.columns = append(keys.columns, order.Column[strings.LastIndex(order.Column, ".")+1:])
	}

	// The zero value is only a sample of the field types.
	sample := query.Values(reflect.New(root).Interface(), query.Only(keys.columns...))

	for _, column := range keys.columns {
		value, ok := sample[column]

		if !ok || value == nil {
			return nil, fmt.Errorf("column %q in %s: %w", column, typ, ErrCursorColumn)
		}

		keys.types = append(keys.types, derefType(reflect.TypeOf(value)))
	}

	return keys, nil
}

// fingerprint returns a short hash of the order columns and directions.
func fingerprint(orders []query.Order) string {
	var builder strings.Builder

	for _, order := range orders {
		direction := "asc"

		if order.Desc {
			direction = "desc"
		}

		fmt.Fprintf(&builder, "%s:%s;", order.Column, direction)
	}

	sum := sha256.Sum256([]byte(builder.String()))

	return hex.EncodeToString(sum[:fingerprintLength])
}

// derefType strips pointer layers from typ.
func derefType(typ reflect.Type) reflect.Type {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	return typ
}

// values reads the ordering key values of item, a value of the result type.
// It retains driver.Valuer method sets for SQL binding and rejects nullable
// values that represent NULL. Value errors retain their original causes.
func (keys *keyset) values(item any) ([]any, error) {
	row := reflect.ValueOf(item)

	if row.Kind() == reflect.Pointer && row.IsNil() {
		return nil, fmt.Errorf("row is nil: %w", ErrCursorValue)
	}

	columns := query.Values(item, query.Only(keys.columns...))
	values := make([]any, 0, len(keys.columns))

	for _, column := range keys.columns {
		value, err := cursorValue(columns[column])

		if err != nil {
			return nil, fmt.Errorf("column %q: %w", column, err)
		}

		values = append(values, value)
	}

	return values, nil
}

// cursorValue dereferences a key and rejects representations of SQL NULL.
// Driver values are inspected but retained with their method set for SQL
// binding. encode separately dereferences them to preserve legacy cursor JSON.
func cursorValue(value any) (any, error) {
	field := reflect.ValueOf(value)

	for field.IsValid() && field.Kind() == reflect.Pointer {
		field = field.Elem()
	}

	if nullCursorField(field) {
		return nil, ErrCursorValue
	}

	valuer, nullable := value.(driver.Valuer)
	value = field.Interface()

	if !nullable {
		valuer, nullable = value.(driver.Valuer)
	}

	if nullable {
		raw, err := valuer.Value()

		if err != nil {
			return nil, errors.Join(ErrCursorValue, err)
		}

		if nullCursorField(reflect.ValueOf(raw)) {
			return nil, ErrCursorValue
		}

		return valuer, nil
	}

	return value, nil
}

// nullCursorField recognizes absent values, nil pointers and nil containers.
// A typed nil byte slice is SQL NULL too, despite its non-nil any wrapper.
func nullCursorField(field reflect.Value) bool {
	if !field.IsValid() {
		return true
	}

	switch field.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return field.IsNil()
	default:
		return false
	}
}

// windowValues checks the entire fetched window, including lookahead and rows
// that will not become a cursor boundary. SQL keyset comparisons exclude NULL
// partitions, so accepting such rows could otherwise hide an incomplete walk.
// It returns the first and last key of the retained limit rows in fetch order,
// so cursor encoding does not need to read or validate those keys a second time.
func (keys *keyset) windowValues[T any](ctx context.Context, items []T, limit int) ([]any, []any, error) {
	var first, last []any
	lastIndex := min(len(items), limit) - 1

	for index, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		values, err := keys.values(item)

		if err != nil {
			return nil, nil, err
		}

		if index == 0 {
			first = values
		}

		if index == lastIndex {
			last = values
		}
	}

	return first, last, nil
}

// yieldItems emits a buffered batch while checking cancellation and every key.
// It returns the last ordering values for the next seek, avoiding a second
// read of that row. False means the consumer stopped or an error was yielded.
func (keys *keyset) yieldItems[T any](
	ctx context.Context,
	items []T,
	yield func(T, error) bool,
) ([]any, bool) {
	var zero T
	var values []any

	for _, item := range items {
		if err := ctx.Err(); err != nil {
			yield(zero, err)

			return nil, false
		}

		var err error
		values, err = keys.values(item)

		if err != nil {
			yield(zero, err)

			return nil, false
		}

		if !yield(item, nil) {
			return nil, false
		}
	}

	return values, true
}

// encode builds the opaque cursor from boundary values already checked by
// windowValues. Keeping the original JSON values preserves issued cursor formats.
// Pointer-valued SQL keys are dereferenced for JSON just as in older cursors;
// their pointer-only driver.Valuer method remains available for SQL binding.
func (keys *keyset) encode(values []any, backward bool) (string, error) {
	payload := cursorPayload{
		Values:      make([]jsontext.Value, 0, len(values)),
		Fingerprint: keys.fingerprint,
		Backward:    backward,
	}

	for _, value := range values {
		field := reflect.ValueOf(value)

		for field.Kind() == reflect.Pointer {
			field = field.Elem()
		}

		raw, err := json.Marshal(field.Interface())

		if err != nil {
			return "", fmt.Errorf("encode cursor value: %w", err)
		}

		payload.Values = append(payload.Values, raw)
	}

	if keys.codec != nil {
		return keys.codec.Encode(payload)
	}

	return pagination.CursorEncode(payload)
}

// seek validates an optional cursor before deriving its keyset query. An empty
// cursor selects the original forward window without modifying the definition.
func (keys *keyset) seek(statement query.Query, cursor string) (query.Query, bool, error) {
	if cursor == "" {
		return statement, false, nil
	}

	values, backward, err := keys.decode(cursor)

	if err != nil {
		return query.Query{}, false, err
	}

	return statement.Seek(values, backward), backward, nil
}

// decode reads cursor into typed key values and its direction. Any
// mismatch with the query's orders wraps [pagination.ErrCursorDecode].
func (keys *keyset) decode(cursor string) ([]any, bool, error) {
	decode := pagination.CursorDecode[cursorPayload]

	if keys.codec != nil {
		decode = keys.codec.Decode[cursorPayload]
	}

	payload, err := decode(cursor)

	if err != nil {
		return nil, false, err
	}

	if payload.Fingerprint != keys.fingerprint || len(payload.Values) != len(keys.types) {
		return nil, false, fmt.Errorf("cursor does not match the query order: %w", pagination.ErrCursorDecode)
	}

	values := make([]any, 0, len(keys.types))

	for index, typ := range keys.types {
		if string(payload.Values[index]) == "null" {
			return nil, false, fmt.Errorf("null key value: %w", pagination.ErrCursorDecode)
		}

		target := reflect.New(typ)

		if err := json.Unmarshal(payload.Values[index], target.Interface()); err != nil {
			return nil, false, errors.Join(pagination.ErrCursorDecode, err)
		}

		value, err := cursorValue(target.Interface())

		if err != nil {
			return nil, false, errors.Join(pagination.ErrCursorDecode, err)
		}

		values = append(values, value)
	}

	return values, payload.Backward, nil
}
