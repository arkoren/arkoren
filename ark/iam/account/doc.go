// Package account is the built-in users table of arkoren.dev/ark/iam: a
// [Directory] of users identified by email, the [auth.UserProvider] that serves
// them to the guard, and email verification with signed links.
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
// [Builder.Instance]. The table is "users", prefixed "<instance>_" for a named
// instance. Subjects are bytewise string columns and every time is a bigint of
// Unix seconds, so the table behaves identically on SQLite, PostgreSQL and
// MySQL. [Directory.Ping] checks, read-only, that the table exists.
//
// # Email verification
//
// Verification is off until [Builder.Verification] gives the directory a core
// link.Signer, a [mail.Mailer] and the application page the link opens, and
// [Builder.Queue] a dispatcher. A verification link is a value signed for the
// path "/email/verify" and the subject, bound to the user's current email and
// verified flag ([link.Bind]) and expiring after [Builder.VerifyTTL]; nothing is
// stored. [Directory.RequestVerification] (what the resend route and a
// registration call) stores a [VerifyRequest] as a job on the dispatcher inside
// the request; the worker, which registers the job with
// [Directory.RegisterJobs], later runs [Directory.SendVerification], which signs
// the link and sends the verification email through the mailer. The send may run
// more than once for one request. The email is a short plain-text message;
// [VerificationBuilder.Email] replaces it. The job runs on the queue "iam", named
// "iam-email-verification" plus "-<instance>" for a named [Builder.Instance].
//
// [Directory.Verify] verifies the link against the user's current state and then runs a conditional update that matches only the
// unverified user with that email, so a link works once: the second click, and a
// click after the email changed, are [ErrInvalidToken], and [EmailVerified] is
// published only when a row changed.
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
