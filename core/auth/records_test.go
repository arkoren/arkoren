package auth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// device logs subject in on a new session that presents agent, and returns the
// session and a request carrying it.
func device(t *testing.T, login *auth.Login, subject, agent string) (*session.Session, *http.Request) {
	t.Helper()

	current := newSession()
	r := sessionRequest(current)
	r.Header.Set("User-Agent", agent)
	r.RemoteAddr = "203.0.113.7:4711"
	begin(t, login, r, subject)

	return current, r
}

// loginsOf lists the records of subject and fails the test on error.
func loginsOf(t *testing.T, login *auth.Login, subject string) []auth.LoginRecord {
	t.Helper()

	records, err := login.LoginsOf(t.Context(), subject)

	if err != nil {
		t.Fatal(err)
	}

	return records
}

// TestLoginCreatesRecord verifies a login stores one record with the agent and
// the host of the remote address.
func TestLoginCreatesRecord(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	device(t, login, "7", "Firefox")

	records := loginsOf(t, login, "7")

	if len(records) != 1 || records[0].UserAgent != "Firefox" || records[0].IP != "203.0.113.7" || records[0].ID == "" || records[0].Current {
		t.Fatalf("records = %+v", records)
	}

	if records[0].CreatedAt.IsZero() || !records[0].SeenAt.Equal(records[0].CreatedAt) {
		t.Fatalf("times = %v %v", records[0].CreatedAt, records[0].SeenAt)
	}
}

// TestCompleteCreatesRecord verifies a login finished with a second factor
// creates its record at Complete, not at Begin.
func TestCompleteCreatesRecord(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t, withChecks(secondFactor(true, nil)))
	current := newSession()
	r := sessionRequest(current)

	if begin(t, login, r, "7") {
		t.Fatal("login completed without second factor")
	}

	if len(loginsOf(t, login, "7")) != 0 {
		t.Fatal("pending login has a record")
	}

	if err := login.Complete(r, password); err != nil {
		t.Fatal(err)
	}

	if len(loginsOf(t, login, "7")) != 1 {
		t.Fatal("completed login has no record")
	}

	if _, err := authenticate(login, current); err != nil {
		t.Fatal(err)
	}
}

// TestLoginsMarksCurrent verifies Logins flags the request's own login only.
func TestLoginsMarksCurrent(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	_, first := device(t, login, "7", "Firefox")
	device(t, login, "7", "Safari")

	records, err := login.Logins(first)

	if err != nil {
		t.Fatal(err)
	}

	if len(records) != 2 || !records[0].Current || records[1].Current || records[0].UserAgent != "Firefox" {
		t.Fatalf("records = %+v", records)
	}
}

// TestLoginsRequiresLogin verifies Logins and RevokeOthers need a logged-in
// session.
func TestLoginsRequiresLogin(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	r := sessionRequest(newSession())

	if _, err := login.Logins(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Logins: %v", err)
	}

	if err := login.RevokeOthers(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("RevokeOthers: %v", err)
	}

	if _, err := login.Logins(httpRequest()); !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("no session: %v", err)
	}
}

// httpRequest returns a request without a session.
func httpRequest() *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)

	return r
}

// TestRevokedDeviceIsAnonymous verifies a revoked device is anonymous on its
// next request while the others keep working.
func TestRevokedDeviceIsAnonymous(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	first, _ := device(t, login, "7", "Firefox")
	second, _ := device(t, login, "7", "Safari")
	records := loginsOf(t, login, "7")

	if err := login.Revoke(t.Context(), "7", records[0].ID, "unknown"); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(login, first); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("revoked device: %v", err)
	}

	if _, err := authenticate(login, second); err != nil {
		t.Fatalf("other device: %v", err)
	}
}

// TestRevokeOthersKeepsCurrent verifies RevokeOthers leaves only the request's
// own login.
func TestRevokeOthersKeepsCurrent(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	first, firstRequest := device(t, login, "7", "Firefox")
	second, _ := device(t, login, "7", "Safari")

	if err := login.RevokeOthers(firstRequest); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(login, first); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(login, second); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("other device: %v", err)
	}
}

// TestRevokeAllLogsEveryDeviceOut verifies RevokeAll ends every login of the
// subject only, and that revoking an unknown subject is not an error.
func TestRevokeAllLogsEveryDeviceOut(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	first, _ := device(t, login, "7", "Firefox")
	device(t, login, "7", "Safari")
	other, _ := device(t, login, "9", "Chrome")

	if err := login.RevokeAll(t.Context(), "7"); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(login, first); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("revoked device: %v", err)
	}

	if _, err := authenticate(login, other); err != nil {
		t.Fatalf("other subject: %v", err)
	}

	if err := login.Revoke(t.Context(), "nobody", "x"); err != nil {
		t.Fatal(err)
	}

	if err := login.RevokeAll(t.Context(), "nobody"); err != nil {
		t.Fatal(err)
	}
}

