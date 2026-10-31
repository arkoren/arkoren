package suite

import (
	"bytes"
	"math"
	"math/big"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// schemaTests create one table per portable column type from
// [schema.Create], write a Go value with the query builder, and read the
// same value back through [database.Database]. They pin the claim that every
// driver round-trips each portable type, so a migration written once behaves
// the same on Postgres, MySQL, and SQLite.
var schemaTests = []conformanceTest{
	{"Schema/String", schemaString},
	{"Schema/Text", schemaText},
	{"Schema/Char", schemaChar},
	{"Schema/SmallInteger", schemaSmallInteger},
	{"Schema/Integer", schemaInteger},
	{"Schema/BigInteger", schemaBigInteger},
	{"Schema/Boolean", schemaBoolean},
	{"Schema/Float", schemaFloat},
	{"Schema/Decimal", schemaDecimal},
	{"Schema/Binary", schemaBinary},
	{"Schema/UUID", schemaUUID},
	{"Schema/JSON", schemaJSON},
	{"Schema/Date", schemaDate},
	{"Schema/Timestamp", schemaTimestamp},
	{"Schema/TimestampTZ", schemaTimestampTZ},
	{"Schema/Nullable", schemaNullable},
	{"Schema/NullableJSON", schemaNullableJSON},
}

// cell is the row type of a schema table: the value under test in column v.
type cell[T any] struct {
	// V is the value read back from column v.
	V T `db:"v"`
}

// jsonPayload is the document stored by the JSON round-trip tests.
type jsonPayload struct {
	// Name is a string member.
	Name string `json:"name"`

	// Tags is an array member.
	Tags []string `json:"tags"`

	// Nested is an object member holding a number.
	Nested struct {
		// Count is an integer member.
		Count int `json:"count"`
	} `json:"nested"`
}

// roundTrip creates a table whose column v is defined by column, inserts each
// of values through the query builder (positional args, never SQL text),
// reads each back with [database.Find], and reports any value for which
// equal is false. The table is dropped when the subtest ends.
//
// values are written as given, so T must be a type the driver accepts as an
// argument and scans back. Rows are keyed by their index in values so every
// value is read independently.
func roundTrip[T any](h *harness, column func(*schema.Table), values []T, equal func(got, want T) bool) {
	h.t.Helper()

	db := database.New(h.driver)
	name := h.table + "_s"

	create := schema.Create(name, func(table *schema.Table) {
		table.Integer("id").Primary()
		column(table)
	})

	if err := create.Err(); err != nil {
		h.t.Fatalf("create table: %v", err)
	}

	for _, statement := range create.Statements() {
		if _, err := db.Exec(h.ctx(), statement); err != nil {
			h.t.Fatalf("create table: %v", err)
		}
	}

	h.t.Cleanup(func() {
		for _, statement := range schema.DropIfExists(name).Statements() {
			if _, err := db.Exec(h.ctx(), statement); err != nil {
				h.t.Errorf("drop table: %v", err)
			}
		}
	})

	for id, want := range values {
		insert := query.Table(name, func(statement *query.Builder) {
			statement.Insert(map[string]any{"id": id, "v": want})
		})

		if affected, err := db.Exec(h.ctx(), insert); err != nil || affected != 1 {
			h.t.Fatalf("insert %v: affected = %d, error = %v", want, affected, err)
		}

		got, err := db.Find[cell[T]](h.ctx(), query.Table(name, func(statement *query.Builder) {
			statement.Select("v")
			statement.Where("id", id)
		}))

		if err != nil {
			h.t.Fatalf("find %v: %v", want, err)
		}

		if !equal(got.V, want) {
			h.t.Errorf("round trip = %#v, want %#v", got.V, want)
		}
	}
}

// same compares with ==.
func same[T comparable](got, want T) bool {
	return got == want
}

// schemaString checks String(n) with ASCII, multi-byte, and empty text.
func schemaString(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.String("v", 64)
	},
		[]string{"hello", "héllo wörld ✓", ""}, same)
}

// schemaText checks Text with a value longer than any String column.
func schemaText(h *harness) {
	long := string(bytes.Repeat([]byte("abcdefghij"), 1000))

	roundTrip(h, func(table *schema.Table) {
		table.Text("v")
	},
		[]string{"short", long}, same)
}

// schemaChar checks Char(n) with values exactly n characters long. Fixed
// width columns pad shorter values on some databases (Postgres and MySQL
// differ on trailing spaces), so a shorter value is not portable.
func schemaChar(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.Char("v", 3)
	},
		[]string{"abc", "xyz"}, same)
}

// schemaSmallInteger checks SmallInteger at the 16-bit limits.
func schemaSmallInteger(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.SmallInteger("v")
	},
		[]int64{0, -1, math.MaxInt16, math.MinInt16}, same)
}

// schemaInteger checks Integer at the 32-bit limits.
func schemaInteger(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.Integer("v")
	},
		[]int64{0, -1, math.MaxInt32, math.MinInt32}, same)
}

