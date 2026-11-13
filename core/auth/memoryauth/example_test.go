package memoryauth_test

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
)

func ExampleNew() {
	driver := memoryauth.New()

	_, err := driver.Find(context.Background(), "42")

	fmt.Println(errors.Is(err, auth.ErrUserNotFound))
	// Output: true
}

func ExampleDriver() {
	driver := memoryauth.New()
	_ = driver.Put(auth.UserRecord{Subject: "42", Claims: map[string]any{"role": "admin"}})

	users, _ := auth.NewUserProvider(driver)
	record, err := users.Find(context.Background(), "42")

	fmt.Println(record.Claims["role"], err)
	// Output: admin <nil>
}

func ExampleDriver_Put() {
	driver := memoryauth.New()

	fmt.Println(driver.Put(auth.UserRecord{Subject: "42", Version: "v1"}))
	fmt.Println(errors.Is(driver.Put(auth.UserRecord{Subject: ""}), auth.ErrInvalidSubject))
	// Output:
	// <nil>
	// true
}

func ExampleDriver_Disable() {
	driver := memoryauth.New()
	_ = driver.Put(auth.UserRecord{Subject: "42"})
	_ = driver.Disable("42")

	_, err := driver.Find(context.Background(), "42")

	fmt.Println(errors.Is(err, auth.ErrUserDisabled))
	// Output: true
}

func ExampleDriver_Delete() {
	driver := memoryauth.New()
	_ = driver.Put(auth.UserRecord{Subject: "42"})
	driver.Delete("42")

	_, err := driver.Find(context.Background(), "42")

	fmt.Println(errors.Is(err, auth.ErrUserNotFound))
	// Output: true
}

func ExampleDriver_Find() {
	driver := memoryauth.New()
	_ = driver.Put(auth.UserRecord{Subject: "42", Version: "v1"})

	record, err := driver.Find(context.Background(), "42")

	fmt.Println(record.Version, err)
	// Output: v1 <nil>
}
