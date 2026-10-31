package vaultsecret

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/vault/api"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/vault/vaultsecret/internal/lease"
)

// Config is a redacted display record for Vault KV v2 settings. It is not an
// input to New; use Builder callbacks to configure a driver. String, GoString,
// and LogValue redact nonempty Token values. Other fields retain their existing
// formatting, so do not put secrets in Address or in an auth method's formatter.
//
//	config := vaultsecret.Config{
//		Address: "https://vault.internal:8200",
//		Token: "example-token",
//	}
//	fmt.Println(config) // token contents are redacted
type Config struct {
	// Address overrides the Vault server address when non-empty.
	// Empty follows native environment/default configuration; text is not redacted.
	// Example: record := vaultsecret.Config{Address: "https://vault.internal:8200"}
	Address string

	// Token authenticates requests when non-empty.
	// Formatting masks this field; native Auth formatting has separate ownership.
	// Example: record.Token = token
	Token string

	// Mount selects the KV v2 mount when non-empty.
	// Empty selects "secret"; neither construction nor formatting creates it.
	// Example: record.Mount = "application"
	Mount string

	// Auth logs in with a Vault auth method instead of a static Token, for
	// example approle.NewAppRoleAuth or kubernetes.NewKubernetesAuth from
	// github.com/hashicorp/vault/api/auth. [Driver.Start] logs in and, when
	// the login token is renewable, keeps it renewed until [Driver.Stop];
	// when renewal ends (maximum TTL reached or renewal failed) the driver
	// logs in again. A static Token is never renewed: the caller manages it.
	// The method and its formatter remain shared, not recursively sanitized.
	// Example: record.Auth = loginMethod
	Auth api.AuthMethod

	// Logger receives renewal/re-login stages and retry delays, not native error
	// text, credentials, or response bodies. Nil uses [slog.Default].
	// Example: record.Logger = slog.New(slog.NewJSONHandler(output, nil))
	Logger *slog.Logger
}

// client defines the Vault KV v2 operations used by [Driver].
type client interface {
	// Get retrieves the current Vault secret.
	Get(context.Context, string) (*api.KVSecret, error)

	// GetVersion retrieves a specific Vault secret version.
	GetVersion(context.Context, string, int) (*api.KVSecret, error)

	// Put appends a Vault secret version.
	Put(context.Context, string, map[string]any, ...api.KVOption) (*api.KVSecret, error)

	// DeleteMetadata deletes all Vault secret versions.
	DeleteMetadata(context.Context, string) error
}

// ErrNotStarted is returned when a driver configured with an auth method
// is used before [Driver.Start] has logged in.
// A stopped managed driver also rejects operations until a new Start succeeds.
//
// Example:
//
//	if errors.Is(err, vaultsecret.ErrNotStarted) {
//		return fmt.Errorf("start managed authentication before reading secrets: %w", err)
//	}
var ErrNotStarted = errors.New("vault driver not started")

// Driver manages versioned JSON secrets in HashiCorp Vault KV v2.
// Write values must be objects. Native clients/auth methods remain shared;
// managed renewal belongs to Start/Stop, not to a secret operation's context.
//
// Example:
//
//	driver := vaultsecret.NewFrom(client.KVv2("application"))
//	store := secret.New(driver)
//	_ = store
type Driver struct {
	// lifecycle serializes Start and Stop. Native login callbacks must honor
	// their context; a concurrent lifecycle call waits for that login to return.
	lifecycle sync.Mutex

	// client performs requests against Vault.
	client client

	// renewal owns managed authentication and renewal workers. Nil selects
	// static-token or borrowed-client operation without managed lifecycle work.
	renewal *lease.Manager

	// started reports whether the driver is ready to serve requests.
	started atomic.Bool
}

