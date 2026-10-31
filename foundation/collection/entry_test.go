package collection_test

import (
	"fmt"

	"arkoren.dev/foundation/collection"
)

// ExampleMapEntry keeps the entry and both field examples executable.
func ExampleMapEntry() {
	entry := collection.NewMapEntry("apples", 3)

	fmt.Println(entry.Key, entry.Value)
	// Output: apples 3
}
