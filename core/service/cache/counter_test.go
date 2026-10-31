package cache_test

import (
	"context"
	"fmt"
	"math"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// ExampleCache_Increment_overflow handles a rejected counter update without
// relying on a portable overflow sentinel. The previously stored value remains.
func ExampleCache_Increment_overflow() {
	ctx := context.Background()
	store := cache.New(memorycache.New())
	key := cache.NewKey[int64]("visits")

	if err := store.Put(ctx, key, math.MaxInt64); err != nil {
		fmt.Println(err)

		return
	}

	_, err := store.Increment(ctx, key, 1)
	fmt.Println("overflow rejected:", err != nil)

	value, err := store.Get(ctx, key)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value)
	// Output:
	// overflow rejected: true
	// 9223372036854775807
}

// ExampleCache_Decrement_minimumDelta demonstrates valid mathematical
// subtraction even when negating the delta would not fit int64.
func ExampleCache_Decrement_minimumDelta() {
	ctx := context.Background()
	store := cache.New(memorycache.New())
	key := cache.NewKey[int64]("balance")

	if err := store.Put(ctx, key, math.MinInt64); err != nil {
		fmt.Println(err)

		return
	}

	value, err := store.Decrement(ctx, key, math.MinInt64)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value)
	// Output: 0
}
