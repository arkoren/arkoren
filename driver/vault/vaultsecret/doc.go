// Package vaultsecret provides a HashiCorp Vault KV v2 [secret.Driver]. It requires
// positive decimal version identifiers and non-null JSON-object write values.
// Numbers that change value at the native float64 JSON boundary are rejected
// before writing; use application-authored strings for exact IDs and decimals.
// Accepted numeric tokens are transmitted unchanged; Get may normalize formatting. Missing,
// deleted, and destroyed payloads return [secret.ErrNotFound]. Update uses a
// native version CAS without driver-level retries; native SDK retry policy remains.
//
// # Authentication
//
// With [Builder.Token] (or the client's VAULT_TOKEN) the driver uses a static
// token and never renews it: the caller manages that token's lifetime. With
// [Builder.Auth], [Driver.Start] logs in, and when the login token is renewable
// the driver renews it in a background goroutine. When renewal ends, because
// the token reached its maximum TTL or a renewal failed, the driver logs in
// again, retrying with exponential backoff (1s up to 1m) until it succeeds.
// [Driver.Stop] signals and joins the owner and watcher within its caller's wait
// budget. Native in-flight renewal uses SDK timeouts, not the Stop context;
// ownership remains tracked after an early Stop return. See Driver.Stop for
// lifecycle serialization and restart limits. Failed stages and retry delays,
// not native diagnostic text, are logged through Builder.Logger (slog.Default
// when unset).
//
// Construction callbacks run once in order on one fresh draft. They do not log
// in; Start owns authentication and Stop owns renewal cancellation.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Mount("application")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
//
// A non-renewable login token is not refreshed. [NewFrom] wraps a caller-built
// client and never logs in or renews.
package vaultsecret
