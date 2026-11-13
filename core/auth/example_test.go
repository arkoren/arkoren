package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// exampleUser is the application user of the examples.
type exampleUser struct {
	ID        int64
	Role      string
	Suspended bool
}

// exampleDriver builds a typed driver over a fixed set of users.
func exampleDriver() *auth.TypedUserDriver[int64, exampleUser] {
	users := map[int64]exampleUser{1: {ID: 1, Role: "admin"}, 2: {ID: 2, Suspended: true}}

	driver, err := auth.NewUserDriver(
		func(_ context.Context, id int64) (exampleUser, error) {
			if user, ok := users[id]; ok {
				return user, nil
			}

			return exampleUser{}, auth.ErrUserNotFound
		},
		func(user exampleUser) int64 { return user.ID },
		func(driver *auth.UserDriverBuilder[exampleUser]) {
			driver.Claims(func(user exampleUser) map[string]any { return map[string]any{"role": user.Role} })
			driver.Disabled(func(user exampleUser) bool { return user.Suspended })
		},
	)

	if err != nil {
		panic(err)
	}

	return driver
}

func ExampleNewUserProvider() {
	users, err := auth.NewUserProvider(exampleDriver(), func(users *auth.UserProviderBuilder) {
		users.Events(event.New())
	})

	if err != nil {
		panic(err)
	}

	record, err := users.Find(context.Background(), "1")

	fmt.Println(record.Subject, record.Claims["role"], err)
	// Output: 1 admin <nil>
}

func ExampleUserProvider_Find() {
	users, _ := auth.NewUserProvider(exampleDriver())

	_, err := users.Find(context.Background(), "2")
	fmt.Println(errors.Is(err, auth.ErrUserDisabled))

	_, err = users.Find(context.Background(), "3")
	fmt.Println(errors.Is(err, auth.ErrUserNotFound))
	// Output:
	// true
	// true
}

func ExampleUserProvider_OnForget() {
	users, _ := auth.NewUserProvider(exampleDriver())

	users.OnForget(func(_ context.Context, subject string) error {
		fmt.Println("delete subscriptions of", subject)

		return nil
	})
	users.OnForget(func(_ context.Context, subject string) error {
		fmt.Println("delete sessions of", subject)

		return nil
	})

	_ = users.Forget(context.Background(), "1")
	// Output:
	// delete subscriptions of 1
	// delete sessions of 1
}

func ExampleUserProvider_Forget() {
	events := event.New()
	users, _ := auth.NewUserProvider(exampleDriver(), func(users *auth.UserProviderBuilder) {
		users.Events(events)
	})

	_, _ = events.Listen(auth.UserForgotten, func(_ context.Context, forgotten auth.UserForgottenEvent) error {
		fmt.Println("forgotten:", forgotten.Subject)

		return nil
	})

	fmt.Println(users.Forget(context.Background(), "1"))
	// Output:
	// forgotten: 1
	// <nil>
}

func ExampleUserProviderBuilder_Events() {
	events := event.New()
	users, _ := auth.NewUserProvider(exampleDriver(), func(users *auth.UserProviderBuilder) {
		users.Events(events)
	})

	_, _ = events.Listen(auth.UserForgotten, func(context.Context, auth.UserForgottenEvent) error {
		fmt.Println("event received")

		return nil
	})

	_ = users.Forget(context.Background(), "1")
	// Output: event received
}

func ExampleUserProviderBuilderFunc() {
	policy := auth.UserProviderBuilderFunc(func(users *auth.UserProviderBuilder) {
		users.Events(event.New())
	})

	users, err := auth.NewUserProvider(exampleDriver(), policy)

	fmt.Println(users != nil, err)
	// Output: true <nil>
}

func ExampleUserForgotten() {
	events := event.New()

	_, _ = events.Listen(auth.UserForgotten, func(_ context.Context, forgotten auth.UserForgottenEvent) error {
		fmt.Println(forgotten.Subject)

		return nil
	})

	events.Emit(context.Background(), auth.UserForgotten, auth.UserForgottenEvent{Subject: "42"})
	// Output: 42
}

func ExampleUserForgottenEvent() {
	forgotten := auth.UserForgottenEvent{Subject: "42"}

	fmt.Println(auth.UserForgotten.Name(), forgotten.Subject)
	// Output: auth.user.forgotten 42
}

func ExampleUserRecord() {
	record, _ := exampleDriver().Find(context.Background(), "1")

	fmt.Println(record.Subject, record.Claims["role"], record.Value.(exampleUser).ID)
	// Output: 1 admin 1
}

func ExampleUserDriver() {
	var driver auth.UserDriver = memoryauth.New()

	_, err := driver.Find(context.Background(), "1")

	fmt.Println(errors.Is(err, auth.ErrUserNotFound))
	// Output: true
}

