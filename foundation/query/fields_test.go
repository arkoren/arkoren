package query_test

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"arkoren.dev/foundation/query"
)

// TestInsertValuesKeepsEmbeddedDatabaseScalars verifies embedded time and
// driver.Valuer structs bind as whole values rather than exposing their fields.
func TestInsertValuesKeepsEmbeddedDatabaseScalars(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, time.October, 30, 12, 0, 0, 0, time.UTC)
	nickname := sql.NullString{String: "ada", Valid: true}
	row := struct {
		// Time is a scalar despite being an untagged embedded struct.
		time.Time
		// NullString implements database conversion and must stay intact.
		sql.NullString
		// Name is an ordinary column beside the embedded scalar values.
		Name string
	}{Time: createdAt, NullString: nickname, Name: "Ada"}

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Insert(query.Values(row))
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := `INSERT INTO "users" ("name", "nullstring", "time") VALUES ($1, $2, $3)`; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{"Ada", nickname, createdAt}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// ExampleTargets handles column-mapping failure before consuming destinations.
// Writing through those destinations models the values supplied by Rows.Scan.
func ExampleTargets() {
	var user struct {
		// ID receives the result's id column.
		ID int

		// Name receives the result's name column.
		Name string
	}

	targets, err := query.Targets(&user, []string{"id", "name"})

	if err != nil {
		fmt.Println(err)

		return
	}

	*targets[0].(*int) = 7
	*targets[1].(*string) = "Ada"

	fmt.Println(user.ID, user.Name)
	// Output:
	// 7 Ada
}

// TestOnlyCapturesColumns verifies an option snapshots an expanded input slice
// before its first Values call and keeps that selection when reused.
func TestOnlyCapturesColumns(t *testing.T) {
	t.Parallel()

	value := struct {
		// ID is the column selected when the option is created.
		ID int `db:"id"`

		// Name detects a changed selection after the caller mutates its slice.
		Name string `db:"name"`
	}{ID: 7, Name: "Ada"}
	columns := []string{"id"}
	option := query.Only(columns...)

	columns[0] = "name"

	want := map[string]any{"id": 7}

	if got := query.Values(value, option); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values after input mutation = %#v, want %#v", got, want)
	}

	columns[0] = "missing"

	if got := query.Values(value, option); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values with reused option = %#v, want %#v", got, want)
	}
}

// TestExceptCapturesColumns verifies an option snapshots an expanded input slice
// before its first Values call and keeps that exclusion when reused.
func TestExceptCapturesColumns(t *testing.T) {
	t.Parallel()

	value := struct {
		// ID is the column excluded when the option is created.
		ID int `db:"id"`

		// Name detects a changed exclusion after the caller mutates its slice.
		Name string `db:"name"`
	}{ID: 7, Name: "Ada"}
	columns := []string{"id"}
	option := query.Except(columns...)

	columns[0] = "name"

	want := map[string]any{"name": "Ada"}

	if got := query.Values(value, option); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values after input mutation = %#v, want %#v", got, want)
	}

	columns[0] = "missing"

	if got := query.Values(value, option); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values with reused option = %#v, want %#v", got, want)
	}
}

// TestOnlyEmptyKeepsEveryColumn preserves the unrestricted behavior of an empty
// option, including options constructed from expanded nil and empty slices.
func TestOnlyEmptyKeepsEveryColumn(t *testing.T) {
	t.Parallel()

	value := struct {
		// ID is retained because empty Only options add no restriction.
		ID int `db:"id"`
	}{ID: 7}
	var omitted []string
	empty := []string{}

	want := map[string]any{"id": 7}

	if got := query.Values(value, query.Only()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values with Only() = %#v, want %#v", got, want)
	}

	if got := query.Values(value, query.Only(omitted...)); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values with expanded nil columns = %#v, want %#v", got, want)
	}

	if got := query.Values(value, query.Only(empty...)); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values with expanded empty columns = %#v, want %#v", got, want)
	}
}

