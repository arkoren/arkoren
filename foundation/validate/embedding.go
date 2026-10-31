package validate

import (
	"reflect"
	"slices"
)

// mutableEmbeddedPointers records intermediate pointer paths that preparation
// or conditional effects can write through. A pre hook can replace collection
// or pointer storage even when no rule mutates its values. Prefix ordering
// detaches parents before descendants; deduplication preserves promoted siblings.
func mutableEmbeddedPointers(typ reflect.Type, fields []objField) [][]int {
	var paths [][]int

	for _, field := range fields {
		if !field.node.mut && field.node.pre == nil {
			continue
		}

		paths = append(paths, embeddedPointerPaths(typ, field.info.Index)...)
	}

	slices.SortFunc(paths, slices.Compare[[]int])

	return slices.CompactFunc(paths, slices.Equal[[]int])
}

// embeddedPointerPaths finds intermediate owners on one compiled field path.
// Terminal pointers are excluded because their own node handles detachment.
// Returned paths are copied so collecting another field cannot overwrite them.
func embeddedPointerPaths(typ reflect.Type, index []int) [][]int {
	var paths [][]int

	for depth, position := range index[:len(index)-1] {
		typ = typ.Field(position).Type

		if typ.Kind() != reflect.Pointer {
			continue
		}

		paths = append(paths, slices.Clone(index[:depth+1]))
		typ = typ.Elem()
	}

	return paths
}

// detachEmbeddedPointers copies reachable embedded pointees before any field
// preparation. Nil pointers stay nil, and each compiled path is copied once.
func detachEmbeddedPointers(st *state, object reflect.Value, paths [][]int) {
	for _, path := range paths {
		if st.stopped() {
			return
		}

		pointer, err := object.FieldByIndexErr(path)

		if err != nil || pointer.IsNil() {
			continue
		}

		copied := reflect.New(pointer.Type().Elem())
		copied.Elem().Set(pointer.Elem())
		pointer.Set(copied)
	}
}
