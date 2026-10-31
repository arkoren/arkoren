package collection

import "reflect"

// JSONSchemaType declares the native slice type whose default JSON encoding
// describes this collection. Schema tools can use this structural protocol
// without importing collection. It does not inspect or retain the receiver.
// Byte elements preserve the native base64 string representation, not an array.
// This declaration assumes default encoding options, just like MarshalJSON.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2})
//	fmt.Println(items.JSONSchemaType() == reflect.TypeFor[[]int]()) // true
func (slice Slice[T]) JSONSchemaType() reflect.Type {
	return reflect.TypeFor[[]T]()
}

// JSONSchemaType declares the native map type whose default JSON encoding
// describes this collection. It enables structural schema inference without a
// dependency between collection and schema libraries. The receiver is ignored;
// unsupported JSON key or value types still require rejection by the consumer.
//
// Example:
//
//	entries := collection.NewMap(map[string]int{"apples": 3})
//	fmt.Println(entries.JSONSchemaType() == reflect.TypeFor[map[string]int]()) // true
func (mapping Map[K, V]) JSONSchemaType() reflect.Type {
	return reflect.TypeFor[map[K]V]()
}
