package iam_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"reflect"
	"slices"
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

// ownAccount is the record of the application's user 42.
var ownAccount = Account{ID: 42, Name: "Ada Lovelace", Email: "ada@example.com", Plan: "pro", CredentialsStamp: "s3cret"}

// ownRecord returns the user record of ownAccount with claims and value.
func ownRecord(claims map[string]any, value any) auth.UserRecord {
	return auth.UserRecord{Subject: "42", Version: "0", Claims: claims, Value: value}
}

// ownUsersOf returns the application's user provider and configuration over one
// record, found by the identifier "ada@example.com".
func ownUsersOf(t *testing.T, record auth.UserRecord) (*auth.UserProvider, iam.UsersBuilderFunc) {
	t.Helper()

	driver := memoryauth.New()
	if err := driver.Put(record); err != nil {
		t.Fatalf("Put: %v", err)
	}
	provider, err := auth.NewUserProvider(driver)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}

	return provider, func(users *iam.UsersBuilder) {
		users.Lookup(func(_ context.Context, identifier string) (string, string, error) {
			if identifier != "ada@example.com" {
				return "", "", auth.ErrUserNotFound
			}

			return record.Subject, identifier, nil
		})
		users.Rotate(func(context.Context, string) error { return nil })
	}
}

// ownApp composes the application's users with password login and the account
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

// accountOf fetches GET /account on device, which must answer 200, and decodes
// the body.
func accountOf(t *testing.T, device *apptest.Client, headers ...string) map[string]any {
	t.Helper()

	reply := device.Do(http.MethodGet, "/account", "", headers...)
	if reply.Status != http.StatusOK {
		t.Fatalf("GET /account = %d %s", reply.Status, reply.Body)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reply.Body), &body); err != nil {
		t.Fatalf("decode %q: %v", reply.Body, err)
	}

	return body
}

// wantAccount fails the test unless got equals the JSON document want.
func wantAccount(t *testing.T, got map[string]any, want string) {
	t.Helper()

	var expected map[string]any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("decode want %q: %v", want, err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("GET /account = %v, want %v", got, expected)
	}
}

// withoutTimes removes the timestamps of a built-in user, after checking that
// the verified one has both and the unverified one has only created_at.
func withoutTimes(t *testing.T, body map[string]any, verified bool) {
	t.Helper()

	for _, name := range []string{"created_at", "verified_at"} {
		_, present := body[name]
		if want := name == "created_at" || verified; present != want {
			t.Fatalf("%s present = %v, want %v", name, present, want)
		}
		delete(body, name)
	}
}

// TestAccountBuiltInUsers verifies the default response of the built-in users:
// the subject, the email facts and the two timestamps.
func TestAccountBuiltInUsers(t *testing.T) {
	t.Parallel()

	_, device, subject := directoryApp(t, true, func(identity *iam.Builder) { identity.Account() })
	body := accountOf(t, device)
	withoutTimes(t, body, true)
	wantAccount(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true}`)
}

// TestAccountBuiltInUnverifiedEmail verifies an unverified email shows
// email_verified false and no verified_at.
func TestAccountBuiltInUnverifiedEmail(t *testing.T) {
	t.Parallel()

	_, device, subject := directoryApp(t, false, func(identity *iam.Builder) { identity.Account() })
	body := accountOf(t, device)
	withoutTimes(t, body, false)
	wantAccount(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":false}`)
}

// TestAccountEmptyListsWhenNothingIsGranted verifies every part on, with nothing
// granted and no authenticator: roles and permissions are [] and never null,
// and two_factor is false.
func TestAccountEmptyListsWhenNothingIsGranted(t *testing.T) {
	t.Parallel()

	_, device, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Permissions(func(policy *permission.Builder) { policy.Roles(admin) })
		identity.Account(func(view *iam.AccountBuilder) { view.Permissions(true).TwoFactor(true).Login(true) })
	})
	body := accountOf(t, device)
	withoutTimes(t, body, true)
	wantAccount(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,
		"roles":[],"permissions":[],"two_factor":false,
		"login":{"session":true,"level":"single","methods":["pwd"]}}`)
}

// TestAccountListsAbsentWhenPermissionsAreOff verifies that with the feature
// present but the setting off, roles and permissions are left out, not [].
func TestAccountListsAbsentWhenPermissionsAreOff(t *testing.T) {
	t.Parallel()

	_, device, _ := directoryApp(t, true, func(identity *iam.Builder) {
		identity.Permissions(func(policy *permission.Builder) { policy.Roles(admin) })
		identity.Account()
	})
	body := accountOf(t, device)
	for _, name := range []string{"roles", "permissions", "two_factor", "login"} {
		if _, present := body[name]; present {
			t.Fatalf("%s is present in %v, want it omitted", name, body)
		}
	}
}

// TestAccountShowsRolesPermissionsAndTwoFactor verifies the parts with data: the
// expanded roles, the defined permissions, an enrolled authenticator and a
// login with two methods.
func TestAccountShowsRolesPermissionsAndTwoFactor(t *testing.T) {
	t.Parallel()

	sys, device, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Permissions(func(policy *permission.Builder) { policy.Roles(editor) })
		identity.Account(func(view *iam.AccountBuilder) { view.Permissions(true).TwoFactor(true).Login(true) })
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
	body := accountOf(t, other)
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
	wantAccount(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,"two_factor":true,
		"login":{"session":true,"level":"multi","methods":["pwd","otp"]}}`)
}

