package auth_test

import (
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/auth"
)

// password, otp, hardware and federated are factors reused by the level tests.
var (
	password  = auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
	otp       = auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession}
	hardware  = auth.Factor{Method: auth.MethodHardwareKey, Kind: auth.KindPossession}
	biometric = auth.Factor{Method: "fpt", Kind: auth.KindInherence}
	federated = auth.Factor{Method: auth.MethodFederated, Kind: auth.KindFederated}
)

// levelOf builds an identity with factors and returns its level.
func levelOf(factors ...auth.Factor) auth.Level {
	return auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Factors(factors...)
	}).Level()
}

// TestLevelNoFactorsIsNone verifies an identity without factors is LevelNone.
func TestLevelNoFactorsIsNone(t *testing.T) {
	t.Parallel()

	if got := levelOf(); got != auth.LevelNone {
		t.Fatalf("level = %v, want LevelNone", got)
	}
}

// TestLevelOneKindIsSingle verifies that any number of factors of one kind is
// single-factor, so two passwords are not multi-factor.
func TestLevelOneKindIsSingle(t *testing.T) {
	t.Parallel()

	for _, factors := range [][]auth.Factor{{password}, {password, password}, {otp, hardware}} {
		if got := levelOf(factors...); got != auth.LevelSingle {
			t.Fatalf("level of %v = %v, want LevelSingle", factors, got)
		}
	}
}

// TestLevelTwoDistinctKindsIsMulti verifies each pair of the three kinds.
func TestLevelTwoDistinctKindsIsMulti(t *testing.T) {
	t.Parallel()

	for _, factors := range [][]auth.Factor{{password, otp}, {password, biometric}, {hardware, biometric}} {
		if got := levelOf(factors...); got != auth.LevelMulti {
			t.Fatalf("level of %v = %v, want LevelMulti", factors, got)
		}
	}
}

// TestLevelFederatedNeverCountsTowardMulti verifies federated factors, alone or
// next to one other kind, stay single.
func TestLevelFederatedNeverCountsTowardMulti(t *testing.T) {
	t.Parallel()

	for _, factors := range [][]auth.Factor{{federated}, {federated, federated}, {federated, password}} {
		if got := levelOf(factors...); got != auth.LevelSingle {
			t.Fatalf("level of %v = %v, want LevelSingle", factors, got)
		}
	}
}

// TestIdentityGettersReturnCopies verifies callers cannot mutate an identity
// through returned slices, claims, or the slices they passed to the builder.
func TestIdentityGettersReturnCopies(t *testing.T) {
	t.Parallel()

	factors := []auth.Factor{password}
	scopes := []string{"read"}
	claims := map[string]any{"role": "admin"}
	identity := auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Factors(factors...).Scopes(scopes...).Record(auth.UserRecord{Claims: claims})
	})

	factors[0] = otp
	scopes[0] = "admin"
	claims["role"] = "guest"
	identity.Factors()[0] = otp
	identity.Scopes()[0] = "admin"
	identity.Record().Claims["role"] = "guest"

	if identity.Factors()[0] != password || identity.Scopes()[0] != "read" || identity.Record().Claims["role"] != "admin" {
		t.Fatalf("identity changed: %v %v %v", identity.Factors(), identity.Scopes(), identity.Record().Claims)
	}
}

// TestIdentityRetainedBuilderCannotChangePublishedIdentity verifies the draft
// is detached at publication.
func TestIdentityRetainedBuilderCannotChangePublishedIdentity(t *testing.T) {
	t.Parallel()

	var retained *auth.IdentityBuilder
	identity := auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		retained = principal
		principal.Scopes("read")
	})

	retained.Scopes("admin").Actor("someone")

	if len(identity.Scopes()) != 1 || identity.Actor() != "" {
		t.Fatalf("scopes = %v, actor = %q", identity.Scopes(), identity.Actor())
	}
}

