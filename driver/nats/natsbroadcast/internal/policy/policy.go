// Package policy builds native NATS authentication and connection options in
// their original precedence order. Credential files remain deferred SDK inputs.
package policy

import (
	"cmp"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// Config contains only native connection policy, not broadcast routing.
type Config struct {
	// Name is the native client name.
	Name string
	// NoReconnect disables automatic reconnection.
	NoReconnect bool
	// MaxReconnects overrides the supplied default when nonzero.
	MaxReconnects int
	// ReconnectWait overrides the supplied default when nonzero.
	ReconnectWait time.Duration
	// Timeout is the optional native connection timeout.
	Timeout time.Duration
	// Username and Password are a complete native user credential pair.
	Username, Password string
	// Token is applied after user credentials.
	Token string
	// NKeySeed contains user seed material, never a path.
	NKeySeed string
	// CredentialsFile is passed unchanged to the SDK after NKey policy.
	CredentialsFile string
	// TLS is the shared native TLS configuration.
	TLS *tls.Config
	// RootCAs are applied after the shared TLS configuration.
	RootCAs []string
}

// Options preserves native option ordering, including authentication overrides.
func Options(config Config, reconnects int, wait time.Duration) ([]nats.Option, error) {
	var opts []nats.Option

	if config.Name != "" {
		opts = append(opts, nats.Name(config.Name))
	}

	if config.NoReconnect {
		opts = append(opts, nats.NoReconnect())
	} else {
		opts = append(opts, nats.MaxReconnects(cmp.Or(config.MaxReconnects, reconnects)))

		if config.ReconnectWait != 0 {
			wait = config.ReconnectWait
		}

		opts = append(opts, nats.ReconnectWait(wait))
	}

	if config.Timeout != 0 {
		opts = append(opts, nats.Timeout(config.Timeout))
	}

	if config.Username != "" && config.Password != "" {
		opts = append(opts, nats.UserInfo(config.Username, config.Password))
	}

	if config.Token != "" {
		opts = append(opts, nats.Token(config.Token))
	}

	option, err := nkey(config.NKeySeed)

	if err != nil {
		return nil, err
	}

	if option != nil {
		opts = append(opts, option)
	}

	if config.CredentialsFile != "" {
		opts = append(opts, nats.UserCredentials(config.CredentialsFile))
	}

	if config.TLS != nil {
		opts = append(opts, nats.Secure(config.TLS))
	}

	if len(config.RootCAs) != 0 {
		opts = append(opts, nats.RootCAs(config.RootCAs...))
	}

	return opts, nil
}

// nkey validates seed material without filesystem I/O and wipes each temporary
// private key. The immutable seed is parsed again for each native signing nonce.
func nkey(seed string) (nats.Option, error) {
	if seed == "" {
		return nil, nil
	}

	pair, err := nkeys.FromSeed([]byte(seed))

	if err != nil {
		return nil, fmt.Errorf("invalid NKeySeed: %w", err)
	}

	defer pair.Wipe()
	public, err := pair.PublicKey()

	if err != nil {
		return nil, fmt.Errorf("NKeySeed public key: %w", err)
	}

	if !nkeys.IsValidPublicUserKey(public) {
		return nil, errors.New("NKeySeed must contain a user seed")
	}

	return nats.Nkey(public, func(nonce []byte) ([]byte, error) {
		pair, err := nkeys.FromSeed([]byte(seed))

		if err != nil {
			return nil, fmt.Errorf("NKeySeed signing key: %w", err)
		}

		defer pair.Wipe()

		return pair.Sign(nonce)
	}), nil
}
