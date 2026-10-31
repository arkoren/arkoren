// Package memorysecret provides an in-memory versioned [secret.Driver] for tests
// and local development. Missing secrets return [secret.ErrNotFound].
//
// Seed initial secrets with [Builder.Values]; the map and its byte slices are
// copied.
//
// Get, Put, Update, and Delete return ctx.Err() before reading or mutating
// state. Values are copied at the driver boundary, so caller mutation cannot
// change a stored secret.
//
// Versions start at "1" for each new logical secret. Put and Update append
// versions without changing earlier payloads. Delete immediately removes the
// entire history, with no retention or recovery. A later Put under the same
// name creates a new logical secret starting at "1", so an old name/version
// selector can return different contents. Pin only while that secret and
// version remain available. Immediate name reuse and repeated version strings
// are memory-driver behavior, not requirements on other secret drivers.
//
// New takes Builder policies for Values. They run directly once in order on a
// fresh draft; nil entries panic and expanded nil/empty slices supply no edits.
// Values replaces and copies both the map and each byte slice immediately. New
// copies completed seeds again into version one; retained drafts, input edits
// and returned byte-slice edits cannot change those stored secrets.
//
// Example:
//
//	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
//		secrets.Values(map[string][]byte{"api": []byte("development-token")})
//	})
//	value, err := driver.Get(ctx, secret.Reference{Name: "api"})
//	if err != nil {
//		return err
//	}
//	_ = value.Version
package memorysecret
