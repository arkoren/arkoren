// Package memoryauth provides an in-memory [auth.UserDriver] for tests, local
// development and single-process applications.
//
// Seed users with [Driver.Put], mark them with [Driver.Disable] and remove
// them with [Driver.Delete]. Find returns [auth.ErrInvalidSubject] for an
// invalid subject, [auth.ErrUserNotFound] for an unknown one and
// [auth.ErrUserDisabled] for a disabled user. Claims maps are copied on the way
// in and out, so callers cannot change stored state.
//
// Example:
//
//	driver := memoryauth.New()
//	if err := driver.Put(auth.UserRecord{Subject: "42"}); err != nil {
//		return err
//	}
//	users, err := auth.NewUserProvider(driver)
package memoryauth
