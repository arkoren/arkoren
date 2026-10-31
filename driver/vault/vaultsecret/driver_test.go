package vaultsecret_test

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/vault/api"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/secrettest"
	"arkoren.dev/driver/vault/vaultsecret"
)

const (
	// vaultAddress is the Compose Vault development server address.
	vaultAddress = "http://127.0.0.1:8200"

	// vaultRootToken authenticates against the disposable development server.
	vaultRootToken = "arkoren-integration"

	// vaultOperationTimeout bounds each Vault test operation.
	vaultOperationTimeout = 10 * time.Second

	// vaultCleanupTimeout bounds deletion after the test context has ended.
	vaultCleanupTimeout = 10 * time.Second
)

// vaultClient provides predictable responses for deterministic client tests.
type vaultClient struct {
	// secret supplies the configured result for current and versioned reads.
	secret *api.KVSecret

	// err injects the native read failure without contacting Vault.
	err error
}

// Get returns the configured secret response.
func (client vaultClient) Get(context.Context, string) (*api.KVSecret, error) {
	return client.secret, client.err
}

// GetVersion returns the configured secret response.
func (client vaultClient) GetVersion(context.Context, string, int) (*api.KVSecret, error) {
	return client.secret, client.err
}

// Put accepts a secret write without persisting it.
func (vaultClient) Put(
	context.Context,
	string,
	map[string]any,
	...api.KVOption,
) (*api.KVSecret, error) {
	return nil, nil
}

// DeleteMetadata accepts metadata deletion without persisting it.
func (vaultClient) DeleteMetadata(context.Context, string) error {
	return nil
}

// newVaultClient creates a driver for the shared Vault development server.
func newVaultClient(t *testing.T) *vaultsecret.Driver {
	t.Helper()

	client, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
		secrets.Address(vaultAddress)
		secrets.Token(vaultRootToken)
	})

	if err != nil {
		t.Fatal(err)
	}

	return client
}

// newVaultOperationContext creates a bounded context for one Vault test.
func newVaultOperationContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), vaultOperationTimeout)
	t.Cleanup(cancel)

	return ctx
}

// cleanupVaultSecret removes a secret path created by one test.
func cleanupVaultSecret(t *testing.T, client *vaultsecret.Driver, path string) {
	t.Helper()

	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), vaultCleanupTimeout)
	defer cancel()

	if err := client.Delete(cleanupContext, path); err != nil {
		t.Errorf("delete Vault secret %q during cleanup: %v", path, err)
	}
}

// newVaultSecretPath creates a unique Vault-safe path for one test invocation.
func newVaultSecretPath(t *testing.T) string {
	t.Helper()

	randomBytes := make([]byte, 8)

	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatal(err)
	}

	name := strings.Map(sanitizeVaultSecretPath, t.Name())

	return fmt.Sprintf("integration/%s-%x", name, randomBytes)
}

// sanitizeVaultSecretPath replaces unsupported Vault secret-path characters.
func sanitizeVaultSecretPath(character rune) rune {
	if strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", character) {
		return character
	}

	if strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ", character) {
		return character + ('a' - 'A')
	}

	return '-'
}

// TestClientGetReturnsJSONData verifies that Get encodes Vault data as JSON.
func TestClientGetReturnsJSONData(t *testing.T) {
	t.Parallel()

	ctx := newVaultOperationContext(t)

	client := vaultsecret.NewFrom(vaultClient{
		secret: &api.KVSecret{
			Data: map[string]any{
				"database": map[string]any{"dsn": "postgres://example"},
			},
		},
	})

	contents, err := client.Get(ctx, secret.Reference{
		Name: "api/config",
	})

	if err != nil {
		t.Fatal(err)
	}

	var got any

	if err := json.Unmarshal(contents.Data, &got); err != nil {
		t.Fatal(err)
	}

	if got.(map[string]any)["database"].(map[string]any)["dsn"] != "postgres://example" {
		t.Fatalf("value=%v", got)
	}
}

