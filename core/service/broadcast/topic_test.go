package broadcast_test

import (
	"fmt"

	"arkoren.dev/core/service/broadcast"
)

// ExampleMatch demonstrates whole-token matching and the zero-token # tail.
// Invalid exact names never match, even when a pattern would otherwise accept.
func ExampleMatch() {
	fmt.Println(broadcast.Match("orders.*", "orders.created"))
	fmt.Println(broadcast.Match("orders.#", "orders"))
	fmt.Println(broadcast.Match("orders.#", "orders..created"))
	// Output:
	// true
	// true
	// false
}
