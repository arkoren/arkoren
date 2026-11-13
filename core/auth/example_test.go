package auth_test

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/event"
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
