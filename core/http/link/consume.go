package link

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"arkoren.dev/core/service/cache"
)

// consumedKeyPrefix namespaces and versions the cache keys that record
// consumed links.
const consumedKeyPrefix = "link:v1:consumed:"

// consumeMargin extends a consumed link's cache entry past its expiry. The
// link stays rejected by expiry alone after that, so the margin only has to
// cover clock skew between servers: a server whose clock runs behind still
// sees the entry while it accepts the link.
const consumeMargin = time.Minute

// ErrConsumed reports an authentic temporary link that was already consumed.
// Return [ErrLinkUsed] to the client.
// Expired or unauthenticated links fail before the consumed-marker lookup.
//
// Example:
//
//	if err := signer.Consume(ctx, rawURL); errors.Is(err, link.ErrConsumed) {
//		return link.ErrLinkUsed
//	}
var ErrConsumed = errors.New("link was already used")

// ErrNoCache reports that [Signer.Consume] was called on a signer created
// without [Builder.Cache].
// A malformed raw URL is rejected before this configuration check.
//
// Example:
//
//	if err := signer.Consume(ctx, rawURL); errors.Is(err, link.ErrNoCache) {
//		return err // configure a shared cache before accepting one-time actions
//	}
var ErrNoCache = errors.New("link signer has no cache")

// ErrNotTemporary reports that [Signer.Consume] was called on a permanent
// link. A permanent link never expires, so its consumed record would have to
// be kept forever.
// Use Temporary rather than Sign when the action must have a single use.
//
// Example:
//
//	if err := signer.Consume(ctx, rawURL); errors.Is(err, link.ErrNotTemporary) {
//		return err
//	}
var ErrNotTemporary = errors.New("link is not temporary")

// Consume verifies rawURL and spends it, so no later Consume of the same link
// succeeds. Use it for single-use links such as a password reset: show the
// form for any valid link, then call Consume in the handler that applies the
// change.
//
// Consume verifies the signature, the binding ([Bind]), and the expiry exactly
// like [Signer.Verify], then records the link in the signer's cache with an
// atomic add, so concurrent calls on one link have exactly one winner, even
// across servers sharing a cache driver. It returns:
//
//   - [ErrInvalidURL] when rawURL cannot be parsed, before checking the cache;
//   - [ErrNoCache] for a parsed URL when the signer has no cache, before verifying;
//   - [ErrInvalidURL], [ErrInvalidSignature], or an [*ExpiredError] on verification;
//   - [ErrNotTemporary] for a permanent link;
//   - [ErrConsumed] when the link was already spent;
//   - a wrapped cache error when consumption could not be confirmed. The marker
//     may already have been committed, so the caller must not proceed or assume
//     a retry can succeed. Offer a new link when recovery cannot establish use.
//
// With [Builder.Events], a successful call publishes [Consumed]; failures and
// repeated calls publish nothing.
//
// Consume before committing the side effect. That gives at-most-once
// behavior: if the side effect fails afterwards the link stays spent and the
// user must request a new one. Consuming after the side effect would allow a
// crash or a concurrent request to apply it twice.
//
// The cache entry lives until the link's expiry plus one minute for clock
// skew between servers, so the cache holds one small key per consumed link
// for the link's remaining lifetime. A cache that loses entries early, such
// as an evicting or restarted in-memory cache, lets a spent link work again.
//
// Example:
//
//	err := signer.Consume(ctx, rawURL, link.Bind(user.PasswordHash))
//	if errors.Is(err, link.ErrConsumed) {
//		return link.ErrLinkUsed.Derive(func(details *problem.Builder) {
//			details.Error(err)
//		})
//	}
//	if err != nil {
//		return err
//	}
func (signer *Signer) Consume(ctx context.Context, rawURL string, opts ...CallOption) error {
	value, err := url.Parse(rawURL)

	if err != nil {
		return fmt.Errorf("parse link URL: %w", ErrInvalidURL)
	}

	return signer.ConsumeURL(ctx, value, opts...)
}

// ConsumeRequest consumes the link a request was made to, using the same URL
// reconstruction as [Middleware] (the request path and query, completed with
// the signer's origin), and the request's context. Handlers use it so they do
// not have to rebuild the URL. See [Signer.Consume].
//
// Example:
//
//	if err := signer.ConsumeRequest(r, link.Bind(user.PasswordHash)); err != nil {
//		return err
//	}
func (signer *Signer) ConsumeRequest(request *http.Request, opts ...CallOption) error {
	return signer.consume(request.Context(), request.Method, signer.requestURL(request), opts...)
}

// ConsumeURL consumes value without mutating it. value is a parsed signed URL,
// typically returned by TemporaryURL. It checks cache configuration before URL
// validation; nil value with a configured cache returns ErrInvalidURL. Other
// errors and uncertain-write behavior follow [Signer.Consume].
//
// Example:
//
//	if err := signer.ConsumeURL(ctx, signedURL, link.Bind(user.PasswordHash)); err != nil {
//		return err
//	}
func (signer *Signer) ConsumeURL(ctx context.Context, value *url.URL, opts ...CallOption) error {
	return signer.consume(ctx, "", value, opts...)
}

// consume implements [Signer.ConsumeURL] and [Signer.ConsumeRequest]. method
// is the request method for [ConsumedEvent.Method], empty when the caller has
// no request. It publishes [Consumed] only when this call spent the link.
func (signer *Signer) consume(ctx context.Context, method string, value *url.URL, opts ...CallOption) error {
	if signer.cache == nil {
		return ErrNoCache
	}

	link, err := signer.authenticate(value, newCall(opts))

	if err != nil {
		return err
	}

	if err := link.expiry(); err != nil {
		return err
	}

	if !link.temporary {
		return ErrNotTemporary
	}

	ttl := time.Until(link.expiresAt) + consumeMargin
	entry := cache.NewKey[bool](consumedKey(link), func(entry *cache.KeyBuilder) {
		entry.TTL(ttl)
	})

	added, err := signer.cache.Add(ctx, entry, true)

	if err != nil {
		return fmt.Errorf("consume link: %w", err)
	}

	if !added {
		return ErrConsumed
	}

	signer.events.Emit(ctx, Consumed, ConsumedEvent{
		Method: method,
	})

	return nil
}

// consumedKey returns the cache key recording that link was spent: a prefix
// and the digest of its signature, so the key neither leaks the signature nor
// grows with the URL.
func consumedKey(link *authenticated) string {
	digest := sha256.Sum256(link.signature)

	return consumedKeyPrefix + base64.RawURLEncoding.EncodeToString(digest[:])
}

// consumed reports whether link was spent. It is false without a cache and
// for permanent links, which cannot be consumed.
func (signer *Signer) consumed(ctx context.Context, link *authenticated) (bool, error) {
	if signer.cache == nil || !link.temporary {
		return false, nil
	}

	consumed, err := signer.cache.Has(ctx, cache.NewKey[bool](consumedKey(link)))

	if err != nil {
		return false, fmt.Errorf("check link use: %w", err)
	}

	return consumed, nil
}