// TestClientGetMapsNilSecret verifies that Get maps a nil Vault secret.
func TestClientGetMapsNilSecret(t *testing.T) {
	t.Parallel()

	ctx := newVaultOperationContext(t)

	client := vaultsecret.NewFrom(vaultClient{})

	_, err := client.Get(ctx, secret.Reference{
		Name: "missing",
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
}

// TestClientSetAndGetVaultSecret verifies that Put persists a Vault secret.
func TestClientSetAndGetVaultSecret(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx := newVaultOperationContext(t)
	client := newVaultClient(t)
	path := newVaultSecretPath(t)
	t.Cleanup(func() {
		cleanupVaultSecret(t, client, path)
	})

	want := []byte(`{"database":{"dsn":"postgres://vault"}}`)

	created := setVaultSecret(t, client, ctx, path, want)

	assertVaultVersion(t, created.Version, "1")

	got := getVaultSecret(t, client, ctx, path)

	assertVaultData(t, got.Data, want)
}

// TestClientUpdateVaultSecret verifies that Update creates the next version.
func TestClientUpdateVaultSecret(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	testVaultSecretUpdate(t)
}

// testVaultSecretUpdate verifies that Vault assigns the next version on update.
func testVaultSecretUpdate(t *testing.T) {
	t.Helper()

	ctx := newVaultOperationContext(t)
	client := newVaultClient(t)
	path := newVaultSecretPath(t)
	t.Cleanup(func() {
		cleanupVaultSecret(t, client, path)
	})

	seedVaultSecret(t, client, ctx, path)

	want := []byte(`{"enabled":true}`)

	updated := updateVaultSecret(t, client, ctx, path, want)

	assertVaultVersion(t, updated.Version, "2")

	got := getVaultSecret(t, client, ctx, path)

	assertVaultData(t, got.Data, want)
}

// seedVaultSecret writes the initial Vault secret version for an update test.
func seedVaultSecret(t *testing.T, client *vaultsecret.Driver, ctx context.Context, path string) {
	t.Helper()

	if _, err := client.Put(ctx, path, []byte(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
}

// setVaultSecret writes a Vault secret and fails the calling test on error.
func setVaultSecret(t *testing.T, client *vaultsecret.Driver, ctx context.Context, path string, data []byte) secret.Value {
	t.Helper()

	value, err := client.Put(ctx, path, data)

	if err != nil {
		t.Fatal(err)
	}

	return value
}

// updateVaultSecret writes an updated Vault secret and fails the calling test on error.
func updateVaultSecret(t *testing.T, client *vaultsecret.Driver, ctx context.Context, path string, value []byte) secret.Value {
	t.Helper()

	updated, err := client.Update(ctx, path, value)

	if err != nil {
		t.Fatal(err)
	}

	return updated
}

// getVaultSecret retrieves the current Vault secret and fails the calling test on error.
func getVaultSecret(t *testing.T, client *vaultsecret.Driver, ctx context.Context, path string) secret.Value {
	t.Helper()

	value, err := client.Get(ctx, secret.Reference{
		Name: path,
	})

	if err != nil {
		t.Fatal(err)
	}

	return value
}

// assertVaultVersion verifies the Vault version expected by a test scenario.
func assertVaultVersion(t *testing.T, actual, expected string) {
	t.Helper()

	if actual != expected {
		t.Fatalf("version=%q", actual)
	}
}

// assertVaultData verifies the Vault secret data expected by a test scenario.
func assertVaultData(t *testing.T, actual, expected []byte) {
	t.Helper()

	if string(actual) != string(expected) {
		t.Fatalf("data=%s", actual)
	}
}

// TestClientDeleteVaultSecret verifies that Delete removes a Vault secret.
func TestClientDeleteVaultSecret(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx := newVaultOperationContext(t)
	client := newVaultClient(t)
	path := newVaultSecretPath(t)

	if _, err := client.Put(ctx, path, []byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}

	if err := client.Delete(ctx, path); err != nil {
		t.Fatal(err)
	}

	_, err := client.Get(ctx, secret.Reference{
		Name: path,
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
}

// TestClientGetMapsMissingVaultSecret verifies that Get maps a missing Vault secret.
func TestClientGetMapsMissingVaultSecret(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx := newVaultOperationContext(t)
	client := newVaultClient(t)

	_, err := client.Get(ctx, secret.Reference{
		Name: newVaultSecretPath(t),
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
}

// TestVaultConformance runs the secret conformance suite against Vault.
func TestVaultConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	secrettest.Run(t, func(t *testing.T) secret.Driver {
		return newVaultClient(t)
	}, secrettest.WithJSONObjectValues())
}

// vaultTransport exercises real Vault SDK encoding and error handling without
// opening sockets. Each function owns one test's isolated protocol scenario.
type vaultTransport func(*http.Request) (*http.Response, error)

// RoundTrip delegates a native SDK request, including its cancellation context.
// Tests that block must release their worker before cleanup completes.
func (vaultTransport vaultTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := vaultTransport(request)

	if response != nil {
		response.Request = request
	}

	return response, err
}

// nativeVaultDriver creates a borrowed KV v2 client using only a synthetic token
// and a supplied in-memory HTTP transport. It never reads Vault environment state.
func nativeVaultDriver(t *testing.T, transport http.RoundTripper) *vaultsecret.Driver {
	t.Helper()

	native, err := api.NewClient(&api.Config{
		Address:    "https://vault.example",
		HttpClient: &http.Client{Transport: transport},
	})

	if err != nil {
		t.Fatal(err)
	}

	native.SetToken("synthetic-test-token")

	return vaultsecret.NewFrom(native.KVv2("secret"))
}

// vaultResponse supplies the SDK's JSON response shape without owning a socket
// or native client. Callers choose the status to exercise actual SDK errors.
func vaultResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestPutPreservesJSONNumberPrecision checks the SDK's serialized request for
// native-round-trippable numbers. Validation may inspect their values but must
// not rewrite their tokens or convert the transmitted members through float64.
func TestPutPreservesJSONNumberPrecision(t *testing.T) {
	t.Parallel()

	var body string
	driver := nativeVaultDriver(t, vaultTransport(func(request *http.Request) (*http.Response, error) {
		contents, err := io.ReadAll(request.Body)
		body = string(contents)

		return vaultResponse(http.StatusOK, `{"data":{"version":1}}`), err
	}))
	input := []byte(`{"nested":{"id":9007199254740992,"fraction":1.2300e2}}`)

	written, err := driver.Put(context.Background(), "application", input)

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(body, `9007199254740992`) || !strings.Contains(body, `1.2300e2`) {
		t.Fatalf("native JSON lost numeric precision: %s", body)
	}

	if string(written.Data) != string(input) || written.Version != "1" {
		t.Fatal("Put did not return the written value and native version")
	}
}

// TestPutRejectsLossyNumbersBeforeNativeWrite keeps exact numeric values from
// being silently changed by Vault's native float64 JSON boundary. Nested arrays
// and objects receive the same policy; no rejected payload creates a version.
func TestPutRejectsLossyNumbersBeforeNativeWrite(t *testing.T) {
	t.Parallel()

	requests := 0
	driver := nativeVaultDriver(t, vaultTransport(func(*http.Request) (*http.Response, error) {
		requests++

		return vaultResponse(http.StatusOK, `{"data":{"version":1}}`), nil
	}))

	for _, input := range []string{
		`{"id":9007199254740993}`,
		`{"id":9007199254740995}`,
		`{"nested":[{"fraction":0.123456789012345678901}]}`,
		`{"overflow":1e1000000000}`,
		`{"underflow":1e-1000000000}`,
	} {
		if _, err := driver.Put(t.Context(), "application", []byte(input)); err == nil {
			t.Error("Put accepted a number that changes at the native JSON boundary")
		}
	}

	if requests != 0 {
		t.Fatalf("rejected payloads made %d native requests", requests)
	}
}

// TestPutAcceptsExactStringsAndEquivalentNumberSpellings retains native JSON
// interoperability: exact IDs/decimals are application-authored strings, while
// equivalent numeric notation and ordinary round-trippable decimals stay numbers.
func TestPutAcceptsExactStringsAndEquivalentNumberSpellings(t *testing.T) {
	t.Parallel()

	driver := nativeVaultDriver(t, vaultTransport(func(*http.Request) (*http.Response, error) {
		return vaultResponse(http.StatusOK, `{"data":{"version":1}}`), nil
	}))
	input := []byte(`{"id":"9007199254740993","fraction":"0.123456789012345678901","numbers":[1e3,1.0000,-0e999999,0.1]}`)

	written, err := driver.Put(t.Context(), "application", input)

	if err != nil {
		t.Fatal(err)
	}

	if string(written.Data) != string(input) {
		t.Fatal("Put changed the application-authored JSON representation")
	}
}

// TestUpdateRejectsLossyNumbersWithoutNativeWrite permits the existing-version
// read but forbids mutation when its replacement would lose numeric precision.
// The existing CAS path is left in place for valid replacements.
func TestUpdateRejectsLossyNumbersWithoutNativeWrite(t *testing.T) {
	t.Parallel()

	writes := 0
	driver := nativeVaultDriver(t, vaultTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return vaultResponse(http.StatusOK, `{"data":{"data":{"id":1},"metadata":{"version":7}}}`), nil
		}

		writes++

		return vaultResponse(http.StatusOK, `{"data":{"version":8}}`), nil
	}))

	if _, err := driver.Update(t.Context(), "application", []byte(`{"id":9007199254740993}`)); err == nil {
		t.Fatal("Update accepted a replacement that loses numeric precision")
	}

	if writes != 0 {
		t.Fatal("Update wrote a replacement that loses numeric precision")
	}
}

// TestPutRejectsNullBeforeNativeWrite distinguishes a JSON object from JSON
// null. The provider must not receive an invalid object or create a version.
func TestPutRejectsNullBeforeNativeWrite(t *testing.T) {
	t.Parallel()

	requests := 0
	driver := nativeVaultDriver(t, vaultTransport(func(*http.Request) (*http.Response, error) {
		requests++

		return vaultResponse(http.StatusOK, `{"data":{"version":1}}`), nil
	}))

	_, err := driver.Put(t.Context(), "application", []byte("null"))

	if err == nil || requests != 0 {
		t.Fatalf("Put(null) error = %v, requests = %d; want local rejection", err, requests)
	}
}

// TestGetRejectsNonpositiveVersions protects immutable version selection from
// Vault's special version-zero current-value behavior and negative arguments.
func TestGetRejectsNonpositiveVersions(t *testing.T) {
	t.Parallel()

	requests := 0
	driver := nativeVaultDriver(t, vaultTransport(func(*http.Request) (*http.Response, error) {
		requests++

		return vaultResponse(http.StatusOK, `{"data":{"data":{},"metadata":{"version":1}}}`), nil
	}))

	for _, version := range []string{"0", "-1"} {
		_, err := driver.Get(t.Context(), secret.Reference{Name: "application", Version: version})

		if !errors.Is(err, secret.ErrNotFound) {
			t.Errorf("Get version %q error = %v, want ErrNotFound", version, err)
		}
	}

	if requests != 0 {
		t.Fatalf("nonpositive versions made %d native requests", requests)
	}
}

// TestGetMapsDeletedVersionPayload exercises the real SDK's nil-data response
// for a deleted or destroyed version, which is missing rather than JSON null.
func TestGetMapsDeletedVersionPayload(t *testing.T) {
	t.Parallel()

	driver := nativeVaultDriver(t, vaultTransport(func(*http.Request) (*http.Response, error) {
		return vaultResponse(http.StatusOK, `{"data":{"data":null,"metadata":{"version":1,"destroyed":true}}}`), nil
	}))

	_, err := driver.Get(t.Context(), secret.Reference{Name: "application", Version: "1"})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get destroyed version error = %v, want ErrNotFound", err)
	}
}

// TestCanceledVaultOperationsPrecedeValidation requires cancellation identity
// before invalid version/JSON errors and proves no HTTP side effect occurred.
func TestCanceledVaultOperationsPrecedeValidation(t *testing.T) {
	t.Parallel()

	requests := 0
	driver := nativeVaultDriver(t, vaultTransport(func(*http.Request) (*http.Response, error) {
		requests++

		return nil, errors.New("unexpected native request")
	}))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, get := driver.Get(ctx, secret.Reference{Name: "application", Version: "0"})
	_, put := driver.Put(ctx, "application", []byte("invalid JSON"))
	_, update := driver.Update(ctx, "application", []byte("invalid JSON"))
	deleted := driver.Delete(ctx, "application")

	for _, err := range []error{get, put, update, deleted} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("operation error = %v, want context.Canceled", err)
		}
	}

	if requests != 0 {
		t.Fatalf("canceled operations made %d native requests", requests)
	}
}

// TestUpdateDoesNotRecreateConcurrentlyDeletedSecret models a deletion between
// the read and write at the native HTTP boundary. A nonzero Vault CAS must make
// the write fail instead of creating a replacement version at the same name.
func TestUpdateDoesNotRecreateConcurrentlyDeletedSecret(t *testing.T) {
	t.Parallel()

	removed, recreated := false, false
	driver := nativeVaultDriver(t, vaultTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			if removed {
				return vaultResponse(http.StatusNotFound, `{"errors":[]}`), nil
			}

			removed = true

			return vaultResponse(http.StatusOK, `{"data":{"data":{"enabled":false},"metadata":{"version":7}}}`), nil
		}

		var body struct {
			// Options carries the native Vault CAS precondition, not driver state.
			Options map[string]int `json:"options"`
		}

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			return nil, err
		}

		if body.Options["cas"] == 7 {
			return vaultResponse(http.StatusBadRequest, `{"errors":["check-and-set parameter did not match the current version"]}`), nil
		}

		recreated = true

		return vaultResponse(http.StatusOK, `{"data":{"version":1}}`), nil
	}))

	_, err := driver.Update(t.Context(), "application", []byte(`{"enabled":true}`))

	if !errors.Is(err, secret.ErrNotFound) || recreated {
		t.Fatalf("Update error = %v, recreated = %t; want ErrNotFound without creation", err, recreated)
	}
}

