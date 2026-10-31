// Package link creates and verifies signed application links that can expire,
// be used once, or be revoked when state changes.
//
// A signed link carries a "signature" query parameter that proves the server
// issued it and nobody altered it. Use signed links for email verification,
// unsubscribe, file downloads, and password reset, where the recipient holds
// the only proof of authorization. Build one [Signer] with [New] at startup and
// share it: it is immutable and safe for concurrent use.
// Load a key.Shared with key.LoadShared or key.ParseShared before construction.
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
//	signed, err := signer.Sign("/unsubscribe?user=42")
//	if err != nil {
//		return err
//	}
//	if err := signer.Verify(signed); err != nil {
//		return err
//	}
//
// # Absolute and relative signers
//
// A signer with [Builder.Origin] set is absolute. It signs the scheme and host
// as well as the path, so a link for one origin never verifies for another.
// It accepts a path such as "/files/a.pdf", which it completes with the
// configured origin, or a full URL whose scheme and host match the origin
// (default ports are ignored). A different origin returns [ErrInvalidURL]. The
// origin must be an HTTP or HTTPS origin without a path, query, fragment, or
// user info, or [New] returns [ErrInvalidOrigin].
//
// A signer with an empty origin is relative. It signs the path and query only
// and accepts only values that start with "/". A URL with a scheme or host
// returns [ErrInvalidURL]. Use it when the same application is reachable under
// several hostnames.
//
// # Permanent and temporary links
//
// [Signer.Sign] creates a permanent link, which stays valid until the secret
// is retired. [Signer.Temporary] creates a link that expires after a ttl:
//
//	signed, err := signer.Temporary("/reset?user=42", 15*time.Minute)
//
// The expiry is stored as a Unix timestamp in the "expires" query parameter and
// is covered by the signature. A ttl that is not positive returns
// [ErrInvalidExpiry]. [Signer.SignURL] and [Signer.TemporaryURL] do the same
// for a parsed [url.URL] without mutating it. A "signature" or "expires"
// parameter already present in the input is replaced, never trusted.
//
// # What is signed
//
// The signature covers the canonical form of the link: for absolute signers
// the lowercased scheme and host with default ports dropped, then the escaped
// path, then the query sorted by name, including "expires". It excludes the
// "signature" parameter and any [Builder.IgnoreQuery] names.
//
// The signature does not cover the HTTP method, and the URL must not contain a
// fragment: a link with a fragment returns [ErrInvalidURL], because browsers
// never send fragments to the server.
//
// # Protecting routes
//
// [Middleware] returns middleware that verifies each request and calls the next
// handler only for valid links. Register a signed route for a single method so
// a link issued for GET cannot be replayed as POST; the router answers other
// methods with 405 before the middleware runs.
//
//	r := router.New()
//	r.With(link.Middleware(signer)).Get("/unsubscribe", unsubscribe)
//
// An absolute signer verifies against its configured origin, not the request
// Host header, which clients control. Verification uses the request path as the
// router sees it, so wrapping a handler in [http.StripPrefix] breaks
// verification: sign and verify the same path.
//
// # Single-use links
//
// A temporary link can be spent once. Give the signer a cache with
// [Builder.Cache] and call [Signer.Consume] in the handler that applies the
// change; [Middleware] only checks. A password reset shows its form on GET,
// which any valid link may do, and burns the link on POST:
//
//	signer, err := link.New(secret, func(signer *link.Builder) {
//		signer.Cache(cache.New(memorycache.New()))
//	})
//	if err != nil {
//		return err
//	}
//
//	r.With(link.Middleware(signer)).Get("/reset", showResetForm)
//	r.Post("/reset", func(w http.ResponseWriter, r *http.Request) error {
//		err := signer.ConsumeRequest(r)
//		if errors.Is(err, link.ErrConsumed) {
//			return link.ErrLinkUsed.Derive(func(details *problem.Builder) {
//				details.Error(err)
//			})
//		}
//		if err != nil {
//			return link.ErrLinkRejected.Derive(func(details *problem.Builder) {
//				details.Error(err)
//			})
//		}
//
//		return resetPassword(r)
//	})
//
// The POST request carries the signed URL in its query, so the handler needs
// no URL rebuilding. [Signer.Consume] and [Signer.ConsumeURL] take the link
// directly.
//
// Consume verifies the link like [Signer.Verify], then records the hash of its
// signature in the cache with an atomic add under the key prefix
// "link:v1:consumed:". Concurrent calls, including from several servers
// sharing a cache driver, have exactly one winner; the others get
// [ErrConsumed]. The record lives until the link expires plus one minute for
// clock skew between servers, after which expiry alone rejects the link. Only
// temporary links can be consumed: a permanent link returns
// [ErrNotTemporary], since its record would have to last forever. A signer
// without a cache returns [ErrNoCache].
//
// Consume first, then commit the side effect. That gives at-most-once
// behavior: if the side effect fails after Consume succeeded, the link is
// spent and the user must request a new one. A cache that evicts entries early
// lets a spent link work again, so use a driver that keeps them for their TTL.
// A cache error is uncertain: its Add may have committed before confirmation was
// lost. Do not proceed with the action after any error or assume the link remains
// available. Confirm state or issue a new link through your recovery flow.
//
// With a cache, [Middleware] also rejects temporary links that were already
// consumed with [ErrLinkUsed] ("Link Already Used", 403), so a form page does
// not render for a dead link. It only reads the cache and publishes no
// [Rejected] event: like an expired link, a used link is authentic.
//
// # Bound links
//
// [Bind] adds server-side state to the signature, which revokes links without
// a list of revoked links. Bind a reset link to the user's password hash and
// it stops working the moment the password changes:
//
//	signed, err := signer.Temporary("/reset?user=42", time.Hour, link.Bind(user.PasswordHash))
//	if err != nil {
//		return err
//	}
//	if err := signer.Verify(signed, link.Bind(user.PasswordHash)); err != nil {
//		return err
//	}
//	// After the password changes:
//	err = signer.Verify(signed, link.Bind(user.PasswordHash)) // ErrInvalidSignature
//
// Pass the same [Bind] to every Sign, Temporary, Verify, Consume, and Inspect
// call for the link. For [Middleware], pass [MiddlewareBuilder.BindRequest] to derive the
// binding from the request:
//
//	link.Middleware(signer, func(policy *link.MiddlewareBuilder) {
//		policy.BindRequest(func(r *http.Request) ([]byte, error) {
//			user, err := users.Find(r.Context(), r.URL.Query().Get("user"))
//			if err != nil {
//				return nil, err
//			}
//
//			return user.PasswordHash, nil
//		})
//	})
//
// The function runs before the signature is checked, so treat everything it
// reads from the request as untrusted. An error from it rejects the link with
// [ErrLinkRejected]. The returned problem retains the cause; the [Rejected]
// payload and HTTP error detail do not contain it. Redact causes before logging
// them: application/provider errors can contain credentials or full URLs.
//
// A binding enters the signed text only as a SHA-256 digest on its own final
// line, so the link discloses nothing about it, and links signed without
// [Bind] keep verifying as before. A wrong or missing binding, or a binding
// on a link signed without one, returns [ErrInvalidSignature].
//
// # Inspecting links
//
// [Signer.Inspect] authenticates a link and describes it without acting on
// it. Unlike Verify, an expired link is not an error:
//
//	details, err := signer.Inspect(ctx, rawURL)
//	switch {
//	case err != nil:
//		// Forged or malformed: no details are returned.
//	case details.Expired:
//		// Offer a new link for details.URL.
//	case details.Previous:
//		// Signed with a previous secret: re-issue it before rotation ends.
//	}
//
// The [Inspection] carries the URL without its "signature" and "expires"
// parameters, the expiry, whether the link expired, whether a previous secret
// signed it, and, when the signer has a cache, whether it was consumed.
// Consumed is false without a cache and for permanent links, so false means
// "not known to be spent". Inspect never changes state.
//
// # Secrets and rotation
//
// Every secret is a validated key.Shared of exactly [MinSecretLength] bytes.
// New rejects zero values with [ErrInvalidSecret]. The package derives its MAC
// key with HKDF-SHA256; still provision separate material for unrelated purposes.
//
// To rotate, move the old secret to [Builder.Previous] and pass a new
// secret to [New]. New links use the new secret, and links signed with the old
// one still verify. Drop the old secret after the longest lifetime of the links
// it signed; for permanent links, dropping it invalidates them.
//
//	link.New(
//		newSecret,
//		func(signer *link.Builder) {
//			signer.Previous(oldSecret)
//		},
//	)
//
// # Ignored query parameters
//
// Mail clients and analytics add parameters such as utm_source to links. List
// them in [Builder.IgnoreQuery] so they may be added, changed, or removed
// without invalidating the link:
//
//	link.New(secret, func(signer *link.Builder) {
//		signer.IgnoreQuery("utm_source", "utm_medium")
//	})
//
// The setting belongs to the signer, so signing and verifying always agree.
// Ignored parameters are not protected: never read authorization-relevant
// data from them. The names "signature" and "expires" are reserved, and an
// empty or reserved name returns [ErrInvalidIgnoreQuery].
//
// # Errors
//
// [Signer.Verify] returns [ErrInvalidURL] for parsing and signer-mode failures,
// and [ErrInvalidSignature] for malformed authentication data, unauthenticated,
// or tampered links. Authentication errors carry no further detail, so callers
// cannot learn which signature or binding component failed.
//
// A link that authenticates but is past its expiry returns an [*ExpiredError],
// which matches [ErrExpired] with [errors.Is]. Use [errors.As] to read
// [ExpiredError.ExpiresAt]:
//
//	var expired *link.ExpiredError
//	if errors.As(err, &expired) {
//		// Offer to send a new link; expired.ExpiresAt says when it lapsed.
//	}
//
// An expiry is reported only after the signature verifies, so an attacker
// cannot probe expiry times of forged links.
//
// [Middleware] turns these into safe responses: [ErrLinkExpired] (403, "Link
// Expired") for expired links, [ErrLinkUsed] (403, "Link Already Used") for
// consumed links, and [ErrLinkRejected] (403, "Invalid Link") for everything
// else. The underlying error is attached for logging but is not sent to the
// client.
//
// Signing and verifying functions return [ErrInvalidURL] for a URL the
// signer's mode does not accept. [New] returns [ErrInvalidSecret],
// [ErrInvalidOrigin], or [ErrInvalidIgnoreQuery] for invalid configuration.
//
// # Events
//
// With [Builder.Events], [Middleware] publishes [Rejected] ("link.rejected")
// with the method for every invalid link; expired
// and consumed links publish nothing. A [Signer.Consume], [Signer.ConsumeURL],
// or [Signer.ConsumeRequest] call that spends a one-time link publishes
// [Consumed] ("link.consumed") with the method when a request is known; direct
// URL calls leave it empty. Paths, queries, bindings, and raw causes are omitted
// because each can contain secrets. Failed and repeated consumption publish
// nothing. Calls to [Signer.Verify] and [Signer.Inspect] publish nothing.
// Event contexts retain caller/request context values; these payload limits do
// not sanitize context values or the HTTP problem's normal instance member.
package link
