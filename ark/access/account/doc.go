// Package account is the built-in users table of arkoren.dev/ark/access: a
// [Directory] of users identified by email, the [auth.UserProvider] that serves
// them to the guard, and email verification.
//
// Use it when the application has no users table of its own. An application
// that keeps its own users skips this package and hands the facade an
// access.Users value instead; the other packages work the same either way,
// because they only ever see subjects (strings).
//
// # Model
//
// A user row holds a random identifier (the subject), a lowercase email, a
// version, and the times it was created, verified and disabled. The version is
// the [auth.UserRecord.Version]: [Directory.Rotate] bumps it, which ends every
// session, pending login and API token of the user at once. Email comparison
// is byte-exact on the stored, trimmed, lowercased form, so "Ada@Example.com"
// and "ada@example.com" are one user. Email verification never blocks login;
// protect routes with [RequireVerified].
//
// # Storage
//
// The package never runs DDL. [Directory.Migrations] describes the tables and
// the application's migrator executes them, in the group
// "arkoren.dev:ark/access:account", plus ":<instance>" for a named
// [Builder.Instance]. Tables are "users" and "email_verifications", prefixed
// "<instance>_" for a named instance. Subjects are bytewise string columns,
// verification tokens are stored as SHA-256 hex, and every time is a bigint of
// Unix seconds, so the tables behave identically on SQLite, PostgreSQL and
// MySQL. [Directory.Ping] checks, read-only, that the tables exist.
//
// Every package of the module assumes the same [database.Database]: writes that
// span packages (registering a user and storing a password, deleting a user
// and forgetting their rows) join the transaction carried by the context.
//
// Example:
//
//	directory, err := account.NewDirectory(db, store, ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
//		accounts.Events(events)
//	})
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(directory.Migrations()...); err != nil {
//		return err
//	}
//	subject, err := directory.Create(ctx, "ada@example.com")
//	_ = subject
package account
