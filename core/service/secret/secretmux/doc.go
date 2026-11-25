// Package secretmux provides a [secret.Driver] that routes references by scheme
// to other drivers, so one [secret.Store] can read from several backends.
//
// A reference is "scheme:name". The mux registers one source driver per scheme
// with [Builder.Source], splits each reference at its first colon, and passes
// the rest to the source as the name. "file:/run/secrets/token" reads
// "/run/secrets/token" from the file source; "aws:billing/key" reads
// "billing/key" from the AWS driver. A reference with no registered scheme
// fails with [ErrUnknownScheme], and the error lists the registered schemes in
// sorted order. Construction errors, such as an empty or duplicate scheme or a
// nil driver, wrap [ErrInvalidConfig].
//
// Combine the mux with bindings (see [secret.NewBinding]) to let each
// deployment choose where a secret lives: the code names DATABASE_PASSWORD, the
// environment says "aws:prod/db" or "file:/run/secrets/db".
//
// The mux manages no lifecycle. Start and stop each source driver yourself
// (for example with the application's Startup and Cleanup callbacks); the mux
// only routes calls. Results and errors from a source pass through unchanged,
// so [secret.ErrNotFound] and [secret.ErrUnsupported] keep their meaning.
//
// Example:
//
//	sources, err := secretmux.New(func(sources *secretmux.Builder) {
//		sources.Source("file", filesecret.New())
//		sources.Source("env", envsecret.New(os.LookupEnv))
//	})
//	if err != nil {
//		return err
//	}
//	secrets := secret.New(sources, func(secrets *secret.Builder) {
//		secrets.Bindings(os.LookupEnv)
//	})
//	password, err := secrets.Get(ctx, secret.NewBinding[string]("DATABASE_PASSWORD"))
//	if err != nil {
//		return err
//	}
//	useCredential(password)
package secretmux
