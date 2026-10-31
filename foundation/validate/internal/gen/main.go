// Command gen writes wrappers.go: the chain methods that every schema type
// forwards to the shared base. Run it with go generate in the validate module.
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"strings"
	"text/template"
)

// schemaType describes one concrete schema role emitted by the wrapper
// generator. Template substitutions keep each role tied to its exact validated
// Go type.
type schemaType struct {
	// Name selects the published schema type, such as StringSchema.
	Name string
	// Use supplies type arguments at references, such as "[S]".
	Use string
	// T is the exact Go value type accepted by this schema role.
	T string
	// NoDefault omits the shared Default method for specialized roles.
	NoDefault bool
	// SealReturn identifies the handwritten StructBuilder publication boundary.
	SealReturn bool
	// Decl supplies constrained type parameters on the draft declaration.
	Decl string
	// Fields declares role-specific child schemas and their ownership markers.
	Fields string
	// Publish supplies role-specific schema literal entries beyond the base.
	Publish string
	// ExampleUse supplies concrete type arguments for executable examples.
	ExampleUse string
	// ExampleType names the concrete validated type used by examples.
	ExampleType string
	// ExampleValue supplies a deterministic default in the generated example.
	ExampleValue string
}

// method describes one shared builder setting and its generated documentation.
// Optional role filters omit operations supplied by handwritten
// implementations.
type method struct {
	// Name names the generated method at the public draft boundary.
	Name string
	// Params is the Go parameter list; {{.T}} expands to the validated type.
	Params string
	// Call is the shared-base expression; {{.T}} expands to the validated type.
	Call string
	// Doc contains the caller-facing behavior documentation before its example.
	Doc string
	// Skip omits this setting for roles with a handwritten implementation; nil
	// includes every role.
	Skip func(schemaType) bool
	// Section retains the output grouping metadata for shared settings.
	Section int
}

// schemas lists concrete schema roles in generated output order. Role metadata
// controls draft types, forwarding methods and executable examples.
var schemas = []schemaType{
	{
		Name: "StringSchema",
		Use:  "[S]",
		Decl: "[S ~string]",
		T:    "S",
	},
	{
		Name: "IntSchema",
		Use:  "[N]",
		Decl: "[N signed]",
		T:    "N",
	},
	{
		Name: "UintSchema",
		Use:  "[N]",
		Decl: "[N unsigned]",
		T:    "N",
	},
	{
		Name: "FloatSchema",
		Use:  "[N]",
		Decl: "[N floating]",
		T:    "N",
	},
	{
		Name: "BoolSchema",
		Use:  "[B]",
		Decl: "[B ~bool]",
		T:    "B",
	},
	{
		Name: "TimeSchema",
		T:    "time.Time",
	},
	{
		Name:    "TextSchema",
		Use:     "[T]",
		Decl:    "[T any]",
		T:       "T",
		Publish: "format: checkedTextFormat(reflect.TypeFor[T]()),",
	},
	{
		Name: "AnySchema",
		T:    "any",
	},
	{
		Name:      "FileSchema",
		T:         "*multipart.FileHeader",
		NoDefault: true,
	},
	{
		Name:      "SliceSchema",
		Use:       "[E]",
		Decl:      "[E any]",
		T:         "[]E",
		NoDefault: true,
		Fields:    "// elem retains the optional exact-element-type child schema.\nelem anySchema\n// sharedElement protects imported child rules from local replacement.\nsharedElement bool",
		Publish:   "elem: draft.elem,\nconstructed: true,",
	},
	{
		Name:      "MapSchema",
		Use:       "[K, V]",
		Decl:      "[K comparable, V any]",
		T:         "map[K]V",
		NoDefault: true,
		Fields:    "// key and val retain optional exact-type schemas for map entries.\nkey, val anySchema\n// sharedKey and sharedValue protect imported child rules from replacement.\nsharedKey, sharedValue bool",
		Publish:   "key: draft.key,\nval: draft.val,\nconstructed: true,",
	},
	{
		Name:      "PtrSchema",
		Use:       "[T]",
		Decl:      "[T any]",
		T:         "*T",
		NoDefault: true,
		Fields:    "// inner retains the optional exact-pointee-type child schema.\ninner anySchema\n// sharedInner protects imported pointee rules from local replacement.\nsharedInner bool",
		Publish:   "inner: draft.inner,\nconstructed: true,",
	},
	{
		Name:       "StructSchema",
		Use:        "[T]",
		Decl:       "[T any]",
		T:          "T",
		SealReturn: true,
	},
}

