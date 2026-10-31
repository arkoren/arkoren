package cachetest

import (
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
)

// must fails the test immediately when err is not nil.
func must(t *testing.T, op string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

// put stores value under key with a ttl, failing the test on error.
func put(t *testing.T, d cache.Driver, key, value string, ttl time.Duration) {
	t.Helper()

	must(t, "Put", d.Put(t.Context(), key, []byte(value), ttl))
}

// wantValue asserts that Get returns want for key.
func wantValue(t *testing.T, d cache.Driver, key, want string) {
	t.Helper()

	got, err := d.Get(t.Context(), key)

	if err != nil {
		t.Fatalf("Get(%q) = %v, want %q", key, err, want)
	}

	if string(got) != want {
		t.Errorf("Get(%q) = %q, want %q", key, got, want)
	}
}

// wantMissing asserts that key is reported absent by Get, Has and TTL.
func wantMissing(t *testing.T, d cache.Driver, key string) {
	t.Helper()

	if _, err := d.Get(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("Get(%q) error = %v, want ErrNotFound", key, err)
	}

	if has, err := d.Has(t.Context(), key); err != nil || has {
		t.Errorf("Has(%q) = %t, %v, want false, nil", key, has, err)
	}

	if _, err := d.TTL(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("TTL(%q) error = %v, want ErrNotFound", key, err)
	}
}

// wantTTLWithin asserts that TTL of key is positive and at most max.
func wantTTLWithin(t *testing.T, d cache.Driver, key string, limit time.Duration) time.Duration {
	t.Helper()

	got, err := d.TTL(t.Context(), key)

	must(t, "TTL", err)

	if got <= 0 || got > limit {
		t.Errorf("TTL(%q) = %s, want in (0, %s]", key, got, limit)
	}

	return got
}

// wantNoExpiry asserts that key exists without an expiry.
func wantNoExpiry(t *testing.T, d cache.Driver, key string) {
	t.Helper()

	got, err := d.TTL(t.Context(), key)

	if err != nil || got != 0 {
		t.Errorf("TTL(%q) = %s, %v, want 0, nil", key, got, err)
	}
}

// wantCount asserts the result of a counter operation.
func wantCount(t *testing.T, op string, got int64, err error, want int64) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}

	if got != want {
		t.Errorf("%s = %d, want %d", op, got, want)
	}
}

// acquire takes the lock name with token for lockTTL, failing the test on
// error, and reports whether it was held.
func acquire(t *testing.T, d cache.Driver, name, token string) bool {
	t.Helper()

	held, err := d.TryLock(t.Context(), name, token, ttl)

	must(t, "TryLock", err)

	return held
}

// wantLockHeld asserts that no other token can take the lock name.
func wantLockHeld(t *testing.T, d cache.Driver, name string) {
	t.Helper()

	if acquire(t, d, name, "probe") {
		t.Errorf("lock %q was free, want held", name)
	}
}
