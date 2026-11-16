// Package throttle counts attempts in the application's cache for the packages
// of arkoren.dev/ark/access, so a wrong-secret handler can answer 429 with
// Retry-After before it does any expensive work. It is internal so the public
// packages never import each other.
package throttle

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/foundation/problem"
)

// Reserve counts one attempt on key within window. It returns zero while the
// count is within limit, and the time left until the counter expires once the
// limit is exceeded. The counter starts at the first attempt and is never
// extended by later ones, so a client that keeps trying is not locked out for
// longer. A cache failure is returned as is: the caller fails closed.
func Reserve(ctx context.Context, store *cache.Cache, key cache.Key[int64], limit int64, window time.Duration) (time.Duration, error) {
	count, remaining, err := store.IncrementWithTTL(ctx, key, 1, window)
	if err != nil {
		return 0, fmt.Errorf("access: count attempt: %w", err)
	}
	if count <= limit {
		return 0, nil
	}

	return max(remaining, time.Second), nil
}

// Attempt counts one attempt on key with [Reserve] and returns [Reject] of base
// once the limit is exceeded, so a handler answers 429 with Retry-After before
// it does any expensive work. A cache failure is returned as is.
func Attempt(ctx context.Context, store *cache.Cache, key cache.Key[int64], limit int64, window time.Duration, base problem.Details) error {
	wait, err := Reserve(ctx, store, key, limit, window)
	if err != nil {
		return err
	}
	if wait > 0 {
		return Reject(base, wait)
	}

	return nil
}

// Bucket returns the key part for a client address: an IPv6 address becomes its
// /64 prefix, because one subscriber commonly controls a whole /64 and could
// otherwise take a fresh address for every attempt; an IPv4 address, or text
// that is not an address, is returned as is.
func Bucket(client string) string {
	address, err := netip.ParseAddr(client)
	if err != nil || address.Is4() || address.Is4In6() {
		return client
	}
	prefix, err := address.Prefix(64)
	if err != nil {
		return client
	}

	return prefix.String()
}

// Reject returns base with a Retry-After header of wait, rounded up to whole
// seconds. base is a 429 problem; the result still matches it with errors.Is.
func Reject(base problem.Details, wait time.Duration) error {
	seconds := int((wait + time.Second - 1) / time.Second)

	return base.Derive(func(details *problem.Builder) {
		details.Header("Retry-After", strconv.Itoa(seconds))
	})
}

// RemoteHost is the default client address of the packages: the host part of
// r.RemoteAddr, or all of it when it has no port.
func RemoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
