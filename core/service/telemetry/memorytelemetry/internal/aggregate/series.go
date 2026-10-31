// Package aggregate retains bounded, ordered metric label tuples.
package aggregate

import (
	"encoding/binary"
	"slices"
)

// Point holds one aggregate and its owned label values.
type Point struct {
	// Values is the exact declared label tuple.
	Values []string

	// Count counts recordings rather than increments.
	Count uint64

	// Sum is the total of recorded values.
	Sum float64

	// Last is the most recent recorded value.
	Last float64
}

// Series stores aggregates in first-recording order. The owner synchronizes all
// calls, including Reset and Snapshot, with its lifecycle commit lock.
type Series struct {
	// points holds aggregates in first-recording order.
	points []Point

	// index maps exact length-prefixed tuples to positions in points.
	index map[string]int
}

// Reset drops recordings without changing the registered instrument.
func (series *Series) Reset() {
	series.points, series.index = nil, make(map[string]int)
}

// Record updates an existing tuple or admits one while capacity permits.
// It keeps no reference to values, and never evicts existing tuples.
func (series *Series) Record(value float64, values []string, capacity int) {
	key := Identity(values...)
	position, ok := series.index[key]

	if !ok {
		if len(series.points) >= capacity {
			return
		}

		if series.index == nil {
			series.index = make(map[string]int)
		}

		position = len(series.points)
		series.index[key] = position
		series.points = append(series.points, Point{Values: slices.Clone(values)})
	}

	point := &series.points[position]
	point.Count++
	point.Sum += value
	point.Last = value
}

// Snapshot returns detached aggregates, including a non-nil empty slice when
// no recordings remain. This preserves the driver's snapshot representation.
func (series *Series) Snapshot() []Point {
	points := make([]Point, len(series.points))

	for index, point := range series.points {
		point.Values = slices.Clone(point.Values)
		points[index] = point
	}

	return points
}

// Identity encodes each string as its unsigned-varint byte length followed by
// its exact bytes. Embedded NULs, empty values and different tuple lengths stay
// distinct. Nil and empty tuples encode to "". This is an in-process map key,
// not a wire or storage format, and retains no reference to the input slice.
func Identity(values ...string) string {
	// buffer supplies initial space for short tuples; append grows it as needed.
	var buffer [128]byte

	key := buffer[:0]

	for _, value := range values {
		key = binary.AppendUvarint(key, uint64(len(value)))
		key = append(key, value...)
	}

	return string(key)
}
