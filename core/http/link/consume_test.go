package link_test

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// uncertainAdd simulates a backend that commits an Add but loses its response.
// All other public driver operations retain the real memory driver's behavior.
type uncertainAdd struct {
	// Driver supplies actual cache storage and every non-Add operation.
	cache.Driver

	// err is the transport failure returned after the first successful write.
	err error
}

// Add commits through the real backend before reporting an uncertain failure.
// An existing marker still returns the backend's ordinary duplicate result.
func (driver *uncertainAdd) Add(ctx context.Context, name string, value []byte, ttl time.Duration) (bool, error) {
	added, err := driver.Driver.Add(ctx, name, value, ttl)

	if err != nil || !added {
		return added, err
	}

	return false, driver.err
}

// TestConsumeCacheErrorCanLeaveLinkSpent verifies recovery through the public
// signer API after a backend committed a write but could not confirm it.
func TestConsumeCacheErrorCanLeaveLinkSpent(t *testing.T) {
	t.Parallel()

	cause := errors.New("cache response lost")
	driver := &uncertainAdd{
		Driver: memorycache.New(),
		err:    cause,
	}
	bus, published := listenConsumed(t)
	signer := mustNewSigner(t, func(signer *link.Builder) {
		signer.Cache(cache.New(driver))
		signer.Events(bus)
	})

	signed := mustTemporary(t, signer, "/reset?account=42")

	if err := signer.Consume(t.Context(), signed); !errors.Is(err, cause) {
		t.Fatalf("Consume error = %v; want backend cause", err)
	}

	details, err := signer.Inspect(t.Context(), signed)

	if err != nil || !details.Consumed {
		t.Fatalf("Inspect = %+v, %v; want spent marker", details, err)
	}

	if err := signer.Consume(t.Context(), signed); !errors.Is(err, link.ErrConsumed) {
		t.Fatalf("retry error = %v; want ErrConsumed", err)
	}

	if len(*published) != 0 {
		t.Fatalf("uncertain consumption published %d success events", len(*published))
	}
}

// TestConsumeMalformedURLPrecedesMissingCache records parse-before-cache error
// precedence without changing how valid parsed links report missing cache setup.
func TestConsumeMalformedURLPrecedesMissingCache(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	if err := signer.Consume(t.Context(), "%"); !errors.Is(err, link.ErrInvalidURL) {
		t.Fatalf("Consume error = %v; want ErrInvalidURL", err)
	}

	if err := signer.Consume(t.Context(), "/reset"); !errors.Is(err, link.ErrNoCache) {
		t.Fatalf("parsed URL error = %v; want ErrNoCache", err)
	}
}

// ExampleSigner_Consume shows using a cache-backed signer to accept a
// temporary link only once and report later reuse.
func ExampleSigner_Consume() {
	signer, err := link.New(
		secret,
		func(signer *link.Builder) {
			signer.Cache(cache.New(memorycache.New()))
		},
	)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Temporary("/reset?user=42", time.Hour)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(signer.Consume(context.Background(), signed))

	err = signer.Consume(context.Background(), signed)
	fmt.Println(errors.Is(err, link.ErrConsumed))
	// Output:
	// <nil>
	// true
}

// newCachedSigner creates an absolute signer with a memory cache.
func newCachedSigner(t *testing.T, opts ...func(*link.Builder)) *link.Signer {
	t.Helper()

	return mustNewSigner(t, append([]func(*link.Builder){func(signer *link.Builder) {
		signer.Cache(cache.New(memorycache.New()))
	}}, opts...)...)
}

// mustTemporary signs rawURL as a temporary link or fails the test.
func mustTemporary(t *testing.T, signer *link.Signer, rawURL string, opts ...link.CallOption) string {
	t.Helper()

	signed, err := signer.Temporary(rawURL, time.Hour, opts...)

	if err != nil {
		t.Fatalf("Temporary() error = %v", err)
	}

	return signed
}

// serveWith runs target through Middleware built with opts and returns the
// response and whether the handler ran.
func serveWith(signer *link.Signer, target string, opts ...func(*link.MiddlewareBuilder)) (*http.Response, bool) {
	called := false
	handler := link.Middleware(signer, opts...)(func(http.ResponseWriter, *http.Request) error {
		called = true

		return nil
	})

	return router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, target, nil)), called
}

