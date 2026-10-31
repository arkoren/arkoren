package pagination_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/pagination"
	"arkoren.dev/foundation/collection"
	"arkoren.dev/foundation/validate"
)

// ExamplePage demonstrates mapping only the fetched window while retaining the
// server's total rather than treating a local projection as a new result set.
func ExamplePage() {
	page := pagination.Paginate([]int{41, 42}, 21, 2, 10)
	labels := page.Items.Map(func(_ int, id int) string {
		return fmt.Sprintf("invoice-%d", id)
	})
	fmt.Println(labels.Items(), page.Total, page.LastPage)
	// Output: [invoice-41 invoice-42] 21 3
}

// TestPageItemsTransformCurrentWindow verifies native collection operations
// preserve ordering and leave both source storage and server metadata alone.
func TestPageItemsTransformCurrentWindow(t *testing.T) {
	t.Parallel()

	input := collection.NewSlice([]int{3, 4, 5})
	page := pagination.Paginate(input, 30, 2, 3)
	values := page.Items.Filter(func(_ int, value int) bool {
		return value != 4
	}).Map(func(_ int, value int) int {
		return value * 10
	})

	groups := page.Items.GroupBy(func(_ int, value int) int {
		return value % 2
	})

	if !slices.Equal(values, []int{30, 50}) || !slices.Equal(groups[1], []int{3, 5}) {
		t.Fatalf("values = %v, groups = %v", values, groups)
	}

	values[0] = 99
	input[0] = 8

	if !slices.Equal(page.Items, []int{3, 4, 5}) {
		t.Fatalf("page storage changed: %v", page.Items)
	}

	if page.Total != 30 || page.LastPage != 10 {
		t.Fatalf("server counts changed: %+v", page)
	}

	if page.CurrentPage != 2 || page.PerPage != 3 {
		t.Fatalf("window metadata changed: %+v", page)
	}
}

// TestCursorItemsTransformWithoutMovingAnchors checks removing both boundary
// items locally cannot alter the already encoded navigation positions.
func TestCursorItemsTransformWithoutMovingAnchors(t *testing.T) {
	t.Parallel()

	page, err := pagination.CursorPaginate([]int{3, 4, 5}, 3, true, true, pagination.CursorEncode[int])

	if err != nil {
		t.Fatal(err)
	}

	page.Items = page.Items.Filter(func(_ int, value int) bool {
		return value == 4
	})

	next, err := pagination.CursorDecode[int](page.NextCursor)

	if err != nil {
		t.Fatal(err)
	}

	previous, err := pagination.CursorDecode[int](page.PrevCursor)

	if err != nil {
		t.Fatal(err)
	}

	if next != 5 || previous != 3 {
		t.Fatalf("anchors = %d, %d; want 5, 3", next, previous)
	}

	if page.PerPage != 3 || !slices.Equal(page.Items, []int{4}) {
		t.Fatalf("window = %+v", page)
	}
}

// TestPageItemsShallowSnapshot checks snapshot ownership stops at the outer
// slice; mutating an element's referenced object is intentionally visible.
func TestPageItemsShallowSnapshot(t *testing.T) {
	t.Parallel()

	value := 3
	input := []*int{&value}
	page := pagination.Paginate(input, 1, 1, 1)
	input[0] = nil
	value = 9

	if page.Items[0] == nil || *page.Items[0] != 9 {
		t.Fatalf("referenced value not shared: %v", page.Items)
	}

	cursor, err := pagination.CursorPaginate([]*int{&value}, 1, false, false, nil)

	if err != nil {
		t.Fatal(err)
	}

	value = 10

	if *cursor.Items[0] != 10 {
		t.Fatal("cursor recursively copied its item")
	}
}

// TestPageCollectionJSONRoundTrip pins the existing wire fields and restores
// the collection methods after decoding a page response.
func TestPageCollectionJSONRoundTrip(t *testing.T) {
	t.Parallel()

	page := pagination.Paginate([]int{3, 4}, 9, 2, 2)

	encoded, err := json.Marshal(page)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"items":[3,4],"total":9,"per_page":2,"current_page":2,"last_page":5}` {
		t.Fatalf("JSON = %s", encoded)
	}

	var decoded pagination.Page[int]

	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(page, decoded) || decoded.Items.Len() != 2 {
		t.Fatalf("round trip = %+v", decoded)
	}
}

// TestCursorCollectionJSONRoundTrip pins opaque tokens and omission behavior
// while retaining native collection values after decoding.
func TestCursorCollectionJSONRoundTrip(t *testing.T) {
	t.Parallel()

	page, err := pagination.CursorPaginate([]int{3, 4}, 2, true, true, pagination.CursorEncode[int])

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(page)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"items":[3,4],"per_page":2,"next_cursor":"NA","prev_cursor":"Mw"}` {
		t.Fatalf("JSON = %s", encoded)
	}

	var decoded pagination.Cursor[int]

	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(page, decoded) || decoded.Items.Len() != 2 {
		t.Fatalf("round trip = %+v", decoded)
	}
}

