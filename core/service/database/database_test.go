package database_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// adapterStub supplies configurable results for facade delegation tests.
type adapterStub struct {
	// selectFn populates destinations for adapter delegation tests.
	selectFn func(dest any)
	// lockFn observes lock names and supplies the configured lock result.
	lockFn func(name string) error
}

// Exec returns a successful execution result.
func (adapterStub adapterStub) Exec(context.Context, string, ...any) (int64, error) {
	return 0, nil
}

// Select populates the destination with the configured test value.
func (adapterStub adapterStub) Select(_ context.Context, _ string, dest any, _ ...any) error {
	adapterStub.selectFn(dest)

	return nil
}

// Query returns an empty result set.
func (adapterStub adapterStub) Query(context.Context, string, ...any) (database.Rows, error) {
	return nil, nil
}

// Dialect reports the ANSI dialect.
func (adapterStub adapterStub) Dialect() query.Dialect {
	return query.ANSI
}

// Find returns successfully without populating the destination.
func (adapterStub adapterStub) Find(context.Context, string, any, ...any) error {
	return nil
}

// Lock reports the name to the configured hook and returns a no-op unlock.
func (adapterStub adapterStub) Lock(_ context.Context, name string) (func(context.Context) error, error) {
	if adapterStub.lockFn != nil {
		if err := adapterStub.lockFn(name); err != nil {
			return nil, err
		}
	}

	return func(context.Context) error {
		return nil
	}, nil
}

// TryLock reports the name to the configured hook and acquires a no-op lock.
func (adapterStub adapterStub) TryLock(ctx context.Context, name string) (func(context.Context) error, bool, error) {
	unlock, err := adapterStub.Lock(ctx, name)

	if err != nil {
		return nil, false, err
	}

	return unlock, true, nil
}

// WithTransaction invokes the transaction function with this driver.
func (adapterStub adapterStub) WithTransaction(_ context.Context, fn func(database.Driver) error) error {
	return fn(adapterStub)
}

// WithSession invokes fn without changing this stub's behavior. Callback
// expiration is enforced by the facade rather than this non-I/O test driver.
func (adapterStub adapterStub) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(adapterStub)
}

// TestNewExposesDriver verifies that Database exposes its driver.
func TestNewExposesDriver(t *testing.T) {
	t.Parallel()

	driver := adapterStub{}

	if !reflect.DeepEqual(database.New(driver).Driver(), driver) {
		t.Fatal("database did not expose its driver")
	}
}

// TestSelectReturnsCollectionSlice verifies typed selection results.
func TestSelectReturnsCollectionSlice(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]int) = []int{1, 2}
	}})

	values, err := db.Select[int](context.Background(), query.Raw("SELECT value"))

	if err != nil || !reflect.DeepEqual(values.Items(), []int{1, 2}) {
		t.Fatalf("values = %#v, err = %v", values.Items(), err)
	}
}

// TestLockDelegatesNameToDriver verifies Lock passes the name to the driver.
func TestLockDelegatesNameToDriver(t *testing.T) {
	t.Parallel()

	var got string
	db := database.New(adapterStub{lockFn: func(name string) error {
		got = name

		return nil
	}})

	unlock, err := db.Lock(context.Background(), "jobs")

	if err != nil {
		t.Fatal(err)
	}

	if got != "jobs" {
		t.Fatalf("driver lock name = %q, want %q", got, "jobs")
	}

	if err := unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestLockReturnsDriverError verifies Lock returns the driver's error.
func TestLockReturnsDriverError(t *testing.T) {
	t.Parallel()

	want := errors.New("lock failed")
	db := database.New(adapterStub{lockFn: func(string) error {
		return want
	}})

	unlock, err := db.Lock(context.Background(), "jobs")

	if !errors.Is(err, want) {
		t.Fatalf("Lock() error = %v, want %v", err, want)
	}

	if unlock != nil {
		t.Fatal("Lock() returned an unlock function with an error")
	}
}

// TestTryLockDelegatesNameToDriver verifies TryLock passes the name to the
// driver and returns its result.
func TestTryLockDelegatesNameToDriver(t *testing.T) {
	t.Parallel()

	var got string
	db := database.New(adapterStub{lockFn: func(name string) error {
		got = name

		return nil
	}})

	unlock, acquired, err := db.TryLock(context.Background(), "jobs")

	if err != nil || !acquired {
		t.Fatalf("TryLock() = acquired %v, error %v, want acquired", acquired, err)
	}

	if got != "jobs" {
		t.Fatalf("driver lock name = %q, want %q", got, "jobs")
	}

	if err := unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestTryLockReturnsDriverError verifies TryLock returns the driver's error.
func TestTryLockReturnsDriverError(t *testing.T) {
	t.Parallel()

	want := errors.New("lock failed")
	db := database.New(adapterStub{lockFn: func(string) error {
		return want
	}})

	unlock, acquired, err := db.TryLock(context.Background(), "jobs")

	if !errors.Is(err, want) {
		t.Fatalf("TryLock() error = %v, want %v", err, want)
	}

	if unlock != nil || acquired {
		t.Fatal("TryLock() returned a lock with an error")
	}
}
