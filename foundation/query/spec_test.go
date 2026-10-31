package query_test

import (
	"fmt"
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// ExampleSpec inspects paging and ordering without compiling SQL, then shows
// that changing the snapshot's pointer does not change the published query.
func ExampleSpec() {
	users := query.Table("users", func(statement *query.Builder) {
		statement.OrderByDesc("users.id")
		statement.Limit(20)
	})

	spec := users.Inspect()

	fmt.Println(spec.Kind.String(), spec.Table, spec.Orders[0].Column, spec.Orders[0].Desc)

	*spec.Limit = 0

	fmt.Println(*users.Inspect().Limit)
	// Output:
	// SELECT users users.id true
	// 20
}

// TestInspectSelect verifies Inspect reports the structure of a select.
func TestInspectSelect(t *testing.T) {
	t.Parallel()

	spec := query.Table("public.users", func(statement *query.Builder) {
		statement.Distinct()
		statement.GroupBy("role")
		statement.HavingRaw("COUNT(*) > ?", 1)
		statement.OrderBy("name")
		statement.OrderByDesc("users.id")
		statement.Limit(10)
		statement.Offset(20)
	}).Inspect()

	want := query.Spec{
		Kind:  query.KindSelect,
		Table: "public.users",
		Orders: []query.Order{{
			Column: "name",
		}, {
			Column: "users.id",
			Desc:   true,
		}},
		Limit:    new(10),
		Offset:   new(20),
		Distinct: true,
		Grouped:  true,
		Having:   true,
	}

	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("got %+v, want %+v", spec, want)
	}
}

// TestInspectEmpty verifies unset fields stay zero and orders stay nil.
func TestInspectEmpty(t *testing.T) {
	t.Parallel()

	spec := query.Table("users").Inspect()

	if spec.Orders != nil || spec.Limit != nil || spec.Offset != nil {
		t.Fatalf("unexpected values: %+v", spec)
	}

	if spec.Distinct || spec.Grouped || spec.Having || spec.Raw {
		t.Fatalf("unexpected flags: %+v", spec)
	}
}

// TestInspectKinds verifies Inspect reports each statement kind.
func TestInspectKinds(t *testing.T) {
	t.Parallel()

	if kind := query.Table("t", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
	}).Inspect().Kind; kind != query.KindInsert {
		t.Fatalf("got %s, want INSERT", kind)
	}

	if kind := query.Table("t", func(statement *query.Builder) {
		statement.Update(map[string]int{"a": 1})
	}).Inspect().Kind; kind != query.KindUpdate {
		t.Fatalf("got %s, want UPDATE", kind)
	}

	if kind := query.Table("t", func(statement *query.Builder) {
		statement.Delete()
	}).Inspect().Kind; kind != query.KindDelete {
		t.Fatalf("got %s, want DELETE", kind)
	}

	if kind := query.Table("t").Inspect().Kind; kind != query.KindSelect {
		t.Fatalf("got %s, want SELECT", kind)
	}
}

// TestInspectRawSource verifies Inspect reports a FromRaw source with no table.
func TestInspectRawSource(t *testing.T) {
	t.Parallel()

	spec := query.FromRaw(query.Raw("SELECT 1")).Inspect()

	if !spec.Raw || spec.Table != "" {
		t.Fatalf("got %+v", spec)
	}
}

// TestInspectReturnsCopies verifies mutating a Spec leaves the builder intact.
func TestInspectReturnsCopies(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.OrderBy("name")
		statement.Limit(5)
		statement.Offset(1)
	})

	spec := builder.Inspect()

	spec.Orders[0].Column = "other"
	spec.Orders[0].Desc = true
	*spec.Limit = 99
	*spec.Offset = 98

	again := builder.Inspect()

	if again.Orders[0].Column != "name" || again.Orders[0].Desc {
		t.Fatalf("orders changed: %+v", again.Orders)
	}

	if *again.Limit != 5 || *again.Offset != 1 {
		t.Fatalf("limit or offset changed: %d %d", *again.Limit, *again.Offset)
	}
}

// TestKindString verifies the statement keywords.
func TestKindString(t *testing.T) {
	t.Parallel()

	if query.KindSelect.String() != "SELECT" || query.KindInsert.String() != "INSERT" ||
		query.KindUpdate.String() != "UPDATE" || query.KindDelete.String() != "DELETE" {
		t.Fatal("unexpected kind names")
	}
}

// TestInspectReturning verifies Inspect reports a RETURNING clause.
func TestInspectReturning(t *testing.T) {
	t.Parallel()

	spec := query.Table("t", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
		statement.Returning("id")
	}).Inspect()

	if !spec.Returning {
		t.Fatalf("got %+v, want Returning", spec)
	}

	spec = query.Table("t", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
	}).Inspect()

	if spec.Returning {
		t.Fatalf("got %+v, want no Returning", spec)
	}
}