// New creates a HashiCorp Vault KV v2 client. It performs no network IO.
// When an auth method is configured, call [Driver.Start] to log in
// before using the driver.
// Callbacks run directly once, synchronously in order on one fresh draft,
// before SDK validation. Nil entries panic; nil or empty expansions omit edits.
//
//	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
//		secrets.Mount("application")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func New(configure ...BuilderFunc) (*Driver, error) {
	secrets := &Builder{}

	for _, configure := range configure {
		configure(secrets)
	}

	config := secrets.config
	vaultClient, err := api.NewClient(vaultConfig(config))

	if err != nil {
		return nil, fmt.Errorf("create Vault client: %w", err)
	}

	if config.Token != "" {
		vaultClient.SetToken(config.Token)
	}

	driver := NewFrom(vaultClient.KVv2(vaultMount(config.Mount)))

	if config.Auth != nil {
		driver.started.Store(false)

		login := func(ctx context.Context) (*api.Secret, error) {
			return vaultClient.Auth().Login(ctx, config.Auth)
		}

		newWatcher := func(login *api.Secret) (lease.Watcher, error) {
			lifetime, err := vaultClient.NewLifetimeWatcher(&api.LifetimeWatcherInput{
				Secret: login,
			})

			if err != nil {
				return nil, err
			}

			return lifetime, nil
		}

		driver.renewal = lease.New(login, newWatcher, cmp.Or(config.Logger, slog.Default()), time.Second, time.Minute)
	}

	return driver, nil
}

// vaultConfig derives a Vault API configuration from config.
func vaultConfig(config Config) *api.Config {
	apiConfig := api.DefaultConfig()

	if config.Address != "" {
		apiConfig.Address = config.Address
	}

	return apiConfig
}

// vaultMount returns the configured KV v2 mount or Vault's default mount.
func vaultMount(mount string) string {
	if mount == "" {
		return "secret"
	}

	return mount
}

// NewFrom borrows a configured Vault KV v2 client without copying it or closing
// its underlying HTTP transport. The caller owns native configuration, tokens,
// logging, and transport cleanup. There are no callbacks or managed login/renewal;
// Start and Stop are no-ops. Configure the native client before sharing it.
//
// Example:
//
//	driver := vaultsecret.NewFrom(client.KVv2("application"))
//	secrets := secret.New(driver)
//	_ = secrets
func NewFrom(client client) *Driver {
	driver := &Driver{
		client: client,
	}
	driver.started.Store(true)

	return driver
}

// Get retrieves reference, using its version when supplied.
// Explicit versions must be positive decimal integers. Missing/deleted/destroyed
// payloads wrap secret.ErrNotFound; object formatting may differ from a write.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "application", Version: "1"})
//	if err != nil {
//		return err
//	}
//	_ = value.Version
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	if !driver.started.Load() {
		return secret.Value{}, ErrNotStarted
	}

	response, err := driver.getSecret(ctx, reference)

	if err != nil {
		return secret.Value{}, err
	}

	return secretValue(reference.Name, response)
}

// getSecret retrieves the Vault version selected by reference.
func (driver *Driver) getSecret(ctx context.Context, reference secret.Reference) (*api.KVSecret, error) {
	if reference.Version == "" {
		return driver.currentSecret(ctx, reference.Name)
	}

	return driver.versionedSecret(ctx, reference.Name, reference.Version)
}

// currentSecret retrieves and maps errors for the current Vault secret version.
func (driver *Driver) currentSecret(ctx context.Context, name string) (*api.KVSecret, error) {
	response, err := driver.client.Get(ctx, name)

	if err != nil {
		return nil, driver.mapError("get", name, err)
	}

	return response, nil
}

// versionedSecret retrieves and maps errors for a specific Vault secret version.
func (driver *Driver) versionedSecret(ctx context.Context, name, rawVersion string) (*api.KVSecret, error) {
	version, err := strconv.Atoi(rawVersion)

	if err != nil || version <= 0 {
		return nil, fmt.Errorf("%w: %s: version %q was never issued", secret.ErrNotFound, name, rawVersion)
	}

	response, err := driver.client.GetVersion(ctx, name, version)

	if err != nil {
		return nil, driver.mapError("get", name, err)
	}

	return response, nil
}

// secretValue maps a Vault KV response to the portable secret contract.
func secretValue(name string, response *api.KVSecret) (secret.Value, error) {
	if response == nil || response.Data == nil {
		return secret.Value{}, fmt.Errorf("%w: %s", secret.ErrNotFound, name)
	}

	contents, err := json.Marshal(response.Data)

	if err != nil {
		return secret.Value{}, fmt.Errorf("marshal secret %q: %w", name, err)
	}

	return secret.Value{
		Data:    contents,
		Version: secretVersion(response),
	}, nil
}

// secretVersion returns the Vault version when the response provides one.
func secretVersion(response *api.KVSecret) string {
	if response == nil || response.VersionMetadata == nil {
		return ""
	}

	return strconv.Itoa(response.VersionMetadata.Version)
}