// disposableVaultDriver permits only an explicitly provisioned loopback Vault
// development server with the synthetic token used by the repository fixture.
// It neither starts infrastructure nor falls back to a shared running service.
func disposableVaultDriver(t *testing.T) *vaultsecret.Driver {
	t.Helper()

	endpoint := os.Getenv("ARKOREN_DISPOSABLE_VAULT_ENDPOINT")

	if testing.Short() || endpoint == "" {
		t.Skip("requires an explicitly provisioned disposable Vault server")
	}

	parsed, err := url.Parse(endpoint)

	if err != nil {
		t.Fatal(err)
	}

	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" {
		t.Fatal("disposable Vault endpoint must be loopback")
	}

	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
		secrets.Address(endpoint)
		secrets.Token("arkoren-integration")
	})

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// disposableVaultName registers deletion before a test can publish its unique
// fixture. A failed rejection test is therefore cleaned up even on an older
// implementation that accidentally accepts its unsafe numeric payload.
func disposableVaultName(t *testing.T, driver *vaultsecret.Driver) string {
	t.Helper()

	name := "audit/" + rand.Text()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := driver.Delete(ctx, name); err != nil && !errors.Is(err, secret.ErrNotFound) {
			t.Error(err)
		}
	})

	return name
}

// precisionFixture separates exact application-authored strings from numeric
// values. Comparing decoded values permits equivalent exponent notation without
// hiding rounding of the integer or changing either string's contents.
type precisionFixture struct {
	// ID is an exact identifier that exceeds consecutive float64 integer precision.
	ID string `json:"id"`
	// Fraction retains an application-authored exact decimal representation.
	Fraction string `json:"fraction"`
	// Count is an accepted, exactly representable large integer.
	Count int64 `json:"count"`
	// Notation checks numeric equivalence between 1e3 and 1000.000.
	Notation float64 `json:"notation"`
}