// noDefault reports whether a schema role supplies its own default handling.
// The method generator uses it to omit the generic Default wrapper.
func noDefault(s schemaType) bool {
	return s.NoDefault
}

// methods lists shared builder settings in generated output order. Role
// filters leave specialized defaults in their handwritten source files.
var methods = []method{
	{
		Name: "Required",
		Call: "s.requireValue()",
		Doc: `Required fails when the value is absent. Absent means zero, or nil
for pointers, slices and maps. It cannot be combined with Default.`,
	},
	{
		Name:   "Default",
		Params: "value {{.T}}",
		Call:   "s.setDefault(func() {{.T}} {\nreturn value\n})",
		Skip:   noDefault,
		Doc: `Default sets the value Parse uses when the prepared input is absent.
Registered defaults are checked while a Struct is built; standalone defaults
are checked by execution, not metadata export. Input normalizers run before
default selection and do not run again on the selected default. Arbitrary
default objects retain application ownership. Default cannot be combined with
Required.`,
	},
	{
		Name:   "Check",
		Params: "fn func(context.Context, {{.T}}) error",
		Call:   "s.check(fn)",
		Doc: `Check adds a custom rule that runs only for present values. Return
[Fail] (or [At] in a Struct check) to report a violation; any other
error aborts validation and is returned to the caller unchanged.`,
	},
	// A raw self-callback avoids a Go 1.27 importer deadlock on the recursive
	// generic BuilderFunc alias. Alias slices remain identical at call sites.
	{
		Name:   "When",
		Params: "condition ConditionBuilderFunc, configure ...func(*{{.Builder}}{{.Use}})",
		Call:   "draft.when(observation, branch.snapshot())",
		Doc: `When applies additional rules when the condition holds for the enclosing struct.
The extra schema runs in place, in the order written, and sees defaults
and exclusions like the rest of the chain.`,
	},
	{
		Name: "Bail",
		Call: "s.setBail()",
		Doc: `Bail stops checking this value at its first failure. By default every
failure is reported.`,
	},
	{
		Name:   "RequiredIf",
		Params: "configure ...ConditionBuilderFunc",
		Call:   "draft.conditional(opRequiredIf, buildCondition(configure...))",
		Doc:    `RequiredIf fails when the value is absent and the conditions hold.`,
	},
	{
		Name:   "RequiredUnless",
		Params: "configure ...ConditionBuilderFunc",
		Call:   "draft.conditional(opRequiredUnless, buildCondition(configure...))",
		Doc:    `RequiredUnless fails when the value is absent and the conditions do not hold.`,
	},
	{
		Name:   "RequiredWith",
		Params: "fields ...string",
		Call:   "s.presence(opRequiredWith, fields)",
		Doc: `RequiredWith fails when the value is absent and any of the sibling
fields is present.`,
	},
	{
		Name:   "RequiredWithAll",
		Params: "fields ...string",
		Call:   "s.presence(opRequiredWithAll, fields)",
		Doc: `RequiredWithAll fails when the value is absent and all of the sibling
fields are present.`,
	},
	{
		Name:   "RequiredWithout",
		Params: "fields ...string",
		Call:   "s.presence(opRequiredWithout, fields)",
		Doc: `RequiredWithout fails when the value is absent and any of the sibling
fields is absent.`,
	},
	{
		Name:   "ProhibitedIf",
		Params: "configure ...ConditionBuilderFunc",
		Call:   "draft.conditional(opProhibitedIf, buildCondition(configure...))",
		Doc:    `ProhibitedIf fails when the value is present and the conditions hold.`,
	},
	{
		Name:   "ProhibitedUnless",
		Params: "configure ...ConditionBuilderFunc",
		Call:   "draft.conditional(opProhibitedUnless, buildCondition(configure...))",
		Doc:    `ProhibitedUnless fails when the value is present and the conditions do not hold.`,
	},
	{
		Name:   "ExcludeIf",
		Params: "configure ...ConditionBuilderFunc",
		Call:   "draft.conditional(opExcludeIf, buildCondition(configure...))",
		Doc: `ExcludeIf treats the value as absent, and clears it, when the conditions hold.
Later ordinary value constraints skip absence, but later presence constraints
still observe it and can report violations. Unconditional preparation may
already have run before this rule is evaluated.`,
	},
	{
		Name:   "EqField",
		Params: "field string",
		Call:   "s.relate(relEq, field)",
		Doc:    `EqField requires the value to equal the sibling field of the same type.`,
	},
	{
		Name:   "NeField",
		Params: "field string",
		Call:   "s.relate(relNe, field)",
		Doc:    `NeField requires the value to differ from the sibling field of the same type.`,
	},
	{
		Name:   "Describe",
		Params: "text string",
		Call:   "s.describe(text)",
		Doc: `Describe sets the human-readable description that JSONSchema renders as
"description". It does not affect validation. Use it for field and tool
descriptions.`,
	},
	{
		Name:    "Normalize",
		Params:  "fn func({{.T}}) {{.T}}",
		Call:    "s.normalize(fn)",
		Section: 1,
		Doc: `Normalize adds a custom normalizer. It runs before the presence check,
Default and every rule, in the order written among the normalizers. It
must return the value it is given or a new one, and must not modify its
argument. Normalize cannot be used on Map key schemas.`,
	},
}

