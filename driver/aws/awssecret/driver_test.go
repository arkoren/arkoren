package awssecret

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/secrettest"
)

const (
	// miniStackEndpoint is the Compose MiniStack gateway endpoint.
	miniStackEndpoint = "http://127.0.0.1:4567"

	// miniStackAccessKey identifies the disposable MiniStack test account.
	miniStackAccessKey = "000000000000"

	// miniStackSecretKey authenticates the disposable MiniStack test account.
	miniStackSecretKey = "test"

	// miniStackRegion is the AWS region used by MiniStack test resources.
	miniStackRegion = "us-east-1"

	// miniStackOperationTimeout bounds MiniStack test operations.
	miniStackOperationTimeout = time.Minute
)

// secretsManagerClient provides predictable responses for deterministic client tests.
type secretsManagerClient struct {
	// output supplies the native read response without contacting AWS.
	output *secretsmanager.GetSecretValueOutput

	// err injects the same native failure into read, update, and delete calls.
	err error
}

// GetSecretValue returns the configured secret response.
func (client secretsManagerClient) GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	return client.output, client.err
}

// CreateSecret accepts a secret creation without persisting it.
func (secretsManagerClient) CreateSecret(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	return nil, nil
}

// PutSecretValue returns the configured error without persisting the write.
func (client secretsManagerClient) PutSecretValue(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	return &secretsmanager.PutSecretValueOutput{}, client.err
}

// DeleteSecret returns the configured error without persisting the deletion.
func (client secretsManagerClient) DeleteSecret(context.Context, *secretsmanager.DeleteSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	return &secretsmanager.DeleteSecretOutput{}, client.err
}

// newMiniStackClient returns a driver for the Compose MiniStack service.
func newMiniStackClient(t *testing.T, ctx context.Context) *Driver {
	t.Helper()

	return newMiniStackDriver(t, ctx)
}

// newMiniStackDriver creates a driver configured for the MiniStack endpoint.
func newMiniStackDriver(t *testing.T, ctx context.Context) *Driver {
	t.Helper()

	client, err := New(ctx, func(secrets *Builder) {
		secrets.Region(miniStackRegion)
		secrets.Endpoint(miniStackEndpoint)
		secrets.Credentials(credentials.NewStaticCredentialsProvider(miniStackAccessKey, miniStackSecretKey, ""))
	})

	if err != nil {
		t.Fatal(err)
	}

	return client
}

// newMiniStackSecretName creates a unique Secrets Manager-safe name for one test invocation.
func newMiniStackSecretName(t *testing.T) string {
	t.Helper()

	randomBytes := make([]byte, 8)

	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatal(err)
	}

	name := strings.Map(sanitizeMiniStackSecretName, t.Name())

	return fmt.Sprintf("arkoren/%s-%x", name, randomBytes)
}

// sanitizeMiniStackSecretName replaces unsupported AWS secret-name characters.
func sanitizeMiniStackSecretName(character rune) rune {
	if strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", character) {
		return character
	}

	if strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ", character) {
		return character + ('a' - 'A')
	}

	return '-'
}

// TestClientGetReturnsStringSecret verifies that Get returns a string secret.
func TestClientGetReturnsStringSecret(t *testing.T) {
	t.Parallel()

	client := NewFrom(secretsManagerClient{
		output: &secretsmanager.GetSecretValueOutput{
			SecretString: aws.String("value"),
			VersionId:    aws.String("v1"),
		},
	})

	value, err := client.Get(t.Context(), secret.Reference{
		Name: "example",
	})

	if err != nil || string(value.Data) != "value" || value.Version != "v1" {
		t.Fatalf("value = %#v, err = %v", value, err)
	}
}

