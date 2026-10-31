package auth_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// TestLogoutDeletesWronglyTypedIdentifier preserves fail-closed logout even
// when stale data has the wrong Go type, while retaining unrelated state.
func TestLogoutDeletesWronglyTypedIdentifier(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), map[string]any{
		"arkoren:auth:user": struct{ ID int }{ID: 7},
		"theme":             "dark",
	})
	id := current.ID()

	if err := auth.NewSessionIdentity(loadByID).Logout(current); err != nil {
		t.Fatal(err)
	}

	if current.ID() == id || !current.HasRegenerated() {
		t.Fatal("logout did not rotate the session")
	}

	if _, ok := current.All()["arkoren:auth:user"]; ok {
		t.Fatal("logout retained the stale identifier")
	}

	if current.All()["theme"] != "dark" {
		t.Fatal("logout removed unrelated state")
	}
}

// TestIdentityReadsPhysicalLegacyRecord loads an actual pre-definition session
// record through middleware. It preserves the physical namespace, JSON member,
// lack of type tags and exact integer decoding from persisted raw bytes.
func TestIdentityReadsPhysicalLegacyRecord(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorycache.New()
		const id = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		const fixture = `{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","created_at":"2000-01-01T00:00:00Z","expires_at":"2000-01-01T02:00:00Z","storage":{"arkoren:auth:user":9007199254740993,"theme":"dark"}}`

		if err := driver.Put(t.Context(), "arkoren:session:"+id, []byte(fixture), time.Hour); err != nil {
			t.Fatal(err)
		}

		identity := auth.NewSessionIdentity(func(_ context.Context, key int64) (int64, error) {
			return key, nil
		})

		handler := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
			key, err := identity.Authenticator()(r)

			if err != nil || key != 9007199254740993 {
				t.Fatalf("legacy identifier = %d, error = %v", key, err)
			}

			current, err := identity.Session(r.Context())

			if err != nil {
				return err
			}

			if current.ID() != id {
				t.Fatal("legacy record was replaced")
			}

			return nil
		}))

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: session.HostPrefix + session.DefaultCookie, Value: id})

		if response := handler.Record(r); response.StatusCode != http.StatusNoContent {
			t.Fatal(response.StatusCode)
		}
	})
}

// TestIdentityLoginPersistsExistingMember verifies typed login still writes the
// unwrapped identifier under the existing physical name, not a new envelope.
func TestIdentityLoginPersistsExistingMember(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	identity := auth.NewSessionIdentity(func(_ context.Context, key int64) (int64, error) {
		return key, nil
	})

	var id string
	handler := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		current, err := identity.Session(r.Context())

		if err != nil {
			return err
		}

		if err := identity.Login(current, 9007199254740993); err != nil {
			return err
		}

		id = current.ID()

		return nil
	}))

	if response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil)); response.StatusCode != http.StatusNoContent {
		t.Fatal(response.StatusCode)
	}

	raw, err := driver.Get(t.Context(), "arkoren:session:"+id)

	if err != nil {
		t.Fatal(err)
	}

	var stored struct {
		// Storage retains the physical JSON object without numeric conversion.
		Storage jsontext.Value `json:"storage"`
	}

	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}

	if string(stored.Storage) != `{"arkoren:auth:user":9007199254740993}` {
		t.Fatalf("stored identity = %s", stored.Storage)
	}
}