// TestRecordsRejectInvalidSubject verifies the subject-taking methods validate
// their subject.
func TestRecordsRejectInvalidSubject(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)

	if _, err := login.LoginsOf(t.Context(), ""); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("LoginsOf: %v", err)
	}

	if err := login.Revoke(t.Context(), "a b", "x"); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("Revoke: %v", err)
	}

	if err := login.RevokeAll(t.Context(), ""); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("RevokeAll: %v", err)
	}
}

// TestRotationKeepsRecord verifies the record ID survives session ID rotation.
func TestRotationKeepsRecord(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	current, r := device(t, login, "7", "Firefox")
	before := loginsOf(t, login, "7")[0].ID
	id := current.ID()

	if err := login.Confirm(r, password); err != nil {
		t.Fatal(err)
	}

	records, err := login.Logins(r)

	if current.ID() == id || err != nil || len(records) != 1 || records[0].ID != before || !records[0].Current {
		t.Fatalf("rotated = %v, records = %+v, err = %v", current.ID() != id, records, err)
	}

	if _, err := authenticate(login, current); err != nil {
		t.Fatal(err)
	}
}

// TestLogoutRemovesRecord verifies Logout removes only the current record.
func TestLogoutRemovesRecord(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	_, first := device(t, login, "7", "Firefox")
	second, _ := device(t, login, "7", "Safari")

	if err := login.Logout(first); err != nil {
		t.Fatal(err)
	}

	records := loginsOf(t, login, "7")

	if len(records) != 1 || records[0].UserAgent != "Safari" {
		t.Fatalf("records = %+v", records)
	}

	if _, err := authenticate(login, second); err != nil {
		t.Fatal(err)
	}
}

// TestLoginAgainReplacesRecord verifies logging in again on a session replaces
// its record instead of leaving a phantom device.
func TestLoginAgainReplacesRecord(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	_, r := device(t, login, "7", "Firefox")
	begin(t, login, r, "7")

	if records := loginsOf(t, login, "7"); len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
}

// TestParkingLoginReleasesRecord verifies a logged-in session that starts a
// second-factor login loses its record.
func TestParkingLoginReleasesRecord(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	store := cache.New(memorycache.New())
	plain, err := auth.NewLogin(provider, store)

	if err != nil {
		t.Fatal(err)
	}

	strict, err := auth.NewLogin(provider, store, withChecks(secondFactor(true, nil)))

	if err != nil {
		t.Fatal(err)
	}

	_, r := device(t, plain, "7", "Firefox")

	if begin(t, strict, r, "7") {
		t.Fatal("login completed without second factor")
	}

	if len(loginsOf(t, plain, "7")) != 0 {
		t.Fatal("record survived")
	}
}

// TestSeenAtIsThrottled verifies SeenAt is refreshed at most once per five
// minutes.
func TestSeenAtIsThrottled(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		login, _ := newLogin(t)
		current, _ := device(t, login, "7", "Firefox")
		created := loginsOf(t, login, "7")[0].SeenAt

		time.Sleep(4 * time.Minute)

		if _, err := authenticate(login, current); err != nil {
			t.Fatal(err)
		}

		if seen := loginsOf(t, login, "7")[0].SeenAt; !seen.Equal(created) {
			t.Fatalf("SeenAt changed to %v within five minutes", seen)
		}

		time.Sleep(2 * time.Minute)

		if _, err := authenticate(login, current); err != nil {
			t.Fatal(err)
		}

		if seen := loginsOf(t, login, "7")[0].SeenAt; !seen.Equal(created.Add(6 * time.Minute)) {
			t.Fatalf("SeenAt = %v, want %v", seen, created.Add(6*time.Minute))
		}
	})
}

// TestDevicesFalseStoresNoDeviceData verifies Devices(false) keeps no agent or
// address.
func TestDevicesFalseStoresNoDeviceData(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t, func(policy *auth.LoginBuilder) { policy.Devices(false) })
	device(t, login, "7", "Firefox")

	if records := loginsOf(t, login, "7"); len(records) != 1 || records[0].UserAgent != "" || records[0].IP != "" {
		t.Fatalf("records = %+v", records)
	}
}

// TestUserAgentIsTruncated verifies a long agent is cut to 255 bytes without
// splitting a character.
func TestUserAgentIsTruncated(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	device(t, login, "7", strings.Repeat("a", 254)+"é"+"tail")

	if agent := loginsOf(t, login, "7")[0].UserAgent; agent != strings.Repeat("a", 254) {
		t.Fatalf("agent = %q (%d bytes)", agent, len(agent))
	}
}

