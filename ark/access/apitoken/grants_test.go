package apitoken_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access/apitoken"
)

// grantList is a grant function whose answer, error and call count the test
// controls while the system runs.
type grantList struct {
	mu     sync.Mutex
	scopes []string
	err    error
	calls  int
}

// newGrantList returns a grant function that lists scopes.
func newGrantList(scopes ...string) *grantList {
	return &grantList{scopes: scopes}
}

// grant is the function given to [apitoken.Builder.Grants].
func (list *grantList) grant(context.Context, string) ([]string, error) {
	list.mu.Lock()
	defer list.mu.Unlock()
	list.calls++

	return list.scopes, list.err
}

// set replaces the answer and clears the error.
func (list *grantList) set(scopes ...string) {
	list.mu.Lock()
	defer list.mu.Unlock()
	list.scopes, list.err = scopes, nil
}

// fail makes the function return err.
func (list *grantList) fail(err error) {
	list.mu.Lock()
	defer list.mu.Unlock()
	list.err = err
}

// called returns how many times the function ran.
func (list *grantList) called() int {
	list.mu.Lock()
	defer list.mu.Unlock()

	return list.calls
}

// grantsOf configures a system with the catalog posts:read and posts:write
// capped by list, with the given GrantTTL.
func grantsOf(list *grantList, ttl time.Duration) apitoken.BuilderFunc {
	return func(tokens *apitoken.Builder) {
		tokens.Grants(list.grant, "posts:read", "posts:write").GrantTTL(ttl)
	}
}

// grantStore is a cache driver that records the lifetime of the cached grants
// instead of applying it, so a test expires them on demand, and that can fail
// reads or writes of them.
type grantStore struct {
	cache.Driver
	failing    atomic.Bool
	failingPut atomic.Bool

	mu   sync.Mutex
	ttls map[string]time.Duration
}

// newGrantStore returns a grantStore over a memory cache.
func newGrantStore() *grantStore {
	return &grantStore{Driver: memorycache.New(), ttls: map[string]time.Duration{}}
}

// isGrant tells whether key holds a cached grant.
func isGrant(key string) bool {
	return strings.Contains(key, ":apitoken:grants:")
}

// Get fails for cached grants while failing is set.
func (store *grantStore) Get(ctx context.Context, key string) ([]byte, error) {
	if isGrant(key) && store.failing.Load() {
		return nil, errors.New("cache unavailable")
	}

	return store.Driver.Get(ctx, key)
}

// Put keeps cached grants without expiry and remembers the requested lifetime.
func (store *grantStore) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if !isGrant(key) {
		return store.Driver.Put(ctx, key, value, ttl)
	}
	if store.failingPut.Load() {
		return errors.New("cache write failed")
	}
	store.mu.Lock()
	store.ttls[key] = ttl
	store.mu.Unlock()

	return store.Driver.Put(ctx, key, value, 0)
}

// expire drops every cached grant, as their lifetime would have.
func (store *grantStore) expire(t *testing.T) {
	t.Helper()

	store.mu.Lock()
	defer store.mu.Unlock()
	for key := range store.ttls {
		if err := store.Delete(testContext(t), key); err != nil {
			t.Fatalf("expire %s: %v", key, err)
		}
	}
}

// lifetimes returns the lifetimes the cached grants were stored with.
func (store *grantStore) lifetimes() []time.Duration {
	store.mu.Lock()
	defer store.mu.Unlock()
	var ttls []time.Duration
	for _, ttl := range store.ttls {
		ttls = append(ttls, ttl)
	}

	return ttls
}

// TestCreateRefusesScopeNotGranted verifies a scope of the catalog outside the
// user's grant is 403, one outside the catalog stays 422, and a granted one is
// issued.
func TestCreateRefusesScopeNotGranted(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, grantsOf(newGrantList("posts:read"), 0))
		browser := sys.login(t, "1")
		refused := browser.Post("/tokens", `{"name":"CI","scopes":["posts:write"]}`)
		if refused.Status != http.StatusForbidden || !strings.Contains(refused.Body, "apitoken:scope-not-granted") {
			t.Fatalf("ungranted scope = %d %s, want 403 scope-not-granted", refused.Status, refused.Body)
		}
		if unknown := browser.Post("/tokens", `{"name":"CI","scopes":["admin"]}`); unknown.Status != http.StatusUnprocessableEntity {
			t.Fatalf("unknown scope = %d %s, want 422", unknown.Status, unknown.Body)
		}
		createToken(t, browser, `{"name":"CI","scopes":["posts:read"]}`)
	})
}

// TestDeviceTokenGetsCatalogIntersectGrant verifies a device token carries only
// the catalog scopes its user may hold.
func TestDeviceTokenGetsCatalogIntersectGrant(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, grantsOf(newGrantList("posts:write", "not-in-catalog"), 0))
		token, secret, err := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err != nil || strings.Join(token.Scopes, ",") != "posts:write" {
			t.Fatalf("IssueDevice = %+v, %v; want only posts:write", token, err)
		}
		reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret))
		if reply.Body != whoami("1", "posts:write", 0, false) {
			t.Fatalf("whoami = %q", reply.Body)
		}
	})
}

