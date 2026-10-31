package awssecret

import "github.com/aws/aws-sdk-go-v2/aws"

// Builder configures a Secrets Manager client before AWS configuration loads.
// Native credentials remain shared; completed settings are copied. NewFrom
// borrows an already configured client and therefore takes no callbacks.
//
// Example:
//
//	configure := func(secrets *Builder) {
//		secrets.Region("eu-west-1")
//	}
//	driver, err := New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config contains construction settings, never a published driver.
	config Config
}

// BuilderFunc edits a Secrets Manager draft once before native configuration
// loading. Nil entries panic; credential providers retain their native ownership.
//
// Example:
//
//	var configure awssecret.BuilderFunc = func(secrets *awssecret.Builder) {
//		secrets.Region("eu-west-1")
//	}
//	driver, err := awssecret.New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// Region replaces the AWS region. Empty uses the default chain; the same draft
// is returned without changing an already constructed client.
//
// Example:
//
//	configure := func(secrets *Builder) {
//		secrets.Region("eu-west-1")
//	}
//	driver, err := New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (secrets *Builder) Region(region string) *Builder {
	secrets.config.Region = region

	return secrets
}

// Endpoint replaces the service endpoint. Empty restores the regular endpoint;
// it returns the same draft and does not contact the service.
//
// Example:
//
//	configure := func(secrets *Builder) {
//		secrets.Endpoint("http://localhost:4566")
//	}
//	driver, err := New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (secrets *Builder) Endpoint(endpoint string) *Builder {
	secrets.config.Endpoint = endpoint

	return secrets
}

// Name is a compatibility no-op. Secret operations always take their explicit
// names; the constructed driver retains no diagnostic Name setting. It returns
// the same draft and does not select a default secret or namespace.
//
// Deprecated: Pass the name to each operation or use a secret definition.
//
// Example:
//
//	configure := func(secrets *Builder) {
//		secrets.Name("application")
//	}
//	driver, err := New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (secrets *Builder) Name(name string) *Builder {
	secrets.config.Name = name

	return secrets
}

// Credentials replaces the shared provider. Nil restores the default chain;
// providers are not cloned. The same construction draft is returned.
//
// Example:
//
//	configure := func(secrets *Builder) {
//		secrets.Credentials(nil)
//	}
//	driver, err := New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (secrets *Builder) Credentials(credentials aws.CredentialsProvider) *Builder {
	secrets.config.Credentials = credentials

	return secrets
}

// configured invokes policies directly once in order on a fresh draft. Nil
// entries panic; empty or nil slice expansion supplies no policies.
func configured(configure ...BuilderFunc) Config {
	secret := &Builder{}

	for _, configure := range configure {
		configure(secret)
	}

	return secret.config
}
