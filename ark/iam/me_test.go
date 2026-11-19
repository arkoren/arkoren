package iam_test

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/permission"
)

// Account is an application's own user as its user driver stores it. It carries
// fields that must never reach a client.
type Account struct {
	ID               int
	Name, Email      string
	Plan             string
	Disabled         bool
	CredentialsStamp string
}

// PublicAccount is the projection of [Account] that GET /me publishes.
type PublicAccount struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

// ownAccount is the record of the application's user 42.
var ownAccount = Account{ID: 42, Name: "Ada Lovelace", Email: "ada@example.com", Plan: "pro", CredentialsStamp: "s3cret"}

// ownRecord returns the user record of ownAccount with claims and value.
func ownRecord(claims map[string]any, value any) auth.UserRecord {
	return auth.UserRecord{Subject: "42", Version: "0", Claims: claims, Value: value}
}

// ownUsersOf returns the application's users over one record, found by the
// identifier "ada@example.com".
func ownUsersOf(t *testing.T, record auth.UserRecord) iam.Users {
	t.Helper()

	driver := memoryauth.New()
	if err := driver.Put(record); err != nil {
		t.Fatalf("Put: %v", err)
	}
	provider, err := auth.NewUserProvider(driver)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}

	return iam.Users{
		Provider: provider,
		Lookup: func(_ context.Context, identifier string) (string, string, error) {
			if identifier != "ada@example.com" {
				return "", "", auth.ErrUserNotFound
			}

			return record.Subject, identifier, nil
		},
		Rotate: func(context.Context, string) error { return nil },
	}
}

// presentAccount is the projection the tests give MeBuilder.User.
func presentAccount(account Account) PublicAccount {
	return PublicAccount{ID: account.ID, Name: account.Name, Email: account.Email, Plan: account.Plan}
}

// ownApp composes the application's users with password login and the me
// settings, and logs ada@example.com in on a new device.
func ownApp(t *testing.T, record auth.UserRecord, configure ...iam.BuilderFunc) (*app, *apptest.Client) {
	t.Helper()

	sys := newAppWith(t, memoryDatabase(t), newInstance(t), wire{}, append([]iam.BuilderFunc{func(identity *iam.Builder) {
		identity.Users(ownUsersOf(t, record))
		identity.Password(hasher())
	}}, configure...)...)
	if err := sys.identity.Verifier().Set(testContext(t), record.Subject, "correct horse"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	device := sys.client.NewDevice()
	if reply := device.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("login = %d %s", reply.Status, reply.Body)
	}

	return sys, device
}

// directoryApp composes the built-in users with password login and the given
// configuration, creates ada@example.com (verified or not) and logs it in on a
// new device. It returns the subject too.
func directoryApp(t *testing.T, verified bool, configure ...iam.BuilderFunc) (*app, *apptest.Client, string) {
	t.Helper()

	sys := newAppWith(t, memoryDatabase(t), newInstance(t), wire{}, append([]iam.BuilderFunc{func(identity *iam.Builder) {
		identity.Password(hasher())
	}}, configure...)...)
	create := sys.identity.Directory().Create
	if verified {
		create = sys.identity.Directory().CreateVerified
	}
	subject, err := create(testContext(t), "ada@example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := sys.identity.Verifier().Set(testContext(t), subject, "correct horse"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	device := sys.client.NewDevice()
	if reply := device.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("login = %d %s", reply.Status, reply.Body)
	}

	return sys, device, subject
}

// meOf fetches GET /me on device, which must answer 200, and decodes the body.
func meOf(t *testing.T, device *apptest.Client, headers ...string) map[string]any {
	t.Helper()

	reply := device.Do(http.MethodGet, "/me", "", headers...)
	if reply.Status != http.StatusOK {
		t.Fatalf("GET /me = %d %s", reply.Status, reply.Body)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reply.Body), &body); err != nil {
		t.Fatalf("decode %q: %v", reply.Body, err)
	}

	return body
}

// wantMe fails the test unless got equals the JSON document want.
func wantMe(t *testing.T, got map[string]any, want string) {
	t.Helper()

	var expected map[string]any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("decode want %q: %v", want, err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("GET /me = %v, want %v", got, expected)
	}
}

// withoutTimes removes the timestamps of the built-in user member, after
// checking that the verified one has them and the unverified one has only
// created_at.
func withoutTimes(t *testing.T, body map[string]any, verified bool) {
	t.Helper()

	user, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("user = %v, want an object", body["user"])
	}
	for _, name := range []string{"created_at", "verified_at"} {
		_, present := user[name]
		if want := name == "created_at" || verified; present != want {
			t.Fatalf("user.%s present = %v, want %v", name, present, want)
		}
		delete(user, name)
	}
}

