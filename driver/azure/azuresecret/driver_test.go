package azuresecret_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/secrettest"
	"arkoren.dev/driver/azure/azuresecret"
)

// keyVaultClient records Azure Key Vault requests for client tests.
type keyVaultClient struct {
	// getResponse supplies the configured secret value and version identifier.
	getResponse azsecrets.GetSecretResponse

	// getErr injects a native read failure; nil allows getResponse.
	getErr error

	// setResponse supplies the configured identifier after a write.
	setResponse azsecrets.SetSecretResponse

	// setErr injects a native write failure; nil allows setResponse.
	setErr error

	// deleteResponse supplies the configured native deletion result.
	deleteResponse azsecrets.DeleteSecretResponse

	// deleteErr injects a native deletion failure.
	deleteErr error

	// getName retains the last secret name requested by a read.
	getName string

	// getVersion retains the selected version, including empty for the latest.
	getVersion string

	// setName retains the last secret name passed to a write.
	setName string

	// setValue retains the supplied string when the SDK value pointer is non-nil.
	setValue string

	// deleteName retains the last secret name selected for deletion.
	deleteName string
}

// GetSecret records and returns the configured secret response.
func (client *keyVaultClient) GetSecret(
	_ context.Context,
	name string,
	version string,
	_ *azsecrets.GetSecretOptions,
) (azsecrets.GetSecretResponse, error) {
	client.getName = name
	client.getVersion = version

	return client.getResponse, client.getErr
}

// SetSecret records and returns the configured secret response.
func (client *keyVaultClient) SetSecret(
	_ context.Context,
	name string,
	parameters azsecrets.SetSecretParameters,
	_ *azsecrets.SetSecretOptions,
) (azsecrets.SetSecretResponse, error) {
	client.setName = name

	if parameters.Value != nil {
		client.setValue = *parameters.Value
	}

	return client.setResponse, client.setErr
}

// DeleteSecret records and returns the configured secret response.
func (client *keyVaultClient) DeleteSecret(
	_ context.Context,
	name string,
	_ *azsecrets.DeleteSecretOptions,
) (azsecrets.DeleteSecretResponse, error) {
	client.deleteName = name

	return client.deleteResponse, client.deleteErr
}

// requireSecretValue verifies a secret value and version.
func requireSecretValue(t *testing.T, value secret.Value, wantData, wantVersion string) {
	t.Helper()

	if string(value.Data) != wantData || value.Version != wantVersion {
		t.Fatalf("value = %#v", value)
	}
}

// requireGetRequest verifies a recorded Azure Key Vault get request.
func requireGetRequest(t *testing.T, client *keyVaultClient, name, version string) {
	t.Helper()

	if client.getName != name || client.getVersion != version {
		t.Fatalf("get name = %q, version = %q", client.getName, client.getVersion)
	}
}

// requireSetRequest verifies a recorded Azure Key Vault set request.
func requireSetRequest(t *testing.T, client *keyVaultClient, name, value string) {
	t.Helper()

	if client.setName != name || client.setValue != value {
		t.Fatalf("set name = %q, value = %q", client.setName, client.setValue)
	}
}

// TestClientSetCreatesSecret verifies that Put writes a new Azure secret.
func TestClientSetCreatesSecret(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	identifier := azsecrets.ID("https://example.vault.azure.net/secrets/database-dsn/version-1")
	fake := &keyVaultClient{
		setResponse: azsecrets.SetSecretResponse{
			Secret: azsecrets.Secret{
				ID: &identifier,
			},
		},
	}
	client := azuresecret.NewFrom(fake)

	value, err := client.Put(ctx, "database-dsn", []byte("postgres://example"))

	if err != nil {
		t.Fatalf("set secret: %v", err)
	}

	requireSecretValue(t, value, "postgres://example", "version-1")
	requireSetRequest(t, fake, "database-dsn", "postgres://example")
}

// TestClientGetUsesReferenceVersion verifies that Get passes the requested version.
func TestClientGetUsesReferenceVersion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	text := "postgres://example"
	identifier := azsecrets.ID("https://example.vault.azure.net/secrets/database-dsn/version-1")
	fake := &keyVaultClient{
		getResponse: azsecrets.GetSecretResponse{
			Secret: azsecrets.Secret{
				ID:    &identifier,
				Value: &text,
			},
		},
	}
	client := azuresecret.NewFrom(fake)

	value, err := client.Get(ctx, secret.Reference{
		Name:    "database-dsn",
		Version: "version-1",
	})

	if err != nil {
		t.Fatalf("get secret: %v", err)
	}

	requireSecretValue(t, value, "postgres://example", "version-1")
	requireGetRequest(t, fake, "database-dsn", "version-1")
}

// TestClientUpdateWritesCurrentVersion verifies that Update writes a new version.
func TestClientUpdateWritesCurrentVersion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	existing := "postgres://old"
	identifier := azsecrets.ID("https://example.vault.azure.net/secrets/database-dsn/version-2")
	fake := &keyVaultClient{
		getResponse: azsecrets.GetSecretResponse{
			Secret: azsecrets.Secret{
				Value: &existing,
			},
		},
		setResponse: azsecrets.SetSecretResponse{
			Secret: azsecrets.Secret{
				ID: &identifier,
			},
		},
	}
	client := azuresecret.NewFrom(fake)

	value, err := client.Update(ctx, "database-dsn", []byte("postgres://new"))

	if err != nil {
		t.Fatalf("update secret: %v", err)
	}

	requireSecretValue(t, value, "postgres://new", "version-2")
	requireGetRequest(t, fake, "database-dsn", "")
	requireSetRequest(t, fake, "database-dsn", "postgres://new")
}