// header starts the generated wrapper file with its ownership marker and
// imports. The generator formats the combined source before returning it.
const header = `// Code generated by internal/gen; DO NOT EDIT.

package validate

import (
	"context"
	"mime/multipart"
	"reflect"
	"slices"
	"time"
)
`

// methodTmpl renders one fluent setting on a concrete construction draft. Each
// generated method updates the shared base and returns the same draft.
const methodTmpl = `
{{.Doc}}
func (draft *{{.Builder}}{{.Use}}) {{.Name}}({{.Params}}) *{{.Builder}}{{.Use}} {
	{{.Before}}
	draft.base = {{.Call}}

	return draft
}
`

// draftTmpl publishes concrete mutable drafts without promoting execution
// methods. The self-typed marker excludes wrappers with promoted methods.
const draftTmpl = `
// {{.Builder}} configures a detached {{.Type}} during construction.
// Settings return the same draft. Retaining a draft cannot change a schema;
// concurrent mutation of a draft is not supported.
//
// Example:
//
//  var configure validate.{{.Builder}}Func{{.ExampleUse}} = func(rules *validate.{{.Builder}}{{.ExampleUse}}) {
//      rules.Required()
//  }
//  _ = configure
type {{.Builder}}{{.Decl}} struct {
    // base retains shared rules until publication detaches owned containers.
    base[{{.T}}]
    {{.Fields}}
}

// {{.Builder}}Func edits one temporary {{.Builder}}. Constructors invoke
// callbacks once, synchronously in order; a nil supplied callback panics.
//
// Example:
//
//  var configure validate.{{.Builder}}Func{{.ExampleUse}} = func(rules *validate.{{.Builder}}{{.ExampleUse}}) {
//      rules.Required()
//  }
//  _ = configure
type {{.Builder}}Func{{.Decl}} = func(*{{.Builder}}{{.Use}})

// draftSelf seals the draft's concrete pointer identity for typed children.
func (draft *{{.Builder}}{{.Use}}) draftSelf() *{{.Builder}}{{.Use}} {
    return draft
}

// snapshot detaches owned rule containers before publishing the schema.
func (draft *{{.Builder}}{{.Use}}) snapshot() {{.Type}}{{.Use}} {
    {{.Check}}
    return {{.Type}}{{.Use}}{
        base: draft.base.detached(),
        {{.Publish}}
    }
}

// publish erases the schema type only after the typed draft is complete.
func (draft *{{.Builder}}{{.Use}}) publish() anySchema {
    return draft.snapshot()
}

// ruleBase exposes immutable rules to another exact-value-type draft.
func (schema {{.Type}}{{.Use}}) ruleBase() base[{{.T}}] {
    rules := schema.base
    {{.SourceCheck}}
    return rules
}

// Use adds rules from the same published schema role, as a value or nonnil
// pointer, before subsequent local settings. InferSchema contributes type-only
// documentation, not validation or missing child definitions. FromJSONSchema
// provides a separate decoded-JSON API, not a Schema value accepted here.
// Other schema roles are rejected even when their
// value type matches, because importing only their base could lose child rules.
// Schema-level rules accumulate. Overlapping child definitions are construction
// errors, and later local child callbacks cannot replace an imported child.
// Ptr permits an unconstrained scalar adapter to yield to a shared pointee.
// The source remains immutable. Typed nil pointers wrap ErrInvalidSchema.
//
// Example:
//
//  var configure validate.{{.Builder}}Func{{.ExampleUse}} = func(rules *validate.{{.Builder}}{{.ExampleUse}}) {
//      rules.Use({{.ExampleSchema}})
//  }
//  _ = configure
func (draft *{{.Builder}}{{.Use}}) Use(schema Schema[{{.T}}]) *{{.Builder}}{{.Use}} {
    source := schemaValue(schema)
    {{.UseChildren}}
    draft.base = draft.base.inherit(source)
    return draft
}
`