// TestConsumeSucceedsOnceThenReportsConsumed checks that consuming a temporary
// link succeeds once and later consumption reports the used-link error.
func TestConsumeSucceedsOnceThenReportsConsumed(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)
	signed := mustTemporary(t, signer, "/reset?user=42")

	if err := signer.Consume(t.Context(), signed); err != nil {
		t.Fatalf("first Consume() error = %v", err)
	}

	if err := signer.Consume(t.Context(), signed); !errors.Is(err, link.ErrConsumed) {
		t.Fatalf("second Consume() error = %v, want ErrConsumed", err)
	}
}

// TestConsumeURLDoesNotMutate checks that consuming a parsed URL leaves the
// caller-owned URL unchanged.
func TestConsumeURLDoesNotMutate(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)
	value := mustParseURL(t, mustTemporary(t, signer, "/reset?user=42"))
	before := value.String()

	if err := signer.ConsumeURL(t.Context(), value); err != nil {
		t.Fatalf("ConsumeURL() error = %v", err)
	}

	if value.String() != before {
		t.Fatalf("ConsumeURL() changed the URL to %q", value)
	}
}

// TestConsumeRequestUsesRequestURL checks that request-based consumption
// verifies the signed request URL rather than unrelated request metadata.
func TestConsumeRequestUsesRequestURL(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)
	signed := mustParseURL(t, mustTemporary(t, signer, "/reset?user=42"))
	request := httptest.NewRequest(http.MethodPost, signed.RequestURI(), nil)

	if err := signer.ConsumeRequest(request); err != nil {
		t.Fatalf("ConsumeRequest() error = %v", err)
	}

	if err := signer.ConsumeRequest(request); !errors.Is(err, link.ErrConsumed) {
		t.Fatalf("second ConsumeRequest() error = %v, want ErrConsumed", err)
	}
}

// TestConsumeConcurrentHasOneWinner checks atomic consumption under concurrent
// callers so exactly one request can use the link.
func TestConsumeConcurrentHasOneWinner(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)
	signed := mustTemporary(t, signer, "/reset?user=42")
	var wins, consumed atomic.Int32
	var group sync.WaitGroup

	for range 32 {
		group.Go(func() {
			switch err := signer.Consume(context.Background(), signed); {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, link.ErrConsumed):
				consumed.Add(1)
			}
		})
	}

	group.Wait()

	if wins.Load() != 1 || consumed.Load() != 31 {
		t.Fatalf("wins = %d, consumed = %d, want 1 and 31", wins.Load(), consumed.Load())
	}
}

// TestConsumeRejectsPermanentLink checks that a non-expiring link cannot enter
// the single-use consumption flow.
func TestConsumeRejectsPermanentLink(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)

	err := signer.Consume(t.Context(), mustSign(t, signer, "/reset?user=42"))

	if !errors.Is(err, link.ErrNotTemporary) {
		t.Fatalf("Consume() error = %v, want ErrNotTemporary", err)
	}
}

// TestConsumeWithoutCacheFails checks that consuming a temporary link without
// a backing cache reports the missing capability.
func TestConsumeWithoutCacheFails(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	err := signer.Consume(t.Context(), mustTemporary(t, signer, "/reset"))

	if !errors.Is(err, link.ErrNoCache) {
		t.Fatalf("Consume() error = %v, want ErrNoCache", err)
	}
}

// TestConsumeRejectsForgedLink checks that altered application parameters fail
// signature verification before the link can be consumed.
func TestConsumeRejectsForgedLink(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)
	signed := mustTemporary(t, signer, "/reset?user=42")

	err := signer.Consume(t.Context(), strings.Replace(signed, "user=42", "user=43", 1))

	if !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Consume() error = %v, want ErrInvalidSignature", err)
	}
}

// TestConsumeRejectsExpiredLink checks expiration using simulated time so an
// authentic but stale link cannot be consumed.
func TestConsumeRejectsExpiredLink(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		signer := newCachedSigner(t)
		signed := mustTemporary(t, signer, "/reset")
		time.Sleep(time.Hour)

		if err := signer.Consume(t.Context(), signed); !errors.Is(err, link.ErrExpired) {
			t.Fatalf("Consume() error = %v, want ErrExpired", err)
		}
	})
}

// TestConsumeRecordOutlivesLinkByMargin verifies the consumed record is kept
// after the expiry, so skewed clocks still see it, and then released.
func TestConsumeRecordOutlivesLinkByMargin(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		signer := mustNewSigner(t, func(signer *link.Builder) {
			signer.Cache(store)
		})

		signed := mustTemporary(t, signer, "/reset")

		if err := signer.Consume(t.Context(), signed); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Hour + 30*time.Second)

		details, err := signer.Inspect(t.Context(), signed)

		if err != nil || !details.Consumed || !details.Expired {
			t.Fatalf("Inspect() = %+v, %v, want consumed and expired", details, err)
		}

		time.Sleep(time.Minute)

		details, err = signer.Inspect(t.Context(), signed)

		if err != nil || details.Consumed {
			t.Fatalf("Inspect() = %+v, %v, want record released", details, err)
		}
	})
}