// TestAccountOwnUsersShowClaimsNeverTheValue verifies the application's users
// give the email claims, no timestamps and never a raw record value.
func TestAccountOwnUsersShowClaimsNeverTheValue(t *testing.T) {
	t.Parallel()

	claims := map[string]any{"email": "ada@example.com", "email_verified": true}
	_, device := ownApp(t, ownRecord(claims, ownAccount), func(identity *iam.Builder) { identity.Account() })
	wantAccount(t, accountOf(t, device), `{"subject":"42","email":"ada@example.com","email_verified":true}`)
}

// TestAccountOwnUsersEverythingOn verifies every part over the application's
// users.
func TestAccountOwnUsersEverythingOn(t *testing.T) {
	t.Parallel()

	_, device := ownApp(t, ownRecord(nil, ownAccount), func(identity *iam.Builder) {
		identity.Permissions(func(policy *permission.Builder) { policy.Roles(admin) })
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Account(func(view *iam.AccountBuilder) { view.Permissions(true).TwoFactor(true).Login(true) })
	})
	wantAccount(t, accountOf(t, device), `{"subject":"42","roles":[],"permissions":[],"two_factor":false,
		"login":{"session":true,"level":"single","methods":["pwd"]}}`)
}

// TestAccountEmptyClaimsAndNilValue verifies a record without claims or a value
// shows the subject only.
func TestAccountEmptyClaimsAndNilValue(t *testing.T) {
	t.Parallel()

	_, device := ownApp(t, ownRecord(nil, nil), func(identity *iam.Builder) { identity.Account() })
	wantAccount(t, accountOf(t, device), `{"subject":"42"}`)
}

// TestAccountAnonymousIs401 verifies a request without an identity is refused.
func TestAccountAnonymousIs401(t *testing.T) {
	t.Parallel()

	sys, _, _ := directoryApp(t, true, func(identity *iam.Builder) { identity.Account() })
	if reply := sys.client.NewDevice().Do(http.MethodGet, "/account", ""); reply.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /account = %d %s, want 401", reply.Status, reply.Body)
	}
}

// TestAccountPendingSecondFactorIs401 verifies a login that still waits for its
// second factor is not an identity.
func TestAccountPendingSecondFactorIs401(t *testing.T) {
	t.Parallel()

	sys, device, _ := directoryApp(t, true, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.Account()
	})
	setup := device.Post("/two-factor/setup", "")
	if enable := device.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`); enable.Status != http.StatusOK {
		t.Fatalf("enable = %d %s", enable.Status, enable.Body)
	}
	other := sys.client.NewDevice()
	if login := other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); login.Status != http.StatusAccepted {
		t.Fatalf("login = %d %s", login.Status, login.Body)
	}
	if reply := other.Do(http.MethodGet, "/account", ""); reply.Status != http.StatusUnauthorized {
		t.Fatalf("pending GET /account = %d %s, want 401", reply.Status, reply.Body)
	}
}

// TestAccountTokenRequestIsNotASession verifies a bearer token shows session
// false, level none and no methods.
func TestAccountTokenRequestIsNotASession(t *testing.T) {
	t.Parallel()

	sys, _, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.Tokens()
		identity.Account(func(view *iam.AccountBuilder) { view.Login(true) })
	})
	_, secret, err := sys.identity.Issuer().Issue(testContext(t), subject, apitoken.Request{Name: "ci"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	body := accountOf(t, sys.client.NewDevice(), "Authorization: Bearer "+secret)
	withoutTimes(t, body, true)
	wantAccount(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true,
		"login":{"session":false,"level":"none","methods":[]}}`)
}

// TestAccountIgnoresPartsWhoseFeatureIsOff verifies Permissions and TwoFactor
// without their features are ignored with one warning each and leave the parts
// out.
func TestAccountIgnoresPartsWhoseFeatureIsOff(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	_, device, subject := directoryApp(t, true, func(identity *iam.Builder) {
		identity.Account(func(view *iam.AccountBuilder) { view.Permissions(true).TwoFactor(true) })
	})
	body := accountOf(t, device)
	withoutTimes(t, body, true)
	wantAccount(t, body, `{"subject":"`+subject+`","email":"ada@example.com","email_verified":true}`)
	want := []string{"AccountBuilder.Permissions", "AccountBuilder.TwoFactor"}
	if got := warnings.Settings(); !slices.Equal(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
}

// TestAccountMethodWorksWithoutBuilderAccount verifies IAM.Account answers with
// the subject and the email facts when Builder.Account was never called, and
// that the route is absent.
func TestAccountMethodWorksWithoutBuilderAccount(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), newInstance(t))
	record := ownRecord(map[string]any{"email": "ada@example.com"}, nil)
	ctx := auth.NewContext(testContext(t), auth.NewIdentity(record.Subject, func(principal *auth.IdentityBuilder) {
		principal.Record(record)
	}))
	view, err := sys.identity.Account(ctx)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	want := iam.AccountView{Subject: "42", Email: "ada@example.com"}
	if !reflect.DeepEqual(view, want) {
		t.Fatalf("Account = %+v, want %+v", view, want)
	}
	absent(t, sys, "GET /account")
}

// TestAccountMethodWithoutIdentityIsUnauthenticated verifies a context without
// an identity returns auth.ErrUnauthenticated.
func TestAccountMethodWithoutIdentityIsUnauthenticated(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), newInstance(t))
	if _, err := sys.identity.Account(context.Background()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Account = %v, want auth.ErrUnauthenticated", err)
	}
}

// TestAccountNotCalledMountsNoRoute verifies that without an Account call the
// route is left out and AccountHandler panics.
func TestAccountNotCalledMountsNoRoute(t *testing.T) {
	t.Parallel()

	never := newApp(t, memoryDatabase(t), newInstance(t))
	absent(t, never, "GET /account")
	if !panics(func() { never.identity.AccountHandler() }) {
		t.Fatal("AccountHandler did not panic without an Account call")
	}
}
