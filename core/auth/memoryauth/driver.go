package memoryauth

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"arkoren.dev/core/auth"
)

// entry is one stored user.
type entry struct {
	// record is the stored record; its Claims map is private to the driver.
	record auth.UserRecord

	// disabled makes Find return [auth.ErrUserDisabled].
	disabled bool
}

// Driver is an in-memory [auth.UserDriver]. The zero value is not usable; create
// it with [New]. It is safe for concurrent use.
//
// Example:
//
//	driver := memoryauth.New()
//	_ = driver.Put(auth.UserRecord{Subject: "42", Claims: map[string]any{"role": "admin"}})
//	users, err := auth.NewUserProvider(driver)
type Driver struct {
	// mu protects users.
	mu sync.RWMutex

	// users maps subjects to stored users.
	users map[string]entry
}

// New returns an empty driver.
//
// Example:
//
//	driver := memoryauth.New()
func New() *Driver {
	return &Driver{users: map[string]entry{}}
}

// Put stores record under its Subject, replacing any previous user and making
// the user enabled. The Claims map is copied; Value is stored as is. It returns
// an error wrapping [auth.ErrInvalidSubject] when the subject is invalid.
//
// Example:
//
//	err := driver.Put(auth.UserRecord{Subject: "42", Version: "v1"})
func (driver *Driver) Put(record auth.UserRecord) error {
	if err := auth.ValidateSubject(record.Subject); err != nil {
		return fmt.Errorf("memoryauth: put: %w", err)
	}

	record.Claims = maps.Clone(record.Claims)

	driver.mu.Lock()
	defer driver.mu.Unlock()

	driver.users[record.Subject] = entry{record: record}

	return nil
}

// Disable marks an existing user disabled, so [Driver.Find] returns
// [auth.ErrUserDisabled]. It returns [auth.ErrUserNotFound] when no user has
// the subject.
//
// Example:
//
//	err := driver.Disable("42")
func (driver *Driver) Disable(subject string) error {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	stored, ok := driver.users[subject]

	if !ok {
		return auth.ErrUserNotFound
	}

	stored.disabled = true
	driver.users[subject] = stored

	return nil
}

// Delete removes the user. Deleting a missing user succeeds.
//
// Example:
//
//	driver.Delete("42")
func (driver *Driver) Delete(subject string) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	delete(driver.users, subject)
}

// Find implements [auth.UserDriver]. The returned Claims are a copy.
//
// Example:
//
//	record, err := driver.Find(ctx, "42")
func (driver *Driver) Find(ctx context.Context, subject string) (auth.UserRecord, error) {
	if err := ctx.Err(); err != nil {
		return auth.UserRecord{}, err
	}

	if err := auth.ValidateSubject(subject); err != nil {
		return auth.UserRecord{}, err
	}

	driver.mu.RLock()
	stored, ok := driver.users[subject]
	driver.mu.RUnlock()

	switch {
	case !ok:
		return auth.UserRecord{}, auth.ErrUserNotFound
	case stored.disabled:
		return auth.UserRecord{}, auth.ErrUserDisabled
	}

	stored.record.Claims = maps.Clone(stored.record.Claims)

	return stored.record, nil
}