// TestBindMatchesOnlyTheSameValue checks that bound links require the original
// state bytes and reject both different and absent bindings.
func TestBindMatchesOnlyTheSameValue(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signed := mustTemporary(t, signer, "/reset?user=42", link.Bind([]byte("hash-1")))

	if err := signer.Verify(signed, link.Bind([]byte("hash-1"))); err != nil {
		t.Fatalf("Verify() with binding error = %v", err)
	}

	if err := signer.Verify(signed, link.Bind([]byte("hash-2"))); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() with wrong binding error = %v, want ErrInvalidSignature", err)
	}

	if err := signer.Verify(signed); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() without binding error = %v, want ErrInvalidSignature", err)
	}
}

// TestBindOnUnboundLinkFails checks that supplying binding state cannot
// validate a link originally signed without a binding.
func TestBindOnUnboundLinkFails(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signed := mustSign(t, signer, "/reset?user=42")

	if err := signer.Verify(signed, link.Bind([]byte("hash"))); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() error = %v, want ErrInvalidSignature", err)
	}
}

// TestBindEmptyValueDiffersFromNoBinding checks that nil and empty binding
// bytes are equivalent, but both remain distinct from an omitted binding
// option.
func TestBindEmptyValueDiffersFromNoBinding(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signed := mustSign(t, signer, "/reset", link.Bind(nil))

	if err := signer.Verify(signed, link.Bind([]byte{})); err != nil {
		t.Fatalf("Verify() with empty binding error = %v", err)
	}

	if err := signer.Verify(signed); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() without binding error = %v, want ErrInvalidSignature", err)
	}
}

// TestBindCopiesValue checks that mutating the caller-owned byte slice after
// creating an option does not change its captured binding.
func TestBindCopiesValue(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	value := []byte("hash-1")
	option := link.Bind(value)
	value[0] = 'X'

	signed := mustSign(t, signer, "/reset", option)

	if err := signer.Verify(signed, link.Bind([]byte("hash-1"))); err != nil {
		t.Fatalf("Verify() error = %v, want the value as passed to Bind", err)
	}
}

// TestBindURLVariantsAgree checks that string and parsed-URL signing variants
// preserve the same binding verification contract.
func TestBindURLVariantsAgree(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	signed, err := signer.TemporaryURL(mustParseURL(t, "/reset"), time.Hour, link.Bind([]byte("h")))

	if err != nil {
		t.Fatal(err)
	}

	permanent, err := signer.SignURL(mustParseURL(t, "/reset"), link.Bind([]byte("h")))

	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{signed.String(), permanent.String()} {
		if err := signer.Verify(value, link.Bind([]byte("h"))); err != nil {
			t.Fatalf("Verify(%q) error = %v", value, err)
		}
	}
}

// TestUnboundLinkWithEarlierCanonicalFormStillVerifies builds a signature from
// the canonical form used before Bind existed, so links already issued keep
// verifying.
func TestUnboundLinkWithEarlierCanonicalFormStillVerifies(t *testing.T) {
	t.Parallel()

	const prefix = "arkoren.dev/core/http/link/v1"

	key, err := hkdf.Key(sha256.New, testSecret.Bytes(), nil, prefix, sha256.Size)

	if err != nil {
		t.Fatal(err)
	}

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(prefix + "\nabsolute\nhttps://app.example.com/files/a.pdf?id=1"))

	legacy := "https://app.example.com/files/a.pdf?id=1&signature=" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if err := mustNewSigner(t).Verify(legacy); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

// TestMiddlewareBindRequest checks that request-derived binding state must
// match the signed state before the protected handler runs.
func TestMiddlewareBindRequest(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signed := mustTemporary(t, signer, "/reset?user=42", link.Bind([]byte("hash-1")))
	derive := func(value string) func(*link.MiddlewareBuilder) {
		return func(policy *link.MiddlewareBuilder) {
			policy.BindRequest(func(*http.Request) ([]byte, error) {
				return []byte(value), nil
			})
		}
	}

	response, called := serveWith(signer, signed, derive("hash-1"))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if !called {
		t.Fatalf("matching binding: called = %v, status = %d", called, response.StatusCode)
	}

	response, called = serveWith(signer, signed, derive("hash-2"))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if called || response.StatusCode != http.StatusForbidden {
		t.Fatalf("changed binding: called = %v, status = %d", called, response.StatusCode)
	}

	response, called = serveWith(signer, signed)
	t.Cleanup(func() {
		response.Body.Close()
	})

	if called || response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing binding: called = %v, status = %d", called, response.StatusCode)
	}
}

