package telemetry

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AttrKind identifies the type of value an [Attr] holds. The kinds map one
// to one onto OpenTelemetry attribute types, without depending on SDK numbers.
// The empty kind identifies an invalid attribute, preserving zero Attr behavior.
//
//	if telemetry.String("method", "GET").Kind() == telemetry.AttrString {
//		// Read the attribute with AsString.
//	}
type AttrKind string

const (
	// AttrInvalid is the kind of the zero [Attr]. Attributes of this kind are
	// dropped by [Telemetry].
	//
	//	valid := (telemetry.Attr{}).Kind() != telemetry.AttrInvalid // false
	AttrInvalid AttrKind = ""

	// AttrString holds a string.
	//
	//	kind := telemetry.String("method", "GET").Kind() // telemetry.AttrString
	AttrString AttrKind = "string"

	// AttrInt64 holds a 64-bit signed integer.
	//
	//	kind := telemetry.Int64("count", 1).Kind() // telemetry.AttrInt64
	AttrInt64 AttrKind = "int64"

	// AttrFloat64 holds a 64-bit float.
	//
	//	kind := telemetry.Float64("ratio", 0.5).Kind() // telemetry.AttrFloat64
	AttrFloat64 AttrKind = "float64"

	// AttrBool holds a boolean.
	//
	//	kind := telemetry.Bool("cached", true).Kind() // telemetry.AttrBool
	AttrBool AttrKind = "bool"

	// AttrStrings holds a slice of strings.
	//
	//	kind := telemetry.Strings("tags", []string{"fast"}).Kind() // telemetry.AttrStrings
	AttrStrings AttrKind = "strings"
)

// Attr is an immutable key-value pair attached to spans, events, resources,
// and metric recordings. It is deliberately not [log/slog.Attr]: it has no
// log-only kinds (groups, lazy values, times) and it has typed slices, so a
// driver maps it onto its backend without conversion.
//
// The zero Attr is invalid and is dropped by [Telemetry]. Attr values are
// safe to copy and share between goroutines.
//
//	attr := telemetry.String("region", "eu")
//	fmt.Println(attr.Key(), attr.AsString())
type Attr struct {
	// key is the attribute name.
	key string

	// kind selects which value field is meaningful.
	kind AttrKind

	// text holds the value of an AttrString attribute.
	text string

	// number holds the AttrInt64 value, the AttrFloat64 bits, or 0 or 1 for
	// AttrBool.
	number uint64

	// texts holds the AttrStrings value. It is never modified after
	// construction.
	texts []string
}

// String returns a string attribute.
//
//	telemetry.String("http.request.method", "GET")
func String(key, value string) Attr {
	return Attr{
		key:  key,
		kind: AttrString,
		text: value,
	}
}

// Int returns an integer attribute. It has kind [AttrInt64].
//
//	telemetry.Int("http.response.status_code", 200)
func Int(key string, value int) Attr {
	return Int64(key, int64(value))
}

// Int64 returns a 64-bit integer attribute.
//
//	telemetry.Int64("messaging.message.size", size)
func Int64(key string, value int64) Attr {
	return Attr{
		key:    key,
		kind:   AttrInt64,
		number: uint64(value),
	}
}

// Float64 returns a 64-bit float attribute.
//
//	telemetry.Float64("sample.ratio", 0.25)
func Float64(key string, value float64) Attr {
	return Attr{
		key:    key,
		kind:   AttrFloat64,
		number: math.Float64bits(value),
	}
}

// Bool returns a boolean attribute.
//
//	telemetry.Bool("arkoren.cache.hit", true)
func Bool(key string, value bool) Attr {
	attr := Attr{
		key:  key,
		kind: AttrBool,
	}

	if value {
		attr.number = 1
	}

	return attr
}

// Strings returns a string-slice attribute. The slice is copied, so later
// changes by the caller do not affect the attribute.
//
//	telemetry.Strings("roles", []string{"admin", "billing"})
func Strings(key string, values []string) Attr {
	return Attr{
		key:   key,
		kind:  AttrStrings,
		texts: slices.Clone(values),
	}
}

// Duration returns a float attribute holding value in seconds, the unit
// OpenTelemetry semantic conventions use for durations. It has kind
// [AttrFloat64]; name the key accordingly, for example "retry.delay".
//
//	telemetry.Duration("retry.delay", 1500*time.Millisecond) // 1.5
func Duration(key string, value time.Duration) Attr {
	return Float64(key, value.Seconds())
}

// Key returns the attribute name.
// Use it when inspecting a snapshot without converting the typed value.
//
//	attr := telemetry.Int("attempt", 2)
//	fmt.Println(attr.Key()) // attempt
func (attr Attr) Key() string {
	return attr.key
}

