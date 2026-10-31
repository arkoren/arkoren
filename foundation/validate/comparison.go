package validate

// GtField requires a value greater than the sibling field of the same type.
//
// Example:
//
//	type Bounds struct {
//		Low, High int
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("High", func(number *validate.IntBuilder[int]) {
//			number.GtField("Low")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) GtField(field string) *IntBuilder[N] {
	number.base = number.relate(relGt, field)

	return number
}

// GteField requires a value greater than or equal to the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High int
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("High", func(number *validate.IntBuilder[int]) {
//			number.GteField("Low")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) GteField(field string) *IntBuilder[N] {
	number.base = number.relate(relGte, field)

	return number
}

// LtField requires a value less than the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High int
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("Low", func(number *validate.IntBuilder[int]) {
//			number.LtField("High")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) LtField(field string) *IntBuilder[N] {
	number.base = number.relate(relLt, field)

	return number
}

// LteField requires a value less than or equal to the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High int
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("Low", func(number *validate.IntBuilder[int]) {
//			number.LteField("High")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) LteField(field string) *IntBuilder[N] {
	number.base = number.relate(relLte, field)

	return number
}

// GtField requires a value greater than the sibling field of the same type.
//
// Example:
//
//	type Bounds struct {
//		Low, High uint
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("High", func(number *validate.UintBuilder[uint]) {
//			number.GtField("Low")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) GtField(field string) *UintBuilder[N] {
	number.base = number.relate(relGt, field)

	return number
}

// GteField requires a value greater than or equal to the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High uint
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("High", func(number *validate.UintBuilder[uint]) {
//			number.GteField("Low")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) GteField(field string) *UintBuilder[N] {
	number.base = number.relate(relGte, field)

	return number
}

// LtField requires a value less than the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High uint
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("Low", func(number *validate.UintBuilder[uint]) {
//			number.LtField("High")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) LtField(field string) *UintBuilder[N] {
	number.base = number.relate(relLt, field)

	return number
}

// LteField requires a value less than or equal to the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High uint
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("Low", func(number *validate.UintBuilder[uint]) {
//			number.LteField("High")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) LteField(field string) *UintBuilder[N] {
	number.base = number.relate(relLte, field)

	return number
}

// GtField requires a value greater than the sibling field of the same type.
//
// Example:
//
//	type Bounds struct {
//		Low, High float64
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("High", func(number *validate.FloatBuilder[float64]) {
//			number.GtField("Low")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) GtField(field string) *FloatBuilder[N] {
	number.base = number.relate(relGt, field)

	return number
}

// GteField requires a value greater than or equal to the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High float64
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("High", func(number *validate.FloatBuilder[float64]) {
//			number.GteField("Low")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) GteField(field string) *FloatBuilder[N] {
	number.base = number.relate(relGte, field)

	return number
}

// LtField requires a value less than the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High float64
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("Low", func(number *validate.FloatBuilder[float64]) {
//			number.LtField("High")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) LtField(field string) *FloatBuilder[N] {
	number.base = number.relate(relLt, field)

	return number
}

// LteField requires a value less than or equal to the sibling field.
//
// Example:
//
//	type Bounds struct {
//		Low, High float64
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Bounds]) {
//		object.Field("Low", func(number *validate.FloatBuilder[float64]) {
//			number.LteField("High")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) LteField(field string) *FloatBuilder[N] {
	number.base = number.relate(relLte, field)

	return number
}
