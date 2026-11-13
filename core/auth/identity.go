package auth

import (
	"maps"
	"slices"
	"time"
)

// Kind classifies what a [Factor] proves, following the usual three authentication
// categories plus an externally asserted one. The values are our own strings.
//
// [Identity.Level] counts distinct kinds: two different kinds among
// [KindKnowledge], [KindPossession] and [KindInherence] make a multi-factor
// identity. [KindFederated] never counts toward that, because it only
// reports that another provider authenticated the user.
//
// Example:
//
//	factor := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
type Kind string

const (
	// KindKnowledge is something the user knows, such as a password.
	KindKnowledge Kind = "knowledge"

	// KindPossession is something the user has, such as a hardware key.
	KindPossession Kind = "possession"

	// KindInherence is something the user is, such as a fingerprint.
	KindInherence Kind = "inherence"

	// KindFederated marks an authentication performed by an external identity
	// provider. It never contributes to [LevelMulti], however many federated
	// factors an identity has, because Arkoren cannot see which factors the
	// provider required.
	KindFederated Kind = "federated"
)

// Method names how a [Factor] was verified. The values "pwd", "otp", "hwk" and
// "swk" are the Authentication Method Reference values of RFC 8176;
// [MethodRecovery] and [MethodFederated] are Arkoren's own values, which RFC 8176
// does not define. Applications may use further strings.
//
// Example:
//
//	factor := auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession}
type Method string

const (
	// MethodPassword is a password check ("pwd", RFC 8176).
	MethodPassword Method = "pwd"

	// MethodOTP is a one-time password such as TOTP ("otp", RFC 8176).
	MethodOTP Method = "otp"

	// MethodHardwareKey is proof of possession of a hardware-secured key
	// ("hwk", RFC 8176).
	MethodHardwareKey Method = "hwk"

	// MethodSoftwareKey is proof of possession of a software-stored key
	// ("swk", RFC 8176).
	MethodSoftwareKey Method = "swk"

	// MethodRecovery is a single-use recovery code. It is an Arkoren value, not
	// an RFC 8176 one.
	MethodRecovery Method = "recovery"

	// MethodFederated is authentication by an external identity provider. It is
	// an Arkoren value, not an RFC 8176 one.
	MethodFederated Method = "federated"
)

// Factor records one verified proof of identity.
//
// Example:
//
//	factor := auth.Factor{
//		Method: auth.MethodPassword,
//		Kind:   auth.KindKnowledge,
//		At:     time.Now(),
//	}
type Factor struct {
	// Method is how the factor was verified.
	Method Method

	// Kind is what the factor proves. [Identity.Level] is derived from it.
	Kind Kind

	// At is when the factor was verified. The zero time means unknown.
	At time.Time
}

// Level summarizes how strongly an [Identity] was authenticated. It is derived
// from the identity's factors by [Identity.Level], never set directly.
//
// Example:
//
//	identity := auth.NewIdentity("42")
//	if identity.Level() < auth.LevelSingle {
//		return auth.ErrUnauthenticated
//	}
type Level int

const (
	// LevelNone means the identity has no factors.
	LevelNone Level = iota

	// LevelSingle means the identity has factors, but not two distinct
	// knowledge, possession or inherence kinds.
	LevelSingle

	// LevelMulti means the identity has factors of at least two distinct kinds
	// among [KindKnowledge], [KindPossession] and [KindInherence].
	LevelMulti
)

// Identity is who is acting: a subject, how it was authenticated and what it may
// do. It is an immutable value that carries no application user type, so any
// package can read it from a context with [FromContext]. Build one with
// [NewIdentity]. The zero Identity has an empty subject and no factors.
//
// Getters return copies of slices and maps, so callers cannot change an
// identity after it is built.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Factors(auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
//		principal.Scopes("posts:write")
//	})
//	fmt.Println(identity.Subject(), identity.Level() == auth.LevelSingle)
type Identity struct {
	// subject is the user's stable subject, see [ValidateSubject].
	subject string

	// factors are the verified proofs, copied on input and on output.
	factors []Factor

	// scopes are the granted scopes, copied on input and on output.
	scopes []string

	// actor is the subject acting on behalf of the identity, empty if none.
	actor string

	// record is what the user provider knows about the user.
	record UserRecord
}

// IdentityBuilder configures [NewIdentity] before publication. Callbacks run
// directly once, synchronously in order on one fresh draft; nil entries panic
// and expanded nil or empty slices supply no edits. Methods return the same
// draft, and retaining it cannot change a published identity.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Scopes("read").Actor("support-7")
//	})
type IdentityBuilder struct {
	// identity accumulates construction edits until publication.
	identity Identity
}

// IdentityBuilderFunc configures an [IdentityBuilder].
//
// Example:
//
//	var policy auth.IdentityBuilderFunc = func(principal *auth.IdentityBuilder) {
//		principal.Scopes("read")
//	}
//	identity := auth.NewIdentity("42", policy)
type IdentityBuilderFunc = func(*IdentityBuilder)