// TestClientDeleteRemovesSecret verifies that Delete removes an Azure secret.
func TestClientDeleteRemovesSecret(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	fake := &keyVaultClient{}
	client := azuresecret.NewFrom(fake)

	err := client.Delete(ctx, "database-dsn")

	if err != nil {
		t.Fatalf("delete secret: %v", err)
	}

	if fake.deleteName != "database-dsn" {
		t.Fatalf("delete name = %q", fake.deleteName)
	}
}

// TestClientGetMapsNotFoundResponse verifies that Get maps a missing Azure secret.
func TestClientGetMapsNotFoundResponse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	fake := &keyVaultClient{
		getErr: &azcore.ResponseError{
			StatusCode: 404,
		},
	}
	client := azuresecret.NewFrom(fake)

	_, err := client.Get(ctx, secret.Reference{
		Name: "missing",
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}

	if fake.getName != "missing" || fake.getVersion != "" {
		t.Fatalf("get name = %q, version = %q", fake.getName, fake.getVersion)
	}
}

// TestNewRejectsInvalidVaultURL verifies that New rejects vault URLs that are
// empty, relative, or not https, and names VaultURL in the error.
func TestNewRejectsInvalidVaultURL(t *testing.T) {
	t.Parallel()

	for _, vaultURL := range []string{"", "myvault", "http://myvault.vault.azure.net", "https://"} {
		_, err := azuresecret.New(t.Context(), vaultURL)

		if err == nil || !strings.Contains(err.Error(), "VaultURL") {
			t.Fatalf("New(%q) error = %v, want error naming VaultURL", vaultURL, err)
		}
	}
}

// keyVaultURLEnv names the environment variable holding the URL of a Key
// Vault the conformance suite may use.
const keyVaultURLEnv = "ARKOREN_AZURE_KEYVAULT_URL"

// TestAzureConformance runs the secret conformance suite against Azure Key
// Vault. It needs Azure's default credential chain and the vault named by
// ARKOREN_AZURE_KEYVAULT_URL. Key Vault keeps deleted secrets soft-deleted, so
// each run leaves uniquely named secrets awaiting purge.
func TestAzureConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	vaultURL := os.Getenv(keyVaultURLEnv)

	if vaultURL == "" {
		t.Skipf("set %s to run against Azure Key Vault", keyVaultURLEnv)
	}

	t.Parallel()

	secrettest.Run(t, func(t *testing.T) secret.Driver {
		driver, err := azuresecret.New(t.Context(), vaultURL)

		if err != nil {
			t.Fatal(err)
		}

		return driver
	}, secrettest.WithTextValues(), secrettest.WithMissingVersion("00000000000000000000000000000000"))
}

// azureHTTPFunc handles real SDK requests in memory. No default Azure
// credentials, network endpoints, or private driver helpers are involved.
type azureHTTPFunc func(*http.Request) (*http.Response, error)

// Do supplies the HTTP protocol response chosen by one isolated test.
// Blocking implementations must honor the request context.
func (azureHTTPFunc azureHTTPFunc) Do(request *http.Request) (*http.Response, error) {
	return azureHTTPFunc(request)
}

// syntheticCredential is an explicit native credential, not a production token
// source. It avoids environment and developer-account fallback in SDK tests.
type syntheticCredential struct{}

// GetToken returns a fixed synthetic credential with a deterministic expiry.
// Neither this credential nor its token grants access to an Azure resource.
func (syntheticCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "synthetic-test-token", ExpiresOn: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

// nativeAzureDriver borrows a real SDK client with the test's HTTP transport.
// The dummy HTTPS origin satisfies SDK policy without opening a connection.
func nativeAzureDriver(t *testing.T, transport policy.Transporter) *azuresecret.Driver {
	t.Helper()

	native, err := azsecrets.NewClient("https://vault.example", syntheticCredential{}, &azsecrets.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: transport},
	})

	if err != nil {
		t.Fatal(err)
	}

	return azuresecret.NewFrom(native)
}

// TestPutRejectsInvalidUTF8BeforeNativeWrite prevents the SDK JSON encoder from
// silently replacing bytes while the driver returns the original payload.
func TestPutRejectsInvalidUTF8BeforeNativeWrite(t *testing.T) {
	t.Parallel()

	requests := 0
	driver := nativeAzureDriver(t, azureHTTPFunc(func(*http.Request) (*http.Response, error) {
		requests++

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"https://vault.example/secrets/example/version-1","value":"replacement"}`)),
		}, nil
	}))

	_, err := driver.Put(t.Context(), "example", []byte{0xff})

	if err == nil || requests != 0 {
		t.Fatalf("Put invalid UTF-8 error = %v, requests = %d; want local rejection", err, requests)
	}
}

// TestCanceledAzureOperationsPrecedeValidation protects cancellation identity
// before UTF-8 and native parameter checks, and forbids all provider side effects.
func TestCanceledAzureOperationsPrecedeValidation(t *testing.T) {
	t.Parallel()

	requests := 0
	driver := nativeAzureDriver(t, azureHTTPFunc(func(*http.Request) (*http.Response, error) {
		requests++

		return nil, context.Canceled
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, get := driver.Get(ctx, secret.Reference{})
	_, put := driver.Put(ctx, "example", []byte{0xff})
	_, update := driver.Update(ctx, "", []byte{0xff})
	deleted := driver.Delete(ctx, "")

	for _, err := range []error{get, put, update, deleted} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("operation error = %v, want context.Canceled", err)
		}
	}

	if requests != 0 {
		t.Fatalf("canceled operations made %d native requests", requests)
	}
}
