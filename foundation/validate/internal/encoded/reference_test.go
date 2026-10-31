package encoded_test

import (
	"errors"
	"testing"

	"arkoren.dev/foundation/validate"
)

func TestEncodedReferenceAndDependenciesContributeCoverage(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$defs":{"base":{"properties":{"a":true}}},"$ref":"#/$defs/base","dependentSchemas":{"a":{"properties":{"b":true}}},"unevaluatedProperties":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"a":null,"b":0}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"b":0}`)
}

func TestEncodedLocalPointerRefSupportsEscapedNames(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"definitions":{"a/b~c":{"const":9007199254740993}},"$ref":"#/definitions/a~1b~0c"}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`9007199254740993`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `9007199254740992`)
}

func TestEncodedLocalIDsAndAnchors(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$id":"https://example.invalid/root","$defs":{"child":{"$id":"child","$defs":{"value":{"$anchor":"value","type":"integer"}},"$ref":"#value"}},"$ref":"child"}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`0`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `null`)
}

func TestEncodedDraft07RefIgnoresAssertionSiblings(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$schema":"https://json-schema.org/draft-07/schema#","definitions":{"value":{"type":"integer"}},"$ref":"#/definitions/value","not":true}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`0`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedModernRefAppliesAssertionSiblings(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$defs":{"value":{"type":"integer"}},"$ref":"#/$defs/value","minimum":1}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `0`)
}

func TestEncodedDynamicRefUsesOutermostResource(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$id":"https://example.invalid/strict","$dynamicAnchor":"node","$ref":"tree","unevaluatedProperties":false,"$defs":{"tree":{"$id":"tree","$dynamicAnchor":"node","type":"object","properties":{"value":true,"children":{"type":"array","items":{"$dynamicRef":"#node"}}}}}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"value":1,"children":[{"value":2}]}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"value":1,"children":[{"extra":2}]}`)
}

func TestEncodedDynamicRefToPointerIsStatic(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$defs":{"n":{"type":"integer"}},"$dynamicRef":"#/$defs/n"}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `null`)
}

func TestEncodedExternalRefFailsCompilationWithoutLoading(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$ref":"file:///private/schema.json"}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedDuplicateAnchorsFailCompilation(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$defs":{"a":{"$anchor":"same"},"b":{"$anchor":"same"}}}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedIDFragmentFailsCompilation(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$id":"https://example.invalid/schema#anchor"}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedUnproductiveRecursiveRefIsAResourceFailure(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$ref":"#"}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`null`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("reference resource failure classified as mismatch")
	}
}

func TestEncodedAnyOfCannotSwallowReferenceResourceFailure(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$defs":{"cycle":{"$ref":"#/$defs/cycle"}},"anyOf":[true,{"$ref":"#/$defs/cycle"}]}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`null`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedContainsCannotSwallowReferenceFailure(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"contains":{"$ref":"#/$defs/cycle"},"minContains":0,"$defs":{"cycle":{"$ref":"#/$defs/cycle"}}}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`[null]`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedNotCannotInvertReferenceFailure(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"not":{"$ref":"#/$defs/cycle"},"$defs":{"cycle":{"$ref":"#/$defs/cycle"}}}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`null`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedIfCannotTreatReferenceFailureAsElse(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"if":{"$ref":"#/$defs/cycle"},"else":true,"$defs":{"cycle":{"$ref":"#/$defs/cycle"}}}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`null`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

// TestEncodedDynamicRefToOrdinaryAnchorRemainsStatic ensures an outer dynamic
// anchor cannot replace a target that was declared only with ordinary $anchor.
// Dynamic rebinding requires a dynamic anchor on the statically resolved target.
func TestEncodedDynamicRefToOrdinaryAnchorRemainsStatic(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$id":"https://example.invalid/root","$dynamicAnchor":"value","type":"integer","$dynamicRef":"child#value","$defs":{"child":{"$id":"child","$anchor":"value","minimum":2}}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`2`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `1`)
}

// TestEncodedDynamicScopeDoesNotLeakBetweenBranches exercises independent
// resource stacks for composition siblings. An anchor visited in the first
// branch cannot override a later branch's self-contained dynamic reference.
func TestEncodedDynamicScopeDoesNotLeakBetweenBranches(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"allOf":[{"$ref":"first"},{"$ref":"second"}],"$defs":{"first":{"$id":"first","$dynamicAnchor":"value","minimum":1},"second":{"$id":"second","$dynamicAnchor":"value","maximum":2,"properties":{"n":{"$dynamicRef":"#value"}}}}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"n":0}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"n":3}`)
}

// TestEncodedPointerAliasesAcrossIDBoundaries retains both the parent pointer
// and embedded resource name for a schema; referencing through either address
// must preserve exact assertions without requiring any external resource loader.
func TestEncodedPointerAliasesAcrossIDBoundaries(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$id":"https://example.invalid/root","$defs":{"child":{"$id":"child","$defs":{"n":{"const":9007199254740993}}}},"allOf":[{"$ref":"#/$defs/child/$defs/n"},{"$ref":"child#/$defs/n"}]}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`9007199254740993`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `9007199254740992`)
}

// TestEncodedAnchorRejectsColon enforces the supported 2020-12 plain-name
// grammar instead of accepting the broader URI-fragment character alphabet.
// A colon is legal in a URI but not in a modern $anchor or $dynamicAnchor.
func TestEncodedAnchorRejectsColon(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$anchor":"value:name"}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}