// Factors appends verified factors. The slice is copied, so later edits by the
// caller do not change the identity.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Factors(
//			auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge},
//			auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession},
//		)
//	})
//	fmt.Println(identity.Level() == auth.LevelMulti)
func (principal *IdentityBuilder) Factors(factors ...Factor) *IdentityBuilder {
	principal.identity.factors = append(principal.identity.factors, factors...)

	return principal
}

// Scopes appends granted scopes. The slice is copied, so later edits by the
// caller do not change the identity. Scopes are opaque strings; use
// gate.HasScopes to require them.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Scopes("posts:read", "posts:write")
//	})
//	fmt.Println(identity.Scopes())
func (principal *IdentityBuilder) Scopes(scopes ...string) *IdentityBuilder {
	principal.identity.scopes = append(principal.identity.scopes, scopes...)

	return principal
}

// Actor sets the subject acting on behalf of the identity, for example a
// support agent impersonating a user. The default, empty, means the identity
// acts for itself. The setting replaces the previous value on this draft.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Actor("support-7")
//	})
//	fmt.Println(identity.Actor())
func (principal *IdentityBuilder) Actor(actor string) *IdentityBuilder {
	principal.identity.actor = actor

	return principal
}

// Record attaches what the user provider knows about the user, including the
// application's own user in [UserRecord.Value], which [UserFromContext] and
// gate rules read. The claims map is copied. The setting replaces the previous
// value on this draft.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Record(auth.UserRecord{Subject: "42", Value: User{ID: 42}})
//	})
//	fmt.Println(identity.Record().Subject)
func (principal *IdentityBuilder) Record(record UserRecord) *IdentityBuilder {
	record.Claims = maps.Clone(record.Claims)
	principal.identity.record = record

	return principal
}

// NewIdentity publishes an immutable [Identity] for subject. Callbacks run once
// in order on one fresh draft; nil entries panic. NewIdentity does not validate
// subject: authenticators that take it from untrusted input should check it with
// [ValidateSubject] first.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Factors(auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
//	})
//	ctx := auth.NewContext(ctx, identity)
func NewIdentity(subject string, configure ...IdentityBuilderFunc) Identity {
	principal := &IdentityBuilder{}

	for _, configure := range configure {
		configure(principal)
	}

	identity := principal.identity
	identity.subject = subject
	identity.factors = slices.Clone(identity.factors)
	identity.scopes = slices.Clone(identity.scopes)

	return identity
}

// Subject returns the stable subject of the user.
//
// Example:
//
//	fmt.Println(auth.NewIdentity("42").Subject())
func (identity Identity) Subject() string {
	return identity.subject
}

// Factors returns a copy of the verified factors, in the order they were added.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Factors(auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
//	})
//	fmt.Println(len(identity.Factors()))
func (identity Identity) Factors() []Factor {
	return slices.Clone(identity.factors)
}

// Level derives the authentication level from the factors. No factors is
// [LevelNone]. Factors of at least two distinct kinds among [KindKnowledge],
// [KindPossession] and [KindInherence] are [LevelMulti]; two passwords are not.
// [KindFederated] and unknown kinds never count toward [LevelMulti], so any
// other non-empty set of factors is [LevelSingle].
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Factors(auth.Factor{Method: auth.MethodFederated, Kind: auth.KindFederated})
//	})
//	fmt.Println(identity.Level() == auth.LevelSingle)
func (identity Identity) Level() Level {
	if len(identity.factors) == 0 {
		return LevelNone
	}

	var kinds []Kind

	for _, factor := range identity.factors {
		switch factor.Kind {
		case KindKnowledge, KindPossession, KindInherence:
			if !slices.Contains(kinds, factor.Kind) {
				kinds = append(kinds, factor.Kind)
			}
		}
	}

	if len(kinds) >= 2 {
		return LevelMulti
	}

	return LevelSingle
}

// Scopes returns a copy of the granted scopes.
//
// Example:
//
//	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
//		principal.Scopes("read")
//	})
//	fmt.Println(identity.Scopes())
func (identity Identity) Scopes() []string {
	return slices.Clone(identity.scopes)
}

// Actor returns the subject acting on behalf of the identity, or "" when the
// identity acts for itself.
//
// Example:
//
//	fmt.Println(auth.NewIdentity("42").Actor() == "")
func (identity Identity) Actor() string {
	return identity.actor
}

// Record returns what the user provider knew about the user when the identity
// was built; the zero record if none was attached. The claims map is a copy.
//
// Example:
//
//	record := auth.NewIdentity("42").Record()
//	fmt.Println(record.Value == nil)
func (identity Identity) Record() UserRecord {
	record := identity.record
	record.Claims = maps.Clone(record.Claims)

	return record
}
