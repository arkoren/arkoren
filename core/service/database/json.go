package database

import (
	"database/sql/driver"
	"encoding/json/v2"
	"fmt"
)

// JSON stores a Go value of type T in a JSON column. Use it as a struct field
// type so the same code reads and writes JSON on every database.
//
// It exists because Postgres, MySQL, and SQLite return JSON columns as
// different Go types (SQLite returns a string from the TEXT column that holds
// the document, the others a []byte). An encoding/json RawMessage field
// therefore fails to scan from a string on SQLite, and a string field fails
// elsewhere. JSON accepts both forms in [JSON.Scan] and always writes the
// encoded document as a string in [JSON.Value], which every driver accepts
// for json, jsonb, and TEXT columns.
//
// The zero JSON holds the zero V and uses encoding/json/v2 defaults: a nil
// pointer becomes JSON null, a nil map {}, a nil ordinary slice [], and nil
// bytes an empty base64 JSON string. Value returns a string, never SQL NULL.
// These representations follow T and its JSON methods; custom marshalers can
// choose a different valid document or return an encoding error.
//
// Example:
//
//	type Account struct {
//		ID       int                            `db:"id"`
//		Settings database.JSON[map[string]bool] `db:"settings"`
//	}
//
//	account := Account{ID: 1, Settings: database.JSON[map[string]bool]{V: map[string]bool{"beta": true}}}
//	_, err := db.Exec(ctx, query.Raw("INSERT INTO accounts (id, settings) VALUES (?, ?)", account.ID, account.Settings))
//	if err != nil {
//		return err
//	}
type JSON[T any] struct {
	// V is the decoded value.
	V T
}

// Scan implements [database/sql.Scanner]. It decodes src, which must be a string or a
// []byte holding a JSON document, into V. A nil src (SQL NULL) resets V to
// its zero value and succeeds. Any other source type fails with an error, as
// does a document that does not decode into T; V is left as it was on error.
//
// Example:
//
//	var settings database.JSON[map[string]bool]
//	if err := settings.Scan(`{"beta":true}`); err != nil {
//		return err
//	}
//	_ = settings.V["beta"]
func (jsonValue *JSON[T]) Scan(src any) error {
	var data []byte

	switch value := src.(type) {
	case nil:
		var zero T
		jsonValue.V = zero

		return nil
	case string:
		data = []byte(value)
	case []byte:
		data = value
	default:
		return fmt.Errorf("database: scan JSON: unsupported source type %T", src)
	}

	var decoded T

	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("database: scan JSON: %w", err)
	}

	jsonValue.V = decoded

	return nil
}

// Value implements [driver.Valuer]. It returns V encoded as a JSON string, or
// an encoding error wrapped with database context. Encoding uses JSON v2's nil
// container defaults, not legacy null-map/null-slice behavior. The returned
// string is a document, including "null" for a nil pointer, never SQL NULL.
//
// Example:
//
//	value, err := (database.JSON[[]int]{}).Value()
//	if err != nil {
//		return err
//	}
//	_ = value // The string "[]".
func (jsonValue JSON[T]) Value() (driver.Value, error) {
	data, err := json.Marshal(jsonValue.V)

	if err != nil {
		return nil, fmt.Errorf("database: encode JSON: %w", err)
	}

	return string(data), nil
}