// Kind returns the type of value the attribute holds.
// It lets consumers select the matching typed accessor without guessing.
//
//	attr := telemetry.Bool("cached", true)
//	if attr.Kind() == telemetry.AttrBool {
//		fmt.Println(attr.AsBool())
//	}
func (attr Attr) Kind() AttrKind {
	return attr.kind
}

// Valid reports whether the attribute has a non-empty key and a value kind.
// The zero value and attributes with an empty key fail this check.
//
//	fmt.Println(telemetry.String("region", "eu").Valid()) // true
//	fmt.Println((telemetry.Attr{}).Valid())               // false
func (attr Attr) Valid() bool {
	return attr.key != "" && attr.kind != AttrInvalid
}

// AsString returns the value of an [AttrString] attribute and "" for other
// kinds. Use [Attr.Text] for a string form of any kind.
//
//	attr := telemetry.String("region", "eu")
//	fmt.Println(attr.AsString()) // eu
func (attr Attr) AsString() string {
	return attr.text
}

// AsInt64 returns the value of an [AttrInt64] attribute and 0 for other
// kinds.
//
//	attr := telemetry.Int64("size", 2048)
//	fmt.Println(attr.AsInt64()) // 2048
func (attr Attr) AsInt64() int64 {
	if attr.kind != AttrInt64 {
		return 0
	}

	return int64(attr.number)
}

// AsFloat64 returns the value of an [AttrFloat64] attribute and 0 for other
// kinds.
//
//	attr := telemetry.Duration("elapsed", 250*time.Millisecond)
//	fmt.Println(attr.AsFloat64()) // 0.25 seconds
func (attr Attr) AsFloat64() float64 {
	if attr.kind != AttrFloat64 {
		return 0
	}

	return math.Float64frombits(attr.number)
}

// AsBool returns the value of an [AttrBool] attribute and false for other
// kinds.
//
//	attr := telemetry.Bool("cached", true)
//	fmt.Println(attr.AsBool()) // true
func (attr Attr) AsBool() bool {
	return attr.kind == AttrBool && attr.number == 1
}

// AsStrings returns a copy of the value of an [AttrStrings] attribute and nil
// for other kinds.
//
//	attr := telemetry.Strings("roles", []string{"reader"})
//	roles := attr.AsStrings()
//	roles[0] = "editor" // Does not change attr.
//	fmt.Println(attr.AsStrings())
func (attr Attr) AsStrings() []string {
	return slices.Clone(attr.texts)
}

// Value returns the value as string, int64, float64, bool, or []string
// according to [Attr.Kind], and nil for an invalid attribute. A returned
// slice is a copy.
//
//	attr := telemetry.Int64("size", 2048)
//	if size, ok := attr.Value().(int64); ok {
//		fmt.Println(size)
//	}
func (attr Attr) Value() any {
	switch attr.kind {
	case AttrString:
		return attr.text
	case AttrInt64:
		return attr.AsInt64()
	case AttrFloat64:
		return attr.AsFloat64()
	case AttrBool:
		return attr.AsBool()
	case AttrStrings:
		return attr.AsStrings()
	default:
		return nil
	}
}

// Text returns the value as a string: strings as they are, numbers in their
// shortest decimal form, booleans as true or false, and string slices joined
// by commas. Metric label values use this form.
//
//	attr := telemetry.Bool("cached", true)
//	fmt.Println(attr.Text()) // true
func (attr Attr) Text() string {
	switch attr.kind {
	case AttrString:
		return attr.text
	case AttrInt64:
		return strconv.FormatInt(attr.AsInt64(), 10)
	case AttrFloat64:
		return strconv.FormatFloat(attr.AsFloat64(), 'g', -1, 64)
	case AttrBool:
		return strconv.FormatBool(attr.AsBool())
	case AttrStrings:
		return strings.Join(attr.texts, ",")
	default:
		return ""
	}
}

// Equal reports whether both attributes have the same key, kind, and value.
// String-slice values are compared by contents rather than slice identity.
//
//	left := telemetry.Strings("roles", []string{"reader"})
//	right := telemetry.Strings("roles", []string{"reader"})
//	fmt.Println(left.Equal(right)) // true
func (attr Attr) Equal(other Attr) bool {
	return attr.key == other.key &&
		attr.kind == other.kind &&
		attr.text == other.text &&
		attr.number == other.number &&
		slices.Equal(attr.texts, other.texts)
}

// String formats the attribute as key=value.
// It is diagnostic text, not an escaped wire encoding for propagation.
//
//	fmt.Println(telemetry.Int("attempt", 2).String()) // attempt=2
func (attr Attr) String() string {
	return attr.key + "=" + attr.Text()
}

// validAttrs returns a copy of attrs without invalid attributes. It returns
// nil when nothing is left.
func validAttrs(attrs []Attr) []Attr {
	var valid []Attr

	for _, attr := range attrs {
		if attr.Valid() {
			valid = append(valid, attr)
		}
	}

	return valid
}
