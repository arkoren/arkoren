package vaultsecret

import (
	"log/slog"

	"github.com/hashicorp/vault/api"
)

// Builder is a construction draft for [New]. Settings replace previous values;
// retained drafts cannot change a published driver. SDK auth methods and loggers
// remain caller-owned, shared handles rather than recursively copied objects.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Mount("application")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
type Builder struct {
	// config holds optional settings until New publishes its own value.
	config Config
}

// BuilderFunc edits one Vault construction draft before SDK validation, without
// logging in. Nil entries panic; shared auth methods run later through Start.
//
// Example:
//
//	var configure vaultsecret.BuilderFunc = func(secrets *vaultsecret.Builder) {
//		secrets.Mount("application")
//	}
//	driver, err := vaultsecret.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// Address overrides the server address. Empty retains the SDK environment and
// default chain. It does not connect to Vault.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Address("https://vault.example.com")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (secrets *Builder) Address(address string) *Builder {
	secrets.config.Address = address

	return secrets
}

// Token supplies a static token. Empty retains the SDK token; static tokens are
// never renewed by the driver. Prefer Auth for renewable login credentials.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Token(os.Getenv("VAULT_TOKEN"))
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (secrets *Builder) Token(token string) *Builder {
	secrets.config.Token = token

	return secrets
}

// Mount selects the KV v2 mount. Empty selects "secret"; no mount is created.
// Later calls replace the previous mount.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Mount("application")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (secrets *Builder) Mount(mount string) *Builder {
	secrets.config.Mount = mount

	return secrets
}

// Auth retains a native login method without invoking it. Start logs in and
// renews renewable tokens until Stop. Nil disables managed authentication.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Auth(nil)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (secrets *Builder) Auth(auth api.AuthMethod) *Builder {
	secrets.config.Auth = auth

	return secrets
}

// Logger retains the logger used for renewal failures. Nil selects slog.Default
// when a managed-authentication driver is constructed.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Logger(slog.Default())
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (secrets *Builder) Logger(logger *slog.Logger) *Builder {
	secrets.config.Logger = logger

	return secrets
}
