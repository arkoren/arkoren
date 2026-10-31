package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"arkoren.dev/core/http/middleware/ratelimit/internal/window"
	"arkoren.dev/core/service/cache"
)

// usage is the outcome of counting one request.
type usage struct {
	// denied is set when the request exceeds the limit.
	denied bool

	// remaining is the number of requests still available.
	remaining int64

	// reset is the header value for the seconds until the limit recovers.
	reset string

	// first is set on the first denied request of the key in the current
	// fixed window, checked only when events are enabled.
	first bool

	// key is the client part of the counter key.
	key string
}

// consume counts the request against its key at now and returns the outcome.
// A denied request is not counted: its increment is refunded.
func consume(request *http.Request, store *cache.Cache, config settings, now time.Time) (usage, error) {
	ctx := request.Context()
	nanos := config.Window.Nanoseconds()
	unixNanos := now.UnixNano()
	index := unixNanos / nanos
	elapsed := time.Duration(unixNanos % nanos)
	key := config.KeyFunc(request)
	currentKey := counterKey(config.Namespace, key, index)

	current, _, err := store.IncrementWithTTL(ctx, currentKey, 1, 2*config.Window)

	if err != nil {
		return usage{}, err
	}

	if current <= 0 {
		return usage{}, errors.New("ratelimit: cache returned a nonpositive counter after increment")
	}

	previous, err := previousCount(ctx, store, counterKey(config.Namespace, key, index-1))

	if err != nil {
		return usage{}, err
	}

	limit := int64(config.Limit)
	counter := window.New(previous, current, elapsed, config.Window)

	if remaining := counter.Remaining(limit); remaining >= 0 {
		return usage{
			remaining: remaining,
			reset:     seconds(counter.Until(0)),
		}, nil
	}

	if _, err := store.Decrement(ctx, currentKey, 1); err != nil {
		return usage{}, fmt.Errorf("refund denied request: %w", err)
	}

	counter.Refund()

	denied := usage{
		denied: true,
		reset:  seconds(counter.Until(limit - 1)),
		key:    key,
	}

	if config.Events != nil {
		// A failed marker publishes anyway: a duplicate beats a missed event.
		marker := deniedMarker(currentKey, config.Namespace.Separator(), config.Window)

		added, err := store.Add(ctx, marker, true)
		denied.first = added || err != nil
	}

	return denied, nil
}

// previousCount reads the previous window's counter. A missing counter, or one
// pushed below zero by refunds, is zero.
func previousCount(ctx context.Context, store *cache.Cache, key cache.Key[int64]) (int64, error) {
	count, err := store.Get(ctx, key)

	if errors.Is(err, cache.ErrNotFound) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	return max(count, 0), nil
}

// setHeaders exposes the current policy using the standard rate-limit headers.
func setHeaders(writer http.ResponseWriter, limit int, result usage) {
	header := writer.Header()
	header.Set("RateLimit-Limit", strconv.Itoa(limit))
	header.Set("RateLimit-Remaining", strconv.FormatInt(result.remaining, 10))
	header.Set("RateLimit-Reset", result.reset)
}

// seconds rounds wait up to whole seconds, at least one, so clients never
// retry before the limit has actually recovered.
func seconds(wait time.Duration) string {
	whole := wait / time.Second

	if wait%time.Second != 0 {
		whole++
	}

	return strconv.FormatInt(max(int64(whole), 1), 10)
}
