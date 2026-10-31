package awssecret

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"arkoren.dev/core/service/secret"
)

// Config is a redacted reporting record for AWS Secrets Manager settings. It is
// not a constructor input: configure clients through Builder callbacks instead.
// Credentials remain shared native providers; formatting never reveals them.
//
// Example:
//
//	record := awssecret.Config{
//		Region: "eu-west-1",
//		Endpoint: "https://example.test",
//	}
//	slog.Info("secret settings", "config", record)
type Config struct {
	// Region selects the AWS region for secret operations.
	// Empty follows the default AWS chain when configuring a new client.
	// Example: record := awssecret.Config{Region: "eu-west-1"}
	Region string

	// Endpoint overrides the AWS service endpoint when non-empty.
	// This diagnostic string is not redacted; keep credentials out of URLs.
	// Example: record.Endpoint = "http://localhost:4566"
	Endpoint string

	// Name is caller-authored diagnostic text only. Constructors and operations
	// do not use it to select a secret. Avoid putting secret values in this field.
	// Example: record.Name = "application"
	Name string

	// Credentials overrides the AWS default credential chain when non-nil.
	// Formatting records only its type; the provider itself remains shared.
	// Example: record.Credentials = provider
	Credentials aws.CredentialsProvider
}

// client defines the AWS Secrets Manager operations used by [Driver].
type client interface {
	// GetSecretValue retrieves a versioned AWS secret.
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)

	// CreateSecret creates an AWS secret.
	CreateSecret(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)

	// PutSecretValue appends an AWS secret version.
	PutSecretValue(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)

	// DeleteSecret deletes an AWS secret.
	DeleteSecret(context.Context, *secretsmanager.DeleteSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
}

// Driver manages versioned binary or text secrets in AWS Secrets Manager.
// Share a constructed driver; native client calls bypass the facade's telemetry.
//
// Example:
//
//	driver := awssecret.NewFrom(secretsmanager.NewFromConfig(cfg))
//	store := secret.New(driver)
//	_ = store
type Driver struct {
	// client performs requests against AWS Secrets Manager.
	client client
}

// New creates an AWS Secrets Manager client using configured or default credentials.
// Callbacks run directly once, synchronously in order on one fresh draft before
// loading AWS configuration. Nil entries panic; nil/empty slice expansion omits
// edits. Completed settings are copied; credential providers remain shared.
//
// Example:
//
//	driver, err := awssecret.New(ctx, func(secrets *awssecret.Builder) {
//		secrets.Region("eu-west-1")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(ctx context.Context, configure ...BuilderFunc) (*Driver, error) {
	config := configured(configure...)

	awsConfig, err := loadConfig(ctx, config)

	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	return NewFrom(secretsmanager.NewFromConfig(awsConfig)), nil
}

// loadConfig applies driver configuration to the AWS default credential chain.
func loadConfig(ctx context.Context, config Config) (aws.Config, error) {
	options := make([]func(*awsconfig.LoadOptions) error, 0, 3)

	if config.Region != "" {
		options = append(options, awsconfig.WithRegion(config.Region))
	}

	if config.Endpoint != "" {
		options = append(options, awsconfig.WithBaseEndpoint(config.Endpoint))
	}

	if config.Credentials != nil {
		options = append(options, awsconfig.WithCredentialsProvider(config.Credentials))
	}

	return awsconfig.LoadDefaultConfig(ctx, options...)
}

// NewFrom wraps an existing AWS Secrets Manager client without copying or
// closing it. It has no optional settings or callbacks: native configuration
// belongs to the caller, and operations retain their explicit secret names.
//
// Example:
//
//	driver := awssecret.NewFrom(secretsmanager.NewFromConfig(cfg))
//	_ = driver
func NewFrom(client client) *Driver {
	return &Driver{
		client: client,
	}
}