// comment prefixes every documentation line for insertion into generated Go
// source. Empty lines remain comment lines so examples stay in one doc block.
func comment(doc string) string {
	lines := strings.Split(doc, "\n")

	for i, l := range lines {
		lines[i] = "// " + l
	}

	return strings.Join(lines, "\n")
}

// generate renders construction drafts and forwarding methods in deterministic
// schema order. It returns formatted Go source or the first template or
// formatting error without writing files.
func generate() ([]byte, error) {
	tmpl, err := template.New("method").Parse(methodTmpl)

	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	buf.WriteString(header)

	for _, st := range schemas {
		if st.SealReturn {
			continue
		}

		if err := generateDraft(&buf, st); err != nil {
			return nil, err
		}
	}

	for _, st := range schemas {
		if err := generateMethods(&buf, tmpl, st); err != nil {
			return nil, err
		}
	}

	return format.Source(buf.Bytes())
}

// generateMethods emits the shared settings for one concrete draft, leaving
// type-specific rules in their domain source files.
func generateMethods(buf *bytes.Buffer, tmpl *template.Template, st schemaType) error {
	for _, method := range methods {
		if method.Skip != nil && method.Skip(st) {
			continue
		}

		if err := tmpl.Execute(buf, methodData(st, method)); err != nil {
			return err
		}
	}

	return nil
}

// methodData binds common rule templates and supplies a complete typed example.
func methodData(st schemaType, method method) map[string]string {
	role := strings.Replace(st.Name, "Schema", "Builder", 1)

	data := map[string]string{
		"Before": "", "Doc": comment(method.Doc + "\n\n" + methodExample(st, method)),
		"Builder": role, "Use": st.Use, "Name": method.Name,
		"Params": expand(method.Params, st.T),
		"Call":   strings.ReplaceAll(expand(method.Call, st.T), "s.", "draft."),
	}
	data["Params"] = strings.ReplaceAll(strings.ReplaceAll(data["Params"], "{{.Builder}}", role), "{{.Use}}", st.Use)

	if method.Name == "When" {
		data["Before"] = "observation := buildCondition(condition); branch := " + role + st.Use + "{}; for _, callback := range configure { callback(&branch) }"

		if st.Name == "PtrSchema" {
			data["Call"] = "draft.when(observation, PtrSchema[T]{\nbase: branch.detached(),\ninner: branch.inner,\nconstructed: true,\n})"
		}
	}

	return data
}

// exampleSchema supplies deterministic concrete arguments for every public
// generic role, including Text's marshaling-constrained value type.
func exampleSchema(st schemaType) schemaType {
	examples := map[string][3]string{
		"StringSchema": {"[string]", "string", `"Ada"`},
		"IntSchema":    {"[int]", "int", "3"},
		"UintSchema":   {"[uint]", "uint", "3"},
		"FloatSchema":  {"[float64]", "float64", "3.5"},
		"BoolSchema":   {"[bool]", "bool", "true"},
		"TimeSchema":   {"", "time.Time", "time.Date(2026,1,1,0,0,0,0,time.UTC)"},
		"TextSchema":   {"[validate.Duration]", "validate.Duration", "validate.Duration(time.Second)"},
		"AnySchema":    {"", "any", `"Ada"`},
		"FileSchema":   {"", "*multipart.FileHeader", "nil"},
		"SliceSchema":  {"[string]", "[]string", `[]string{"Ada"}`},
		"MapSchema":    {"[string, string]", "map[string]string", `map[string]string{"name":"Ada"}`},
		"PtrSchema":    {"[string]", "*string", "nil"},
		"StructSchema": {"[Value]", "Value", "Value{\nName: \"Ada\",\n}"},
	}

	values := examples[st.Name]
	st.ExampleUse, st.ExampleType, st.ExampleValue = values[0], values[1], values[2]

	return st
}

