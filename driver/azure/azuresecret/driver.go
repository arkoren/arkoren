package azuresecret

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"

	"arkoren.dev/core/service/secret"
)

// client defines the Azure Key Vault operations used by [Driver].
type client interface {
	// GetSecret retrieves an Azure Key Vault secret.
	GetSecret(context.Context, string, string, *azsecrets.GetSecretOptions) (azsecrets.GetSecretResponse, error)

	// SetSecret writes an Azure Key Vault secret.
	SetSecret(context.Context, string, azsecrets.SetSecretParameters, *azsecrets.SetSecretOptions) (azsecrets.SetSecretResponse, error)

	// DeleteSecret deletes an Azure Key Vault secret.
	DeleteSecret(context.Context, string, *azsecrets.DeleteSecretOptions) (azsecrets.DeleteSecretResponse, error)
}

// Driver manages versioned secrets in Azure Key Vault.
// Values must be valid UTF-8 text; explicitly encode binary data before writing.
// The native client and credential remain shared and are never closed here.
//
// Example:
//
//	driver := azuresecret.NewFrom(client)
//	store := secret.New(driver)
//	_ = store
type Driver struct {
	// client performs requests against Azure Key Vault.
	client client
}

// New creates an Azure Key Vault driver for the vault at vaultURL, using
// Azure's default credential chain.
//
// vaultURL is the vault endpoint, for example https://myvault.vault.azure.net.
// It has no safe default, so it is a required positional argument. New returns
// an error naming VaultURL when it is empty, is not a valid URL, or is not an
// absolute https URL with a host. The driver has no other settings, so New
// takes no callbacks or empty builder. Credential creation does not contact Azure.
//
// Example:
//
//	driver, err := azuresecret.New(ctx, "https://myvault.vault.azure.net")
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(ctx context.Context, vaultURL string) (*Driver, error) {
	parsed, err := url.Parse(vaultURL)

	if err != nil {
		return nil, fmt.Errorf("invalid VaultURL %q: %w", vaultURL, err)
	}

	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid VaultURL %q: an absolute https URL is required", vaultURL)
	}

	credential, err := azidentity.NewDefaultAzureCredential(nil)

	if err != nil {
		return nil, fmt.Errorf("create Azure credential: %w", err)
	}

	client, err := azsecrets.NewClient(vaultURL, credential, nil)

	if err != nil {
		return nil, fmt.Errorf("create Azure Key Vault client: %w", err)
	}

	return NewFrom(client), nil
}

// NewFrom wraps an existing Azure Key Vault client without copying or closing
// it. There are no optional settings or configuration callbacks; native client
// configuration remains the caller's responsibility.
//
// Example:
//
//	driver := azuresecret.NewFrom(client)
//	_ = driver
func NewFrom(client client) *Driver {
	return &Driver{
		client: client,
	}
}

// Get retrieves reference, using its version when supplied.
// It returns detached UTF-8 text bytes and the native opaque version. HTTP 404
// maps to secret.ErrNotFound; provider diagnostics can remain in other errors.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "api-token"})
//	if err != nil {
//		return err
//	}
//	_ = value.Version
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	response, err := driver.client.GetSecret(ctx, reference.Name, reference.Version, nil)

	if err != nil {
		return secret.Value{}, driver.mapError("get", reference.Name, err)
	}

	if response.Value == nil {
		return secret.Value{}, fmt.Errorf("secret %q has no value", reference.Name)
	}

	version := ""

	if response.ID != nil {
		version = response.ID.Version()
	}

	return secret.Value{
		Data:    []byte(*response.Value),
		Version: version,
	}, nil
}

// Put creates name or writes a new current version when it already exists.
// Invalid UTF-8 fails locally instead of being replaced during JSON encoding.
// Successful results contain detached bytes and the provider-issued version.
//
// Example:
//
//	written, err := driver.Put(ctx, "api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Put(ctx context.Context, name string, value []byte) (secret.Value, error) {
	return driver.write(ctx, name, value)
}

// Update writes a new current version for an existing secret.
// It first reads the secret, requiring read permission, then writes valid UTF-8
// text. This existence check is not a native compare-and-set transaction.
//
// Example:
//
//	written, err := driver.Update(ctx, "api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Update(ctx context.Context, name string, value []byte) (secret.Value, error) {
	_, err := driver.Get(ctx, secret.Reference{
		Name: name,
	})

	if err != nil {
		return secret.Value{}, err
	}

	return driver.write(ctx, name, value)
}

// write validates Key Vault's UTF-8 text representation before creating a
// version. Rejecting invalid bytes locally prevents silent JSON replacement.
func (driver *Driver) write(ctx context.Context, name string, value []byte) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	if !utf8.Valid(value) {
		return secret.Value{}, errors.New("azure Key Vault secret value must be valid UTF-8")
	}

	text := string(value)
	response, err := driver.client.SetSecret(ctx, name, azsecrets.SetSecretParameters{
		Value: &text,
	}, nil)

	if err != nil {
		return secret.Value{}, driver.mapError("set", name, err)
	}

	version := ""

	if response.ID != nil {
		version = response.ID.Version()
	}

	return secret.Value{
		Data:    append([]byte(nil), value...),
		Version: version,
	}, nil
}

// Delete removes name and all of its versions according to Key Vault retention policy.
// Soft deletion reserves the name; successful removal does not purge it.
//
// Example:
//
//	if err := driver.Delete(ctx, "retired-api-token"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := driver.client.DeleteSecret(ctx, name, nil)

	if err != nil {
		return driver.mapError("delete", name, err)
	}

	return nil
}

// mapError translates Azure Key Vault not-found responses.
func (driver *Driver) mapError(operation, name string, err error) error {
	var responseError *azcore.ResponseError

	if errors.As(err, &responseError) && responseError.StatusCode == 404 {
		return fmt.Errorf("%w: %s", secret.ErrNotFound, name)
	}

	// Key Vault keeps deleted secrets in a soft-deleted state; writing the
	// same name fails with 409 until the secret is purged or recovered.
	if errors.As(err, &responseError) && responseError.StatusCode == 409 {
		return fmt.Errorf("%s secret %q: %w: it is soft-deleted; purge or recover it first", operation, name, ErrSoftDeleted)
	}

	return fmt.Errorf("%s secret %q: %w", operation, name, err)
}

// ErrSoftDeleted reports a write to a secret name that is soft-deleted.
// Recover the retained resource when appropriate; do not automatically purge it.
//
// Example:
//
//	if errors.Is(err, azuresecret.ErrSoftDeleted) {
//		return fmt.Errorf("recover the retained secret before writing: %w", err)
//	}
var ErrSoftDeleted = errors.New("secret is soft-deleted")

var _ secret.Driver = (*Driver)(nil)
