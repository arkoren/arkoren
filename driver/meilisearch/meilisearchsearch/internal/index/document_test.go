package index

import (
	"strings"
	"testing"
)

// TestOrderKeyKeepsByteOrder checks that encoded IDs compare, with case
// ignored, in the byte order of the IDs themselves.
func TestOrderKeyKeepsByteOrder(t *testing.T) {
	t.Parallel()

	ids := []string{"-", "0", "9", "A", "B", "Z", "_", "a", "aa", "b", "z"}

	for i := 1; i < len(ids); i++ {
		low, high := orderKey(ids[i-1]), orderKey(ids[i])

		if strings.ToLower(low) != low || low >= high {
			t.Errorf("orderKey(%q) = %q does not sort below orderKey(%q) = %q", ids[i-1], low, ids[i], high)
		}
	}
}
