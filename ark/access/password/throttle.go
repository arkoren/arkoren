package password

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/cache"

	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/ark/access/internal/throttle"
)

// counter is one attempt counter and its limit in [throttleWindow].
type counter struct {
	key   cache.Key[int64]
	limit int64
}

// attempt reserves one attempt on key and fails with [ErrThrottled] and a
// Retry-After header when the counter exceeds limit.
func (verifier *Verifier) attempt(ctx context.Context, key cache.Key[int64], limit int64) error {
	return throttle.Attempt(ctx, verifier.store, key, limit, throttleWindow, ErrThrottled)
}

// reserve reserves one attempt on every counter, in order, and stops at the
// first that is over its limit.
func (verifier *Verifier) reserve(ctx context.Context, counters ...counter) error {
	for _, counter := range counters {
		if err := verifier.attempt(ctx, counter.key, counter.limit); err != nil {
			return err
		}
	}

	return nil
}

// refund gives back one attempt reserved on key, for a login that succeeded:
// only failures count against the limits. It adds -1 with [throttleWindow] as
// the expiry for a counter that no longer exists (it expired between the
// reservation and the refund, or the cache lost it), because a plain Decrement
// would create it with no expiry and leave a stale counter forever. An existing
// counter keeps its own expiry. A failed refund only keeps the attempt counted.
func (verifier *Verifier) refund(ctx context.Context, key cache.Key[int64]) {
	_, _, _ = verifier.store.IncrementWithTTL(ctx, key, -1, throttleWindow)
}

// clear forgets the attempts counted on key after a success. Counters expire on
// their own, so a failed cleanup only keeps a stale count.
func (verifier *Verifier) clear(ctx context.Context, key cache.Key[int64]) {
	_ = verifier.store.Delete(ctx, key)
}

// loginCounters are the counters of a login.
type loginCounters struct {
	// pair counts the failures of the identifier from the client address.
	pair counter

	// identifier counts the failures of the identifier from every address.
	identifier counter

	// client counts the failures of the client address.
	client counter
}

// countersFor returns the counters of a login of identifier from r.
func (verifier *Verifier) countersFor(identifier string, r *http.Request) loginCounters {
	return loginCounters{
		pair:       counter{verifier.pairKey(identifier, r), maxFailures},
		identifier: counter{verifier.identifierKey(identifier), maxIdentifierFailures},
		client:     counter{verifier.clientKey(r), maxClientFailures},
	}
}

// reserveLogin reserves one attempt on the counters of a login, in order.
func (verifier *Verifier) reserveLogin(ctx context.Context, counters loginCounters) error {
	return verifier.reserve(ctx, counters.pair, counters.identifier, counters.client)
}

// settleLogin undoes the counting of a successful login: it clears the
// identifier-and-address counter and refunds the other two. A subject in a
// second-factor lockout ([auth.Guard.LockedOut]) is not settled: its correct
// password only opens a pending login that cannot finish, so the attempt stays
// counted as the failure it is, and someone who knows the password cannot start
// pending logins after every lockout at no cost. The guard keeps the lockout, not
// an event, so a failing lookup is returned and the login fails closed.
func (verifier *Verifier) settleLogin(ctx context.Context, guard *auth.Guard, counters loginCounters, subject string) error {
	locked, err := guard.LockedOut(ctx, subject)
	if err != nil {
		return fmt.Errorf("access: read second-factor lockout: %w", err)
	}
	if locked {
		return nil
	}
	verifier.clear(ctx, counters.pair.key)
	verifier.refund(ctx, counters.identifier.key)
	verifier.refund(ctx, counters.client.key)

	return nil
}

// identifierKey is the counter key of one identifier, lowercased and hashed so
// the key has a fixed shape whatever the user typed.
func (verifier *Verifier) identifierKey(identifier string) cache.Key[int64] {
	return cache.NewKey[int64]("access:" + verifier.instance + ":password:id:" + identifierHash(identifier))
}

// clientKey is the counter key of the client address of r.
func (verifier *Verifier) clientKey(r *http.Request) cache.Key[int64] {
	return cache.NewKey[int64]("access:" + verifier.instance + ":password:ip:" + throttle.Bucket(verifier.clientIP(r)))
}

// pairKey is the counter key of one identifier from one client address, the
// login lockout: an attacker's failures lock only the attacker's own address
// out of the account.
func (verifier *Verifier) pairKey(identifier string, r *http.Request) cache.Key[int64] {
	return cache.NewKey[int64]("access:" + verifier.instance + ":password:pair:" + identifierHash(identifier) + ":" + throttle.Bucket(verifier.clientIP(r)))
}

// throttleAction counts one request of action (such as "forgot") on the
// identifier as typed and on the client address of r, before any lookup, so the
// answer cannot reveal whether the identifier belongs to a user.
func (verifier *Verifier) throttleAction(r *http.Request, action, identifier string, perIdentifier, perClient int64) error {
	prefix := "access:" + verifier.instance + ":password:" + action

	return verifier.reserve(r.Context(),
		counter{cache.NewKey[int64](prefix + ":id:" + identifierHash(identifier)), perIdentifier},
		counter{cache.NewKey[int64](prefix + ":ip:" + throttle.Bucket(verifier.clientIP(r))), perClient},
	)
}

// identifierHash is the key part of an identifier: trimmed, lowercased and
// hashed.
func identifierHash(identifier string) string {
	return storage.Hash(strings.ToLower(strings.TrimSpace(identifier)))
}
