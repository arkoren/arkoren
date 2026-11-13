package auth

import (
	"context"
	"errors"
	"reflect"
)

// userDriverSettings configures a [TypedUserDriver].
type userDriverSettings[U any] struct {
	// version derives [UserRecord.Version]; nil leaves it empty.
	version func(U) string

	// claims derives [UserRecord.Claims]; nil leaves it nil.
	claims func(U) map[string]any

	// disabled reports a user that exists but may not act; nil means never.
	disabled func(U) bool
}

// UserDriverBuilder configures [NewUserDriver] before publication. Callbacks
// run directly once, synchronously in order on one fresh draft; nil entries
// panic and expanded nil or empty slices supply no edits. Methods return the
// same draft. A nil function leaves the corresponding setting off.
//
// Example:
//
//	driver, err := auth.NewUserDriver(loadUser, userID, func(driver *auth.UserDriverBuilder[User]) {
//		driver.Version(func(user User) string { return user.PasswordVersion })
//	})
type UserDriverBuilder[U any] struct {
	// settings holds the private construction values until publication.
	settings userDriverSettings[U]
}

// Version sets the function that derives [UserRecord.Version] from a user. It
// replaces the previous value on the construction draft.
//
// Example:
//
//	driver.Version(func(user User) string { return user.PasswordVersion })
func (builder *UserDriverBuilder[U]) Version(version func(U) string) *UserDriverBuilder[U] {
	builder.settings.version = version

	return builder
}

// Claims sets the function that derives [UserRecord.Claims] from a user. It
// replaces the previous value on the construction draft.
//
// Example:
//
//	driver.Claims(func(user User) map[string]any { return map[string]any{"role": user.Role} })
func (builder *UserDriverBuilder[U]) Claims(claims func(U) map[string]any) *UserDriverBuilder[U] {
	builder.settings.claims = claims

	return builder
}

// Disabled sets the function that reports a user that exists but may not act;
// [TypedUserDriver.Find] then returns [ErrUserDisabled]. It replaces the
// previous value on the construction draft.
//
// Example:
//
//	driver.Disabled(func(user User) bool { return user.SuspendedAt != nil })
func (builder *UserDriverBuilder[U]) Disabled(disabled func(U) bool) *UserDriverBuilder[U] {
	builder.settings.disabled = disabled

	return builder
}

// UserDriverBuilderFunc configures a [UserDriverBuilder].
//
// Example:
//
//	var policy auth.UserDriverBuilderFunc[User] = func(driver *auth.UserDriverBuilder[User]) {
//		driver.Version(func(user User) string { return user.PasswordVersion })
//	}
//	driver, err := auth.NewUserDriver(loadUser, userID, policy)
type UserDriverBuilderFunc[U any] = func(*UserDriverBuilder[U])

// TypedUserDriver is a [UserDriver] that adapts an application's typed loader.
// It converts subjects to the identifier type K, loads the user U, and builds
// the [UserRecord] from it. Create it with [NewUserDriver].
//
// Example:
//
//	driver, err := auth.NewUserDriver(loadUser, func(user User) int64 { return user.ID })
//	subject, err := driver.Subject(User{ID: 42}) // "42"
type TypedUserDriver[K comparable, U any] struct {
	// load fetches a user by identifier.
	load func(context.Context, K) (U, error)

	// id reports the identifier of a user.
	id func(U) K

	// settings are the optional record derivations.
	settings userDriverSettings[U]
}

// NewUserDriver returns a [TypedUserDriver] that loads users with load and
// identifies them with id. K must be a string kind, an integer kind, or a type
// implementing both [encoding.TextMarshaler] and (through its pointer)
// [encoding.TextUnmarshaler], such as UUID, xid and ULID types; any other K
// fails with an error wrapping [ErrInvalidSubject] so the mistake surfaces at
// setup. It also fails for a nil load or id. Construction callbacks configure
// [UserDriverBuilder].
//
// load reports a deleted user with [ErrUserNotFound] (or an error wrapping it);
// returning a nil pointer with a nil error counts as not found too. Other loader
// errors pass through unchanged.
//
// Example:
//
//	driver, err := auth.NewUserDriver(
//		func(ctx context.Context, id int64) (User, error) { return store.User(ctx, id) },
//		func(user User) int64 { return user.ID },
//		func(driver *auth.UserDriverBuilder[User]) {
//			driver.Disabled(func(user User) bool { return user.Suspended })
//		},
//	)
func NewUserDriver[K comparable, U any](
	load func(context.Context, K) (U, error),
	id func(U) K,
	configure ...UserDriverBuilderFunc[U],
) (*TypedUserDriver[K, U], error) {
	if load == nil || id == nil {
		return nil, errors.New("auth: nil user loader or identifier function")
	}

	if err := checkSubjectType[K](); err != nil {
		return nil, err
	}

	builder := &UserDriverBuilder[U]{}

	for _, configure := range configure {
		configure(builder)
	}

	return &TypedUserDriver[K, U]{load: load, id: id, settings: builder.settings}, nil
}

// Subject returns the subject of user, derived from the identifier function.
// It returns an error wrapping [ErrInvalidSubject] when the identifier does not
// render to a valid subject, such as an empty string.
//
// Example:
//
//	subject, err := driver.Subject(user)
//	if err != nil {
//		return err
//	}
//	err = users.Forget(ctx, subject)
func (driver *TypedUserDriver[K, U]) Subject(user U) (string, error) {
	return formatSubject(driver.id(user))
}

// Find implements [UserDriver]. It parses subject canonically (an error
// wrapping [ErrInvalidSubject] otherwise, never [ErrUserNotFound]), calls the
// loader, and returns [ErrUserNotFound] for a nil pointer with a nil error and
// [ErrUserDisabled] when the Disabled function reports the user. The record's
// Value is the loaded U.
//
// Example:
//
//	record, err := driver.Find(ctx, "42")
//	if err != nil {
//		return err
//	}
//	user := record.Value.(User)
func (driver *TypedUserDriver[K, U]) Find(ctx context.Context, subject string) (UserRecord, error) {
	id, err := parseSubject[K](subject)

	if err != nil {
		return UserRecord{}, err
	}

	user, err := driver.load(ctx, id)

	if err != nil {
		return UserRecord{}, err
	}

	if value := reflect.ValueOf(&user).Elem(); (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return UserRecord{}, ErrUserNotFound
	}

	if driver.settings.disabled != nil && driver.settings.disabled(user) {
		return UserRecord{}, ErrUserDisabled
	}

	record := UserRecord{Subject: subject, Value: user}

	if driver.settings.version != nil {
		record.Version = driver.settings.version(user)
	}

	if driver.settings.claims != nil {
		record.Claims = driver.settings.claims(user)
	}

	return record, nil
}