func ExampleErrUserNotFound() {
	users, _ := auth.NewUserProvider(exampleDriver())

	_, err := users.Find(context.Background(), "3")

	fmt.Println(errors.Is(err, auth.ErrUserNotFound))
	// Output: true
}

func ExampleErrUserDisabled() {
	users, _ := auth.NewUserProvider(exampleDriver())

	_, err := users.Find(context.Background(), "2")

	fmt.Println(errors.Is(err, auth.ErrUserDisabled))
	// Output: true
}

func ExampleErrInvalidSubject() {
	users, _ := auth.NewUserProvider(exampleDriver())

	_, err := users.Find(context.Background(), "007")

	fmt.Println(errors.Is(err, auth.ErrInvalidSubject))
	// Output: true
}

func ExampleValidateSubject() {
	fmt.Println(auth.ValidateSubject("usr_9f1c"))
	fmt.Println(errors.Is(auth.ValidateSubject("has space"), auth.ErrInvalidSubject))
	// Output:
	// <nil>
	// true
}

func ExampleNewUserDriver() {
	driver, err := auth.NewUserDriver(
		func(_ context.Context, id int64) (exampleUser, error) { return exampleUser{ID: id}, nil },
		func(user exampleUser) int64 { return user.ID },
	)

	if err != nil {
		panic(err)
	}

	record, _ := driver.Find(context.Background(), "9")

	fmt.Println(record.Subject)
	// Output: 9
}

func ExampleTypedUserDriver() {
	driver := exampleDriver()

	record, err := driver.Find(context.Background(), "1")

	fmt.Println(record.Value.(exampleUser).Role, err)
	// Output: admin <nil>
}

func ExampleTypedUserDriver_Subject() {
	subject, err := exampleDriver().Subject(exampleUser{ID: 42})

	fmt.Println(subject, err)
	// Output: 42 <nil>
}

func ExampleTypedUserDriver_Find() {
	_, err := exampleDriver().Find(context.Background(), "+1")

	fmt.Println(errors.Is(err, auth.ErrInvalidSubject))
	// Output: true
}

func ExampleUserDriverBuilder() {
	driver, _ := auth.NewUserDriver(
		func(_ context.Context, id int64) (exampleUser, error) { return exampleUser{ID: id}, nil },
		func(user exampleUser) int64 { return user.ID },
		func(driver *auth.UserDriverBuilder[exampleUser]) {
			driver.Version(func(user exampleUser) string { return "v1" }).
				Claims(func(user exampleUser) map[string]any { return map[string]any{"id": user.ID} }).
				Disabled(func(user exampleUser) bool { return false })
		},
	)

	record, _ := driver.Find(context.Background(), "5")

	fmt.Println(record.Version, record.Claims["id"])
	// Output: v1 5
}

func ExampleUserDriverBuilder_Version() {
	driver, _ := auth.NewUserDriver(
		func(_ context.Context, id int64) (exampleUser, error) { return exampleUser{ID: id}, nil },
		func(user exampleUser) int64 { return user.ID },
		func(driver *auth.UserDriverBuilder[exampleUser]) {
			driver.Version(func(user exampleUser) string { return fmt.Sprint("v", user.ID) })
		},
	)

	record, _ := driver.Find(context.Background(), "3")

	fmt.Println(record.Version)
	// Output: v3
}

func ExampleUserDriverBuilder_Claims() {
	record, _ := exampleDriver().Find(context.Background(), "1")

	fmt.Println(record.Claims)
	// Output: map[role:admin]
}

func ExampleUserDriverBuilder_Disabled() {
	_, err := exampleDriver().Find(context.Background(), "2")

	fmt.Println(err)
	// Output: auth: user disabled
}

func ExampleUserDriverBuilderFunc() {
	policy := auth.UserDriverBuilderFunc[exampleUser](func(driver *auth.UserDriverBuilder[exampleUser]) {
		driver.Version(func(user exampleUser) string { return "v1" })
	})

	driver, err := auth.NewUserDriver(
		func(_ context.Context, id int64) (exampleUser, error) { return exampleUser{ID: id}, nil },
		func(user exampleUser) int64 { return user.ID },
		policy,
	)

	fmt.Println(driver != nil, err)
	// Output: true <nil>
}

// exampleGuard returns a login over exampleDriver; policy callbacks configure it.
func exampleGuard(configure ...auth.GuardBuilderFunc) *auth.Guard {
	users, err := auth.NewUserProvider(exampleDriver())

	if err != nil {
		panic(err)
	}

	guard, err := auth.NewGuard(users, cache.New(memorycache.New()), configure...)

	if err != nil {
		panic(err)
	}

	return guard
}