// TestClientIPSetting verifies a custom address function replaces the default.
func TestClientIPSetting(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t, func(policy *auth.LoginBuilder) {
		policy.ClientIP(func(r *http.Request) string { return r.Header.Get("X-Real-IP") })
	})

	current := newSession()
	r := sessionRequest(current)
	r.Header.Set("X-Real-IP", "198.51.100.2")
	begin(t, login, r, "7")

	if ip := loginsOf(t, login, "7")[0].IP; ip != "198.51.100.2" {
		t.Fatalf("ip = %q", ip)
	}
}

// TestMaxLoginsDropsOldest verifies the cap drops the oldest record, which logs
// that device out.
func TestMaxLoginsDropsOldest(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t, func(policy *auth.LoginBuilder) { policy.MaxLogins(2) })
	first, _ := device(t, login, "7", "one")
	device(t, login, "7", "two")
	third, _ := device(t, login, "7", "three")

	records := loginsOf(t, login, "7")

	if len(records) != 2 || records[0].UserAgent != "two" || records[1].UserAgent != "three" {
		t.Fatalf("records = %+v", records)
	}

	if _, err := authenticate(login, first); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("dropped device: %v", err)
	}

	if _, err := authenticate(login, third); err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentLoginsAreAllRecorded verifies no login is lost when many
// devices of one user log in at once.
func TestConcurrentLoginsAreAllRecorded(t *testing.T) {
	t.Parallel()

	const devices = 40

	login, _ := newLogin(t, func(policy *auth.LoginBuilder) { policy.MaxLogins(100) })

	var wait sync.WaitGroup

	for range devices {
		wait.Go(func() {
			r := sessionRequest(newSession())

			if _, err := login.Begin(r, "7", password); err != nil {
				t.Error(err)
			}
		})
	}

	wait.Wait()

	if records := loginsOf(t, login, "7"); len(records) != devices {
		t.Fatalf("lost updates: %d of %d", len(records), devices)
	}
}

// TestRecordsExpireWithSessionLifetime verifies the record list lives for the
// configured session lifetime, and a device whose list expired is anonymous.
func TestRecordsExpireWithSessionLifetime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		login, _ := newLogin(t, func(policy *auth.LoginBuilder) { policy.SessionLifetime(time.Hour) })
		current, _ := device(t, login, "7", "Firefox")

		time.Sleep(59 * time.Minute)

		if len(loginsOf(t, login, "7")) != 1 {
			t.Fatal("record expired early")
		}

		time.Sleep(2 * time.Minute)

		if len(loginsOf(t, login, "7")) != 0 {
			t.Fatal("record outlived the lifetime")
		}

		if _, err := authenticate(login, current); !errors.Is(err, auth.ErrNoCredentials) {
			t.Fatalf("authenticate: %v", err)
		}
	})
}

// TestForgetRevokesAllLogins verifies forgetting a user ends their sessions.
func TestForgetRevokesAllLogins(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	login, err := auth.NewLogin(provider, cache.New(memorycache.New()))

	if err != nil {
		t.Fatal(err)
	}

	current, _ := device(t, login, "7", "Firefox")

	if err := provider.Forget(t.Context(), "7"); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(login, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("authenticate: %v", err)
	}
}

// TestLegacySessionWithoutRecordIsAnonymous verifies a session record without a
// login ID, as written before login records, is anonymous.
func TestLegacySessionWithoutRecordIsAnonymous(t *testing.T) {
	t.Parallel()

	login, _ := newLogin(t)
	current := session.New(time.Now().Add(time.Hour), map[string]any{
		"arkoren:auth:session": map[string]any{"subject": "7", "version": "x"},
	})

	if _, err := authenticate(login, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("authenticate: %v", err)
	}
}

// brokenStore is a cache driver whose reads fail.
type brokenStore struct {
	cache.Driver
}

// Get always fails.
func (brokenStore) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("down")
}

// TestAuthenticatorPassesStoreErrors verifies a failing store is an error, not
// an anonymous request.
func TestAuthenticatorPassesStoreErrors(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	store := cache.New(memorycache.New())
	login, err := auth.NewLogin(provider, store)

	if err != nil {
		t.Fatal(err)
	}

	current, _ := device(t, login, "7", "Firefox")

	broken, err := auth.NewLogin(provider, cache.New(brokenStore{memorycache.New()}))

	if err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(broken, current); err == nil || errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("authenticate: %v", err)
	}
}

// TestNewLoginRejectsInvalidRecordSettings verifies unusable record settings are
// construction errors.
func TestNewLoginRejectsInvalidRecordSettings(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	store := cache.New(memorycache.New())

	for _, configure := range []auth.LoginBuilderFunc{
		func(policy *auth.LoginBuilder) { policy.MaxLogins(0) },
		func(policy *auth.LoginBuilder) { policy.SessionLifetime(0) },
		func(policy *auth.LoginBuilder) { policy.ClientIP(nil) },
	} {
		if login, err := auth.NewLogin(provider, store, configure); err == nil || login != nil {
			t.Errorf("login = %v, err = %v", login, err)
		}
	}
}