// requirePrecisionFixture compares values obtained through the public driver,
// not numeric substrings or a private encoder. No fixture contains credentials.
func requirePrecisionFixture(t *testing.T, value secret.Value, expected precisionFixture) {
	t.Helper()

	var actual precisionFixture

	if err := json.Unmarshal(value.Data, &actual); err != nil {
		t.Fatal(err)
	}

	if actual != expected {
		t.Fatalf("native fixture = %+v, want %+v", actual, expected)
	}
}

// TestJSONPrecisionAndCASOnDisposableVault checks precision with explicit string
// representation, accepted native numbers, conditional Update, and historical
// versions. A coordinator must provision the disposable KV v2 mount.
func TestJSONPrecisionAndCASOnDisposableVault(t *testing.T) {
	driver := disposableVaultDriver(t)

	t.Parallel()

	name := disposableVaultName(t, driver)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	created, err := driver.Put(ctx, name, []byte(`{"id":"9007199254740993","fraction":"0.123456789012345678901","count":9007199254740992,"notation":1e3}`))

	if err != nil {
		t.Fatal(err)
	}

	updated, err := driver.Update(ctx, name, []byte(`{"id":"9007199254740995","fraction":"0.123456789012345678903","count":9007199254740994,"notation":1000.000}`))

	if err != nil {
		t.Fatal(err)
	}

	historical, err := driver.Get(ctx, secret.Reference{Name: name, Version: created.Version})

	if err != nil {
		t.Fatal(err)
	}

	current, err := driver.Get(ctx, secret.Reference{Name: name})

	if err != nil {
		t.Fatal(err)
	}

	requirePrecisionFixture(t, historical, precisionFixture{
		ID: "9007199254740993", Fraction: "0.123456789012345678901", Count: 9007199254740992, Notation: 1000,
	})
	requirePrecisionFixture(t, current, precisionFixture{
		ID: "9007199254740995", Fraction: "0.123456789012345678903", Count: 9007199254740994, Notation: 1000,
	})

	if created.Version == updated.Version || current.Version != updated.Version {
		t.Fatal("CAS update did not publish a distinct current version")
	}
}

// TestLossyNumbersDoNotCreateDisposableVaultSecret retains the original failing
// input as a rejection regression. Backend rounding cannot be mislabeled success,
// and a rejected write must not leave even an initial version behind.
func TestLossyNumbersDoNotCreateDisposableVaultSecret(t *testing.T) {
	driver := disposableVaultDriver(t)

	t.Parallel()

	name := disposableVaultName(t, driver)

	if _, err := driver.Put(t.Context(), name, []byte(`{"id":9007199254740993}`)); err == nil {
		t.Fatal("Put accepted the original native rounding regression")
	}

	if _, err := driver.Get(t.Context(), secret.Reference{Name: name}); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get rejected write error = %v, want ErrNotFound", err)
	}
}
