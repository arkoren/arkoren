package query_test

import (
	"errors"
	"testing"

	"arkoren.dev/foundation/query"
)

// TestBuildRejectsUnsafeIdentifier verifies that identifiers cannot contain SQL
// fragments.
func TestBuildRejectsUnsafeIdentifier(t *testing.T) {
	t.Parallel()

	_, err := query.Table("users; DROP TABLE users").Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected invalid query error, got %v", err)
	}
}
