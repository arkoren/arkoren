package awsstorage

import "github.com/aws/aws-sdk-go-v2/aws"

// Builder configures S3 construction. NewFrom applies Prefix only: its borrowed
// client already owns connection settings. Publication copies settings, not
// native credential providers or clients.
//
// Example:
//
//	configure := func(store *Builder) {
//		store.Prefix("tenant-a")
//	}
//	driver, err := New(ctx, "uploads", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config holds settings until client and driver construction.
	config Config
}

// BuilderFunc edits one S3 construction draft in argument order. Nil entries
// panic. NewFrom applies Prefix only; connection settings belong to its client.
//
// Example:
//
//	var configure awsstorage.BuilderFunc = func(store *awsstorage.Builder) {
//		store.Prefix("tenant-a")
//	}
//	driver, err := awsstorage.New(ctx, "uploads", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// Region replaces the AWS region. Empty uses the default chain; NewFrom ignores
// this setting. It returns the same draft without changing a native client.
//
// Example:
//
//	configure := func(store *Builder) {
//		store.Region("eu-west-1")
//	}
//	driver, err := New(ctx, "uploads", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (store *Builder) Region(region string) *Builder {
	store.config.Region = region

	return store
}

// Endpoint replaces the service endpoint and enables path-style addressing in
// New. Empty uses AWS endpoints; NewFrom ignores it. It returns the same draft.
//
// Example:
//
//	configure := func(store *Builder) {
//		store.Endpoint("http://localhost:4566")
//	}
//	driver, err := New(ctx, "uploads", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (store *Builder) Endpoint(endpoint string) *Builder {
	store.config.Endpoint = endpoint

	return store
}

// Prefix replaces the logical object-key scope. Empty selects the bucket root;
// constructors validate it and normalize a trailing slash. It returns the draft.
//
// Example:
//
//	configure := func(store *Builder) {
//		store.Prefix("tenant-a")
//	}
//	driver, err := New(ctx, "uploads", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (store *Builder) Prefix(prefix string) *Builder {
	store.config.Prefix = prefix

	return store
}

// Credentials replaces the shared provider; nil selects AWS's default chain.
// NewFrom ignores this setting. It returns the same construction draft.
//
// Example:
//
//	configure := func(store *Builder) {
//		store.Credentials(nil)
//	}
//	driver, err := New(ctx, "uploads", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (store *Builder) Credentials(credentials aws.CredentialsProvider) *Builder {
	store.config.Credentials = credentials

	return store
}

// configured invokes policies directly once in order on a fresh draft. Nil
// entries panic; empty or nil slice expansion supplies no policies.
func configured(configure ...BuilderFunc) Config {
	storage := &Builder{}

	for _, configure := range configure {
		configure(storage)
	}

	return storage.config
}
