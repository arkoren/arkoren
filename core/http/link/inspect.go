package link

import (
	"context"
	"net/url"
	"time"
)

// Inspection describes an authenticated link, as returned by
// [Signer.Inspect].
// It is an observation, not an authorization or consumption reservation; another
// request can consume the link immediately after inspection.
//
// Example:
//
//	details, err := signer.Inspect(ctx, rawURL)
//	if err != nil {
//		return err
//	}
//	if details.Expired {
//		return link.ErrLinkExpired
//	}
type Inspection struct {
	// URL is the link without its "signature" and "expires" parameters. Pass
	// it to [Signer.Temporary] or [Signer.Sign] to issue a fresh link.
	// It still contains application paths and query values: do not log it as
	// automatically safe data merely because the signature was removed.
	//
	// Example:
	//	fresh, err := signer.Temporary(details.URL, time.Hour)
	//	if err != nil {
	//		return err
	//	}
	//	_ = fresh
	URL string

	// Temporary reports that the link carries an expiry.
	//
	// Example:
	//	logger.InfoContext(ctx, "link policy", "temporary", details.Temporary)
	Temporary bool

	// ExpiresAt is the expiry with second precision. It is the zero time for a
	// permanent link.
	//
	// Example:
	//	if details.Temporary {
	//		logger.InfoContext(ctx, "link expiry", "expires_at", details.ExpiresAt)
	//	}
	ExpiresAt time.Time

	// Expired reports a temporary link whose expiry has passed.
	//
	// Example:
	//	if details.Expired {
	//		return link.ErrLinkExpired
	//	}
	Expired bool

	// Previous reports that a secret from [Builder.Previous] signed the
	// link. Re-issue such links before dropping that secret.
	//
	// Example:
	//	logger.InfoContext(ctx, "link rotation", "previous", details.Previous)
	Previous bool

	// Consumed reports a temporary link that [Signer.Consume] spent. It is
	// always false when the signer has no cache and for permanent links, so
	// false means "not known to be spent", not a guarantee that it is usable.
	//
	// Example:
	//	if details.Consumed {
	//		return link.ErrLinkUsed
	//	}
	Consumed bool
}

// Inspect authenticates rawURL and describes it. Use it to decide what to
// show for a link without acting on it: offer a new link for an expired
// reset, or re-issue links signed with a previous secret.
//
// Inspect checks the signature and binding ([Bind]) like [Signer.Verify], but
// an expired link is not an error: it returns the details with
// [Inspection.Expired] set. Parsing or signer-mode validation returns
// [ErrInvalidURL]; malformed signature data, forgery, or a wrong binding returns
// [ErrInvalidSignature]. Every failure returns zero details, so unauthenticated
// input never reveals expiry or previous-key metadata. With a cache
// ([Builder.Cache]), [Inspection.Consumed] reports
// whether the link was spent, and a cache failure returns a wrapped error.
// Inspect never changes the link's state.
//
// Example:
//
//	details, err := signer.Inspect(ctx, rawURL)
//	if err == nil && details.Expired {
//		// Offer to send a new link for details.URL.
//	}
func (signer *Signer) Inspect(ctx context.Context, rawURL string, opts ...CallOption) (Inspection, error) {
	value, err := url.Parse(rawURL)

	if err != nil {
		return Inspection{}, ErrInvalidURL
	}

	return signer.InspectURL(ctx, value, opts...)
}

// InspectURL describes value without mutating it. nil or an unsuitable signer
// target returns ErrInvalidURL. Signature and cache failures follow [Signer.Inspect]
// and always return zero details, not a partially authenticated inspection.
//
// Example:
//
//	details, err := signer.InspectURL(ctx, signedURL)
//	if err != nil {
//		return err
//	}
//	if details.Consumed {
//		return link.ErrLinkUsed
//	}
func (signer *Signer) InspectURL(ctx context.Context, value *url.URL, opts ...CallOption) (Inspection, error) {
	link, err := signer.authenticate(value, newCall(opts))

	if err != nil {
		return Inspection{}, err
	}

	query := url.Values{}

	for name, values := range link.query {
		if name != signatureParameter && name != expiresParameter {
			query[name] = values
		}
	}

	base := *link.target
	base.RawQuery = query.Encode()

	consumed, err := signer.consumed(ctx, link)

	if err != nil {
		return Inspection{}, err
	}

	return Inspection{
		URL:       base.String(),
		Temporary: link.temporary,
		ExpiresAt: link.expiresAt,
		Expired:   link.expiry() != nil,
		Previous:  link.previous,
		Consumed:  consumed,
	}, nil
}
