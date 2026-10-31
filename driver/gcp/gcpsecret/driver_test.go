package gcpsecret

import (
	"context"
	"errors"
	"os"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/secrettest"
)

// gcpProjectEnv names the environment variable holding a Google Cloud
// project whose Secret Manager the conformance suite may use.
const gcpProjectEnv = "ARKOREN_GCP_PROJECT"

// TestGoogleConformance runs the secret conformance suite against Google
// Cloud Secret Manager. It needs application default credentials and the
// project named by ARKOREN_GCP_PROJECT, and leaves nothing behind.
func TestGoogleConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	project := os.Getenv(gcpProjectEnv)

	if project == "" {
		t.Skipf("set %s to run against Google Cloud Secret Manager", gcpProjectEnv)
	}

	t.Parallel()

	secrettest.Run(t, func(t *testing.T) secret.Driver {
		driver, err := New(t.Context(), func(secrets *Builder) {
			secrets.Project(project)
		})

		if err != nil {
			t.Fatal(err)
		}

		return driver
	})
}

// secretManagerClient records Google Cloud Secret Manager requests for client tests.
type secretManagerClient struct {
	// accessRequest retains the last version read for resource-name assertions.
	accessRequest *secretmanagerpb.AccessSecretVersionRequest

	// accessResponse supplies the configured payload and version to reads.
	accessResponse *secretmanagerpb.AccessSecretVersionResponse

	// accessErr injects a native read failure; nil allows accessResponse.
	accessErr error

	// createRequest retains the resource and replication settings for creation.
	createRequest *secretmanagerpb.CreateSecretRequest

	// createResponse supplies the configured secret metadata after creation.
	createResponse *secretmanagerpb.Secret

	// createErr injects creation failures, including an already-existing secret.
	createErr error

	// addRequest retains the parent and payload passed to version creation.
	addRequest *secretmanagerpb.AddSecretVersionRequest

	// addResponse supplies the newly published native version name.
	addResponse *secretmanagerpb.SecretVersion

	// addErr injects a version-write failure; nil allows addResponse.
	addErr error

	// deleteRequest retains the native resource name selected for deletion.
	deleteRequest *secretmanagerpb.DeleteSecretRequest

	// deleteErr is returned by deletion without mutating external state.
	deleteErr error
}

// AccessSecretVersion records and returns the configured version response.
func (client *secretManagerClient) AccessSecretVersion(_ context.Context, request *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	client.accessRequest = request

	return client.accessResponse, client.accessErr
}

// CreateSecret records and returns the configured creation response.
func (client *secretManagerClient) CreateSecret(_ context.Context, request *secretmanagerpb.CreateSecretRequest, _ ...gax.CallOption) (*secretmanagerpb.Secret, error) {
	client.createRequest = request

	return client.createResponse, client.createErr
}

// AddSecretVersion records and returns the configured version response.
func (client *secretManagerClient) AddSecretVersion(_ context.Context, request *secretmanagerpb.AddSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.SecretVersion, error) {
	client.addRequest = request

	return client.addResponse, client.addErr
}

// DeleteSecret records and returns the configured deletion response.
func (client *secretManagerClient) DeleteSecret(_ context.Context, request *secretmanagerpb.DeleteSecretRequest, _ ...gax.CallOption) error {
	client.deleteRequest = request

	return client.deleteErr
}

// requireAccessRequest verifies that request targets the expected secret version.
func requireAccessRequest(
	t *testing.T,
	request *secretmanagerpb.AccessSecretVersionRequest,
	wantName string,
) {
	t.Helper()

	if request == nil {
		t.Fatalf("access request = %#v", request)
	}

	if request.Name != wantName {
		t.Fatalf("access request = %#v", request)
	}
}

// requireCreateRequest verifies that request creates the expected secret.
func requireCreateRequest(
	t *testing.T,
	request *secretmanagerpb.CreateSecretRequest,
	wantParent string,
	wantID string,
) {
	t.Helper()

	if request == nil {
		t.Fatalf("create request = %#v", request)
	}

	if request.Parent != wantParent {
		t.Fatalf("create request = %#v", request)
	}

	if request.SecretId != wantID {
		t.Fatalf("create request = %#v", request)
	}
}

// requireAutomaticReplication verifies that request creates an automatically replicated secret.
func requireAutomaticReplication(t *testing.T, request *secretmanagerpb.CreateSecretRequest) {
	t.Helper()

	if request.Secret.GetReplication().GetAutomatic() == nil {
		t.Fatalf("create replication = %#v", request.Secret.GetReplication())
	}
}

// requireAddRequest verifies that request adds the expected secret data.
func requireAddRequest(
	t *testing.T,
	request *secretmanagerpb.AddSecretVersionRequest,
	wantParent string,
	wantData string,
) {
	t.Helper()

	if request == nil {
		t.Fatalf("add request = %#v", request)
	}

	if request.Parent != wantParent {
		t.Fatalf("add request = %#v", request)
	}

	if string(request.Payload.Data) != wantData {
		t.Fatalf("add request = %#v", request)
	}
}

// requireSecretValue verifies the returned secret data and version.
func requireSecretValue(t *testing.T, value secret.Value, wantData string, wantVersion string) {
	t.Helper()

	if string(value.Data) != wantData {
		t.Fatalf("value = %#v", value)
	}

	if value.Version != wantVersion {
		t.Fatalf("value = %#v", value)
	}
}