// schemaBigInteger checks BigInteger at the 64-bit limits.
func schemaBigInteger(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.BigInteger("v")
	},
		[]int64{0, -1, math.MaxInt64, math.MinInt64}, same)
}

// schemaBoolean checks Boolean with true and false.
func schemaBoolean(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.Boolean("v")
	},
		[]bool{true, false}, same)
}

// schemaFloat checks Float with values that are exact in binary, so the
// comparison does not depend on how a database rounds decimal text.
func schemaFloat(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.Float("v")
	},
		[]float64{1.5, -0.25, 1024.0625, 0}, same)
}

// schemaDecimal checks Decimal(10, 2) scanned into a string. Databases
// format the scale differently: Postgres and MySQL return "12.50" while
// SQLite, whose NUMERIC affinity stores a real, returns "12.5". The suite
// therefore compares the numeric value exactly with [big.Rat], never the
// text.
func schemaDecimal(h *harness) {
	equal := func(got, want string) bool {
		g, gotOK := new(big.Rat).SetString(got)
		w, wantOK := new(big.Rat).SetString(want)

		return gotOK && wantOK && g.Cmp(w) == 0
	}

	roundTrip(h, func(table *schema.Table) {
		table.Decimal("v", 10, 2)
	},
		[]string{"12.50", "-0.25", "99999999.99", "0"}, equal)
}

// schemaBinary checks Binary with embedded zero bytes and every byte value.
func schemaBinary(h *harness) {
	all := make([]byte, 256)

	for i := range all {
		all[i] = byte(i)
	}

	roundTrip(h, func(table *schema.Table) {
		table.Binary("v")
	},
		[][]byte{{0x00, 0x01, 0x00, 0xff}, all}, bytes.Equal)
}

// schemaUUID checks UUID scanned into a string. Postgres uses a native uuid,
// MySQL char(36), and SQLite TEXT; all return the canonical lower-case form
// they were given.
func schemaUUID(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.UUID("v")
	},
		[]string{"123e4567-e89b-42d3-a456-426614174000", "00000000-0000-0000-0000-000000000000"}, same)
}

// schemaJSON checks JSON through [database.JSON] with a nested document.
// Documents are compared after decoding, because Postgres jsonb and MySQL
// json normalise whitespace and key order.
func schemaJSON(h *harness) {
	doc := jsonPayload{Name: "arkoren", Tags: []string{"a", "b"}}
	doc.Nested.Count = 7

	equal := func(got, want database.JSON[jsonPayload]) bool {
		return got.V.Name == want.V.Name && got.V.Nested == want.V.Nested && slicesEqual(got.V.Tags, want.V.Tags)
	}

	roundTrip(h, func(table *schema.Table) {
		table.JSON("v")
	},
		[]database.JSON[jsonPayload]{{V: doc}}, equal)
}

// slicesEqual reports whether a and b hold the same strings in order.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// schemaDate checks Date with midnight UTC values, comparing only the
// calendar day: the stored value has no time of day or zone.
func schemaDate(h *harness) {
	equal := func(got, want time.Time) bool {
		return got.Year() == want.Year() && got.Month() == want.Month() && got.Day() == want.Day()
	}

	roundTrip(h, func(table *schema.Table) {
		table.Date("v")
	},
		[]time.Time{time.Date(2024, time.March, 15, 0, 0, 0, 0, time.UTC), time.Date(1999, time.December, 31, 0, 0, 0, 0, time.UTC)},
		equal)
}

// schemaTimestamp checks Timestamp with UTC values at microsecond precision,
// the finest resolution every database keeps.
func schemaTimestamp(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.Timestamp("v")
	},
		[]time.Time{time.Date(2024, time.March, 15, 13, 45, 6, 123456000, time.UTC)},
		time.Time.Equal)
}

// schemaTimestampTZ checks TimestampTZ with UTC values at microsecond
// precision. MySQL has no zone-aware type (it renders datetime(6)), so only
// UTC values are portable.
func schemaTimestampTZ(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.TimestampTZ("v")
	},
		[]time.Time{time.Date(2024, time.March, 15, 13, 45, 6, 123456000, time.UTC)},
		time.Time.Equal)
}

// schemaNullable checks that a Nullable column stores SQL NULL and returns
// it as a nil pointer next to a non-NULL value.
func schemaNullable(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.Text("v").Nullable()
	},
		[]*string{nil, new("x")},
		func(got, want *string) bool {
			return (got == nil) == (want == nil) && (got == nil || *got == *want)
		})
}

// schemaNullableJSON checks that SQL NULL in a nullable JSON column scans
// into a nil [database.JSON] pointer on every driver.
func schemaNullableJSON(h *harness) {
	roundTrip(h, func(table *schema.Table) {
		table.JSON("v").Nullable()
	},
		[]*database.JSON[jsonPayload]{nil},
		func(got, want *database.JSON[jsonPayload]) bool {
			return got == nil && want == nil
		})
}
