package link

import (
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link/internal/signature"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/foundation/key"
	"arkoren.dev/foundation/problem"
)

const (
	// signatureParameter is the query parameter carrying the link MAC.
	signatureParameter = "signature"

	// expiresParameter is the query parameter carrying an expiry Unix time.
	expiresParameter = "expires"

	// MinSecretLength is the required byte length of every signing key. Nonzero
	// key.Shared values enforce it before signer construction; larger keys are
	// not accepted either. It is a size requirement, not a source of randomness.
	//
	// Example:
	//	logger.InfoContext(ctx, "link key requirements", "bytes", link.MinSecretLength)
	MinSecretLength = 32
)

// ErrInvalidSecret reports an invalid zero current or previous key.Shared.
// The key package enforces [MinSecretLength] before publishing nonzero values.
//
// Example:
//
//	_, err := link.New(key.Shared{})
//	if errors.Is(err, link.ErrInvalidSecret) {
//		fmt.Println("configure a link key")
//	}
var ErrInvalidSecret = errors.New("link signing secret is invalid")

// ErrInvalidOrigin reports an origin unsuitable for absolute link signing.
// Only HTTP(S) scheme/host origins without application paths are accepted.
//
// Example:
//
//	_, err := link.New(secret, func(signer *link.Builder) {
//		signer.Origin("https://app.example.com/account")
//	})
//	if errors.Is(err, link.ErrInvalidOrigin) {
//		return err
//	}
var ErrInvalidOrigin = errors.New("link signing origin is invalid")

// ErrInvalidIgnoreQuery reports an empty or reserved ignored query parameter.
// The signature and expiry names cannot be removed from authentication policy.
//
// Example:
//
//	_, err := link.New(secret, func(signer *link.Builder) {
//		signer.IgnoreQuery("signature")
//	})
//	if errors.Is(err, link.ErrInvalidIgnoreQuery) {
//		return err
//	}
var ErrInvalidIgnoreQuery = errors.New("link ignored query parameter is invalid")

// ErrInvalidExpiry reports a temporary link lifetime that is not positive.
// Temporary links require an explicit positive TTL; zero does not pick a default.
//
// Example:
//
//	_, err := signer.Temporary("/reset", 0)
//	if errors.Is(err, link.ErrInvalidExpiry) {
//		return err
//	}
var ErrInvalidExpiry = errors.New("link expiry is invalid")

// ErrInvalidURL reports a URL unsuitable for the signer's configured mode.
// It also classifies parse failures without attaching the potentially secret URL.
//
// Example:
//
//	_, err := signer.Sign("mailto:person@example.com")
//	if errors.Is(err, link.ErrInvalidURL) {
//		return err
//	}
var ErrInvalidURL = errors.New("link URL is invalid")

// ErrInvalidSignature reports a malformed or unauthenticated signed link.
// It deliberately does not identify which signature or binding component failed.
//
// Example:
//
//	if err := signer.Verify(rawURL); errors.Is(err, link.ErrInvalidSignature) {
//		return link.ErrLinkRejected
//	}
var ErrInvalidSignature = errors.New("link signature is invalid")

// ErrExpired reports an authenticated temporary link whose expiry has passed.
// Verification returns an [*ExpiredError] that matches it with [errors.Is].
// An unauthenticated link never reveals whether its apparent expiry passed.
//
// Example:
//
//	if err := signer.Verify(rawURL); errors.Is(err, link.ErrExpired) {
//		return link.ErrLinkExpired
//	}
var ErrExpired = errors.New("link has expired")

// ExpiredError reports an authenticated temporary link whose expiry has passed
// and carries the moment it expired.
// Verification produces this timestamp only after successful authentication.
// Use errors.As to inspect it, or errors.Is with ErrExpired when it is unnecessary.
//
// Example:
//
//	var expired *link.ExpiredError
//	if err := signer.Verify(rawURL); errors.As(err, &expired) {
//		logger.InfoContext(ctx, "link expired", "expires_at", expired.ExpiresAt)
//	}
type ExpiredError struct {
	// ExpiresAt is the time the link stopped being valid, with second precision.
	//
	// Example:
	//	var expired *link.ExpiredError
	//	if errors.As(err, &expired) {
	//		logger.InfoContext(ctx, "link expired", "expires_at", expired.ExpiresAt)
	//	}
	ExpiresAt time.Time
}

