package gcpsecret

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"arkoren.dev/core/service/secret"
)

// settings holds optional naming policy until publication. It is copied from
// the construction draft without retaining the draft or cloning native clients.
type settings struct {
	// Project identifies the default project for unqualified secret names.
	Project string
}

// client defines the Google Cloud Secret Manager operations used by [Driver].
type client interface {
	// AccessSecretVersion retrieves a Google Cloud secret version.
	AccessSecretVersion(context.Context, *secretmanagerpb.AccessSecretVersionRequest, ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)

	// CreateSecret creates a Google Cloud secret.
	CreateSecret(context.Context, *secretmanagerpb.CreateSecretRequest, ...gax.CallOption) (*secretmanagerpb.Secret, error)

	// AddSecretVersion appends a Google Cloud secret version.
	AddSecretVersion(context.Context, *secretmanagerpb.AddSecretVersionRequest, ...gax.CallOption) (*secretmanagerpb.SecretVersion, error)

	// DeleteSecret deletes a Google Cloud secret.
	DeleteSecret(context.Context, *secretmanagerpb.DeleteSecretRequest, ...gax.CallOption) error
}

// Driver manages versioned secrets in Google Cloud Secret Manager.
// Construct it once and register Stop at its acquiring owner, including NewFrom
// clients; native calls bypass the facade's observations and error mapping.
//
// Example:
//
//	driver := gcpsecret.NewFrom(client)
//	store := secret.New(driver)
//	_ = store
type Driver struct {
	// client performs requests against Google Cloud Secret Manager.
	client client

	// project is the fallback project for unqualified secret names.
	project string
}

// New creates a Google Cloud Secret Manager client using application default credentials.
// Callbacks run directly once, synchronously in order on one fresh draft before
// native client creation. Nil entries panic; nil/empty slice expansion omits
// edits. Stop releases native clients, including those supplied to NewFrom.
// Completed project settings are copied, never replayed as callbacks.
// New installs a discard SDK logger because debug request/response and credential
// logs can contain secrets. GOOGLE_SDK_GO_LOGGING_LEVEL does not enable those
// logs for this client. ADC discovery and its construction context are unchanged.
//
// Example:
//
//	driver, err := gcpsecret.New(ctx, func(secrets *gcpsecret.Builder) {
//		secrets.Project("example-project")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(ctx context.Context, configure ...BuilderFunc) (*Driver, error) {
	config := configured(configure...)

	client, err := secretmanager.NewClient(ctx, option.WithLogger(slog.New(slog.DiscardHandler)))

	if err != nil {
		return nil, fmt.Errorf("create Google Cloud Secret Manager client: %w", err)
	}

	return &Driver{
		client:  client,
		project: config.Project,
	}, nil
}

// NewFrom creates a client from an existing Google Cloud Secret Manager client.
// It retains client without copying it and applies Project to naming policy
// only. Stop closes a native Secret Manager client, including one supplied here.
// Callbacks follow New's ordered once-only rules; retained drafts cannot change
// the driver.
// NewFrom does not change the supplied client's logging policy. Configure a
// discard SDK logger before passing a native client: SDK debug logs can include
// secret payloads, including when enabled by GOOGLE_SDK_GO_LOGGING_LEVEL.
//
// Example:
//
//	client, err := secretmanager.NewClient(ctx,
//		option.WithLogger(slog.New(slog.DiscardHandler)))
//	if err != nil {
//		return err
//	}
//	driver := gcpsecret.NewFrom(client, func(secrets *gcpsecret.Builder) {
//		secrets.Project("example-project")
//	})
//	_ = driver
func NewFrom(client client, configure ...BuilderFunc) *Driver {
	config := configured(configure...)

	return &Driver{
		client:  client,
		project: config.Project,
	}
}

// Get retrieves reference. An empty version selects latest.
// Returned bytes are detached. Fully qualified names preserve their project;
// unqualified names require Builder.Project. Missing versions wrap ErrNotFound.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "projects/example/secrets/api-token"})
//	if err != nil {
//		return err
//	}
//	_ = value.Version
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	version := requestedVersion(reference.Version)
	name := driver.qualified(reference.Name) + "/versions/" + version

	response, err := driver.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	})

	if err != nil {
		return secret.Value{}, driver.mapError("get", reference.Name, err)
	}

	if response.Payload == nil {
		return secret.Value{}, errors.New("secret response has no payload")
	}

	return secret.Value{
		Data:    append([]byte(nil), response.Payload.Data...),
		Version: versionID(responseVersion(response.Name, version)),
	}, nil
}