// TestClientGetMapsNotFoundError verifies that Get maps a missing secret.
func TestClientGetMapsNotFoundError(t *testing.T) {
	t.Parallel()

	client := NewFrom(secretsManagerClient{
		err: &types.ResourceNotFoundException{},
	})

	_, err := client.Get(t.Context(), secret.Reference{
		Name: "missing",
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientGetMapsMarkedForDeletionError verifies that Get maps deletion state.
func TestClientGetMapsMarkedForDeletionError(t *testing.T) {
	t.Parallel()

	client := NewFrom(secretsManagerClient{
		err: &types.InvalidRequestException{
			Message: aws.String("secret is marked for deletion"),
		},
	})

	_, err := client.Get(t.Context(), secret.Reference{
		Name: "missing",
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientUpdateMapsMarkedForDeletionError verifies that Update maps deletion state.
func TestClientUpdateMapsMarkedForDeletionError(t *testing.T) {
	t.Parallel()

	client := NewFrom(secretsManagerClient{
		err: &types.InvalidRequestException{
			Message: aws.String("You can't perform this operation on the secret because it was marked for deletion."),
		},
	})

	_, err := client.Update(t.Context(), "deleted", []byte("value"))

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientDeleteMapsScheduledForDeletionError verifies that Delete maps deletion state.
func TestClientDeleteMapsScheduledForDeletionError(t *testing.T) {
	t.Parallel()

	client := NewFrom(secretsManagerClient{
		err: &types.InvalidRequestException{
			Message: aws.String("You can't perform this operation on the secret because it was already scheduled for deletion."),
		},
	})

	err := client.Delete(t.Context(), "deleted")

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientMiniStackSecretLifecycle verifies the AWS driver secret lifecycle.
func TestClientMiniStackSecretLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), miniStackOperationTimeout)
	t.Cleanup(cancel)
	client := newMiniStackClient(t, ctx)

	testMiniStackSecretLifecycle(t, client, ctx, newMiniStackSecretName(t))
}

// testMiniStackSecretLifecycle verifies MiniStack secret creation, versioning, and deletion.
func testMiniStackSecretLifecycle(t *testing.T, client *Driver, ctx context.Context, name string) {
	t.Helper()

	initial := []byte("initial value")

	created := setMiniStackSecret(t, client, ctx, name, initial)

	assertCreatedMiniStackSecret(t, created, initial)

	got := getMiniStackSecret(t, client, ctx, secret.Reference{
		Name: name,
	})

	assertMiniStackSecret(t, "got", got, initial, created.Version)

	updatedValue := []byte("updated value")

	updated := updateMiniStackSecret(t, client, ctx, name, updatedValue)

	assertUpdatedMiniStackSecret(t, updated, created.Version, updatedValue)

	versioned := getMiniStackSecret(t, client, ctx, secret.Reference{
		Name:    name,
		Version: created.Version,
	})

	assertMiniStackSecret(t, "versioned", versioned, initial, created.Version)

	deleteMiniStackSecret(t, client, ctx, name)

	assertMiniStackSecretDeleted(t, client, ctx, name)
}

// setMiniStackSecret creates one MiniStack secret and fails the calling test on error.
func setMiniStackSecret(t *testing.T, client *Driver, ctx context.Context, name string, value []byte) secret.Value {
	t.Helper()

	created, err := client.Put(ctx, name, value)

	if err != nil {
		t.Fatal(err)
	}

	return created
}

// getMiniStackSecret retrieves one MiniStack secret and fails the calling test on error.
func getMiniStackSecret(t *testing.T, client *Driver, ctx context.Context, reference secret.Reference) secret.Value {
	t.Helper()

	value, err := client.Get(ctx, reference)

	if err != nil {
		t.Fatal(err)
	}

	return value
}

// updateMiniStackSecret writes one MiniStack secret version and fails on error.
func updateMiniStackSecret(t *testing.T, client *Driver, ctx context.Context, name string, value []byte) secret.Value {
	t.Helper()

	updated, err := client.Update(ctx, name, value)

	if err != nil {
		t.Fatal(err)
	}

	return updated
}

// deleteMiniStackSecret deletes one MiniStack secret and fails the calling test on error.
func deleteMiniStackSecret(t *testing.T, client *Driver, ctx context.Context, name string) {
	t.Helper()

	if err := client.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
}

// assertCreatedMiniStackSecret verifies the first MiniStack secret version.
func assertCreatedMiniStackSecret(t *testing.T, value secret.Value, expected []byte) {
	t.Helper()

	if value.Version == "" || string(value.Data) != string(expected) {
		t.Fatalf("created = %#v", value)
	}
}

// assertMiniStackSecret verifies a MiniStack secret's data and version.
func assertMiniStackSecret(t *testing.T, label string, value secret.Value, expected []byte, version string) {
	t.Helper()

	if string(value.Data) != string(expected) || value.Version != version {
		t.Fatalf("%s = %#v", label, value)
	}
}

// assertUpdatedMiniStackSecret verifies a distinct current MiniStack secret version.
func assertUpdatedMiniStackSecret(t *testing.T, value secret.Value, previous string, expected []byte) {
	t.Helper()

	if value.Version == "" || value.Version == previous || string(value.Data) != string(expected) {
		t.Fatalf("updated = %#v", value)
	}
}

// assertMiniStackSecretDeleted verifies a deleted MiniStack secret is not found.
func assertMiniStackSecretDeleted(t *testing.T, client *Driver, ctx context.Context, name string) {
	t.Helper()

	_, err := client.Get(ctx, secret.Reference{
		Name: name,
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestMiniStackConformance runs the secret conformance suite against MiniStack.
func TestMiniStackConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	secrettest.Run(t, func(t *testing.T) secret.Driver {
		return newMiniStackClient(t, t.Context())
	}, secrettest.WithMissingVersion("00000000-0000-0000-0000-000000000000"))
}