// Error describes authenticated expiry in UTC RFC3339 without a URL, signature,
// or binding. The timestamp reflects second precision from the signed URL.
//
// Example:
//
//	expired := &link.ExpiredError{
//		ExpiresAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
//	}
//	text := expired.Error() // "link expired at 2026-10-30T12:00:00Z"
//	_ = text
func (expiredError *ExpiredError) Error() string {
	return "link expired at " + expiredError.ExpiresAt.UTC().Format(time.RFC3339)
}

// Is reports whether target is [ErrExpired].
// Use errors.Is so wrapping added by an application does not hide this identity.
//
// Example:
//
//	expired := &link.ExpiredError{
//		ExpiresAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
//	}
//	if errors.Is(expired, link.ErrExpired) {
//		return link.ErrLinkExpired
//	}
func (expiredError *ExpiredError) Is(target error) bool {
	return target == ErrExpired
}

// ErrLinkRejected is the safe response returned for rejected signed requests.
//
// Example:
//
//	failure := link.ErrLinkRejected.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrLinkRejected = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/link:link-rejected")
	details.Title("Invalid Link")
	details.Detail("The link is invalid, has been altered, or has expired.")
	details.Status(http.StatusForbidden)
})

// ErrLinkExpired is the safe response returned for authentic but expired links.
//
// Example:
//
//	failure := link.ErrLinkExpired.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrLinkExpired = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/link:link-expired")
	details.Title("Link Expired")
	details.Detail("The link has expired. Request a new one.")
	details.Status(http.StatusForbidden)
})

// ErrLinkUsed is the safe response returned for authentic links that were
// already consumed. Return it from a handler when [Signer.Consume] reports
// [ErrConsumed]; [Middleware] returns it for consumed links.
//
// Example:
//
//	failure := link.ErrLinkUsed.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrLinkUsed = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/link:link-used")
	details.Title("Link Already Used")
	details.Detail("The link has already been used. Request a new one.")
	details.Status(http.StatusForbidden)
})

// Signer signs and verifies application links with an immutable key set.
// Construct it once and share it across requests. Cache and event services remain
// live dependencies; verification alone never reserves a link's single use.
//
// Example:
//
//	signer, err := link.New(secret)
//	if err != nil {
//		return err
//	}
//	signed, err := signer.Sign("/files/report.pdf")
//	if err != nil {
//		return err
//	}
//	if err := signer.Verify(signed); err != nil {
//		return err
//	}
type Signer struct {
	// origin is the normalized public origin, or nil for a relative signer.
	origin *url.URL

	// keys holds the HKDF-derived MAC keys; keys[0] creates all new signatures
	// and the rest verify links during secret rotation.
	keys [][]byte

	// ignoredQuery names parameters excluded from the signature.
	ignoredQuery []string

	// events receives [Rejected] and [Consumed]. Nil publishes nothing.
	events *event.Bus

	// cache records consumed links. Nil disables [Signer.Consume].
	cache *cache.Cache
}

// New creates a signer from the required secret and the configuration built by
// applying construction callbacks directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil/empty slices supply no callbacks.
// Validation follows all callbacks, and retained drafts cannot change the signer.
//
// secret creates all new signatures. Load a validated key.Shared through the
// key package. New rejects a zero current or previous secret with
// [ErrInvalidSecret]. HKDF-SHA256 derives purpose-bound MAC keys without retaining
// the original secret. Provision separate material for unrelated purposes.
//
// An empty origin yields a relative signer that signs path-and-query values
// only; otherwise the origin must be an HTTP or HTTPS origin without a path,
// query, fragment, or user info (an origin's root slash is also accepted).
// Owned key material and query-name slices are detached. Cache and event buses
// remain shared live services; callback captures are not cloned. Configure those
// dependencies before sharing the signer for concurrent use.
//
// Example:
//
//	signer, err := link.New(
//		secret,
//		func(signer *link.Builder) {
//			signer.Origin("https://app.example.com")
//		},
//	)
//	if err != nil {
//		return err
//	}
//	signed, err := signer.Sign("/files/report.pdf")
//	if err != nil {
//		return err
//	}
//	_ = signed
func New(secret key.Shared, configure ...BuilderFunc) (*Signer, error) {
	config := newSettings(configure...)

	signer := &Signer{
		ignoredQuery: slices.Clone(config.IgnoreQuery),
		events:       config.Events,
		cache:        config.Cache,
	}

	for _, secret := range append([]key.Shared{secret}, config.PreviousSecrets...) {
		if secret.Err() != nil {
			return nil, ErrInvalidSecret
		}

		material, err := signature.Derive(secret.Bytes())

		if err != nil {
			return nil, fmt.Errorf("derive link signing key: %w", err)
		}

		signer.keys = append(signer.keys, material)
	}

	for _, name := range config.IgnoreQuery {
		if name == "" || name == signatureParameter || name == expiresParameter {
			return nil, fmt.Errorf("%w: %q", ErrInvalidIgnoreQuery, name)
		}
	}

	if config.Origin != "" {
		origin, err := parseOrigin(config.Origin)

		if err != nil {
			return nil, err
		}

		signer.origin = origin
	}

	return signer, nil
}