// TestMeBuiltInUsers verifies the default response of the built-in users: the
// subject, the email claims and the account.
func TestMeBuiltInUsers(t *testing.T) {
	t.Parallel()

	_, device, subject := directoryApp(t, true, func(identity *iam.Builder) { identity.Me() })
	body := meOf(t, device)
	withoutTimes(t, body, true)
	wantMe(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,
		"user":{"subject":"`+subject+`","email":"ada@example.com"}}`)
}

// TestMeBuiltInUnverifiedEmail verifies an unverified email shows
// email_verified false and no verified_at.
func TestMeBuiltInUnverifiedEmail(t *testing.T) {
	t.Parallel()

	_, device, subject := directoryApp(t, false, func(identity *iam.Builder) { identity.Me() })
	body := meOf(t, device)
	withoutTimes(t, body, false)
	wantMe(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":false,
		"user":{"subject":"`+subject+`","email":"ada@example.com"}}`)
}

// TestMeEmptyListsWhenNothingIsGranted verifies every section on, with nothing
// granted and no authenticator: roles and permissions are [] and never null,
// and two_factor is false.
func TestMeEmptyListsWhenNothingIsGranted(t *testing.T) {
	t.Parallel()

	_, device, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Permissions(func(policy *permission.Builder) { policy.Roles(admin) })
		identity.Me(func(me *iam.MeBuilder) { me.Permissions(true).TwoFactor(true).Login(true) })
	})
	body := meOf(t, device)
	withoutTimes(t, body, true)
	wantMe(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,
		"roles":[],"permissions":[],"two_factor":false,
		"login":{"session":true,"level":"single","methods":["pwd"]},
		"user":{"subject":"`+subject+`","email":"ada@example.com"}}`)
}

// TestMeShowsRolesPermissionsAndTwoFactor verifies the sections with data: the
// expanded roles, the defined permissions, an enrolled authenticator and a
// login with two methods.
func TestMeShowsRolesPermissionsAndTwoFactor(t *testing.T) {
	t.Parallel()

	sys, device, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Permissions(func(policy *permission.Builder) { policy.Roles(editor) })
		identity.Me(func(me *iam.MeBuilder) { me.Permissions(true).TwoFactor(true).Login(true) })
	})
	if err := sys.identity.GrantRole(testContext(t), subject, editor); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	setup := device.Post("/two-factor/setup", "")
	if setup.Status != http.StatusOK {
		t.Fatalf("setup = %d %s", setup.Status, setup.Body)
	}
	secret := secretOf(setup.Body)
	if enable := device.Post("/two-factor", `{"code":"`+code(t, secret, 0)+`"}`); enable.Status != http.StatusOK {
		t.Fatalf("enable = %d %s", enable.Status, enable.Body)
	}
	other := sys.client.NewDevice()
	if login := other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); login.Status != http.StatusAccepted {
		t.Fatalf("login = %d %s", login.Status, login.Body)
	}
	if reply := other.Post("/login/challenge", `{"method":"otp","code":"`+code(t, secret, 30*time.Second)+`"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("challenge = %d %s", reply.Status, reply.Body)
	}
	body := meOf(t, other)
	withoutTimes(t, body, true)
	roles, _ := body["roles"].([]any)
	if !slices.Contains(roles, any("editor")) || !slices.Contains(roles, any("viewer")) {
		t.Fatalf("roles = %v, want editor and the viewer it includes", roles)
	}
	permissions, _ := body["permissions"].([]any)
	if !slices.Contains(permissions, any("posts:read")) || !slices.Contains(permissions, any("posts:write")) {
		t.Fatalf("permissions = %v, want posts:read and posts:write", permissions)
	}
	delete(body, "roles")
	delete(body, "permissions")
	wantMe(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,"two_factor":true,
		"login":{"session":true,"level":"multi","methods":["pwd","otp"]},
		"user":{"subject":"`+subject+`","email":"ada@example.com"}}`)
}

// TestMeOwnUsersWithoutProjectionShowsNoUser verifies the application's users
// give the claims and never a raw record value.
func TestMeOwnUsersWithoutProjectionShowsNoUser(t *testing.T) {
	t.Parallel()

	_, device := ownApp(t, ownRecord(map[string]any{"email": "ada@example.com"}, ownAccount), func(identity *iam.Builder) { identity.Me() })
	wantMe(t, meOf(t, device), `{"subject":"42","email":"ada@example.com"}`)
}

// TestMeOwnUsersProjection verifies User presents the projection and nothing of
// the driver's own struct.
func TestMeOwnUsersProjection(t *testing.T) {
	t.Parallel()

	claims := map[string]any{"email": "ada@example.com", "email_verified": true}
	_, device := ownApp(t, ownRecord(claims, ownAccount), func(identity *iam.Builder) {
		identity.Me(func(me *iam.MeBuilder) { me.User(presentAccount) })
	})
	wantMe(t, meOf(t, device), `{"subject":"42","email":"ada@example.com","email_verified":true,
		"user":{"id":42,"name":"Ada Lovelace","email":"ada@example.com","plan":"pro"}}`)
}

// TestMeOwnUsersEverythingOn verifies every section over the application's
// users, with a projection holding two fields.
func TestMeOwnUsersEverythingOn(t *testing.T) {
	t.Parallel()

	_, device := ownApp(t, ownRecord(nil, ownAccount), func(identity *iam.Builder) {
		identity.Permissions(func(policy *permission.Builder) { policy.Roles(admin) })
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Me(func(me *iam.MeBuilder) {
			me.User(func(account Account) PublicAccount { return PublicAccount{ID: account.ID, Name: account.Name} })
			me.Permissions(true).TwoFactor(true).Login(true)
		})
	})
	body := meOf(t, device)
	user, _ := body["user"].(map[string]any)
	delete(user, "email")
	delete(user, "plan")
	wantMe(t, body, `{"subject":"42","roles":[],"permissions":[],"two_factor":false,
		"login":{"session":true,"level":"single","methods":["pwd"]},
		"user":{"id":42,"name":"Ada Lovelace"}}`)
}

// TestMeEmptyClaimsAndNilValue verifies a record without claims or a value
// shows the subject only, even with a projection configured.
func TestMeEmptyClaimsAndNilValue(t *testing.T) {
	t.Parallel()

	_, device := ownApp(t, ownRecord(nil, nil), func(identity *iam.Builder) {
		identity.Me(func(me *iam.MeBuilder) { me.User(presentAccount) })
	})
	wantMe(t, meOf(t, device), `{"subject":"42"}`)
}

// TestMeUserTypeMismatchIs500 verifies a record value that is not the
// projection's input fails the request instead of leaking it.
func TestMeUserTypeMismatchIs500(t *testing.T) {
	_ = apptest.CaptureWarnings(t)

	_, device := ownApp(t, ownRecord(nil, "not an Account"), func(identity *iam.Builder) {
		identity.Me(func(me *iam.MeBuilder) { me.User(presentAccount) })
	})
	reply := device.Do(http.MethodGet, "/me", "")
	if reply.Status != http.StatusInternalServerError {
		t.Fatalf("GET /me = %d %s, want 500", reply.Status, reply.Body)
	}
	if strings.Contains(reply.Body, "not an Account") {
		t.Fatalf("body %q leaks the record", reply.Body)
	}
}

// TestMeAnonymousIs401 verifies a request without an identity is refused.
func TestMeAnonymousIs401(t *testing.T) {
	t.Parallel()

	sys, _, _ := directoryApp(t, true, func(identity *iam.Builder) { identity.Me() })
	if reply := sys.client.NewDevice().Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /me = %d %s, want 401", reply.Status, reply.Body)
	}
}

// TestMePendingSecondFactorIs401 verifies a login that still waits for its
// second factor is not an identity.
func TestMePendingSecondFactorIs401(t *testing.T) {
	t.Parallel()

	sys, device, _ := directoryApp(t, true, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Me()
	})
	setup := device.Post("/two-factor/setup", "")
	if enable := device.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`); enable.Status != http.StatusOK {
		t.Fatalf("enable = %d %s", enable.Status, enable.Body)
	}
	other := sys.client.NewDevice()
	if login := other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); login.Status != http.StatusAccepted {
		t.Fatalf("login = %d %s", login.Status, login.Body)
	}
	if reply := other.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
		t.Fatalf("pending GET /me = %d %s, want 401", reply.Status, reply.Body)
	}
}

