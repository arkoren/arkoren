package validate

// StructBuilderFunc configures one temporary struct draft for T. Callbacks run
// once in order; nil supplied callbacks panic and publication detaches owned state.
//
// Example:
//
//	type Person struct {
//		Name string
//	}
//
//	var fields validate.StructBuilderFunc[Person] = func(object *validate.StructBuilder[Person]) {
//		object.Field("Name", func(name *validate.StringBuilder[string]) {
//			name.Required()
//		})
//	}
//
//	schema := validate.Struct(fields)
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
type StructBuilderFunc[T any] = func(*StructBuilder[T])
