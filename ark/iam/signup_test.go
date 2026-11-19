package iam_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/internal/apptest"
)

// TestProviderSignUpIsPerProvider verifies one facade lets one provider create
// users while another refuses the same kind of first login.
func TestProviderSignUpIsPerProvider(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Provider(fakeProvider{name: "open"}, signUp)
			identity.Provider(fakeProvider{name: "closed"})
		})
		if reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath("open", "o1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("sign-up provider = %d %s, want 204", reply.Status, reply.Body)
		}
		reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath("closed", "c1", "grace@example.com", true), "")
		if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "federation:no-account") {
			t.Fatalf("provider without sign-up = %d %s, want 403 no-account", reply.Status, reply.Body)
		}
		if _, _, err := sys.identity.Directory().Lookup(testContext(t), "grace@example.com"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Lookup of the refused identity = %v, want ErrUserNotFound", err)
		}
	})
}

// TestProviderSignUpNeedsAProvenEmail verifies a first login with an unproven
// email still fails with federation:email-unverified when sign-up is on.
func TestProviderSignUpNeedsAProvenEmail(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Provider(fakeProvider{name: "fake"}, signUp)
		})
		reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", false), "")
		if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "federation:email-unverified") {
			t.Fatalf("unproven email = %d %s, want 403 email-unverified", reply.Status, reply.Body)
		}
	})
}

// TestProviderCreateIsPerProvider verifies a Create rule runs only for the
// provider it was given to: it refuses one provider's first login while the
// other provider still signs up.
func TestProviderCreateIsPerProvider(t *testing.T) {
	t.Parallel()

	errNotInvited := errors.New("not invited")
	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Provider(fakeProvider{name: "work"}, signUp, func(login *iam.ProviderBuilder) {
				login.Create(func(context.Context, auth.External) (string, error) { return "", errNotInvited })
			})
			identity.Provider(fakeProvider{name: "home"}, signUp)
		})
		if reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath("work", "w1", "ada@example.com", true), ""); reply.Status == http.StatusNoContent {
			t.Fatalf("work sign-up = %d, want the Create refusal", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath("home", "h1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("home sign-up = %d %s, want 204", reply.Status, reply.Body)
		}
	})
}

// TestProviderCreateCallsAddUp verifies a second Create call runs when the
// first declines, in the order added.
func TestProviderCreateCallsAddUp(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var calls []string
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Provider(fakeProvider{name: "fake"}, signUp, func(login *iam.ProviderBuilder) {
				login.Create(func(context.Context, auth.External) (string, error) {
					calls = append(calls, "first")
					return "", nil
				})
				login.Create(func(context.Context, auth.External) (string, error) {
					calls = append(calls, "second")
					return "", nil
				})
			})
		})
		if reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s, want 204", reply.Status, reply.Body)
		}
		if strings.Join(calls, ",") != "first,second" {
			t.Fatalf("Create calls = %v, want first then second", calls)
		}
	})
}

// TestProviderTakeOverWorksWithoutSignUp verifies the owner of a provider-
// verified address still takes over an unverified user through a provider whose
// sign-up is off, next to one whose sign-up is on.
func TestProviderTakeOverWorksWithoutSignUp(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Provider(fakeProvider{name: "open"}, signUp)
			identity.Provider(fakeProvider{name: "closed"})
		})
		register(t, sys.client, "ada@example.com")
		squatted := strings.SplitN(sys.client.Do(http.MethodGet, "/whoami", "").Body, "|", 2)[0]
		owner := sys.client.NewDevice()
		if reply := owner.Do(http.MethodGet, callbackPath("closed", "c1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("owner callback = %d %s, want 204", reply.Status, reply.Body)
		}
		if me := owner.Do(http.MethodGet, "/whoami", ""); !strings.HasPrefix(me.Body, squatted+"|") {
			t.Fatalf("owner /me = %q, want the registered user %s", me.Body, squatted)
		}
	})
}

// TestSignUpWithoutCreateWarnsPerProvider verifies only the provider with
// sign-up is reported when users cannot be created, and the warning names it.
func TestSignUpWithoutCreateWarnsPerProvider(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	_, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.Users(ownUsersWithoutCreate(t))
		identity.Provider(fakeProvider{name: "closed"})
		identity.Provider(fakeProvider{name: "open"}, signUp)
	})
	if err != nil {
		t.Fatalf("New = %v, want the setting ignored", err)
	}
	if got := warnings.Settings(); len(got) != 1 || !strings.Contains(got[0], "open") {
		t.Fatalf("warnings = %v, want one naming the provider open", got)
	}
}

// TestProviderKeepClaimsIsPerProvider verifies one provider keeps its named
// claims on the connection while another keeps nothing, and that the claims
// show up in the connection list.
func TestProviderKeepClaimsIsPerProvider(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Provider(fakeProvider{name: "keeps"}, signUp, func(login *iam.ProviderBuilder) { login.KeepClaims("name") })
			identity.Provider(fakeProvider{name: "forgets"}, signUp)
		})
		for _, name := range []string{"keeps", "forgets"} {
			if reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath(name, name+"1", name+"@example.com", true)+"&name=Ada", ""); reply.Status != http.StatusNoContent {
				t.Fatalf("sign-up with %s = %d %s", name, reply.Status, reply.Body)
			}
		}
		for name, want := range map[string]map[string]any{"keeps": {"name": "Ada"}, "forgets": nil} {
			subject, _, err := sys.identity.Directory().Lookup(testContext(t), name+"@example.com")
			if err != nil {
				t.Fatalf("Lookup %s: %v", name, err)
			}
			listed, err := sys.identity.Links().List(testContext(t), subject)
			if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Claims, want) {
				t.Fatalf("%s connections = %+v, %v; want claims %v", name, listed, err, want)
			}
		}
	})
}

// TestProviderKeepClaimsRefusesProtocolClaims verifies a protocol claim or an
// empty name makes New fail with ErrInvalidConfig.
func TestProviderKeepClaimsRefusesProtocolClaims(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "sub", "nonce"} {
		_, err := newFacade(t, func(identity *iam.Builder) {
			identity.Provider(fakeProvider{name: "fake"}, func(login *iam.ProviderBuilder) { login.KeepClaims(name) })
		})
		if !errors.Is(err, iam.ErrInvalidConfig) {
			t.Errorf("KeepClaims(%q) = %v, want ErrInvalidConfig", name, err)
		}
	}
}