// exampleRequest returns a request with a fresh session.
func exampleRequest() *http.Request {
	return sessionRequest(session.New(time.Now().Add(time.Hour), map[string]any{}))
}

// needsSecondFactor is a second-factor check that every user needs.
func needsSecondFactor(context.Context, string) (bool, error) {
	return true, nil
}

func ExampleNewGuard() {
	users, err := auth.NewUserProvider(exampleDriver())

	if err != nil {
		panic(err)
	}

	guard, err := auth.NewGuard(users, cache.New(memorycache.New()), func(policy *auth.GuardBuilder) {
		policy.ChallengeTTL(10 * time.Minute)
	})

	fmt.Println(guard != nil, err)
	// Output: true <nil>
}

func ExampleGuard() {
	guard := exampleGuard()
	r := exampleRequest()

	complete, err := guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
	identity, _ := guard.Authenticator()(r)

	fmt.Println(complete, err, identity.Subject(), identity.Level() == auth.LevelSingle)
	// Output: true <nil> 1 true
}

func ExampleGuard_Authenticator() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	handler := auth.Middleware(guard.Authenticator())(func(_ http.ResponseWriter, r *http.Request) error {
		user, _ := auth.UserFromContext[exampleUser](r.Context())
		fmt.Println(user.Role)

		return nil
	})

	_ = handler(httptest.NewRecorder(), r)
	// Output: admin
}

func ExampleGuard_Begin() {
	guard := exampleGuard()
	r := exampleRequest()

	complete, err := guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
	fmt.Println(complete, err)

	_, err = guard.Begin(r, "2", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
	fmt.Println(errors.Is(err, auth.ErrUnauthenticated))
	// Output:
	// true <nil>
	// true
}

func ExampleGuard_Pending() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(needsSecondFactor)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	user, err := guard.Pending(r)

	fmt.Println(user.Subject, err)
	// Output: 1 <nil>
}

func ExampleGuard_Complete() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(needsSecondFactor)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	err := guard.Complete(r, auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession})
	identity, _ := guard.Authenticator()(r)

	fmt.Println(err, identity.Level() == auth.LevelMulti)
	// Output: <nil> true
}

func ExampleGuard_Fail() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(needsSecondFactor)
		policy.MaxAttempts(1)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	fmt.Println(guard.Fail(r))

	_, err := guard.Pending(r)
	fmt.Println(errors.Is(err, auth.ErrUnauthenticated))
	// Output:
	// <nil>
	// true
}

func ExampleGuard_Confirm() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	err := guard.Confirm(r, auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession})
	identity, _ := guard.Authenticator()(r)

	fmt.Println(err, len(identity.Factors()))
	// Output: <nil> 2
}

func ExampleGuard_Logout() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	fmt.Println(guard.Logout(r))

	_, err := guard.Authenticator()(r)
	fmt.Println(errors.Is(err, auth.ErrNoCredentials))
	// Output:
	// <nil>
	// true
}

func ExampleGuardBuilder() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.ChallengeTTL(time.Minute).MaxAttempts(3).SecondFactor(needsSecondFactor)
	})

	complete, err := guard.Begin(exampleRequest(), "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	fmt.Println(complete, err)
	// Output: false <nil>
}

func ExampleGuardBuilder_Handle() {
	handle := session.NewHandle()
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.Handle(handle)
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(session.NewContextFor(r.Context(), handle, session.New(time.Now().Add(time.Hour), map[string]any{})))

	complete, err := guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	fmt.Println(complete, err)
	// Output: true <nil>
}

func ExampleGuardBuilder_ChallengeTTL() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(needsSecondFactor).ChallengeTTL(10 * time.Minute)
	})
	r := exampleRequest()

	complete, err := guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	fmt.Println(complete, err)
	// Output: false <nil>
}

func ExampleGuardBuilder_MaxAttempts() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(needsSecondFactor).MaxAttempts(2)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	_ = guard.Fail(r)
	_, err := guard.Pending(r)
	fmt.Println(err)

	_ = guard.Fail(r)
	_, err = guard.Pending(r)
	fmt.Println(errors.Is(err, auth.ErrUnauthenticated))
	// Output:
	// <nil>
	// true
}

func ExampleGuardBuilder_SecondFactor() {
	enrolled := map[string]bool{"1": true}

	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(func(_ context.Context, subject string) (bool, error) {
			return enrolled[subject], nil
		})
	})

	complete, err := guard.Begin(exampleRequest(), "1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

	fmt.Println(complete, err)
	// Output: false <nil>
}

func ExampleGuardBuilderFunc() {
	policy := auth.GuardBuilderFunc(func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(3)
	})

	fmt.Println(exampleGuard(policy) != nil)
	// Output: true
}

