package session

import (
	"cmp"
	"encoding/json/jsontext"
	"time"

	"arkoren.dev/core/service/cache"
)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	session := &Builder{
		settings: settings{
			Namespace: cache.NewNamespace[jsontext.Value]("arkoren:session", func(space *cache.NamespaceBuilder) {
				space.Separator(":")
			}),
			Path: "/", SameSite: "lax", TTL: DefaultTTL,
			MaxLifetime: DefaultMaxLifetime, ExpirationDelta: DefaultExpirationDelta,
			PersistenceTimeout: defaultPersistenceTimeout,
		},
	}

	for _, configure := range configure {
		configure(session)
	}

	config := session.settings

	return config.withDefaults()
}

// withDefaults returns a copy of the config with secure defaults applied to
// any unset fields.
func (config settings) withDefaults() settings {
	config.Path = cmp.Or(config.Path, "/")

	if config.Name == "" {
		config.Name = DefaultCookie

		if !config.AllowInsecure && config.Domain == "" && config.Path == "/" {
			config.Name = HostPrefix + DefaultCookie
		}
	}

	config.SameSite = cmp.Or(config.SameSite, "lax")
	config.TTL = cmp.Or(config.TTL, DefaultTTL)
	config.ExpirationDelta = cmp.Or(config.ExpirationDelta, DefaultExpirationDelta)
	config.MaxLifetime = cmp.Or(config.MaxLifetime, DefaultMaxLifetime)
	config.PersistenceTimeout = cmp.Or(config.PersistenceTimeout, defaultPersistenceTimeout)

	return config
}

// tombstoneTTL preserves bounded retirement-marker retention: the absolute
// lifetime, or idle TTL when the maximum is disabled. Missing original records
// also reject guarded saves, so retention need not outlive every pending writer.
func tombstoneTTL(config settings) time.Duration {
	if config.MaxLifetime > 0 {
		return config.MaxLifetime
	}

	return config.TTL
}