// TestIdentityBuilderSettings verifies every builder setting reaches its getter,
// in order, and that the zero identity is empty.
func TestIdentityBuilderSettings(t *testing.T) {
	t.Parallel()

	at := time.Unix(1700000000, 0)
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Factors(auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge, At: at})
	}, func(principal *auth.IdentityBuilder) {
		principal.Factors(otp).Scopes("a").Scopes("b").Actor("support").Record(auth.UserRecord{Subject: "42", Version: "v1", Value: 7})
	})

	if identity.Subject() != "42" || identity.Actor() != "support" || len(identity.Factors()) != 2 ||
		!identity.Factors()[0].At.Equal(at) || identity.Scopes()[1] != "b" ||
		identity.Record().Version != "v1" || identity.Record().Value != 7 {
		t.Fatalf("identity = %+v", identity)
	}

	if zero := (auth.Identity{}); zero.Subject() != "" || zero.Level() != auth.LevelNone || zero.Factors() != nil {
		t.Fatalf("zero identity = %+v", zero)
	}
}

func ExampleNewIdentity() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Factors(password)
	})

	fmt.Println(identity.Subject(), identity.Level() == auth.LevelSingle)
	// Output: 42 true
}

func ExampleIdentity() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Factors(password, otp).Scopes("posts:write")
	})

	fmt.Println(identity.Subject(), identity.Level() == auth.LevelMulti, identity.Scopes())
	// Output: 42 true [posts:write]
}

func ExampleIdentity_Subject() {
	fmt.Println(auth.NewIdentity("42").Subject())
	// Output: 42
}

func ExampleIdentity_Factors() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Factors(password)
	})

	fmt.Println(identity.Factors()[0].Method)
	// Output: pwd
}

func ExampleIdentity_Level() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Factors(password, password)
	})

	fmt.Println(identity.Level() == auth.LevelSingle)
	// Output: true
}

func ExampleIdentity_Scopes() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Scopes("read")
	})

	fmt.Println(identity.Scopes())
	// Output: [read]
}

func ExampleIdentity_Actor() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Actor("support-7")
	})

	fmt.Println(identity.Actor())
	// Output: support-7
}

func ExampleIdentity_Record() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Subject: "42", Version: "v1"})
	})

	fmt.Println(identity.Record().Version)
	// Output: v1
}

func ExampleIdentityBuilder() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Scopes("read").Actor("support-7")
	})

	fmt.Println(identity.Scopes(), identity.Actor())
	// Output: [read] support-7
}

func ExampleIdentityBuilderFunc() {
	policy := auth.IdentityBuilderFunc(func(principal *auth.IdentityBuilder) {
		principal.Scopes("read")
	})

	fmt.Println(auth.NewIdentity("42", policy).Scopes())
	// Output: [read]
}

func ExampleIdentityBuilder_Factors() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Factors(password, otp)
	})

	fmt.Println(identity.Level() == auth.LevelMulti)
	// Output: true
}

func ExampleIdentityBuilder_Scopes() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Scopes("posts:read", "posts:write")
	})

	fmt.Println(identity.Scopes())
	// Output: [posts:read posts:write]
}

func ExampleIdentityBuilder_Actor() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Actor("support-7")
	})

	fmt.Println(identity.Actor())
	// Output: support-7
}

func ExampleIdentityBuilder_Record() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Subject: "42", Value: "ann"})
	})

	fmt.Println(identity.Record().Value)
	// Output: ann
}

func ExampleFactor() {
	factor := auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession, At: time.Unix(0, 0)}

	fmt.Println(factor.Method, factor.Kind)
	// Output: otp possession
}

func ExampleKind() {
	fmt.Println(auth.KindKnowledge, auth.KindPossession, auth.KindInherence, auth.KindFederated)
	// Output: knowledge possession inherence federated
}

func ExampleMethod() {
	fmt.Println(auth.MethodPassword, auth.MethodOTP, auth.MethodHardwareKey, auth.MethodSoftwareKey, auth.MethodRecovery, auth.MethodFederated)
	// Output: pwd otp hwk swk recovery federated
}

func ExampleLevel() {
	fmt.Println(levelOf() == auth.LevelNone, levelOf(password) == auth.LevelSingle, levelOf(password, otp) == auth.LevelMulti)
	// Output: true true true
}
