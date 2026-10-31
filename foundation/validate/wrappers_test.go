package validate_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// everything exposes every supported schema role to the builder coverage
// tests. Gate provides a sibling observation for conditional rules.
type everything struct {
	// Gate supplies a sibling observation for conditional validation.
	Gate string
	// Str provides the string-valued target for builder method coverage.
	Str string
	// Int provides the signed-integer target for builder method coverage.
	Int int
	// Uint provides the unsigned-integer target for builder method coverage.
	Uint uint
	// Float provides the floating-point target for builder method coverage.
	Float float64
	// Flag provides the boolean target for acceptance or presence checks.
	Flag bool
	// When provides the instant used by time-schema builder checks.
	When time.Time
	// List provides the slice-valued target for builder method coverage.
	List []string
	// Dict provides the map-valued target for builder method coverage.
	Dict map[string]string
	// Ptr distinguishes an absent pointer from a present zero pointee.
	Ptr *string
	// Obj provides the nested object target for builder method coverage.
	Obj address
}

// TestEveryBuilderSupportsTheSharedMethods verifies the shared builder
// methods configure every concrete draft and bind against the object.
func TestEveryBuilderSupportsTheSharedMethods(t *testing.T) {
	t.Parallel()

	on := func(condition *validate.ConditionBuilder) {
		condition.Eq("Gate", "on")
	}

	pass := func(context.Context, string) error {
		return nil
	}

	schema := validate.Struct[everything](func(object *validate.StructBuilder[everything]) {
		object.Field("Gate", func(value *validate.StringBuilder[string]) {
		})
		object.Field("Str", func(value *validate.StringBuilder[string]) {
			value.Bail()
			value.RequiredIf(on)
			value.RequiredUnless(on)
			value.RequiredWith("Gate")
			value.RequiredWithAll("Gate")
			value.RequiredWithout("Gate")
			value.ProhibitedIf(on)
			value.ProhibitedUnless(on)
			value.ExcludeIf(on)
			value.EqField("Gate")
			value.NeField("Gate")
			value.Check(pass)
			value.When(on, func(value *validate.StringBuilder[string]) {
			})
		})
		object.Field("Int", func(number *validate.IntBuilder[int]) {
			number.Bail()
			number.RequiredIf(on)
			number.RequiredUnless(on)
			number.RequiredWith("Gate")
			number.RequiredWithAll("Gate")
			number.RequiredWithout("Gate")
			number.ProhibitedIf(on)
			number.ProhibitedUnless(on)
			number.ExcludeIf(on)
			number.EqField("Int")
			number.NeField("Int")
			number.GtField("Int")
			number.GteField("Int")
			number.LtField("Int")
			number.LteField("Int")
			number.Check(func(context.Context, int) error {
				return nil
			})
			number.When(on, func(number *validate.IntBuilder[int]) {
			})
			number.NotOneOf(99)
		})
		object.Field("Uint", func(number *validate.UintBuilder[uint]) {
			number.Bail()
			number.Required()
			number.RequiredIf(on)
			number.RequiredUnless(on)
			number.RequiredWith("Gate")
			number.RequiredWithAll("Gate")
			number.RequiredWithout("Gate")
			number.ProhibitedIf(on)
			number.ProhibitedUnless(on)
			number.ExcludeIf(on)
			number.EqField("Uint")
			number.NeField("Uint")
			number.GtField("Uint")
			number.GteField("Uint")
			number.LtField("Uint")
			number.LteField("Uint")
			number.Check(func(context.Context, uint) error {
				return nil
			})
			number.When(on, func(number *validate.UintBuilder[uint]) {
			})
			number.NotOneOf(99)
		})
		object.Field("Float", func(number *validate.FloatBuilder[float64]) {
			number.Bail()
			number.RequiredIf(on)
			number.RequiredUnless(on)
			number.RequiredWith("Gate")
			number.RequiredWithAll("Gate")
			number.RequiredWithout("Gate")
			number.ProhibitedIf(on)
			number.ProhibitedUnless(on)
			number.ExcludeIf(on)
			number.EqField("Float")
			number.NeField("Float")
			number.GtField("Float")
			number.GteField("Float")
			number.LtField("Float")
			number.LteField("Float")
			number.Check(func(context.Context, float64) error {
				return nil
			})
			number.When(on, func(number *validate.FloatBuilder[float64]) {
			})
			number.NotOneOf(99)
		})
		object.Field("Flag", func(value *validate.BoolBuilder[bool]) {
			value.Bail()
			value.RequiredIf(on)
			value.RequiredUnless(on)
			value.RequiredWith("Gate")
			value.RequiredWithAll("Gate")
			value.RequiredWithout("Gate")
			value.ProhibitedIf(on)
			value.ProhibitedUnless(on)
			value.ExcludeIf(on)
			value.EqField("Flag")
			value.NeField("Flag")
			value.Check(func(context.Context, bool) error {
				return nil
			})
			value.When(on, func(value *validate.BoolBuilder[bool]) {
			})
		})
		object.Field("When", func(moment *validate.TimeBuilder) {
			moment.Bail()
			moment.RequiredIf(on)
			moment.RequiredUnless(on)
			moment.RequiredWith("Gate")
			moment.RequiredWithAll("Gate")
			moment.RequiredWithout("Gate")
			moment.ProhibitedIf(on)
			moment.ProhibitedUnless(on)
			moment.ExcludeIf(on)
			moment.EqField("When")
			moment.NeField("When")
			moment.AfterField("When")
			moment.BeforeField("When")
			moment.Check(func(context.Context, time.Time) error {
				return nil
			})
			moment.When(on, func(moment *validate.TimeBuilder) {
			})
		})
		object.Field("List", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
			})
			items.Bail()
			items.RequiredIf(on)
			items.RequiredUnless(on)
			items.RequiredWith("Gate")
			items.RequiredWithAll("Gate")
			items.RequiredWithout("Gate")
			items.ProhibitedIf(on)
			items.ProhibitedUnless(on)
			items.ExcludeIf(on)
			items.Check(func(context.Context, []string) error {
				return nil
			})
			items.When(on, func(items *validate.SliceBuilder[string]) {
				items.Values(func(value *validate.StringBuilder[string]) {
				})
			})
		})
		object.Field("Dict", func(entries *validate.MapBuilder[string, string]) {
			entries.Keys(func(value *validate.StringBuilder[string]) {
			})
			entries.Values(func(value *validate.StringBuilder[string]) {
			})
			entries.Bail()
			entries.RequiredIf(on)
			entries.RequiredUnless(on)
			entries.RequiredWith("Gate")
			entries.RequiredWithAll("Gate")
			entries.RequiredWithout("Gate")
			entries.ProhibitedIf(on)
			entries.ProhibitedUnless(on)
			entries.ExcludeIf(on)
			entries.Check(func(context.Context, map[string]string) error {
				return nil
			})
			entries.When(on, func(entries *validate.MapBuilder[string, string]) {
				entries.Keys(func(value *validate.StringBuilder[string]) {
				})
				entries.Values(func(value *validate.StringBuilder[string]) {
				})
			})
		})
		object.Field("Ptr", func(pointer *validate.PtrBuilder[string]) {
			pointer.Value(func(value *validate.StringBuilder[string]) {
			})
			pointer.Bail()
			pointer.RequiredIf(on)
			pointer.RequiredUnless(on)
			pointer.RequiredWith("Gate")
			pointer.RequiredWithAll("Gate")
			pointer.RequiredWithout("Gate")
			pointer.ProhibitedIf(on)
			pointer.ProhibitedUnless(on)
			pointer.ExcludeIf(on)
			pointer.Check(func(context.Context, *string) error {
				return nil
			})
			pointer.When(on, func(pointer *validate.PtrBuilder[string]) {
				pointer.Value(func(value *validate.StringBuilder[string]) {
				})
			})
		})
		object.Field("Obj", func(object *validate.StructBuilder[address]) {
			object.Bail()
			object.RequiredIf(on)
			object.RequiredUnless(on)
			object.RequiredWith("Gate")
			object.RequiredWithAll("Gate")
			object.RequiredWithout("Gate")
			object.ProhibitedIf(on)
			object.ProhibitedUnless(on)
			object.ExcludeIf(on)
			object.Check(func(context.Context, address) error {
				return nil
			})
			object.When(on, func(object *validate.StructBuilder[address]) {
			})
		})
	})

	if _, err := schema.Parse(context.Background(), everything{}); err != nil {
		if _, ok := errors.AsType[validate.Errors](err); !ok {
			t.Fatal(err)
		}
	}
}