// TestDemotedUserLosesScopeOnNextRequest verifies the default GrantTTL asks the
// grant function on every request.
func TestDemotedUserLosesScopeOnNextRequest(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		list := newGrantList("posts:read", "posts:write")
		sys := newSystem(t, db, instance, grantsOf(list, 0))
		_, secret := createToken(t, sys.login(t, "1"), `{"name":"CI","scopes":["posts:read","posts:write"]}`)
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Body != whoami("1", "posts:read,posts:write", 0, false) {
			t.Fatalf("whoami before the demotion = %q", reply.Body)
		}
		list.set("posts:read")
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Body != whoami("1", "posts:read", 0, false) {
			t.Fatalf("whoami after the demotion = %q, want only posts:read", reply.Body)
		}
	})
}

// TestGrantTTLReusesAnswerUntilItExpires verifies a positive GrantTTL asks the
// grant function once per lifetime, keeps the answer in the shared store for
// that lifetime and refreshes it afterwards.
func TestGrantTTLReusesAnswerUntilItExpires(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		list := newGrantList("posts:read", "posts:write")
		driver := newGrantStore()
		sys := newSystemOn(t, db, instance, cache.New(driver), grantsOf(list, time.Minute))
		token, secret, err := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err != nil || len(token.Scopes) != 2 {
			t.Fatalf("IssueDevice = %+v, %v", token, err)
		}
		atIssue := list.called()
		for range 3 {
			if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Body != whoami("1", "posts:read,posts:write", 0, false) {
				t.Fatalf("whoami = %q", reply.Body)
			}
		}
		if got := list.called() - atIssue; got != 1 {
			t.Fatalf("grant function ran %d times for three requests, want 1", got)
		}
		if ttls := driver.lifetimes(); len(ttls) != 1 || ttls[0] != time.Minute {
			t.Fatalf("cached lifetimes = %v, want one of a minute", ttls)
		}
		list.set("posts:read")
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Body != whoami("1", "posts:read,posts:write", 0, false) {
			t.Fatalf("whoami within the lifetime = %q, want the cached grant", reply.Body)
		}
		driver.expire(t)
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Body != whoami("1", "posts:read", 0, false) {
			t.Fatalf("whoami after the lifetime = %q, want the refreshed grant", reply.Body)
		}
	})
}

// TestGrantTTLIsPerInstance verifies two instances sharing a store keep their
// cached grants apart.
func TestGrantTTLIsPerInstance(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		store := cache.New(memorycache.New())
		wide := newSystemOn(t, db, instance+"_a", store, grantsOf(newGrantList("posts:read", "posts:write"), time.Minute))
		narrow := newSystemOn(t, db, instance+"_b", store, grantsOf(newGrantList("posts:read"), time.Minute))
		_, wideSecret, _ := wide.issuer.IssueDevice(testContext(t), "1", "a")
		_, narrowSecret, _ := narrow.issuer.IssueDevice(testContext(t), "1", "b")
		// The narrow instance caches its answer first; a shared key would hand
		// it to the wide instance next.
		if reply := narrow.client.Do(http.MethodGet, "/whoami", "", bearer(narrowSecret)); reply.Body != whoami("1", "posts:read", 0, false) {
			t.Fatalf("second instance whoami = %q, want its own grant", reply.Body)
		}
		if reply := wide.client.Do(http.MethodGet, "/whoami", "", bearer(wideSecret)); reply.Body != whoami("1", "posts:read,posts:write", 0, false) {
			t.Fatalf("first instance whoami = %q, want its own grant", reply.Body)
		}
	})
}

// TestFixedGrantIgnoresLaterChanges verifies a negative GrantTTL keeps the
// scopes of creation and never calls the grant function again.
func TestFixedGrantIgnoresLaterChanges(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		list := newGrantList("posts:read", "posts:write")
		sys := newSystem(t, db, instance, grantsOf(list, -1))
		_, secret := createToken(t, sys.login(t, "1"), `{"name":"CI","scopes":["posts:read","posts:write"]}`)
		atIssue := list.called()
		list.set()
		list.fail(errors.New("grant source down"))
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Body != whoami("1", "posts:read,posts:write", 0, false) {
			t.Fatalf("whoami = %q, want the scopes of creation", reply.Body)
		}
		if got := list.called(); got != atIssue {
			t.Fatalf("grant function ran %d times after issue, want none", got-atIssue)
		}
	})
}

// TestRotationEndsFixedGrantToken verifies a version change still ends a token
// whose scopes are fixed.
func TestRotationEndsFixedGrantToken(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, grantsOf(newGrantList("posts:read"), -1))
		_, secret, _ := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err := sys.users.Put(auth.UserRecord{Subject: "1", Version: "1"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("whoami after the version changed = %d, want 401", reply.Status)
		}
	})
}