// TestMeTokenRequestIsNotASession verifies a bearer token shows session false,
// level none and no methods.
func TestMeTokenRequestIsNotASession(t *testing.T) {
	t.Parallel()

	sys, _, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.Tokens()
		identity.Me(func(me *iam.MeBuilder) { me.Login(true) })
	})
	_, secret, err := sys.identity.Issuer().Issue(testContext(t), subject, apitoken.Request{Name: "ci"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	body := meOf(t, sys.client.NewDevice(), "Authorization: Bearer "+secret)
	withoutTimes(t, body, true)
	wantMe(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,
		"login":{"session":false,"level":"none","methods":[]},
		"user":{"subject":"`+subject+`","email":"ada@example.com"}}`)
}

// TestMeIgnoresSectionsWhoseFeatureIsOff verifies Permissions and TwoFactor
// without their features, and User with the built-in users, are ignored with
// one warning each and leave the sections out.
func TestMeIgnoresSectionsWhoseFeatureIsOff(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	_, device, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.Me(func(me *iam.MeBuilder) {
			me.Permissions(true).TwoFactor(true).User(presentAccount)
		})
	})
	body := meOf(t, device)
	withoutTimes(t, body, true)
	wantMe(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,
		"user":{"subject":"`+subject+`","email":"ada@example.com"}}`)
	want := []string{"MeBuilder.Permissions", "MeBuilder.TwoFactor", "MeBuilder.User"}
	if got := warnings.Settings(); !slices.Equal(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
}

// TestMeNotCalledMountsNoRoute verifies that without a Me call the route is
// left out and MeHandler panics.
func TestMeNotCalledMountsNoRoute(t *testing.T) {
	t.Parallel()

	never := newApp(t, memoryDatabase(t), newInstance(t))
	absent(t, never, "GET /me")
	if !panics(func() { never.identity.MeHandler() }) {
		t.Fatal("MeHandler did not panic without a Me call")
	}
}
