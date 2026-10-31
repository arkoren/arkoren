package collection

// MapEntry represents one successful key-value pair yielded by a [TryLazyMap].
// Ignore both fields when the accompanying error is non-nil. The value is a
// shallow copy; references within it retain their original ownership.
//
// Example:
//
//	entry := collection.NewMapEntry("apples", 3)
//	fmt.Println(entry.Key, entry.Value) // apples 3
type MapEntry[K comparable, V any] struct {
	// Key is the lookup key of a successful entry. It is not meaningful when
	// the iterator yields an error.
	//
	// Example:
	//
	//	entry := collection.NewMapEntry("apples", 3)
	//	fmt.Println(entry.Key) // apples
	Key K

	// Value is the shallowly copied value associated with Key. References
	// inside it remain shared with the source.
	//
	// Example:
	//
	//	entry := collection.NewMapEntry("apples", 3)
	//	fmt.Println(entry.Value) // 3
	Value V
}

// NewMapEntry pairs key and value for an error-aware map iterator.
// It copies neither nested value storage nor closure captures in value.
//
// Example:
//
//	entry := collection.NewMapEntry("apples", 3)
//	fmt.Println(entry.Key, entry.Value) // apples 3
func NewMapEntry[K comparable, V any](key K, value V) MapEntry[K, V] {
	return MapEntry[K, V]{
		Key:   key,
		Value: value,
	}
}