// TestOnlyEmptyPreservesEarlierSelection verifies an empty option adds no names
// without clearing the restriction established by an earlier Only option.
func TestOnlyEmptyPreservesEarlierSelection(t *testing.T) {
	t.Parallel()

	value := struct {
		// ID is selected by the nonempty option.
		ID int `db:"id"`

		// Name stays excluded after the empty option is applied.
		Name string `db:"name"`
	}{ID: 7, Name: "Ada"}

	want := map[string]any{"id": 7}

	if got := query.Values(value, query.Only("id"), query.Only()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values with Only(id), Only() = %#v, want %#v", got, want)
	}
}

// PointerAmount models a database value whose conversion requires a pointer.
// It deliberately has no Scanner method, so value embedding follows ordinary
// exported-field mapping rather than scanner-based scalar classification.
//
// Example:
//
//	amount := &PointerAmount{Cents: 7}
//	value, err := amount.Value()
//	if err != nil {
//		return err
//	}
//	_ = value
type PointerAmount struct {
	// Cents is the integer amount used by the test conversion.
	//
	// Example:
	//
	//	amount := PointerAmount{Cents: 7}
	//	_ = amount.Cents
	Cents int64
}

// Value exposes the amount only through *PointerAmount's method set. A value
// PointerAmount does not implement driver.Valuer and must not be bound as one.
//
// Example:
//
//	amount := &PointerAmount{Cents: 7}
//	value, err := amount.Value()
//	if err != nil {
//		return err
//	}
//	_ = value
func (amount *PointerAmount) Value() (driver.Value, error) {
	return amount.Cents, nil
}

// TestPointerOnlyValuerValueEmbeddingMapsFields verifies Values and Targets
// share ordinary field mapping when only the embedded type's pointer is Valuer.
func TestPointerOnlyValuerValueEmbeddingMapsFields(t *testing.T) {
	t.Parallel()

	row := struct {
		// PointerAmount is embedded by value, not as a driver.Valuer pointer.
		PointerAmount
		// ID keeps the result visibly different from a scalar target.
		ID int
	}{PointerAmount: PointerAmount{Cents: 7}, ID: 1}

	if got := query.Values(row); !reflect.DeepEqual(got, map[string]any{"cents": int64(7), "id": 1}) {
		t.Fatalf("Values = %#v, want flattened cents and id", got)
	}

	targets, err := query.Targets(&row, []string{"cents", "id"})

	if err != nil {
		t.Fatal(err)
	}

	*targets[0].(*int64) = 9
	*targets[1].(*int) = 2

	if row.Cents != 9 || row.ID != 2 {
		t.Fatalf("row = %#v, want cents 9 and id 2", row)
	}
}

// TestPointerOnlyValuerPointerEmbeddingStaysScalar verifies an actual Valuer
// pointer remains one bound column and retains its original pointer identity.
func TestPointerOnlyValuerPointerEmbeddingStaysScalar(t *testing.T) {
	t.Parallel()

	amount := &PointerAmount{Cents: 7}
	row := struct {
		// PointerAmount implements Valuer in this pointer form.
		*PointerAmount
	}{PointerAmount: amount}

	values := query.Values(row)

	if len(values) != 1 || values["pointeramount"] != amount {
		t.Fatalf("Values = %#v, want the original amount pointer", values)
	}

	value, err := driver.DefaultParameterConverter.ConvertValue(values["pointeramount"])

	if err != nil {
		t.Fatal(err)
	}

	if value != int64(7) {
		t.Fatalf("bound value = %#v, want 7", value)
	}
}

// TestValuesKeepsEmbeddedTimePointer verifies time's pointer remains one scalar
// column even though neither its value nor pointer implements a SQL interface.
func TestValuesKeepsEmbeddedTimePointer(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, time.October, 30, 12, 0, 0, 0, time.UTC)
	row := struct {
		// Time supplies the original timestamp pointer as one column.
		*time.Time
	}{Time: &createdAt}

	values := query.Values(row)

	if len(values) != 1 || values["time"] != &createdAt {
		t.Fatalf("Values = %#v, want the original time pointer", values)
	}
}

