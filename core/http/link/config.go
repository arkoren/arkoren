package link

import (
	"slices"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/foundation/key"
)

// BuilderFunc edits one signer draft synchronously. Callbacks run directly once
// in order, including invalid required keys; nil entries panic. Expanded nil or
// empty slices omit edits. The completed signer detaches owned settings.
//
// Example:
//
//	var policy link.BuilderFunc = func(signer *link.Builder) {
//		signer.Origin("https://app.example.com")
//	}
//	signer, err := link.New(secret, policy)
//	if err != nil {
//		return err
//	}
//	_ = signer
type BuilderFunc = func(*Builder)

// Config is a printable signer-settings record retained for redacted logging.
// It is not a constructor input; configure [New] with [Builder] callbacks.
// Formatting omits service pointers and replaces previous secrets with a marker.
//
// Example:
//
//	record := link.Config{Origin: "https://app.example.com"}
//	slog.Info("link settings", "settings", record)
type Config struct {
	// Origin is the public HTTP or HTTPS origin of absolute links. The
	// default, empty, creates a relative signer that signs path-and-query values only.
	//
	// Example:
	//	record := link.Config{Origin: "https://app.example.com"}
	//	logger.InfoContext(ctx, "link settings", "settings", record)
	Origin string

	// PreviousSecrets still verify links during secret rotation. Each must be
	// exactly [MinSecretLength] bytes. The default, none, accepts only
	// links signed with the current secret.
	//
	// Example:
	//	record := link.Config{PreviousSecrets: []key.Shared{previous}}
	//	slog.Info("link settings", "settings", record)
	PreviousSecrets []key.Shared

	// IgnoreQuery names query parameters excluded from the signature. They may
	// be added, changed, or removed without invalidating a link. The names
	// "signature" and "expires" are reserved and cannot be listed. The default,
	// none, signs every query parameter.
	// This reporting record does not copy caller-owned slices or validate them;
	// use Builder.IgnoreQuery for detached construction policy.
	//
	// Example:
	//	record := link.Config{IgnoreQuery: []string{"utm_source"}}
	//	logger.InfoContext(ctx, "link settings", "settings", record)
	IgnoreQuery []string

	// Events receives [Rejected] from [Middleware] and [Consumed] from the
	// Consume methods. The default, nil, publishes nothing.
	//
	// Example:
	//	record := link.Config{Events: events}
	//	_ = record.Events // the live bus is not printed by Config formatting
	Events *event.Bus

	// Cache records consumed single-use links and lets [Middleware] reject
	// them. It must be shared by every server that verifies the same links.
	// The default, nil, disables [Signer.Consume], which then returns
	// [ErrNoCache].
	//
	// Example:
	//	record := link.Config{Cache: store}
	//	_ = record.Cache // the live service is omitted from formatted diagnostics
	Cache *cache.Cache
}

// settings holds the private completed signer settings.
type settings Config

// cloneSecrets copies the list of immutable key values. The publication copy
// prevents a retained construction draft from replacing previous keys.
func cloneSecrets(secrets []key.Shared) []key.Shared {
	return slices.Clone(secrets)
}

// Builder configures New before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	signer, err := link.New(secret, func(signer *link.Builder) {
//		signer.Origin("https://app.example.com")
//	})
//	if err != nil {
//		return err
//	}
//	_ = signer
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	signer := &Builder{}

	for _, configure := range configure {
		configure(signer)
	}

	config := signer.settings

	config.PreviousSecrets = cloneSecrets(config.PreviousSecrets)
	config.IgnoreQuery = slices.Clone(config.IgnoreQuery)

	return config
}

// Origin is the public HTTP or HTTPS origin of absolute links. The
// default, empty, creates a relative signer that signs path-and-query values only.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	signer, err := link.New(secret, func(signer *link.Builder) {
//		signer.Origin("https://app.example.com")
//	})
//	if err != nil {
//		return err
//	}
//	_ = signer
func (signer *Builder) Origin(origin string) *Builder {
	signer.settings.Origin = origin

	return signer
}

// Previous sets the shared keys that still verify links during rotation.
// Each must contain exactly [MinSecretLength] bytes. The default, none, accepts only
// links signed with the current secret.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	signer, err := link.New(secret, func(signer *link.Builder) {
//		signer.Previous(previousSecret)
//	})
//	if err != nil {
//		return err
//	}
//	_ = signer
func (signer *Builder) Previous(secrets ...key.Shared) *Builder {
	signer.settings.PreviousSecrets = cloneSecrets(secrets)

	return signer
}

// IgnoreQuery names query parameters excluded from the signature. They may
// be added, changed, or removed without invalidating a link. The names
// "signature" and "expires" are reserved and cannot be listed. The default,
// none, signs every query parameter.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	signer, err := link.New(secret, func(signer *link.Builder) {
//		signer.IgnoreQuery("utm_source")
//	})
//	if err != nil {
//		return err
//	}
//	_ = signer
func (signer *Builder) IgnoreQuery(names ...string) *Builder {
	signer.settings.IgnoreQuery = slices.Clone(names)

	return signer
}

// Events receives [Rejected] from [Middleware] and [Consumed] from the
// Consume methods. The default, nil, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	signer, err := link.New(secret, func(signer *link.Builder) {
//		signer.Events(event.New())
//	})
//	if err != nil {
//		return err
//	}
//	_ = signer
func (signer *Builder) Events(bus *event.Bus) *Builder {
	signer.settings.Events = bus

	return signer
}

// Cache records consumed single-use links and lets [Middleware] reject
// them. It must be shared by every server that verifies the same links.
// The default, nil, disables [Signer.Consume], which then returns
// [ErrNoCache].
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	signer, err := link.New(secret, func(signer *link.Builder) {
//		signer.Cache(cache.New(memorycache.New()))
//	})
//	if err != nil {
//		return err
//	}
//	_ = signer
func (signer *Builder) Cache(store *cache.Cache) *Builder {
	signer.settings.Cache = store

	return signer
}
