package webpushpush

import (
	"cmp"
	"encoding/base64"
	"errors"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"arkoren.dev/driver/webpush/webpushpush/internal/network"
	"arkoren.dev/foundation/key"
)

// defaultTimeout keeps all network operations finite when Timeout is omitted
// or explicitly reset to zero. Shorter caller deadlines take precedence.
const defaultTimeout = 10 * time.Second

// Builder holds construction-only settings. New detaches its owned slices,
// so retaining a draft cannot change a published driver.
//
// Example:
//
//	_, err := webpushpush.New(identity, "mailto:push@example.com", func(connection *webpushpush.Builder) {
//		connection.Timeout(5 * time.Second)
//	})
//	_ = err
type Builder struct {
	// timeout bounds a send; zero selects the finite default.
	timeout time.Duration
	// origins replaces curated defaults when non-nil, including an empty list.
	origins []string
}

// BuilderFunc edits one fresh construction draft, synchronously and in order.
// Nil callbacks panic; empty callback slices leave defaults unchanged.
//
// Example:
//
//	var policy webpushpush.BuilderFunc = func(connection *webpushpush.Builder) {
//		connection.Timeout(time.Second)
//	}
//	_, err := webpushpush.New(identity, "mailto:push@example.com", policy)
//	_ = err
type BuilderFunc = func(*Builder)

// Timeout bounds the complete send. Zero restores ten seconds; negative
// values fail New. A shorter caller context deadline always wins.
//
// Example:
//
//	_, err := webpushpush.New(identity, "mailto:push@example.com", func(connection *webpushpush.Builder) {
//		connection.Timeout(3 * time.Second)
//	})
//	_ = err
func (connection *Builder) Timeout(timeout time.Duration) *Builder {
	connection.timeout = timeout

	return connection
}

// AllowedOrigins replaces the curated provider policy with exact HTTPS
// origins, without paths, queries or wildcards. Empty input denies all.
// This is trusted deployment configuration, never subscription input.
// Addresses resolved at connection time must still be public.
//
// Example:
//
//	_, err := webpushpush.New(identity, "mailto:push@example.com", func(connection *webpushpush.Builder) {
//		connection.AllowedOrigins("https://fcm.googleapis.com")
//	})
//	_ = err
func (connection *Builder) AllowedOrigins(origins ...string) *Builder {
	connection.origins = append([]string{}, origins...)

	return connection
}

// New validates configuration and snapshots key material without network I/O.
// identity must be a validated key.Signing value; contact must be
// a mailto address or an HTTPS URI. Invalid settings return a redacted error.
// Callbacks run once in order even for invalid required inputs. The returned
// driver is safe for concurrent use and requires no shutdown operation.
//
// Example:
//
//	identity, err := key.LoadSigning("vapid-private.pem")
//	if err != nil {
//		return err
//	}
//	driver, err := webpushpush.New(identity, "mailto:push@example.com")
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(identity key.Signing, contact string, configure ...BuilderFunc) (*Driver, error) {
	connection := &Builder{}

	for _, configure := range configure {
		configure(connection)
	}

	if err := identity.Err(); err != nil {
		return nil, err
	}

	// VAPID requires a fixed-width 32-byte scalar, including leading zeros.
	raw, _ := identity.PrivateKey().Bytes()
	private := base64.RawURLEncoding.EncodeToString(raw)
	public := identity.VAPIDPublicKey()

	contact, err := normalizeContact(contact)

	if err != nil {
		return nil, err
	}

	if connection.timeout < 0 {
		return nil, errors.New("web push timeout must not be negative")
	}

	if err := network.ValidateOrigins(connection.origins); err != nil {
		return nil, err
	}

	timeout := cmp.Or(connection.timeout, defaultTimeout)

	return &Driver{
		private: private, public: public, contact: contact,
		origins: slices.Clone(connection.origins), timeout: timeout,
		client: newClient(timeout), authorizations: make(map[string]authorization),
	}, nil
}

// normalizeContact validates a VAPID subject and returns the native library's
// input form: a bare mailbox or an HTTPS URI with a lowercase scheme. The
// library adds mailto: unless it recognizes the exact https: prefix. Validation
// errors never echo personal contact details; mailto accepts one bare mailbox,
// not a display name.
func normalizeContact(contact string) (string, error) {
	if strings.HasPrefix(contact, "mailto:") {
		address := strings.TrimPrefix(contact, "mailto:")
		parsed, err := mail.ParseAddress(address)

		if err != nil || parsed.Address != address {
			return "", errors.New("web push contact must contain one mailto address")
		}

		return address, nil
	}

	parsed, err := url.Parse(contact)

	if err != nil {
		return "", errors.New("web push contact URI is invalid")
	}

	if parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", errors.New("web push contact must use mailto or https")
	}

	if parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("web push contact must not contain credentials or a fragment")
	}

	return parsed.String(), nil
}