// TestWhenNilSchemaPanics verifies a conditional child rejects a nil reused schema.
func TestWhenNilSchemaPanics(t *testing.T) {
	t.Parallel()

	type form struct {
		// A provides one independently registered value for sibling-rule checks.
		A, Name string
	}

	requirePanic(t, func() {
		validate.Struct(func(object *validate.StructBuilder[form]) {
			object.Field("Name", func(value *validate.StringBuilder[string]) {
				value.When(func(condition *validate.ConditionBuilder) {
					condition.Present("A")
				}, func(selected *validate.StringBuilder[string]) {
					selected.Use(nil)
				})
			})
		})
	})
}

// TestBuildersAreImmutable verifies deriving two schemas from one parent
// leaves the parent and the siblings unchanged, for every builder type.
func TestBuildersAreImmutable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fail := func(code string) error {
		return validate.Fail(code, nil)
	}

	check := func(t *testing.T, name string, parent, a, b func() error) {
		t.Helper()

		for want, fn := range map[string]func() error{"p": parent, "a,p": a, "b,p": b} {
			var got []string

			for _, codes := range codesOf(t, fn()) {
				got = append(got, codes...)
			}

			slices.Sort(got)

			if strings.Join(got, ",") != want {
				t.Errorf("%s: codes = %v, want %s", name, got, want)
			}
		}
	}

	// A parent built with spare capacity must not be aliased.
	str := validate.String(func(value *validate.StringBuilder[string]) {
		value.Required()
		value.Check(func(context.Context, string) error {
			return fail("p")
		})
	})
	strA := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(str)
		value.Check(func(context.Context, string) error {
			return fail("a")
		})
	})
	strB := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(str)
		value.Check(func(context.Context, string) error {
			return fail("b")
		})
	})

	check(t, "string",
		func() error {
			return str.Validate(ctx, "v")
		},
		func() error {
			return strA.Validate(ctx, "v")
		},
		func() error {
			return strB.Validate(ctx, "v")
		})

	in := validate.Int(func(number *validate.IntBuilder[int]) {
		number.Check(func(context.Context, int) error {
			return fail("p")
		})
	})
	inA := validate.Int(func(number *validate.IntBuilder[int]) {
		number.Use(in)
		number.Check(func(context.Context, int) error {
			return fail("a")
		})
	})
	inB := validate.Int(func(number *validate.IntBuilder[int]) {
		number.Use(in)
		number.Check(func(context.Context, int) error {
			return fail("b")
		})
	})

	check(t, "int",
		func() error {
			return in.Validate(ctx, 1)
		},
		func() error {
			return inA.Validate(ctx, 1)
		},
		func() error {
			return inB.Validate(ctx, 1)
		})

	un := validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Check(func(context.Context, uint) error {
			return fail("p")
		})
	})
	unA := validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Use(un)
		number.Check(func(context.Context, uint) error {
			return fail("a")
		})
	})
	unB := validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Use(un)
		number.Check(func(context.Context, uint) error {
			return fail("b")
		})
	})

	check(t, "uint",
		func() error {
			return un.Validate(ctx, 1)
		},
		func() error {
			return unA.Validate(ctx, 1)
		},
		func() error {
			return unB.Validate(ctx, 1)
		})

	fl := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Check(func(context.Context, float64) error {
			return fail("p")
		})
	})
	flA := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Use(fl)
		number.Check(func(context.Context, float64) error {
			return fail("a")
		})
	})
	flB := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Use(fl)
		number.Check(func(context.Context, float64) error {
			return fail("b")
		})
	})

	check(t, "float",
		func() error {
			return fl.Validate(ctx, 1)
		},
		func() error {
			return flA.Validate(ctx, 1)
		},
		func() error {
			return flB.Validate(ctx, 1)
		})

	bo := validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Check(func(context.Context, bool) error {
			return fail("p")
		})
	})
	boA := validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Use(bo)
		value.Check(func(context.Context, bool) error {
			return fail("a")
		})
	})
	boB := validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Use(bo)
		value.Check(func(context.Context, bool) error {
			return fail("b")
		})
	})

	check(t, "bool",
		func() error {
			return bo.Validate(ctx, true)
		},
		func() error {
			return boA.Validate(ctx, true)
		},
		func() error {
			return boB.Validate(ctx, true)
		})

	now := time.Now()

	ti := validate.Time(func(moment *validate.TimeBuilder) {
		moment.Check(func(context.Context, time.Time) error {
			return fail("p")
		})
	})
	tiA := validate.Time(func(moment *validate.TimeBuilder) {
		moment.Use(ti)
		moment.Check(func(context.Context, time.Time) error {
			return fail("a")
		})
	})
	tiB := validate.Time(func(moment *validate.TimeBuilder) {
		moment.Use(ti)
		moment.Check(func(context.Context, time.Time) error {
			return fail("b")
		})
	})

	check(t, "time",
		func() error {
			return ti.Validate(ctx, now)
		},
		func() error {
			return tiA.Validate(ctx, now)
		},
		func() error {
			return tiB.Validate(ctx, now)
		})

	sl := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Check(func(context.Context, []string) error {
			return fail("p")
		})
	})
	slA := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Use(sl)
		items.Check(func(context.Context, []string) error {
			return fail("a")
		})
	})
	slB := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Use(sl)
		items.Check(func(context.Context, []string) error {
			return fail("b")
		})
	})

	list := []string{"x"}

	check(t, "slice",
		func() error {
			return sl.Validate(ctx, list)
		},
		func() error {
			return slA.Validate(ctx, list)
		},
		func() error {
			return slB.Validate(ctx, list)
		})

	mp := validate.Map(func(entries *validate.MapBuilder[string, string]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(value *validate.StringBuilder[string]) {
		})

		entries.Check(func(context.Context, map[string]string) error {
			return fail("p")
		})
	})
	mpA := validate.Map(func(entries *validate.MapBuilder[string, string]) {
		entries.Use(mp)
		entries.Check(func(context.Context, map[string]string) error {
			return fail("a")
		})
	})
	mpB := validate.Map(func(entries *validate.MapBuilder[string, string]) {
		entries.Use(mp)
		entries.Check(func(context.Context, map[string]string) error {
			return fail("b")
		})
	})

	dict := map[string]string{"k": "v"}

	check(t, "map",
		func() error {
			return mp.Validate(ctx, dict)
		},
		func() error {
			return mpA.Validate(ctx, dict)
		},
		func() error {
			return mpB.Validate(ctx, dict)
		})

	text := "v"

	pt := validate.Ptr[string](validate.String[string](), func(pointer *validate.PtrBuilder[string]) {
		pointer.Check(func(context.Context, *string) error {
			return fail("p")
		})
	})
	ptA := validate.Ptr(validate.String[string](), func(pointer *validate.PtrBuilder[string]) {
		pointer.Use(pt)
		pointer.Check(func(context.Context, *string) error {
			return fail("a")
		})
	})
	ptB := validate.Ptr(validate.String[string](), func(pointer *validate.PtrBuilder[string]) {
		pointer.Use(pt)
		pointer.Check(func(context.Context, *string) error {
			return fail("b")
		})
	})

	check(t, "ptr",
		func() error {
			return pt.Validate(ctx, &text)
		},
		func() error {
			return ptA.Validate(ctx, &text)
		},
		func() error {
			return ptB.Validate(ctx, &text)
		})

	var retained *validate.StructBuilder[derivedObject]

	ob := validate.Struct[derivedObject](func(object *validate.StructBuilder[derivedObject]) {
		retained = object
		object.Field("Gate", func(value *validate.StringBuilder[string]) {
		})
		object.Field("Val", func(value *validate.StringBuilder[string]) {
		})
	}, func(object *validate.StructBuilder[derivedObject]) {
		object.Check(func(context.Context, derivedObject) error {
			return fail("p")
		})
	})
	obA := validate.Struct[derivedObject](func(object *validate.StructBuilder[derivedObject]) {
		object.Use(ob)
		object.Check(func(context.Context, derivedObject) error {
			return fail("a")
		})
	})
	obB := validate.Struct[derivedObject](func(object *validate.StructBuilder[derivedObject]) {
		object.Use(ob)
		object.Check(func(context.Context, derivedObject) error {
			return fail("b")
		})
	})

	value := derivedObject{
		Gate: "g",
	}

	check(t, "object",
		func() error {
			return ob.Validate(ctx, value)
		},
		func() error {
			return obA.Validate(ctx, value)
		},
		func() error {
			return obB.Validate(ctx, value)
		})

	// Later registrations must not affect the published schema or its derivatives.
	retained.Field("Val", func(value *validate.StringBuilder[string]) {
		value.Required()
	})

	if err := ob.Validate(ctx, derivedObject{
		Gate: "g",
	}); codesOf(t, err)[""][0] != "p" || len(codesOf(t, err)) != 1 {
		t.Errorf("builder mutation leaked into the schema: %v", err)
	}
}

