package gcpsecret

// Builder holds optional secret naming settings during construction. Callbacks
// share one fresh draft; publication copies settings, not native clients.
//
// Example:
//
//	driver := NewFrom(client, func(secrets *Builder) {
//		secrets.Project("example-project")
//	})
//	_ = driver
type Builder struct {
	// config holds the default project until publication.
	config settings
}

// BuilderFunc edits the naming draft once in argument order. Nil entries panic;
// publication copies the project setting without copying the native client.
//
// Example:
//
//	var configure gcpsecret.BuilderFunc = func(secrets *gcpsecret.Builder) {
//		secrets.Project("example-project")
//	}
//	driver := gcpsecret.NewFrom(client, configure)
//	_ = driver
type BuilderFunc = func(*Builder)

// Project replaces the fallback project for unqualified names. Empty requires
// fully qualified resource names; it does not discover a project through ADC.
// Fully qualified names always retain their explicit project. It returns the
// same draft and does not contact Google.
//
// Example:
//
//	driver := NewFrom(client, func(secrets *Builder) {
//		secrets.Project("example-project")
//	})
//	_ = driver
func (secrets *Builder) Project(project string) *Builder {
	secrets.config.Project = project

	return secrets
}

// configured invokes each policy directly once in order. A nil entry panics;
// expanding an empty or nil slice supplies no policies.
func configured(configure ...BuilderFunc) settings {
	secret := &Builder{}

	for _, configure := range configure {
		configure(secret)
	}

	return secret.config
}