// valuer is a column value type that implements driver.Valuer and sql.Scanner
// and has exported fields.
type valuer struct {
	// Raw is the stored text.
	Raw string
}

// Value returns the stored text as a database value without conversion.
// It lets the field tests distinguish scalar values from embedded structs.
//
// Example:
//
//	input := valuer{
//		Raw: "settings",
//	}
//	value, err := input.Value()
//	if err != nil {
//		return err
//	}
//	_ = value
func (valuer valuer) Value() (driver.Value, error) {
	return valuer.Raw, nil
}

// Scan stores string input and clears the text for other input types.
// This test scanner always succeeds; it exercises scalar-field detection.
//
// Example:
//
//	var value valuer
//	if err := value.Scan("settings"); err != nil {
//		return err
//	}
//	_ = value.Raw
func (valuer *valuer) Scan(src any) error {
	text, _ := src.(string)
	valuer.Raw = text

	return nil
}

// timestamps is an embedded struct with tagged fields.
type timestamps struct {
	// CreatedAt is when the row was created.
	CreatedAt time.Time `db:"created_at"`

	// Name collides with the outer Name and loses.
	Name string `db:"name"`
}

// account is a struct that covers every field rule.
type account struct {
	// timestamps supplies fields that can be overridden by outer declarations.
	timestamps

	// ID is a tagged column.
	ID int `db:"id"`

	// Name wins over the embedded Name.
	Name string `db:"name,omitempty"`

	// Email is untagged and maps to its lowercased name.
	Email string

	// Secret is skipped.
	Secret string `db:"-"`

	// Settings is a leaf, not flattened.
	Settings valuer `db:"settings"`

	// Nickname is nullable.
	Nickname *string `db:"nickname"`

	// private is unexported.
	private int
}

// TestValuesFollowsFieldRules verifies tags, defaults, skips, embedding, and
// leaves in one struct.
func TestValuesFollowsFieldRules(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	nick := "ada"
	value := account{
		timestamps: timestamps{
			CreatedAt: created,
			Name:      "inner",
		},
		ID:     7,
		Name:   "outer",
		Email:  "a@example.com",
		Secret: "x",
		Settings: valuer{
			Raw: "{}",
		},
		Nickname: &nick,
		private:  1,
	}

	want := map[string]any{
		"created_at": created,
		"id":         7,
		"name":       "outer",
		"email":      "a@example.com",
		"settings": valuer{
			Raw: "{}",
		},
		"nickname": &nick,
	}

	if got := query.Values(value); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %#v, want %#v", got, want)
	}

	if got := query.Values(&value); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values(pointer) = %#v, want %#v", got, want)
	}
}

// TestValuesOnlyAndExcept verifies the column filters.
func TestValuesOnlyAndExcept(t *testing.T) {
	t.Parallel()

	value := account{
		ID:    1,
		Name:  "n",
		Email: "e",
	}

	got := query.Values(value, query.Only("id", "name", "missing"), query.Except("name"))

	if want := map[string]any{"id": 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %#v, want %#v", got, want)
	}
}

// Stamps is an exported embedded struct, so it can be embedded by pointer.
type Stamps struct {
	// CreatedAt is when the row was created.
	CreatedAt time.Time `db:"created_at"`
}

// TestValuesNilEmbeddedPointerYieldsTypedNil verifies the columns of a nil
// embedded pointer are typed nil pointers.
func TestValuesNilEmbeddedPointerYieldsTypedNil(t *testing.T) {
	t.Parallel()

	type row struct {
		// Stamps exercises a nil embedded pointer with exported fields.
		*Stamps

		// ID remains available even when the embedded pointer is nil.
		ID int `db:"id"`
	}

	got := query.Values(row{
		ID: 1,
	})

	if want := (*time.Time)(nil); got["created_at"] != any(want) {
		t.Fatalf("created_at = %#v, want typed nil %T", got["created_at"], want)
	}
}