// TestRedesignConstructorCallbacksShareDraft checks inference, callback aliases,
// and ordered edits without depending on the schema's internal representation.
func TestRedesignConstructorCallbacksShareDraft(t *testing.T) {
	t.Parallel()

	var first *validate.StringBuilder[string]

	callbacks := []validate.StringBuilderFunc[string]{
		func(name *validate.StringBuilder[string]) {
			first = name
			name.Default("Ada")
		},
		func(name *validate.StringBuilder[string]) {
			if name != first {
				t.Fatal("constructor callbacks received different drafts")
			}

			name.Min(3)
		},
	}

	schema := validate.String(callbacks...)

	got, err := schema.Parse(context.Background(), "")

	if err != nil {
		t.Fatal(err)
	}

	if got != "Ada" {
		t.Fatalf("Parse = %q, want Ada", got)
	}
}

// TestRedesignPublicationDetachesRetainedDrafts exercises all retained draft
// roles together; edits after publication must not alter validation behavior.
func TestRedesignPublicationDetachesRetainedDrafts(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name, Mode string
	}
	var root *validate.StructBuilder[form]
	var child *validate.StringBuilder[string]
	var condition *validate.ConditionBuilder

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		root = object
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			child = name
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				condition = predicate
				predicate.Eq("Mode", "active")
			})
		})
	})

	root.Field("Mode", func(mode *validate.StringBuilder[string]) {
		mode.Required()
	})
	child.Min(20)
	condition.Eq("Mode", "impossible")

	if err := schema.Validate(context.Background(), form{
		Name: "Ada",
	}); err != nil {
		t.Fatalf("retained root or child changed schema: %v", err)
	}

	if err := schema.Validate(context.Background(), form{
		Mode: "active",
	}); err == nil {
		t.Fatal("retained condition changed published requirement")
	}
}