// Put creates name or writes a new current value version when it already exists.
// Values must be JSON objects, not null. Numbers that change value under native
// float64 JSON serialization are rejected before writing; explicitly encode exact
// identifiers and decimals as JSON strings. Accepted numeric tokens are sent
// unchanged. Returned bytes are detached; Get may normalize object formatting.
//
// Example:
//
//	written, err := driver.Put(ctx, "application", []byte(`{"enabled":true}`))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Put(ctx context.Context, name string, value []byte) (secret.Value, error) {
	return driver.write(ctx, name, value)
}

// Update writes a new current version only if the observed version still exists
// unchanged. A concurrent modification returns the native CAS error; a deletion
// detected after CAS rejection returns ErrNotFound. The driver does not retry
// either rejection or recreate the secret; native SDK retry policy is unchanged.
// Read and write permissions are both required.
// CAS compares only the native version in current key metadata; metadata deletion
// and recreation can reuse numbers, so it is not a cross-recreation identity check.
//
// Example:
//
//	written, err := driver.Update(ctx, "application", []byte(`{"enabled":true}`))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Update(ctx context.Context, name string, value []byte) (secret.Value, error) {
	current, err := driver.Get(ctx, secret.Reference{Name: name})

	if err != nil {
		return secret.Value{}, err
	}

	version, err := strconv.Atoi(current.Version)

	if err != nil || version <= 0 {
		return secret.Value{}, errors.New("vault response has no positive version for update")
	}

	written, err := driver.write(ctx, name, value, api.WithCheckAndSet(version))

	if err != nil {
		return secret.Value{}, driver.mapUpdateError(ctx, name, err)
	}

	return written, nil
}

// mapUpdateError resolves absence after a native bad-request/CAS rejection.
// Other errors retain their original meaning, including uncertain I/O outcomes;
// checking existence never retries or changes a write and preserves both causes.
func (driver *Driver) mapUpdateError(ctx context.Context, name string, err error) error {
	var response *api.ResponseError

	if !errors.As(err, &response) || response.StatusCode != http.StatusBadRequest {
		return err
	}

	if _, missing := driver.Get(ctx, secret.Reference{Name: name}); errors.Is(missing, secret.ErrNotFound) {
		return fmt.Errorf("update Vault secret: %w: %w", missing, err)
	}

	return err
}

// write validates native JSON numeric representability before appending a KV v2
// version. Member tokens are transmitted unchanged; the driver neither rounds
// values nor introduces a persisted wrapper. Native formatting is not preserved.
func (driver *Driver) write(ctx context.Context, name string, value []byte, options ...api.KVOption) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	if !driver.started.Load() {
		return secret.Value{}, ErrNotStarted
	}

	data, err := decodeVaultData(value)

	if err != nil {
		return secret.Value{}, fmt.Errorf("decode Vault secret %q: %w", name, err)
	}

	response, err := driver.client.Put(ctx, name, data, options...)

	if err != nil {
		return secret.Value{}, driver.mapError("write", name, err)
	}

	return secret.Value{
		Data:    append([]byte(nil), value...),
		Version: secretVersion(response),
	}, nil
}

// Delete removes name and all of its versions. It returns an error wrapping
// [secret.ErrNotFound] when name does not exist, which Vault itself does not
// report, so the driver checks for the secret first.
// This is permanent metadata/history deletion, not the native soft-delete API.
//
// Example:
//
//	if err := driver.Delete(ctx, "application/retired-token"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, name string) error {
	if _, err := driver.Get(ctx, secret.Reference{
		Name: name,
	}); err != nil {
		return err
	}

	if err := driver.client.DeleteMetadata(ctx, name); err != nil {
		return driver.mapError("delete", name, err)
	}

	return nil
}

// mapError translates Vault not-found responses.
func (driver *Driver) mapError(operation, name string, err error) error {
	var responseError *api.ResponseError

	if errors.Is(err, api.ErrSecretNotFound) || errors.As(err, &responseError) && responseError.StatusCode == 404 {
		return fmt.Errorf("%w: %s", secret.ErrNotFound, name)
	}

	return fmt.Errorf("%s secret %q: %w", operation, name, err)
}

var _ secret.Driver = (*Driver)(nil)