func ExampleRequireLevel() {
	handler := auth.RequireLevel(auth.LevelMulti)(router.Handler(func(http.ResponseWriter, *http.Request) error {
		return nil
	}))

	identity := auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Factors(auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	err := handler(httptest.NewRecorder(), r.WithContext(auth.NewContext(r.Context(), identity)))

	fmt.Println(errors.Is(err, auth.ErrStepUpRequired))
	// Output: true
}

func ExampleRequireFresh() {
	handler := auth.RequireFresh(10 * time.Minute)(router.Handler(func(http.ResponseWriter, *http.Request) error {
		return nil
	}))

	identity := auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Factors(auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge, At: time.Now()})
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	fmt.Println(handler(httptest.NewRecorder(), r.WithContext(auth.NewContext(r.Context(), identity))))
	// Output: <nil>
}

func ExampleErrStepUpRequired() {
	fmt.Println(auth.ErrStepUpRequired.Status())
	// Output: 403
}

// examplePassword is the factor the record examples log in with.
var examplePassword = auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}

func ExampleLoginRecord() {
	guard := exampleGuard()
	r := exampleRequest()
	r.Header.Set("User-Agent", "Firefox")
	r.RemoteAddr = "203.0.113.7:4711"
	_, _ = guard.Begin(r, "1", examplePassword)

	records, _ := guard.Logins(r)

	fmt.Println(records[0].UserAgent, records[0].IP, records[0].Current)
	// Output: Firefox 203.0.113.7 true
}

func ExampleGuard_Logins() {
	guard := exampleGuard()
	first := exampleRequest()
	_, _ = guard.Begin(first, "1", examplePassword)
	_, _ = guard.Begin(exampleRequest(), "1", examplePassword)

	records, _ := guard.Logins(first)

	fmt.Println(len(records), records[0].Current, records[1].Current)
	// Output: 2 true false
}

func ExampleGuard_LoginsOf() {
	guard := exampleGuard()
	_, _ = guard.Begin(exampleRequest(), "1", examplePassword)

	records, err := guard.LoginsOf(context.Background(), "1")

	fmt.Println(len(records), err)
	// Output: 1 <nil>
}

func ExampleGuard_Revoke() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)
	records, _ := guard.Logins(r)

	fmt.Println(guard.Revoke(context.Background(), "1", records[0].ID))

	_, err := guard.Authenticator()(r)
	fmt.Println(errors.Is(err, auth.ErrNoCredentials))
	// Output:
	// <nil>
	// true
}

func ExampleGuard_RevokeOthers() {
	guard := exampleGuard()
	mine, other := exampleRequest(), exampleRequest()
	_, _ = guard.Begin(mine, "1", examplePassword)
	_, _ = guard.Begin(other, "1", examplePassword)

	fmt.Println(guard.RevokeOthers(mine))

	_, err := guard.Authenticator()(other)
	fmt.Println(errors.Is(err, auth.ErrNoCredentials))
	// Output:
	// <nil>
	// true
}

func ExampleGuard_RevokeAll() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	fmt.Println(guard.RevokeAll(context.Background(), "1"))

	records, _ := guard.LoginsOf(context.Background(), "1")
	fmt.Println(len(records))
	// Output:
	// <nil>
	// 0
}

func ExampleGuardBuilder_Devices() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.Devices(false)
	})
	r := exampleRequest()
	r.Header.Set("User-Agent", "Firefox")
	_, _ = guard.Begin(r, "1", examplePassword)

	records, _ := guard.Logins(r)

	fmt.Printf("%q\n", records[0].UserAgent)
	// Output: ""
}

func ExampleGuardBuilder_ClientIP() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.ClientIP(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
	})
	r := exampleRequest()
	r.RemoteAddr = "10.0.0.1:4711"
	r.Header.Set("X-Forwarded-For", "198.51.100.2")
	_, _ = guard.Begin(r, "1", examplePassword)

	records, _ := guard.Logins(r)

	fmt.Println(records[0].IP)
	// Output: 198.51.100.2
}

func ExampleGuardBuilder_MaxLogins() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.MaxLogins(1)
	})
	first, second := exampleRequest(), exampleRequest()
	_, _ = guard.Begin(first, "1", examplePassword)
	_, _ = guard.Begin(second, "1", examplePassword)

	_, err := guard.Authenticator()(first)

	fmt.Println(errors.Is(err, auth.ErrNoCredentials))
	// Output: true
}

func ExampleGuardBuilder_SessionLifetime() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SessionLifetime(7 * 24 * time.Hour)
	})

	complete, err := guard.Begin(exampleRequest(), "1", examplePassword)

	fmt.Println(complete, err)
	// Output: true <nil>
}