// TestGrantFunctionErrorFailsClosed verifies an error of the grant function
// fails the request and never yields the stored scopes.
func TestGrantFunctionErrorFailsClosed(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		list := newGrantList("posts:read", "posts:write")
		sys := newSystem(t, db, instance, grantsOf(list, 0))
		_, secret, _ := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		list.fail(errors.New("grant source down"))
		reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret))
		if reply.Status != http.StatusUnauthorized || strings.Contains(reply.Body, "posts:") {
			t.Fatalf("whoami with a failing grant function = %d %q, want a 401 and no scopes", reply.Status, reply.Body)
		}
		if _, _, err := sys.issuer.Issue(testContext(t), "1", apitoken.Request{Name: "a", Scopes: []string{"posts:read"}}); err == nil {
			t.Fatal("Issue succeeded with a failing grant function")
		}
	})
}

// TestGrantCacheErrorFailsClosed verifies an unreadable grant cache fails the
// request instead of widening the scopes.
func TestGrantCacheErrorFailsClosed(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		driver := newGrantStore()
		sys := newSystemOn(t, db, instance, cache.New(driver), grantsOf(newGrantList("posts:read"), time.Minute))
		_, secret, _ := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		driver.failing.Store(true)
		reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret))
		if reply.Status != http.StatusUnauthorized || strings.Contains(reply.Body, "posts:") {
			t.Fatalf("whoami with a failing cache = %d %q, want a 401 and no scopes", reply.Status, reply.Body)
		}
	})
}

// TestGrantCacheWriteErrorFailsClosed verifies a grant that was computed but
// could not be cached is not used: the request fails.
func TestGrantCacheWriteErrorFailsClosed(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		driver := newGrantStore()
		sys := newSystemOn(t, db, instance, cache.New(driver), grantsOf(newGrantList("posts:read"), time.Minute))
		_, secret, _ := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		driver.failingPut.Store(true)
		reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret))
		if reply.Status != http.StatusUnauthorized || strings.Contains(reply.Body, "posts:") {
			t.Fatalf("whoami with a cache that cannot store = %d %q, want a 401 and no scopes", reply.Status, reply.Body)
		}
	})
}

// TestWithoutGrantsTokensCarryNoScopes verifies that with no catalog no scope
// can be requested and tokens carry none.
func TestWithoutGrantsTokensCarryNoScopes(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, func(tokens *apitoken.Builder) {
			tokens.Grants(nil)
		})
		if reply := sys.login(t, "1").Post("/tokens", `{"name":"CI","scopes":["posts:read"]}`); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("scope without a catalog = %d %s, want 422", reply.Status, reply.Body)
		}
		_, secret, err := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err != nil {
			t.Fatalf("IssueDevice: %v", err)
		}
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Body != whoami("1", "", 0, false) {
			t.Fatalf("whoami = %q, want no scopes", reply.Body)
		}
	})
}

// TestAnyScopeGrantsTheWholeCatalog verifies AnyScope expands to every catalog
// scope.
func TestAnyScopeGrantsTheWholeCatalog(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		token, _, err := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err != nil || strings.Join(token.Scopes, ",") != "posts:read,posts:write" {
			t.Fatalf("IssueDevice = %+v, %v; want the whole catalog", token, err)
		}
	})
}

// TestStarFromAGrantFunctionIsNotTheWildcard verifies a grant function that
// returns "*" grants no catalog scope: only AnyScope's private marker means the
// whole catalog, so a permission set that contains "*" cannot widen the
// ceiling.
func TestStarFromAGrantFunctionIsNotTheWildcard(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		star := func(context.Context, string) ([]string, error) { return []string{"*"}, nil }
		sys := newSystem(t, db, instance, func(tokens *apitoken.Builder) {
			tokens.Grants(star, "posts:read", "posts:write")
		})
		token, _, err := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err != nil || len(token.Scopes) != 0 {
			t.Fatalf("IssueDevice = %+v, %v; want no scopes", token, err)
		}
		if reply := sys.login(t, "1").Post("/tokens", `{"name":"CI","scopes":["posts:read"]}`); reply.Status != http.StatusForbidden {
			t.Fatalf("a scope granted only by \"*\" = %d %s, want 403", reply.Status, reply.Body)
		}
	})
}

// TestStarIsAScopeNameInTheCatalog verifies "*" can be a catalog scope, granted
// only when the grant function names it.
func TestStarIsAScopeNameInTheCatalog(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		star := func(context.Context, string) ([]string, error) { return []string{"*"}, nil }
		sys := newSystem(t, db, instance, func(tokens *apitoken.Builder) {
			tokens.Grants(star, "*", "posts:write")
		})
		token, _, err := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err != nil || strings.Join(token.Scopes, ",") != "*" {
			t.Fatalf("IssueDevice = %+v, %v; want only the scope \"*\"", token, err)
		}
	})
}
