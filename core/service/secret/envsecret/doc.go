// Package envsecret provides a read-only, text-only [secret.Driver] over
// environment variables, for the "env:" scheme of a secretmux driver.
//
// The reference name is the variable name and must be a POSIX name,
// [A-Za-z_][A-Za-z0-9_]*; anything else is an error rather than a lookup. The
// lookup is a function, normally os.LookupEnv, so tests supply a map instead of
// changing process state. An unset variable is [secret.ErrNotFound], a value
// that is not valid UTF-8 is an error, and writes are [secret.ErrUnsupported].
// A variable set to the empty string is a present, empty secret.
//
// Environment variables are visible to every child process and often to crash
// dumps and diagnostics. Prefer files or a managed backend for long-lived
// credentials, and use this driver for local development and deployments that
// inject secrets through the environment.
//
// Example:
//
//	sources, err := secretmux.New(func(sources *secretmux.Builder) {
//		sources.Source("env", envsecret.New(os.LookupEnv))
//	})
//	if err != nil {
//		return err
//	}
//	secrets := secret.New(sources)
//	token, err := secrets.Get(ctx, secret.NewSecret[string]("env:API_TOKEN"))
//	if err != nil {
//		return err
//	}
//	useCredential(token)
package envsecret
