// Package auth resolves application users by a text subject, independently of
// HTTP. Jobs, MCP tools and feature packages use it to look a user up and to
// clean up the data attached to a user that is gone.
//
// # Subjects
//
// A subject is the stable text reference of a user: 1 to 255 bytes of
// printable ASCII without spaces. It is case sensitive and compared bytewise.
// [ValidateSubject] checks the rule; [ErrInvalidSubject] reports a violation.
// [NewUserDriver] converts between a typed identifier and its subject: strings
// are used as is, integers become decimal digits, and types implementing
// [encoding.TextMarshaler] and (through a pointer) [encoding.TextUnmarshaler]
// use their text form. Parsing is canonical: a subject that does not render
// back to itself, such as "007" or "+1" for an integer, is an invalid subject.
//
// # Looking up users
//
// A [UserDriver] finds one [UserRecord] by subject. Wrap it with
// [NewUserProvider] and call [UserProvider.Find]. A missing user is
// [ErrUserNotFound]; a user that exists but may not act is [ErrUserDisabled].
// Only [ErrUserNotFound] proves a user is deleted, so cleanup code must never
// treat any other error as deletion.
//
// # Forgetting users
//
// Packages that store data about users register a hook with
// [UserProvider.OnForget]. [UserProvider.Forget] runs the hooks in
// registration order in the caller's context, so hooks join the caller's
// database transaction. The first error stops the run and is returned. When
// every hook succeeded, Forget emits [UserForgotten] on the bus given to
// [UserProviderBuilder.Events]; without a bus nothing is emitted.
//
// # Events
//
// [UserForgotten] ("auth.user.forgotten") carries only the subject. It means
// the forget hooks completed, not that a surrounding transaction committed.
//
// # Drivers and tests
//
// [arkoren.dev/core/auth/memoryauth] is an in-memory driver and
// [arkoren.dev/core/auth/authtest] is the conformance suite every driver runs.
//
// Example:
//
//	driver, err := auth.NewUserDriver(loadUser, func(user User) int64 { return user.ID })
//	if err != nil {
//		return err
//	}
//	users, err := auth.NewUserProvider(driver, func(users *auth.UserProviderBuilder) {
//		users.Events(app.Events())
//	})
//	if err != nil {
//		return err
//	}
//	record, err := users.Find(ctx, "42")
package auth
