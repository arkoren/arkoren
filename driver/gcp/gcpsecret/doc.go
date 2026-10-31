// Package gcpsecret provides a Google Cloud Secret Manager [secret.Driver]. It
// accepts qualified resource names, or unqualified names when Builder.Project
// supplies their project, and returns [secret.ErrNotFound] for missing secrets.
// Constructors invoke ordered Builder callbacks directly once on one fresh
// draft before native client creation. Nil entries panic; expanded nil/empty
// slices omit edits. Completed naming settings are copied, not native clients.
// Stop closes native Secret Manager clients, including NewFrom clients; it is a
// no-op for SDK-shaped test doubles. Project qualifies unqualified secret names;
// fully qualified names retain their explicit project.
//
// New discards SDK request/response and credential logging, even when
// GOOGLE_SDK_GO_LOGGING_LEVEL enables ambient debug logging. It preserves the
// native ADC chain and construction context. NewFrom retains the supplied
// client's logging policy without modification; configure a discard SDK logger
// before wrapping a native client to prevent secret payloads from reaching logs.
//
// Example:
//
//	driver, err := gcpsecret.New(ctx, func(secrets *gcpsecret.Builder) {
//		secrets.Project("example-project")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
package gcpsecret