// Get retrieves reference, using its immutable version when supplied. Missing
// secrets and pending-deletion state wrap secret.ErrNotFound. Data is owned by
// the caller; handle errors without recording provider diagnostics or payloads.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "application/api-token"})
//	if err != nil {
//		return err
//	}
//	_ = value.Version
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	output, err := driver.getSecret(ctx, reference)

	if err := mapGetError(reference.Name, err); err != nil {
		return secret.Value{}, err
	}

	value := secret.Value{
		Version: aws.ToString(output.VersionId),
		Data:    output.SecretBinary,
	}

	if output.SecretString != nil {
		value.Data = []byte(*output.SecretString)
	}

	return value, nil
}

// getSecret retrieves the AWS secret version selected by reference.
func (driver *Driver) getSecret(ctx context.Context, reference secret.Reference) (*secretsmanager.GetSecretValueOutput, error) {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(reference.Name),
	}

	if reference.Version != "" {
		input.VersionId = aws.String(reference.Version)
	}

	return driver.client.GetSecretValue(ctx, input)
}

// mapGetError maps AWS GetSecretValue errors to the driver contract.
func mapGetError(name string, err error) error {
	if notFound(err) {
		return fmt.Errorf("%w: %s", secret.ErrNotFound, name)
	}

	if err != nil {
		return fmt.Errorf("get secret %q: %w", name, err)
	}

	return nil
}

// notFound reports whether err means the secret does not exist: AWS's
// not-found response, or its pending-deletion response, which every operation
// on a deleted secret returns until the recovery window ends.
func notFound(err error) bool {
	if _, ok := errors.AsType[*types.ResourceNotFoundException](err); ok {
		return true
	}

	invalidRequest, ok := errors.AsType[*types.InvalidRequestException](err)

	return ok && (strings.Contains(invalidRequest.Error(), "marked for deletion") ||
		strings.Contains(invalidRequest.Error(), "scheduled for deletion"))
}

// Put creates name or writes a new current version when it already exists.
// It returns detached bytes and the newly assigned version, not a rollout signal.
//
// Example:
//
//	written, err := driver.Put(ctx, "application/api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Put(ctx context.Context, name string, value []byte) (secret.Value, error) {
	output, err := driver.client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretBinary: value,
	})

	if err == nil {
		return secret.Value{
			Data:    append([]byte(nil), value...),
			Version: aws.ToString(output.VersionId),
		}, nil
	}

	if _, ok := errors.AsType[*types.ResourceExistsException](err); !ok {
		return secret.Value{}, fmt.Errorf("set secret %q: %w", name, err)
	}

	return driver.put(ctx, name, value)
}

// Update writes a new current version for an existing secret.
// Missing or pending-deletion names wrap secret.ErrNotFound without creation.
//
// Example:
//
//	written, err := driver.Update(ctx, "application/api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	_ = written.Version
func (driver *Driver) Update(ctx context.Context, name string, value []byte) (secret.Value, error) {
	return driver.put(ctx, name, value)
}

// put appends a current AWS secret version and maps a missing secret.
func (driver *Driver) put(ctx context.Context, name string, value []byte) (secret.Value, error) {
	output, err := driver.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretBinary: value,
	})

	if notFound(err) {
		return secret.Value{}, fmt.Errorf("%w: %s", secret.ErrNotFound, name)
	}

	if err != nil {
		return secret.Value{}, fmt.Errorf("write secret %q: %w", name, err)
	}

	return secret.Value{
		Data:    append([]byte(nil), value...),
		Version: aws.ToString(output.VersionId),
	}, nil
}

// Delete schedules removal of name and all of its versions.
// AWS retention can prevent immediate reuse; this does not purge backups.
//
// Example:
//
//	if err := driver.Delete(ctx, "application/retired-token"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, name string) error {
	_, err := driver.client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId: aws.String(name),
	})

	if notFound(err) {
		return fmt.Errorf("%w: %s", secret.ErrNotFound, name)
	}

	if err != nil {
		return fmt.Errorf("delete secret %q: %w", name, err)
	}

	return nil
}

// Client returns the underlying Secrets Manager client, or nil when the driver was
// built from a test double. Native calls bypass contract error mapping and facade
// observations. Credential providers and transport ownership remain unchanged.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		_ = native.Options().Region
//	}
func (driver *Driver) Client() *secretsmanager.Client {
	client, _ := driver.client.(*secretsmanager.Client)

	return client
}

var _ secret.Driver = (*Driver)(nil)