// parseOrigin parses an HTTP or HTTPS origin and returns its normalized form.
func parseOrigin(rawOrigin string) (*url.URL, error) {
	origin, err := url.Parse(rawOrigin)

	if err != nil {
		return nil, fmt.Errorf("parse link signing origin: %w", ErrInvalidOrigin)
	}

	scheme := strings.ToLower(origin.Scheme)

	if scheme != "http" && scheme != "https" ||
		origin.Host == "" || origin.Opaque != "" || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" ||
		origin.Path != "" && origin.Path != "/" {
		return nil, ErrInvalidOrigin
	}

	return &url.URL{
		Scheme: scheme,
		Host:   normalizeHost(scheme, origin.Host),
	}, nil
}

// Sign signs rawURL as a permanent link and returns its canonical URL string.
// A reserved query parameter already present in rawURL is replaced. Pass
// [Bind] to cover server-side state in the signature.
//
// Example:
//
//	signed, err := signer.Sign("/files/report.pdf")
//	if err != nil {
//		return err
//	}
//	_ = signed
func (signer *Signer) Sign(rawURL string, opts ...CallOption) (string, error) {
	return signText(rawURL, func(value *url.URL) (*url.URL, error) {
		return signer.SignURL(value, opts...)
	})
}

// SignURL signs value as a permanent link without mutating value. See
// [Signer.Sign].
// nil or a target outside the signer's mode returns ErrInvalidURL. The returned
// URL is an independent value; it does not grant protection to ignored parameters.
//
// Example:
//
//	signed, err := signer.SignURL(&url.URL{
//		Path: "/files/report.pdf",
//	})
//	if err != nil {
//		return err
//	}
//	_ = signed.String()
func (signer *Signer) SignURL(value *url.URL, opts ...CallOption) (*url.URL, error) {
	return signer.sign(value, "", newCall(opts))
}

// Temporary signs rawURL as a temporary link that expires ttl from now. A ttl
// that is not positive returns [ErrInvalidExpiry]. Pass [Bind] to cover
// server-side state in the signature.
//
// Example:
//
//	signed, err := signer.Temporary("/files/report.pdf", 15*time.Minute)
//	if err != nil {
//		return err
//	}
//	_ = signed
func (signer *Signer) Temporary(rawURL string, ttl time.Duration, opts ...CallOption) (string, error) {
	return signText(rawURL, func(value *url.URL) (*url.URL, error) {
		return signer.TemporaryURL(value, ttl, opts...)
	})
}

// TemporaryURL signs value as a temporary link that expires ttl from now,
// without mutating value. A ttl that is not positive returns [ErrInvalidExpiry].
// The expiry has second precision and is authenticated with the rest of the URL.
//
// Example:
//
//	signed, err := signer.TemporaryURL(&url.URL{
//		Path: "/reset",
//	}, 15*time.Minute)
//	if err != nil {
//		return err
//	}
//	_ = signed.String()
func (signer *Signer) TemporaryURL(value *url.URL, ttl time.Duration, opts ...CallOption) (*url.URL, error) {
	if ttl <= 0 {
		return nil, ErrInvalidExpiry
	}

	return signer.sign(value, strconv.FormatInt(time.Now().Add(ttl).Unix(), 10), newCall(opts))
}

// signText parses rawURL, applies sign, and returns the resulting URL string.
func signText(rawURL string, sign func(*url.URL) (*url.URL, error)) (string, error) {
	value, err := url.Parse(rawURL)

	if err != nil {
		return "", fmt.Errorf("parse link URL: %w", ErrInvalidURL)
	}

	signed, err := sign(value)

	if err != nil {
		return "", err
	}

	return signed.String(), nil
}