// TestMiddlewareBindRequestErrorRejectsAndPublishes checks that a binding
// lookup failure rejects the request and publishes only safe method metadata.
// Returned-cause preservation is covered through the public middleware seam.
func TestMiddlewareBindRequestErrorRejectsAndPublishes(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got []link.RejectedEvent

	if _, err := bus.Listen(link.Rejected, func(_ context.Context, payload link.RejectedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	signer := mustNewSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	signed := mustTemporary(t, signer, "/reset?user=42", link.Bind([]byte("hash-1")))
	lookupErr := errors.New("no such user")

	response, called := serveWith(signer, signed, func(policy *link.MiddlewareBuilder) {
		policy.BindRequest(func(*http.Request) ([]byte, error) {
			return nil, lookupErr
		})
	})
	t.Cleanup(func() {
		response.Body.Close()
	})

	if called || response.StatusCode != http.StatusForbidden {
		t.Fatalf("called = %v, status = %d, want rejected", called, response.StatusCode)
	}

	if len(got) != 1 || got[0] != (link.RejectedEvent{Method: http.MethodGet}) {
		t.Fatalf("Rejected = %+v, want method metadata only", got)
	}
}

// TestMiddlewareRejectsConsumedLinkWithoutEvent checks that reuse renders the
// used-link problem without emitting an invalid-signature rejection event.
func TestMiddlewareRejectsConsumedLinkWithoutEvent(t *testing.T) {
	t.Parallel()

	bus := event.New()
	published := 0

	if _, err := bus.Listen(link.Rejected, func(context.Context, link.RejectedEvent) error {
		published++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	signer := newCachedSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	signed := mustTemporary(t, signer, "/reset?user=42")

	response, called := serveWith(signer, signed)
	t.Cleanup(func() {
		response.Body.Close()
	})

	if !called {
		t.Fatalf("unused link: status = %d, want handler to run", response.StatusCode)
	}

	if err := signer.Consume(t.Context(), signed); err != nil {
		t.Fatal(err)
	}

	response, called = serveWith(signer, signed)
	t.Cleanup(func() {
		response.Body.Close()
	})

	if called || response.StatusCode != http.StatusForbidden {
		t.Fatalf("consumed link: called = %v, status = %d", called, response.StatusCode)
	}

	if body := readBody(t, response); !strings.Contains(body, link.ErrLinkUsed.Title()) {
		t.Fatalf("body = %s, want %q", body, link.ErrLinkUsed.Title())
	}

	if published != 0 {
		t.Fatalf("published %d Rejected events, want none", published)
	}
}

// TestMiddlewareAllowsPermanentLinkWithCache checks that enabling single-use
// storage does not prevent ordinary permanent links from reaching the handler.
func TestMiddlewareAllowsPermanentLinkWithCache(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)

	response, called := serveWith(signer, mustSign(t, signer, "/files/a.pdf"))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if !called {
		t.Fatalf("status = %d, want handler to run", response.StatusCode)
	}
}

// TestInspectValidLink checks verified metadata for a temporary link,
// including the unsigned application URL and its expiration.
func TestInspectValidLink(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		signer := newCachedSigner(t)
		signed := mustTemporary(t, signer, "/reset?user=42&utm_source=mail")

		details, err := signer.Inspect(t.Context(), signed)

		if err != nil {
			t.Fatalf("Inspect() error = %v", err)
		}

		want := link.Inspection{
			URL:       "https://app.example.com/reset?user=42&utm_source=mail",
			Temporary: true,
			ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second),
		}

		if details != want {
			t.Fatalf("Inspect() = %+v, want %+v", details, want)
		}
	})
}

// TestInspectPermanentLink checks that permanent-link inspection returns the
// application URL without temporary-link state.
func TestInspectPermanentLink(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)

	details, err := signer.InspectURL(t.Context(), mustParseURL(t, mustSign(t, signer, "/files/a.pdf")))

	if err != nil {
		t.Fatalf("InspectURL() error = %v", err)
	}

	want := link.Inspection{
		URL: "https://app.example.com/files/a.pdf",
	}

	if details != want {
		t.Fatalf("InspectURL() = %+v, want %+v", details, want)
	}
}

