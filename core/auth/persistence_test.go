package auth_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// runStep sends one request through session middleware over driver with the
// session cookie id (none when empty), runs step on it and returns the session
// ID after the step, which is the ID the middleware persisted.
func runStep(t *testing.T, driver cache.Driver, id string, step func(*http.Request)) string {
	t.Helper()

	var after string

	handler := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		step(r)

		current, _ := session.FromContextFor(r.Context(), session.Handle{})
		after = current.ID()

		return nil
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)

	if id != "" {
		r.AddCookie(&http.Cookie{Name: session.HostPrefix + session.DefaultCookie, Value: id})
	}

	if response := handler.Record(r); response.StatusCode != http.StatusNoContent {
		t.Fatal(response.StatusCode)
	}

	return after
}

// TestLoginSurvivesReload verifies the single session record and the pending
// login round-trip through real session persistence across requests.
func TestLoginSurvivesReload(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorycache.New()
		login, _ := newLogin(t, func(policy *auth.LoginBuilder) {
			policy.SecondFactor(secondFactor(true, nil))
		})

		id := runStep(t, driver, "", func(r *http.Request) {
			if complete, err := login.Begin(r, "7", password); err != nil || complete {
				t.Fatalf("complete = %v, err = %v", complete, err)
			}
		})

		id = runStep(t, driver, id, func(r *http.Request) {
			if _, err := login.Authenticator()(r); err == nil {
				t.Fatal("pending login authenticated after reload")
			}

			if waiting, err := login.Pending(r); err != nil || waiting.Subject != "7" {
				t.Fatalf("pending = %+v, %v", waiting, err)
			}

			if err := login.Complete(r, otp); err != nil {
				t.Fatal(err)
			}
		})

		runStep(t, driver, id, func(r *http.Request) {
			identity, err := login.Authenticator()(r)

			if err != nil || identity.Subject() != "7" || identity.Level() != auth.LevelMulti || len(identity.Factors()) != 2 {
				t.Fatalf("identity = %q, %v, %v", identity.Subject(), identity.Level(), err)
			}
		})
	})
}

// TestLoginPersistsOneRecord verifies the exact stored bytes: one session
// record under one key with subject, factors and the hashed credential version
// (SHA-256 of "v1"), and nothing else. A change to this shape ends every
// existing login.
func TestLoginPersistsOneRecord(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorycache.New()
		login, _ := newLogin(t)

		id := runStep(t, driver, "", func(r *http.Request) {
			if _, err := login.Begin(r, "7", password); err != nil {
				t.Fatal(err)
			}
		})

		raw, err := driver.Get(t.Context(), "arkoren:session:"+id)

		if err != nil {
			t.Fatal(err)
		}

		var stored struct {
			// Storage retains the physical JSON object.
			Storage jsontext.Value `json:"storage"`
		}

		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}

		const want = `{"arkoren:auth:session":{"subject":"7","factors":[{"method":"pwd","kind":"knowledge","at":"2000-01-01T00:00:00Z"}],"version":"3bfc269594ef649228e9a74bab00f042efc91d5acc6fbee31a382e80d42388fe","login_id":"ID"}}`

		got := regexp.MustCompile(`"login_id":"[A-Z2-7]{26}"`).ReplaceAllString(string(stored.Storage), `"login_id":"ID"`)

		if got != want {
			t.Fatalf("stored session = %s", stored.Storage)
		}
	})
}

// TestLoginIgnoresLegacyRecord verifies a session persisted by the former
// "arkoren:auth:user" shape is anonymous, not an error and not a login.
func TestLoginIgnoresLegacyRecord(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorycache.New()
		login, _ := newLogin(t)

		const id = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		const fixture = `{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","created_at":"2000-01-01T00:00:00Z","expires_at":"2000-01-01T02:00:00Z","storage":{"arkoren:auth:user":7,"theme":"dark"}}`

		if err := driver.Put(t.Context(), "arkoren:session:"+id, []byte(fixture), time.Hour); err != nil {
			t.Fatal(err)
		}

		runStep(t, driver, id, func(r *http.Request) {
			if _, err := login.Authenticator()(r); err == nil {
				t.Fatal("legacy session authenticated")
			}
		})
	})
}