// sign signs value, replacing any reserved parameters it carries. A non-empty
// expires makes the link temporary.
func (signer *Signer) sign(value *url.URL, expires string, call callConfig) (*url.URL, error) {
	target, err := signer.target(value)

	if err != nil {
		return nil, err
	}

	query, err := url.ParseQuery(target.RawQuery)

	if err != nil {
		return nil, ErrInvalidURL
	}

	query.Del(signatureParameter)
	query.Del(expiresParameter)

	if expires != "" {
		query.Set(expiresParameter, expires)
	}

	mac := signature.MAC(signer.keys[0], signature.Canonical(target, query, signer.origin != nil, signer.ignoredQuery, call.binding, call.bound))
	query.Set(signatureParameter, base64.RawURLEncoding.EncodeToString(mac))

	target.RawQuery = query.Encode()

	return target, nil
}

// Verify verifies rawURL's signature and, when present, its expiry. Pass
// [Bind] with the value the link was signed with, or a bound link returns
// [ErrInvalidSignature]. Verify does not know whether a link was consumed;
// see [Signer.Consume] and [Signer.Inspect].
// Parse/target failures return ErrInvalidURL, authentication failures return
// ErrInvalidSignature, and authenticated expiry returns *ExpiredError.
//
// Example:
//
//	if err := signer.Verify(rawURL, link.Bind(user.PasswordHash)); err != nil {
//		return err
//	}
func (signer *Signer) Verify(rawURL string, opts ...CallOption) error {
	value, err := url.Parse(rawURL)

	if err != nil {
		return fmt.Errorf("parse link URL: %w", ErrInvalidURL)
	}

	return signer.VerifyURL(value, opts...)
}

// VerifyURL verifies value's signature and, when present, its expiry. The
// expiry is checked only after the signature, so a forged link never reports
// [ErrExpired].
// value is never mutated; nil or an unsuitable target returns ErrInvalidURL.
//
// Example:
//
//	if err := signer.VerifyURL(signedURL, link.Bind(user.PasswordHash)); err != nil {
//		return err
//	}
func (signer *Signer) VerifyURL(value *url.URL, opts ...CallOption) error {
	link, err := signer.authenticate(value, newCall(opts))

	if err != nil {
		return err
	}

	return link.expiry()
}

// authenticated is a link whose signature and binding verified. It says
// nothing about expiry or consumption.
type authenticated struct {
	// target is the normalized URL, still carrying the reserved parameters.
	target *url.URL

	// query is target's parsed query.
	query url.Values

	// signature is the decoded signature.
	signature []byte

	// previous reports that a previous secret, not the current one, signed it.
	previous bool

	// temporary reports that the link carries a signed expiry.
	temporary bool

	// expiresAt is the expiry with second precision; zero unless temporary.
	expiresAt time.Time
}

// authenticate verifies value's signature and binding against every key and
// returns the parsed link. Unsuitable targets return ErrInvalidURL; malformed
// authentication data, forgery, and wrong bindings return ErrInvalidSignature.
// Neither failure class reveals expiry or which previous key would have matched.
func (signer *Signer) authenticate(value *url.URL, call callConfig) (*authenticated, error) {
	target, err := signer.target(value)

	if err != nil {
		return nil, err
	}

	query, err := url.ParseQuery(target.RawQuery)

	if err != nil || len(query[signatureParameter]) != 1 {
		return nil, ErrInvalidSignature
	}

	mac, err := base64.RawURLEncoding.Strict().DecodeString(query.Get(signatureParameter))

	if err != nil {
		return nil, ErrInvalidSignature
	}

	canonical := signature.Canonical(target, query, signer.origin != nil, signer.ignoredQuery, call.binding, call.bound)
	link := &authenticated{
		target:    target,
		query:     query,
		signature: mac,
	}
	matched := false

	for index, material := range signer.keys {
		if hmac.Equal(signature.MAC(material, canonical), mac) {
			matched = true
			link.previous = index > 0

			break
		}
	}

	if !matched {
		return nil, ErrInvalidSignature
	}

	if !query.Has(expiresParameter) {
		return link, nil
	}

	expiresAt, err := strconv.ParseInt(query.Get(expiresParameter), 10, 64)

	if err != nil {
		return nil, ErrInvalidSignature
	}

	link.temporary = true
	link.expiresAt = time.Unix(expiresAt, 0)

	return link, nil
}