// TestInspectExpiredLinkReturnsDetails checks that an authentic expired link
// still exposes inspection metadata rather than failing like a forged link.
func TestInspectExpiredLinkReturnsDetails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		signer := mustNewSigner(t)
		signed := mustTemporary(t, signer, "/reset?user=42")
		time.Sleep(2 * time.Hour)

		details, err := signer.Inspect(t.Context(), signed)

		if err != nil {
			t.Fatalf("Inspect() error = %v", err)
		}

		if !details.Expired || !details.Temporary || details.URL != "https://app.example.com/reset?user=42" {
			t.Fatalf("Inspect() = %+v, want expired details", details)
		}
	})
}

// TestInspectForgedLinkReturnsNoDetails checks that failed signature
// verification returns zero inspection metadata, avoiding disclosure of
// unverified state.
func TestInspectForgedLinkReturnsNoDetails(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signed := mustTemporary(t, signer, "/reset?user=42")

	details, err := signer.Inspect(t.Context(), strings.Replace(signed, "user=42", "user=43", 1))

	if !errors.Is(err, link.ErrInvalidSignature) || details != (link.Inspection{}) {
		t.Fatalf("Inspect() = %+v, %v, want zero details and ErrInvalidSignature", details, err)
	}
}

// TestInspectForgedExpiredLinkIsInvalidSignature checks that a forged expired
// link reports signature failure rather than trusting its expiration claim.
func TestInspectForgedExpiredLinkIsInvalidSignature(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		signer := mustNewSigner(t)
		signed := mustTemporary(t, signer, "/reset?user=42")
		time.Sleep(2 * time.Hour)

		_, err := signer.Inspect(t.Context(), strings.Replace(signed, "user=42", "user=43", 1))

		if !errors.Is(err, link.ErrInvalidSignature) {
			t.Fatalf("Inspect() error = %v, want ErrInvalidSignature", err)
		}
	})
}

// TestInspectChecksBinding checks that inspection applies the same state-
// binding requirement as ordinary verification.
func TestInspectChecksBinding(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signed := mustTemporary(t, signer, "/reset", link.Bind([]byte("h")))

	if _, err := signer.Inspect(t.Context(), signed); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Inspect() without binding error = %v, want ErrInvalidSignature", err)
	}

	if _, err := signer.Inspect(t.Context(), signed, link.Bind([]byte("h"))); err != nil {
		t.Fatalf("Inspect() with binding error = %v", err)
	}
}

// TestInspectReportsPreviousSecret checks that inspection distinguishes links
// signed by a previous secret from links signed by the current secret.
func TestInspectReportsPreviousSecret(t *testing.T) {
	t.Parallel()

	oldSigner, err := link.New(oldSecret, func(signer *link.Builder) {
		signer.Origin(testOrigin)
	})

	if err != nil {
		t.Fatal(err)
	}

	signed := mustTemporary(t, oldSigner, "/reset")
	rotated := mustNewSigner(t, func(signer *link.Builder) {
		signer.Previous(oldSecret)
	})

	details, err := rotated.Inspect(t.Context(), signed)

	if err != nil || !details.Previous {
		t.Fatalf("Inspect() = %+v, %v, want Previous", details, err)
	}

	details, err = rotated.Inspect(t.Context(), mustTemporary(t, rotated, "/reset"))

	if err != nil || details.Previous {
		t.Fatalf("Inspect() of a current link = %+v, %v, want not Previous", details, err)
	}
}

// TestInspectReportsConsumed checks that inspection observes the transition
// from unused to consumed after a successful cache-backed consumption.
func TestInspectReportsConsumed(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)
	signed := mustTemporary(t, signer, "/reset")

	if details, err := signer.Inspect(t.Context(), signed); err != nil || details.Consumed {
		t.Fatalf("before Consume: %+v, %v", details, err)
	}

	if err := signer.Consume(t.Context(), signed); err != nil {
		t.Fatal(err)
	}

	if details, err := signer.Inspect(t.Context(), signed); err != nil || !details.Consumed {
		t.Fatalf("after Consume: %+v, %v", details, err)
	}
}

// TestInspectWithoutCacheNeverReportsConsumed checks that inspection without
// single-use storage does not invent consumed state.
func TestInspectWithoutCacheNeverReportsConsumed(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	details, err := signer.Inspect(t.Context(), mustTemporary(t, signer, "/reset"))

	if err != nil || details.Consumed {
		t.Fatalf("Inspect() = %+v, %v, want not consumed", details, err)
	}
}

// readBody returns the response body or fails the test.
func readBody(t *testing.T, response *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}