// methodExample places sibling-dependent rules inside a real enclosing struct
// so examples remain valid construction programs rather than pseudocode.
func methodExample(st schemaType, method method) string {
	st = exampleSchema(st)
	role := strings.Replace(st.Name, "Schema", "Builder", 1) + st.ExampleUse
	predicate := "func(predicate *validate.ConditionBuilder) {\n                predicate.Present(\"Enabled\")\n            }"

	arguments := map[string]string{
		"Default": st.ExampleValue, "Check": "func(_ context.Context, value " + st.ExampleType + ") error {\n                return nil\n            }",
		"Normalize":  "func(value " + st.ExampleType + ") " + st.ExampleType + " {\n                return value\n            }",
		"When":       predicate + ", func(selected *validate." + role + ") {\n                selected.Required()\n            }",
		"RequiredIf": predicate, "RequiredUnless": predicate, "ProhibitedIf": predicate, "ProhibitedUnless": predicate, "ExcludeIf": predicate,
		"RequiredWith": `"Peer"`, "RequiredWithAll": `"Peer"`, "RequiredWithout": `"Peer"`, "EqField": `"Peer"`, "NeField": `"Peer"`, "Describe": `"A display value"`,
	}

	setup := ""

	if st.Name == "PtrSchema" {
		setup = "            rules.Value(func(value *validate.StringBuilder[string]) {\n            })\n"
	}

	return "Example:\n\n    type Value struct {\n        Name string\n    }\n    type Form struct {\n        Value " + st.ExampleType + "\n        Peer " + st.ExampleType + "\n        Enabled bool\n    }\n    schema := validate.Struct(func(object *validate.StructBuilder[Form]) {\n        object.Field(\"Value\", func(rules *validate." + role + ") {\n" + setup + "            rules." + method.Name + "(" + arguments[method.Name] + ")\n        })\n    })\n    if _, err := schema.JSONSchema(); err != nil { panic(err) }"
}

// generateDraft emits one concrete role and its publication boundary.
func generateDraft(buf *bytes.Buffer, st schemaType) error {
	st = exampleSchema(st)
	role := strings.Replace(st.Name, "Schema", "Builder", 1)
	use := st.ExampleUse

	data := map[string]string{"Type": st.Name, "Builder": role, "Decl": st.Decl, "Use": st.Use, "T": st.T, "Fields": st.Fields, "Publish": st.Publish, "ExampleUse": use, "UseChildren": sharedChildren(st), "SourceCheck": ""}
	data["ExampleSchema"] = "validate." + strings.TrimSuffix(st.Name, "Schema") + use + "()"

	if st.Name == "PtrSchema" {
		data["ExampleSchema"] = "validate.Ptr(validate.String[string]())"
	}

	checks := map[string]string{"PtrSchema": "requireChild(draft.inner, reflect.TypeFor[T](), \"Ptr.Value\")"}
	data["Check"] = checks[st.Name]

	if st.Fields != "" {
		data["SourceCheck"] = "if !schema.constructed { rules.problems = append(slices.Clip(rules.problems), \"shared " + st.Name + " is uninitialized\") }"
	}

	tmpl, err := template.New("draft").Parse(draftTmpl)

	if err != nil {
		return err
	}

	return tmpl.Execute(buf, data)
}

// sharedChildren enforces role identity and additive child ownership during
// publication. Local child replacement is allowed only before shared imports.
func sharedChildren(st schemaType) string {
	definition := "reusedDefinition[" + st.T + ", " + st.Name + st.Use + "](source)"
	children := map[string]string{
		"SliceSchema": `if importChild(draft.elem,reused.elem,"Slice.Use") { draft.elem = reused.elem; draft.sharedElement = true }`,
		"MapSchema": `if importChild(draft.key,reused.key,"Map.Use keys") { draft.key = reused.key; draft.sharedKey = true }
if importChild(draft.val,reused.val,"Map.Use values") { draft.val = reused.val; draft.sharedValue = true }`,
		"PtrSchema": `if reused.inner != nil {
if draft.inner != nil && (draft.sharedInner || !emptyPointee[T](draft.inner)) { invalidf("Ptr.Use cannot replace an existing pointee definition") }
draft.inner = reused.inner; draft.sharedInner = true
}`,
	}

	child := children[st.Name]

	if child == "" {
		return "_ = " + definition
	}

	return "reused := " + definition + "; " + child
}

// expand replaces the validated-value placeholder in a template fragment.
// Other template placeholders remain available for the role-specific rendering
// step.
func expand(s, t string) string {
	return strings.ReplaceAll(s, "{{.T}}", t)
}

// main writes the formatted wrapper source in the current working directory.
// Generation or write errors are reported to stderr and terminate the command
// unsuccessfully.
func main() {
	out, err := generate()

	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}

	if err := os.WriteFile("wrappers.go", out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