// requestedVersion returns the requested secret version or the current version.
func requestedVersion(version string) string {
	if version == "" {
		return "latest"
	}

	return version
}

// responseVersion selects the provider version or the version requested by the caller.
func responseVersion(actual, requested string) string {
	if actual == "" {
		return requested
	}

	return actual
}

// Put creates name when absent or appends a current version when it exists.
// Creation uses automatic replication; returned bytes are detached from input.
//
// Example:
//
//	written, err := driver.Put(ctx, "projects/example/secrets/api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Put(ctx context.Context, name string, value []byte) (secret.Value, error) {
	_, err := driver.client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   driver.parent(name),
		SecretId: driver.secretID(name),
		Secret: &secretmanagerpb.Secret{
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_Automatic_{
					Automatic: &secretmanagerpb.Replication_Automatic{},
				},
			},
		},
	})

	if err != nil && status.Code(err) != codes.AlreadyExists {
		return secret.Value{}, driver.mapError("create", name, err)
	}

	return driver.write(ctx, name, value)
}

// Update appends a new current version to an existing secret.
// It does not create a secret resource; missing names wrap secret.ErrNotFound.
//
// Example:
//
//	written, err := driver.Update(ctx, "projects/example/secrets/api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Update(ctx context.Context, name string, value []byte) (secret.Value, error) {
	return driver.write(ctx, name, value)
}

// write appends an immutable Google Cloud secret version.
func (driver *Driver) write(ctx context.Context, name string, value []byte) (secret.Value, error) {
	output, err := driver.client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent: driver.qualified(name),
		Payload: &secretmanagerpb.SecretPayload{
			Data: value,
		},
	})

	if err != nil {
		return secret.Value{}, driver.mapError("write", name, err)
	}

	return secret.Value{
		Data:    append([]byte(nil), value...),
		Version: versionID(output.Name),
	}, nil
}

// Delete removes name and all of its versions.
// Missing resources wrap secret.ErrNotFound; native errors retain their cause.
//
// Example:
//
//	if err := driver.Delete(ctx, "projects/example/secrets/retired-token"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, name string) error {
	if err := driver.client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{
		Name: driver.qualified(name),
	}); err != nil {
		return driver.mapError("delete", name, err)
	}

	return nil
}

// qualified returns name as a full secret resource name. Unqualified names
// are placed in the configured project.
func (driver *Driver) qualified(name string) string {
	if strings.HasPrefix(name, "projects/") {
		return name
	}

	return "projects/" + driver.project + "/secrets/" + name
}

// versionID returns the last segment of a version resource name, such as
// "3" for "projects/p/secrets/s/versions/3", so it can be passed back as
// [secret.Reference.Version].
func versionID(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}

// parent derives the Google Cloud project resource parent for name.
func (driver *Driver) parent(name string) string {
	if index := strings.LastIndex(name, "/secrets/"); index >= 0 {
		return name[:index]
	}

	return "projects/" + driver.project
}

// secretID derives the unqualified Google Cloud secret identifier from name.
func (driver *Driver) secretID(name string) string {
	if index := strings.LastIndex(name, "/"); index >= 0 {
		return name[index+1:]
	}

	return name
}

// mapError translates Google Cloud not-found responses and gRPC cancellation
// codes, so callers can match them with [context.Canceled] and
// [context.DeadlineExceeded].
func (driver *Driver) mapError(operation, name string, err error) error {
	switch status.Code(err) {
	case codes.NotFound:
		return fmt.Errorf("%w: %s", secret.ErrNotFound, name)
	case codes.Canceled:
		return fmt.Errorf("%s secret %q: %w: %w", operation, name, context.Canceled, err)
	case codes.DeadlineExceeded:
		return fmt.Errorf("%s secret %q: %w: %w", operation, name, context.DeadlineExceeded, err)
	}

	return fmt.Errorf("%s secret %q: %w", operation, name, err)
}

var _ secret.Driver = (*Driver)(nil)