// TestPageCollectionNilAndEmptyJSON distinguishes Go's nil storage from the
// stable empty arrays emitted by constructors, including explicit JSON null.
func TestPageCollectionNilAndEmptyJSON(t *testing.T) {
	t.Parallel()

	page := pagination.Paginate([]int(nil), 0, 1, 2)

	encoded, err := json.Marshal(page)

	if err != nil {
		t.Fatal(err)
	}

	if page.Items == nil || string(encoded) != `{"items":[],"total":0,"per_page":2,"current_page":1,"last_page":1}` {
		t.Fatalf("empty page = %s", encoded)
	}

	if err := json.Unmarshal([]byte(`{"items":null}`), &page); err != nil {
		t.Fatal(err)
	}

	if page.Items != nil {
		t.Fatal("JSON null did not preserve nil storage")
	}

	if err := json.Unmarshal([]byte(`{"items":[]}`), &page); err != nil {
		t.Fatal(err)
	}

	if page.Items == nil || !page.Items.IsEmpty() {
		t.Fatal("JSON array did not preserve allocated empty storage")
	}
}

// TestCursorCollectionEmptyJSON checks nil constructor input emits an array
// and no navigation keys rather than inventing tokens for absent boundaries.
func TestCursorCollectionEmptyJSON(t *testing.T) {
	t.Parallel()

	page, err := pagination.CursorPaginate([]int(nil), 2, true, true, nil)

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(page)

	if err != nil {
		t.Fatal(err)
	}

	if page.Items == nil || string(encoded) != `{"items":[],"per_page":2}` {
		t.Fatalf("empty cursor = %s", encoded)
	}
}

// wantIntegerItemsSchema rejects opaque marshaler schemas: transport adapters
// need both the array shape and its integer element schema, not merely {}.
func wantIntegerItemsSchema(t *testing.T, encoded []byte) {
	t.Helper()

	var schema map[string]any

	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}

	properties, ok := schema["properties"].(map[string]any)

	if !ok {
		t.Fatalf("missing object properties: %s", encoded)
	}

	want := map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}

	if !reflect.DeepEqual(properties["items"], want) {
		t.Fatalf("items schema = %v, want %v", properties["items"], want)
	}
}

// TestPageCollectionInferSchema protects the array schema used by HTTP and MCP
// documentation even though the collection implements JSON marshaling.
func TestPageCollectionInferSchema(t *testing.T) {
	t.Parallel()

	encoded, err := validate.Infer[pagination.Page[int]]().JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	wantIntegerItemsSchema(t, encoded)
}

// TestCursorCollectionInferSchema checks cursor results have the same concrete
// item schema as offset results rather than an unrestricted marshaler schema.
func TestCursorCollectionInferSchema(t *testing.T) {
	t.Parallel()

	encoded, err := validate.Infer[pagination.Cursor[int]]().JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	wantIntegerItemsSchema(t, encoded)
}

// TestPageCollectionExplicitConstraints checks an application's named-slice
// schema can enforce item bounds without losing the array's wire shape.
func TestPageCollectionExplicitConstraints(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[pagination.Page[int]]) {
		object.Field("Items", func(items *validate.NamedBuilder[collection.Slice[int]]) {
			items.Use(validate.Slice(func(values *validate.SliceBuilder[int]) {
				values.Min(1)
				values.Values(func(value *validate.IntBuilder[int]) {
					value.Min(1)
				})
			}))
		})
	})

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(encoded), `"minItems":1`) || !strings.Contains(string(encoded), `"minimum":1`) {
		t.Fatalf("constraints missing from schema: %s", encoded)
	}

	if _, err := schema.Parse(t.Context(), pagination.Paginate([]int{0}, 1, 1, 1)); err == nil {
		t.Fatal("invalid item accepted")
	}

	if _, err := schema.Parse(t.Context(), pagination.Paginate([]int(nil), 0, 1, 1)); err == nil {
		t.Fatal("empty window accepted")
	}

	if _, err := schema.Parse(t.Context(), pagination.Paginate([]int{1}, 1, 1, 1)); err != nil {
		t.Fatal(err)
	}
}

// TestPageCollectionHTTPResponse checks the HTTP helper emits the same page
// shape as direct JSON serialization without converting Items back to []T.
func TestPageCollectionHTTPResponse(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()

	if err := response.JSON(writer, http.StatusOK, pagination.Paginate([]int{3}, 1, 1, 2)); err != nil {
		t.Fatal(err)
	}

	var page pagination.Page[int]

	if err := json.Unmarshal(writer.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(page.Items, []int{3}) || page.Total != 1 {
		t.Fatalf("HTTP page = %+v", page)
	}
}

// TestCursorCollectionMCPOutput checks inferred tool output describes and emits
// a collection-backed window, including its original navigation metadata.
func TestCursorCollectionMCPOutput(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("rows", func(context.Context, struct{}) (pagination.Cursor[int], error) {
		return pagination.CursorPaginate([]int{3, 4}, 2, true, false, pagination.CursorEncode[int])
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	wantIntegerItemsSchema(t, tool.Describe().OutputSchema)

	encoded, err := tool.Call(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"items":[3,4],"per_page":2,"next_cursor":"NA"}` {
		t.Fatalf("MCP output = %s", encoded)
	}
}
