// Package validate provides type-safe validation with translated messages and
// errors keyed by JSON Pointer. It depends only on the standard library.
//
// # Construction and execution
//
// Construct schemas once, then share them between concurrent Validate, Parse
// and JSONSchema calls. Published schemas expose execution and inspection only.
// All settings belong to temporary typed drafts and return that same draft.
//
// Example:
//
//	type Signup struct {
//		Email, Password, Confirm, Plan string
//	}
//
//	schema := validate.Struct(func(object *validate.StructBuilder[Signup]) {
//		object.Field("Email", func(email *validate.StringBuilder[string]) {
//			email.Trim().Required().Email()
//		})
//		object.Field("Password", func(password *validate.StringBuilder[string]) {
//			password.Required().Min(12)
//		})
//		object.Field("Confirm", func(confirm *validate.StringBuilder[string]) {
//			confirm.EqField("Password")
//		})
//		object.Field("Plan", func(plan *validate.StringBuilder[string]) {
//			plan.OneOf("free", "pro").Default("free")
//		})
//	})
//
//	input := Signup{
//		Email:    "ada@example.com",
//		Password: "correct horse",
//		Confirm:  "correct horse",
//	}
//	value, err := schema.Parse(context.Background(), input)
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value.Plan)
//
// Constructor and child callbacks run once, synchronously, in argument order on
// one fresh draft. Nil supplied callbacks and application panics propagate;
// expanded nil or empty callback slices omit edits. Publication detaches owned
// containers, so retained drafts cannot change a schema. Unregistered struct
// fields and unconfigured collection children receive no validation. Composite
// children use concrete typed callbacks: Field for structs, Values for slices,
// Keys and Values for maps, and Value for pointer pointees. Ptr also adapts an
// explicit published pointee schema. Rule-builder Use imports the same concrete
// schema role, as a value or nonnil pointer, with the exact value type. NamedBuilder
// instead explicitly adapts one compatible container schema. InferSchema may also
// contribute type-only documentation, but never supplies missing child validators.
// FromJSONSchema returns a separate decoded-JSON validator, not a Schema[T]
// accepted by Use. Other roles are rejected rather than
// losing their child validation. Schema-level rules accumulate; overlapping
// child imports fail construction. Disjoint map key/value imports are allowed.
// Local child setters replace earlier local definitions but cannot replace a
// child imported by Use. Ptr's bare, unconfigured scalar adapter may yield to a
// shared pointee; configured or composite adapters cannot be overwritten.
//
// String, Int, Uint, Float and Bool support built-in and named scalar types.
// Their type arguments are inferred from typed callbacks. Without callbacks,
// write String[string]() or Int[int](). Time, Any and File have fixed types;
// Text supports types with text marshaling and unmarshaling methods, including
// Duration. Struct and child definitions require exact Go types, not conversion.
// Duration encodes Go duration text; plain time.Duration is not JSON-compatible.
// Duration and StringBuilder.Duration export the custom "go-duration" format.
// FromJSONSchema enforces that format with time.ParseDuration; the standard
// "duration" format instead uses the incompatible RFC 3339 Appendix A grammar.
// Named adapts an existing slice or map schema to an identical named container,
// including through NamedBuilder child callbacks. It preserves source rules and
// input ownership without introducing a separate family of collection builders.
// Infer recognizes JSONTypeProvider's explicit native container declaration;
// custom marshalers without that opt-in remain unconstrained metadata.
//
// StringBuilder.URL requires an absolute URL with a host and an allowed scheme,
// compared case-insensitively. The default schemes are http and https. An explicit
// list replaces them and is copied when URL is called, not at publication. Empty
// entries do not permit relative or scheme-relative references; an empty list
// selects the defaults. Like other ordinary rules, URL skips absent strings.
//
// # Presence, defaults and normalization
//
// Scalar zero values, nil pointers and nil collections are absent. An allocated
// empty collection and an allocated pointer to zero are present. A time.Time
// is absent at the zero instant, regardless of its location. Optional absent
// values skip value constraints. Slice elements, map keys/values and nonnil
// pointer pointees are forced present, so their zero values are checked.
// For example, an optional integer with Min(1) accepts zero; use Required or
// a pointer when zero must be range-checked. Prefer Accepted or Declined for
// boolean intent. Struct always checks registered fields; Ptr makes it optional.
//
// Default fills an absent value. Required and Default cannot be combined.
// Struct checks each registered default against its schema during construction.
// Unconditional normalizers and defaults in reachable nested validated fields
// run before conditions, regardless of Go field order. Nil intermediate pointers
// are not allocated, except by explicit pointer defaults. A promoted field
// behind a nil embedded pointer is checked as zero without normalization.
//
// Normalizers run before presence, defaults and rules, in their own call order.
// String Trim, ToLower, ToUpper, TrimPrefix, TrimSuffix and Truncate; numeric
// Abs, Clamp and Round; Time UTC; slice Sort, Filter, Reject, RejectZero and
// Dedupe; and custom Normalize functions change values. Element and pointee
// normalization precedes collection normalization. Diagnostics use normalized
// indexes. Map keys cannot be normalized because that would change identity.
// Parse copies struct values, pointees and collection storage before changes;
// it never mutates caller-owned input. Functions, captures and arbitrary default
// objects retain application ownership. Custom callbacks must not mutate inputs
// or data reachable from them. Validate and Parse agree on pass or fail.
//
// # Conditions and field paths
//
// ConditionBuilder records pure read-only observations of the nearest enclosing
// struct. Eq, NotEq, In, NotIn, Present, Absent and generic If drive RequiredIf,
// RequiredUnless, ProhibitedIf, ProhibitedUnless and ExcludeIf. When takes one
// condition callback followed by typed rule callbacks. Multiple observations
// are ANDed; And, Or and Not create nonempty groups. If requires the exact
// enclosing struct type. Conditions never change values or emit diagnostics.
//
// Paths use dot-separated Go field names through structs and pointer-to-struct
// fields, including promoted fields. JSON names, collection indexes, map keys
// and parent selectors are not supported. Binding checks path and value types
// at construction. Nil intermediate pointers make the observation absent without
// allocation. Eq, NotEq, In and NotIn return false for unresolved paths; explicit
// Not complements that false result. Direct scalar zeros compare normally.
// Present distinguishes nil from allocated empty collections and zero pointees.
//
// EqField, NeField, numeric field comparisons and time field comparisons use the
// same resolver and exact type checks. An unreachable comparison target fails
// rather than silently comparing a fabricated zero. RequiredWith variants use
// the same presence semantics. ExcludeIf clears its value and later rules skip
// it. When and exclusions remain order-sensitive after unconditional preparation.
//
// # Collections
//
// Slice and map drafts offer Contains, Every, UniqueBy and CountWhere. Predicates
// receive exact index/value or key/value pairs and must be pure. Every reports
// every failing entry. UniqueBy reports each duplicate with the first conflicting
// key or index; map traversal and diagnostics use sorted diagnostic-name order.
// Encodable keys use their JSON-v2 object names, including custom JSON or text
// marshaling. Other Go keys retain fmt.Sprint names. Colliding names share a
// pointer without losing violations; their relative order is unspecified.
// Map Has, HasAny and HasAll test key existence even when stored values are zero.
// Empty key lists make HasAny false and HasAll true. Nil optional collections
// skip assertions; present empty collections fail Contains and pass Every and
// uniqueness. CountWhere checks a real zero count with forced presence. Its
// callbacks accept pure integer constraints only: no defaults, normalization,
// presence, conditions, field references, exclusions or Check functions.
// ConditionBuilder offers the same pure collection observations on typed paths.
//
// # Failures and construction errors
//
// Rules run in declaration order; Bail stops a value after its first failure.
// Check returns Fail for a validation failure, At for an object field failure,
// or an operational error that propagates unchanged. Errors maps JSON Pointers
// to Violation records with stable codes, parameters and localized messages.
// Errors.All iterates deterministically. Pointers use encoding/json/v2 field
// names and RFC 6901 escaping. Registration and path selectors use Go names.
// Cancellation stops between callbacks and child values and discards partial
// results. An earlier operational error retains precedence over cancellation.
//
// Fail and At capture acyclic slices, arrays and string-keyed maps recursively,
// including containers reached through interfaces. Named container and scalar
// types and nil-versus-empty distinctions are preserved. Engine-created
// violations detach supported containers before rendering and publication,
// including built-in and imported enum/const parameters. Errors.Localize copies
// them again, even for unknown codes whose messages stay unchanged.
// Struct fields, pointers and non-string-keyed maps are not traversed; native
// objects, functions and captures retain application ownership. This is not a
// general object deep copy or JSON conversion. Errors.Add retains supplied
// parameters, and Errors.All yields borrowed violation slices and parameters.
// Do not mutate shared diagnostics or opaque objects concurrently with readers.
// Parameters need to be JSON-serializable only if the diagnostics will be encoded.
//
// Construction mistakes are not user-input violations. Struct reports recorded
// rule and registration problems together after its declaration callbacks, sorted
// by Go name with registration order for ties. Recorded problems survive later
// settings, Use and publication. Examples include negative string lengths,
// negative MultipleOf factors, non-finite floating constraint bounds and factors,
// invalid shared schemas, wrong field types and invalid field paths. Some setters
// still reject immediately: MultipleOf(0), for example, panics before later
// callbacks run. Supplied nil callbacks and application panics also propagate.
//
// Standalone Parse and Validate on invalid explicit definitions panic with an
// error wrapping ErrInvalidSchema during compilation. JSONSchema instead returns
// nil bytes and an error wrapping that sentinel. Omitted collection child rules
// are valid; a zero unconstructed composite schema is not. In particular, a zero
// StructSchema is invalid even when adapted with Ptr. Use Struct[T]() for a valid
// definition with no registered fields. InferSchema returns inference errors from
// its execution and export methods rather than panicking.
//
// # JSON Schema and localization
//
// JSONSchema renders a deterministic 2020-12 subset. Describe adds an annotation,
// never an error override. Runtime functions, normalization, conditions and
// cross-field rules are omitted instead of being misrepresented. Their definitions
// are still checked: export does not run Check, Normalize, condition or collection
// predicates, replay construction callbacks, or verify defaults against rules.
// Default annotations retain the supplied value without normalization. Repeated
// annotations use the last value while runtime rules accumulate; Go absence
// semantics and numeric representation also prevent a general lossless round trip.
// Encoding failures return nil bytes and preserve the encoder's error cause.
//
// Infer derives type-only documentation without adding runtime constraints.
// FromJSONSchema validates decoded JSON using the supported subset and rejects
// unknown keywords. A pattern keyword must be a string: null, booleans, numbers,
// arrays and objects return an error wrapping ErrInvalidSchema with the keyword's
// JSON Pointer.
// An empty string is a valid pattern. Patterns use Go regular expressions, not
// necessarily the ECMA 262 syntax used by another JSON Schema consumer.
// The decoded-JSON validator's runtime copying and traversal respect context
// cancellation. Oversized arrays report size errors without traversing contents.
// uniqueItems uses exact structural keys; input must be finite and acyclic.
// Bound untrusted input bytes, nesting and cardinality. JSON presence semantics
// differ from Go zero absence.
//
// Messages use Catalog and locale values from NewCatalogContext and
// NewLocaleContext. Lookup tries the locale, its base language, the catalog
// default and its base language, built-in English, then the code. Templates use
// {param} placeholders.
// English returns a mutable copy; Errors.Localize rerenders existing violations.
// NewClockContext replaces the clock behind Future and Past. The package keeps
// no global mutable configuration.
package validate