// TestValuesPanicsOnNonStruct verifies the programming-error panics.
func TestValuesPanicsOnNonStruct(t *testing.T) {
	t.Parallel()

	var nilAccount *account

	for _, input := range []any{1, "text", nil, nilAccount, []account{}, map[string]any{}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Values(%#v) did not panic", input)
				}
			}()

			query.Values(input)
		}()
	}
}

// TestTargetsMapsColumnsToFields verifies destinations follow the column order
// and write through to the struct.
func TestTargetsMapsColumnsToFields(t *testing.T) {
	t.Parallel()

	var value account

	targets, err := query.Targets(&value, []string{"email", "id", "created_at", "settings"})

	if err != nil {
		t.Fatal(err)
	}

	*targets[0].(*string) = "e"
	*targets[1].(*int) = 5
	*targets[2].(*time.Time) = time.Unix(1, 0).UTC()

	if value.Email != "e" || value.ID != 5 || !value.CreatedAt.Equal(time.Unix(1, 0)) {
		t.Fatalf("value = %#v", value)
	}

	if _, ok := targets[3].(*valuer); !ok {
		t.Fatalf("settings target = %T, want *valuer", targets[3])
	}
}

// TestTargetsAllocatesEmbeddedPointer verifies a nil embedded pointer is
// allocated for its column.
func TestTargetsAllocatesEmbeddedPointer(t *testing.T) {
	t.Parallel()

	type row struct {
		// Stamps must be allocated before its timestamp field can be addressed.
		*Stamps

		// ID is an unrelated column that does not require pointer allocation.
		ID int `db:"id"`
	}

	var value row

	targets, err := query.Targets(&value, []string{"created_at"})

	if err != nil {
		t.Fatal(err)
	}

	*targets[0].(*time.Time) = time.Unix(2, 0)

	if value.Stamps == nil || value.CreatedAt.Unix() != 2 {
		t.Fatalf("value = %#v", value)
	}
}

// TestTargetsUnknownColumn verifies a column without a field is an error.
func TestTargetsUnknownColumn(t *testing.T) {
	t.Parallel()

	_, err := query.Targets(&account{}, []string{"id", "nope"})

	if !errors.Is(err, query.ErrUnknownColumn) {
		t.Fatalf("Targets() error = %v, want ErrUnknownColumn", err)
	}

	_, err = query.Targets(&account{}, []string{"secret"})

	if !errors.Is(err, query.ErrUnknownColumn) {
		t.Fatalf("Targets(skipped field) error = %v, want ErrUnknownColumn", err)
	}
}

// TestTargetsScalar verifies single values need exactly one column.
func TestTargetsScalar(t *testing.T) {
	t.Parallel()

	var count int64
	var when time.Time
	var text sql.NullString

	for _, dest := range []any{&count, &when, &text} {
		targets, err := query.Targets(dest, []string{"value"})

		if err != nil || len(targets) != 1 || targets[0] != dest {
			t.Fatalf("Targets(%T) = %v, %v", dest, targets, err)
		}

		if _, err := query.Targets(dest, []string{"a", "b"}); !errors.Is(err, query.ErrInvalidTarget) {
			t.Fatalf("Targets(%T, two columns) error = %v, want ErrInvalidTarget", dest, err)
		}
	}
}

// TestTargetsInvalidDestination verifies destinations that are not pointers.
func TestTargetsInvalidDestination(t *testing.T) {
	t.Parallel()

	var nilAccount *account

	for _, dest := range []any{nil, account{}, 3, nilAccount} {
		if _, err := query.Targets(dest, []string{"id"}); !errors.Is(err, query.ErrInvalidTarget) {
			t.Errorf("Targets(%#v) error = %v, want ErrInvalidTarget", dest, err)
		}
	}
}