// requireSetRequests verifies that Put issued both required secret requests.
func requireSetRequests(t *testing.T, client *secretManagerClient) {
	t.Helper()

	if client.createRequest == nil || client.addRequest == nil {
		t.Fatalf("create request = %#v, add request = %#v", client.createRequest, client.addRequest)
	}
}

// TestClientGetRequestsVersion verifies that Get requests the supplied version.
func TestClientGetRequestsVersion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	const name = "projects/example/secrets/database-dsn"
	const version = "3"
	client := &secretManagerClient{
		accessResponse: &secretmanagerpb.AccessSecretVersionResponse{
			Name: name + "/versions/" + version,
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte("secret"),
			},
		},
	}
	driver := NewFrom(client)

	value, err := driver.Get(ctx, secret.Reference{
		Name:    name,
		Version: version,
	})

	if err != nil {
		t.Fatal(err)
	}

	requireAccessRequest(t, client.accessRequest, name+"/versions/"+version)
	requireSecretValue(t, value, "secret", version)
}

// TestClientGetMapsNotFoundResponse verifies that Get maps a missing GCP secret.
func TestClientGetMapsNotFoundResponse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := NewFrom(&secretManagerClient{
		accessErr: status.Error(codes.NotFound, "missing"),
	})

	_, err := driver.Get(ctx, secret.Reference{
		Name: "projects/example/secrets/missing",
	})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientSetCreatesSecretAndAddsVersion verifies that Put creates then versions a secret.
func TestClientSetCreatesSecretAndAddsVersion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	const name = "projects/example/secrets/database-dsn"
	client := &secretManagerClient{
		addResponse: &secretmanagerpb.SecretVersion{
			Name: name + "/versions/1",
		},
	}
	driver := NewFrom(client, func(secrets *Builder) {
		secrets.Project("unused")
	})

	value, err := driver.Put(ctx, name, []byte("postgres://database"))

	if err != nil {
		t.Fatal(err)
	}

	requireCreateRequest(t, client.createRequest, "projects/example", "database-dsn")
	requireAutomaticReplication(t, client.createRequest)
	requireAddRequest(t, client.addRequest, name, "postgres://database")
	requireSecretValue(t, value, "postgres://database", "1")
}

// TestClientSetAddsVersionWhenSecretExists verifies that Put appends to an existing secret.
func TestClientSetAddsVersionWhenSecretExists(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	const name = "projects/example/secrets/database-dsn"
	client := &secretManagerClient{
		createErr: status.Error(codes.AlreadyExists, "already exists"),
		addResponse: &secretmanagerpb.SecretVersion{
			Name: name + "/versions/2",
		},
	}
	driver := NewFrom(client)

	value, err := driver.Put(ctx, name, []byte("postgres://database-v2"))

	if err != nil {
		t.Fatal(err)
	}

	requireSetRequests(t, client)
	requireSecretValue(t, value, "postgres://database-v2", "2")
}

// TestClientUpdateAddsVersion verifies that Update appends a secret version.
func TestClientUpdateAddsVersion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	const name = "projects/example/secrets/database-dsn"
	client := &secretManagerClient{
		addResponse: &secretmanagerpb.SecretVersion{
			Name: name + "/versions/3",
		},
	}
	driver := NewFrom(client)

	value, err := driver.Update(ctx, name, []byte("postgres://database-v3"))

	if err != nil {
		t.Fatal(err)
	}

	if client.createRequest != nil {
		t.Fatalf("create request = %#v", client.createRequest)
	}

	requireAddRequest(t, client.addRequest, name, "postgres://database-v3")
	requireSecretValue(t, value, "postgres://database-v3", "3")
}

// TestClientUpdateMapsNotFoundResponse verifies that Update maps a missing GCP secret.
func TestClientUpdateMapsNotFoundResponse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := NewFrom(&secretManagerClient{
		addErr: status.Error(codes.NotFound, "missing"),
	})

	_, err := driver.Update(ctx, "projects/example/secrets/missing", []byte("value"))

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientDeleteDeletesSecret verifies that Delete removes a GCP secret.
func TestClientDeleteDeletesSecret(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	const name = "projects/example/secrets/database-dsn"
	client := &secretManagerClient{}
	driver := NewFrom(client)

	if err := driver.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}

	if client.deleteRequest == nil || client.deleteRequest.Name != name {
		t.Fatalf("delete request = %#v", client.deleteRequest)
	}
}

// TestClientDeleteMapsNotFoundResponse verifies that Delete maps a missing GCP secret.
func TestClientDeleteMapsNotFoundResponse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := NewFrom(&secretManagerClient{
		deleteErr: status.Error(codes.NotFound, "missing"),
	})

	err := driver.Delete(ctx, "projects/example/secrets/missing")

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestQualifiedNameUsesConfiguredProject checks expansion of short names and
// preservation of fully qualified names that select a different project.
func TestQualifiedNameUsesConfiguredProject(t *testing.T) {
	t.Parallel()

	driver := NewFrom(nil, func(secrets *Builder) {
		secrets.Project("demo")
	})

	if got := driver.qualified("api-key"); got != "projects/demo/secrets/api-key" {
		t.Fatalf("qualified = %q", got)
	}

	if got := driver.qualified("projects/other/secrets/x"); got != "projects/other/secrets/x" {
		t.Fatalf("qualified = %q", got)
	}
}