// expiry returns an [*ExpiredError] when the link is temporary and its expiry
// has passed, and nil otherwise.
func (link *authenticated) expiry() error {
	if link.temporary && !link.expiresAt.After(time.Now()) {
		return &ExpiredError{
			ExpiresAt: link.expiresAt,
		}
	}

	return nil
}

// Middleware returns middleware that permits only requests signed by signer.
//
// The signature covers the URL, not the HTTP method, so a link is accepted
// for any method its route accepts. Register a signed route for a single
// method (for example with [router.Router.Get]), never with
// [router.Router.Any] or [router.Router.Methods], so a link signed for GET
// cannot be replayed as POST: the router answers other methods with 405
// before this middleware runs. Verification uses the request's URL path as
// the router sees it; behind http.StripPrefix the path no longer matches the
// signed URL.
//
// Pass [MiddlewareBuilder.BindRequest] to verify links signed with [Bind]. When the signer has a
// cache ([Builder.Cache]), a temporary link that [Signer.Consume] already spent is
// answered with [ErrLinkUsed], so a form page does not render for a dead link.
// The check only reads the cache; the middleware never consumes a link. A cache
// failure is returned as an ordinary error.
//
// With [Builder.Events] on the signer, a rejected link that has not merely expired
// or been used publishes [Rejected]: expired and used links are authentic, so
// they are ordinary outcomes and publish nothing.
//
// Example:
//
//	r.With(link.Middleware(signer)).Get("/reset", showResetForm)
func Middleware(signer *Signer, configure ...MiddlewareBuilderFunc) router.Middleware {
	if signer == nil {
		panic("link: signer must not be nil")
	}

	policy := &MiddlewareBuilder{}

	for _, configure := range configure {
		configure(policy)
	}

	config := policy.settings

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			var call callConfig
			var err error

			if config.bind != nil {
				call, err = bindRequest(config.bind, r)
			}

			var link *authenticated

			if err == nil {
				link, err = signer.authenticate(signer.requestURL(r), call)
			}

			if err != nil {
				signer.events.Emit(r.Context(), Rejected, RejectedEvent{
					Method: r.Method,
				})

				return ErrLinkRejected.Derive(func(details *problem.Builder) {
					details.Error(err)
				})
			}

			if err := link.expiry(); err != nil {
				return ErrLinkExpired.Derive(func(details *problem.Builder) {
					details.Error(err)
				})
			}

			consumed, err := signer.consumed(r.Context(), link)

			if err != nil {
				return err
			}

			if consumed {
				return ErrLinkUsed.Derive(func(details *problem.Builder) {
					details.Error(ErrConsumed)
				})
			}

			return next(w, r)
		}
	}
}

// target validates value for the signer's mode and returns a normalized copy:
// lowercase scheme and host, no default port, and a non-empty path. An absolute
// signer completes a root-relative value with its origin.
func (signer *Signer) target(value *url.URL) (*url.URL, error) {
	if value == nil || value.Opaque != "" || value.User != nil || value.Fragment != "" {
		return nil, ErrInvalidURL
	}

	target := *value
	target.Scheme = strings.ToLower(target.Scheme)
	target.Host = normalizeHost(target.Scheme, target.Host)

	switch {
	case target.Scheme == "" && target.Host == "":
		if !strings.HasPrefix(target.Path, "/") {
			return nil, ErrInvalidURL
		}

		if signer.origin != nil {
			target.Scheme = signer.origin.Scheme
			target.Host = signer.origin.Host
		}
	case signer.origin == nil || target.Scheme != signer.origin.Scheme || target.Host != signer.origin.Host:
		return nil, ErrInvalidURL
	}

	if target.Path == "" {
		target.Path = "/"
	}

	return &target, nil
}

// normalizeHost lowercases host and drops the scheme's default port, so
// https://host:443 and https://host sign and verify identically.
func normalizeHost(scheme, host string) string {
	host = strings.ToLower(host)

	switch scheme {
	case "https":
		return strings.TrimSuffix(host, ":443")
	case "http":
		return strings.TrimSuffix(host, ":80")
	default:
		return host
	}
}

// requestURL rebuilds the untrusted request target in the signer's scope.
func (signer *Signer) requestURL(request *http.Request) *url.URL {
	value := &url.URL{
		Path:     request.URL.Path,
		RawPath:  request.URL.RawPath,
		RawQuery: request.URL.RawQuery,
	}

	if signer.origin != nil {
		value.Scheme = signer.origin.Scheme
		value.Host = signer.origin.Host
	}

	return value
}
