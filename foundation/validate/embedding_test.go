package validate_test

import (
	"context"
	"sync"
	"testing"

	"arkoren.dev/foundation/validate"
)

// EmbeddedFirst exposes a Go selector whose JSON name differs from its peer.
// Distinct JSON names must not make the ambiguous Go selector valid.
type EmbeddedFirst struct {
	// Role selects the identity or condition branch under test.
	Role string `json:"first_role"`
}

// EmbeddedSecond supplies the competing promoted Role selector.
// Explicit paths must remain available to distinguish these fields.
type EmbeddedSecond struct {
	// Role selects the identity or condition branch under test.
	Role string `json:"second_role"`
}

// embeddedAmbiguous has valid JSON fields but an ambiguous Go Role selector.
type embeddedAmbiguous struct {
	// EmbeddedFirst promotes the first candidate field in an embedding conflict.
	EmbeddedFirst
	// EmbeddedSecond promotes a competing field at the same embedding depth.
	EmbeddedSecond
	// Name holds the name observed by string rules or sibling conditions.
	Name string
}

// EmbeddedMutable holds fields that share one embedded pointer. Preparation
// must detach that pointer once while retaining writes to all sibling fields.
type EmbeddedMutable struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string
	// Role selects the identity or condition branch under test.
	Role string
	// Note provides optional text omitted from the inferred required list.
	Note string
}

// embeddedMutableForm makes mutable fields accessible through promotion.
type embeddedMutableForm struct {
	// EmbeddedMutable promotes fields whose normalization must preserve input
	// storage.
	*EmbeddedMutable
	// Hide provides a field whose JSON tag excludes it from registration.
	Hide bool
}

// EmbeddedMiddle adds another pointer boundary above the promoted fields.
type EmbeddedMiddle struct {
	// EmbeddedMutable promotes fields whose normalization must preserve input
	// storage.
	*EmbeddedMutable
}

// TestEmbeddedBuilderNestedPointersDetachOnce checks each ancestor pointer is
// detached before its descendant, preserving all prepared sibling values.
func TestEmbeddedBuilderNestedPointersDetachOnce(t *testing.T) {
	t.Parallel()

	type form struct {
		// EmbeddedMiddle adds another promotion level to embedded-path checks.
		*EmbeddedMiddle
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Trim()
		})
		object.Field("Role", func(role *validate.StringBuilder[string]) {
			role.Default("guest")
		})
	})

	input := form{
		EmbeddedMiddle: &EmbeddedMiddle{
			EmbeddedMutable: &EmbeddedMutable{
				Name: " Ada ",
			},
		},
	}

	value, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if input.Name != " Ada " || input.Role != "" {
		t.Fatalf("input mutated: %+v", input.EmbeddedMutable)
	}

	if value.Name != "Ada" || value.Role != "guest" {
		t.Fatalf("prepared siblings = %+v", value.EmbeddedMutable)
	}

	if value.EmbeddedMiddle == input.EmbeddedMiddle || value.EmbeddedMutable == input.EmbeddedMutable {
		t.Fatal("result retained an intermediate caller-owned pointer")
	}
}

// TestEmbeddedBuilderPreparationDetachesSharedPointer checks normalization,
// defaults and exclusion operate on the result without modifying input storage.
func TestEmbeddedBuilderPreparationDetachesSharedPointer(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[embeddedMutableForm]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Trim()
		})
		object.Field("Role", func(role *validate.StringBuilder[string]) {
			role.Default("guest")
		})
		object.Field("Note", func(note *validate.StringBuilder[string]) {
			note.ExcludeIf(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Hide", true)
			})
		})
	})

	input := embeddedMutableForm{
		EmbeddedMutable: &EmbeddedMutable{
			Name: " Ada ",
			Note: "private",
		},
		Hide: true,
	}

	value, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if input.Name != " Ada " || input.Role != "" || input.Note != "private" {
		t.Fatalf("input mutated: %+v", input.EmbeddedMutable)
	}

	if value.Name != "Ada" || value.Role != "guest" || value.Note != "" {
		t.Fatalf("result lost sibling writes: %+v", value.EmbeddedMutable)
	}

	if value.EmbeddedMutable == input.EmbeddedMutable {
		t.Fatal("result retained mutable embedded pointer")
	}
}

// TestEmbeddedBuilderConcurrentSharedInput validates independent calls against
// one populated pointer graph; the race detector checks preparation isolation.
func TestEmbeddedBuilderConcurrentSharedInput(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[embeddedMutableForm]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Trim()
		})
		object.Field("Role", func(role *validate.StringBuilder[string]) {
			role.Default("guest")
		})
	})

	input := embeddedMutableForm{
		EmbeddedMutable: &EmbeddedMutable{
			Name: " Ada ",
		},
	}
	var calls sync.WaitGroup

	for range 8 {
		calls.Go(func() {
			value, err := schema.Parse(context.Background(), input)

			if err != nil || value.Name != "Ada" || value.Role != "guest" {
				t.Errorf("Parse = %+v, %v", value, err)
			}
		})
	}

	calls.Wait()

	if input.Name != " Ada " || input.Role != "" {
		t.Fatalf("input mutated: %+v", input.EmbeddedMutable)
	}
}

// TestEmbeddedBuilderNilPointerRemainsNil preserves absent embedded pointers:
// detachment must not allocate a target for defaults or normalization.
func TestEmbeddedBuilderNilPointerRemainsNil(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[embeddedMutableForm]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Trim().Default("guest")
		})
	})

	value, err := schema.Parse(context.Background(), embeddedMutableForm{})

	if err != nil || value.EmbeddedMutable != nil {
		t.Fatalf("Parse = %+v, %v", value, err)
	}
}
