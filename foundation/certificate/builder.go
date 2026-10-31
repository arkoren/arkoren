package certificate

import "time"

// RootBuilder configures a single GenerateRoot call. Callbacks receive one
// fresh draft, run once in order, and may retain it without affecting the
// published identity. Configuration errors are sticky; later edits cannot
// repair an invalid duration. The draft itself is not concurrency-safe.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
//		authority.Validity(365 * 24 * time.Hour)
//	})
//	if err != nil {
//		return err
//	}
//	_ = root.Bundle()
type RootBuilder struct {
	// validity is the requested lifetime from generation time; zero uses default.
	validity time.Duration

	// err preserves the first invalid setting until construction completes.
	err error
}

// RootBuilderFunc configures GenerateRoot synchronously. Nil entries panic;
// an expanded nil or empty callback slice supplies no edits. Application panics
// propagate, and callbacks run even when required inputs are invalid.
//
// Example:
//
//	policy := certificate.RootBuilderFunc(func(authority *certificate.RootBuilder) {
//		authority.Validity(365 * 24 * time.Hour)
//	})
//	root, err := certificate.GenerateRoot("Example development", policy)
//	if err != nil {
//		return err
//	}
//	_ = root
type RootBuilderFunc = func(*RootBuilder)

// Validity sets the root lifetime measured from generation time, not from its
// backdated NotBefore. Zero selects 5*365 days; a positive duration must be at
// least one second because X.509 encodes whole seconds. Negative or subsecond
// durations make generation fail with ErrInvalid. It returns the same draft.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
//		authority.Validity(365 * 24 * time.Hour)
//	})
//	if err != nil {
//		return err
//	}
//	_ = root
func (authority *RootBuilder) Validity(lifetime time.Duration) *RootBuilder {
	if lifetime < 0 || lifetime > 0 && lifetime < time.Second {
		authority.err = ErrInvalid
	}

	authority.validity = lifetime

	return authority
}

// ServerBuilder configures a single GenerateServer call. Callbacks share one
// fresh draft and run once in order. Retained drafts cannot change published
// identities. Invalid duration errors are sticky; drafts are not concurrency-safe.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	leaf, err := certificate.GenerateServer(root, []string{"localhost"}, func(leaf *certificate.ServerBuilder) {
//		leaf.Validity(24 * time.Hour)
//	})
//	if err != nil {
//		return err
//	}
//	_ = leaf
type ServerBuilder struct {
	// validity is zero for the clamped default, otherwise an explicit lifetime.
	validity time.Duration

	// err preserves invalid settings independently of subsequent edits.
	err error
}

// ServerBuilderFunc configures GenerateServer synchronously. Nil entries panic,
// while expanded nil or empty slices omit edits. Callbacks run even for invalid
// issuers or hosts; application panics propagate directly.
//
// Example:
//
//	policy := certificate.ServerBuilderFunc(func(leaf *certificate.ServerBuilder) {
//		leaf.Validity(24 * time.Hour)
//	})
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	leaf, err := certificate.GenerateServer(root, []string{"localhost"}, policy)
//	if err != nil {
//		return err
//	}
//	_ = leaf
type ServerBuilderFunc = func(*ServerBuilder)

// Validity sets the server lifetime from generation time. Zero selects 90 days,
// clamped to the issuer's remaining lifetime. An explicit positive lifetime must
// be at least one second and must not exceed the issuer's expiry. Negative and
// subsecond durations fail with ErrInvalid. It returns the same draft.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	leaf, err := certificate.GenerateServer(root, []string{"localhost"}, func(leaf *certificate.ServerBuilder) {
//		leaf.Validity(24 * time.Hour)
//	})
//	if err != nil {
//		return err
//	}
//	_ = leaf
func (leaf *ServerBuilder) Validity(lifetime time.Duration) *ServerBuilder {
	if lifetime < 0 || lifetime > 0 && lifetime < time.Second {
		leaf.err = ErrInvalid
	}

	leaf.validity = lifetime

	return leaf
}