// TestRedesignUseDoesNotMutatePublishedScalar ensures local restrictions on a
// reusable schema remain local to the draft that imports it.
func TestRedesignUseDoesNotMutatePublishedScalar(t *testing.T) {
	t.Parallel()

	shared := validate.String(func(name *validate.StringBuilder[string]) {
		name.Trim()
	})
	local := validate.String(func(name *validate.StringBuilder[string]) {
		name.Use(shared)
		name.Min(4)
	})

	if err := local.Validate(context.Background(), " Al "); err == nil {
		t.Fatal("local minimum was not applied after shared normalization")
	}

	got, err := shared.Parse(context.Background(), " Al ")

	if err != nil {
		t.Fatal(err)
	}

	if got != "Al" {
		t.Fatalf("shared Parse = %q, want Al", got)
	}
}

// TestReuseBuilderPointerScalarSnapshotDetached checks reassignment through the
// caller's schema pointer does not replace the already published source snapshot.
func TestReuseBuilderPointerScalarSnapshotDetached(t *testing.T) {
	t.Parallel()

	shared := validate.String(func(name *validate.StringBuilder[string]) {
		name.Min(3)
	})
	schema := validate.String(func(name *validate.StringBuilder[string]) {
		name.Use(&shared)
	})

	shared = validate.String[string]()

	if err := schema.Validate(context.Background(), "a"); err == nil {
		t.Fatal("source pointer reassignment weakened the published schema")
	}
}
