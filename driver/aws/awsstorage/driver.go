package awsstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"arkoren.dev/core/service/storage"
)

// Config is a redacted reporting record for optional S3 settings. It is not a
// constructor input: use Builder callbacks and pass the required bucket directly
// to New or NewFrom. Formatting redacts credentials, but not endpoint or prefix;
// do not store secrets in those strings.
//
// Example:
//
//	record := awsstorage.Config{
//		Region: "eu-west-1",
//		Prefix: "uploads",
//	}
//	slog.Info("storage settings", "config", record)
type Config struct {
	// Region selects the AWS region for requests. The default is the region
	// resolved by the AWS default configuration chain.
	// Example: record := awsstorage.Config{Region: "eu-west-1"}
	Region string

	// Endpoint overrides the AWS service endpoint and enables path-style
	// addressing. The default is the regular AWS endpoint for Region.
	// Example: record.Endpoint = "http://localhost:9000"
	Endpoint string

	// Prefix scopes logical object keys within the bucket. The default is
	// empty, which stores keys at the bucket root.
	// Example: record.Prefix = "tenant-a"
	Prefix string

	// Credentials provides request credentials when the default chain is
	// unsuitable. The default is nil, which uses the AWS default credential
	// chain.
	// Example: record.Credentials = provider // Shared, not recursively copied.
	Credentials aws.CredentialsProvider
}

// client defines the S3 operations used by Driver.
type client interface {
	// GetObject opens an object's content and returns its metadata.
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	// PutObject writes an object's content and metadata.
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	// HeadObject reads an object's metadata without its content.
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	// DeleteObject removes an object from S3.
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	// ListObjectsV2 lists one page of objects matching the request filters.
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// maxSignTTL is the longest validity SigV4 presigned URLs allow.
const maxSignTTL = 7 * 24 * time.Hour

// presigner defines the S3 presigning operations used by Driver. It is
// satisfied by *s3.PresignClient.
type presigner interface {
	// PresignGetObject signs a request that reads an object.
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	// PresignPutObject signs a request that writes an object.
	PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// Driver stores portable objects in one S3 bucket and optional key prefix.
// It borrows native dependencies; callers own opened bodies and native transport
// cleanup. Use the storage facade for typed helpers and operation observations.
//
// Example:
//
//	driver, err := awsstorage.NewFrom(client, "uploads")
//	if err != nil {
//		return err
//	}
//	files := storage.New(driver)
//	_ = files
type Driver struct {
	// client performs S3 operations.
	client client

	// presigner signs object URLs. It is nil when the injected client cannot
	// sign, in which case [Driver.SignURL] returns [storage.ErrUnsupported].
	presigner presigner

	// bucket identifies the S3 bucket that stores objects.
	bucket string

	// prefix scopes logical object keys within bucket.
	prefix string
}

// New creates a [Driver] for bucket using configured credentials or the AWS
// default credential chain.
//
// bucket is required and has no default; an empty bucket returns an error
// naming the S3 bucket. configure adjusts the optional settings; an
// invalid [Config.Prefix] also returns an error. No request is sent until the
// first operation. Callbacks run directly once, synchronously in order on a
// fresh draft before validation. Nil entries panic; expanded nil/empty slices
// omit edits. Completed settings are copied, not native credential providers.
//
// Example:
//
//	store, err := awsstorage.New(ctx, "uploads", func(store *awsstorage.Builder) {
//		store.Prefix("tenant-a")
//	})
//	if err != nil {
//		return err
//	}
//	_ = store
func New(ctx context.Context, bucket string, configure ...BuilderFunc) (*Driver, error) {
	config := configured(configure...)

	if err := validateConfig(bucket, config); err != nil {
		return nil, err
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsLoadOptions(config)...)

	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	return newDriver(s3.NewFromConfig(awsConfig, s3ClientOptions(config)...), bucket, config), nil
}

// awsLoadOptions returns AWS configuration options for config.
func awsLoadOptions(config Config) []func(*awsconfig.LoadOptions) error {
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

	return options
}

// s3ClientOptions returns S3 client options for config.
func s3ClientOptions(config Config) []func(*s3.Options) {
	options := make([]func(*s3.Options), 0, 1)

	if config.Endpoint != "" {
		options = append(options, func(options *s3.Options) {
			options.UsePathStyle = true
		})
	}

	return options
}

// NewFrom creates a [Driver] for bucket from a caller-owned S3 client.
//
// The client is not copied or closed by the driver. bucket is validated like
// in [New]. Only [Builder.Prefix] applies from configure; Region, Endpoint, and
// Credentials are already carried by the client and are ignored. A nil client
// returns an error. Callbacks follow New's once-only, ordered draft rules;
// supplied nil entries panic before client validation.
//
// URL signing uses the client's own presigner: an *s3.Client is wrapped with
// s3.NewPresignClient, and any other client that also implements
// PresignGetObject and PresignPutObject is used directly. For a client with
// neither, [Driver.SignURL] returns [storage.ErrUnsupported].
//
// Example:
//
//	store, err := awsstorage.NewFrom(s3.NewFromConfig(cfg), "uploads")
//	if err != nil {
//		return err
//	}
//	_ = store
func NewFrom(injectedClient client, bucket string, configure ...BuilderFunc) (*Driver, error) {
	config := configured(configure...)

	if injectedClient == nil {
		return nil, errors.New("S3 client is nil")
	}

	if err := validateConfig(bucket, config); err != nil {
		return nil, err
	}

	return newDriver(injectedClient, bucket, config), nil
}

// newDriver publishes already validated completed settings without replaying
// construction policies or validating them twice. It borrows the native client
// and detects its existing signing capabilities.
func newDriver(injectedClient client, bucket string, config Config) *Driver {
	prefix := config.Prefix

	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/") + "/"
	}

	driver := &Driver{
		client: injectedClient,
		bucket: bucket,
		prefix: prefix,
	}

	switch injected := injectedClient.(type) {
	case presigner:
		driver.presigner = injected
	case *s3.Client:
		driver.presigner = s3.NewPresignClient(injected)
	}

	return driver
}

// Open returns an S3 object body that the caller must close. Missing objects return an error
// wrapping [storage.ErrNotFound].
// An already canceled context takes precedence over key validation.
//
// Example:
//
//	object, err := driver.Open(ctx, "reports/a.pdf")
//	if err != nil {
//		return err
//	}
//	data, err := io.ReadAll(object.Contents)
//	closeErr := object.Contents.Close()
//	if err != nil {
//		return err
//	}
//	if closeErr != nil {
//		return closeErr
//	}
//	_ = data
func (driver *Driver) Open(ctx context.Context, key string) (storage.Object, error) {
	if err := ctx.Err(); err != nil {
		return storage.Object{}, err
	}

	physicalKey, err := driver.physicalKey(key)

	if err != nil {
		return storage.Object{}, err
	}

	output, err := driver.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(driver.bucket),
		Key:    aws.String(physicalKey),
	})

	if err != nil {
		return storage.Object{}, mapNotFound("open", key, err)
	}

	return storage.Object{
		Key:        key,
		Size:       aws.ToInt64(output.ContentLength),
		ModifiedAt: aws.ToTime(output.LastModified),
		Contents:   output.Body,
	}, nil
}

// Put replaces key then reads canonical metadata from S3.
//
// PutObject needs the content length up front. A contents reader that
// cannot seek, such as an HTTP request body, is buffered in memory first;
// use the S3 upload manager through [Driver.Client] for very large streams.
// An already canceled context consumes nothing. Cancellation during a read
// still requires the caller's reader to cooperate; arbitrary Read cannot be forced.
//
// Example:
//
//	info, err := driver.Put(ctx, "reports/a.json", strings.NewReader(`{"ready":true}`))
//	if err != nil {
//		return err
//	}
//	_ = info.Size
func (driver *Driver) Put(ctx context.Context, key string, contents io.Reader) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}

	physicalKey, err := driver.physicalKey(key)

	if err != nil {
		return storage.ObjectInfo{}, err
	}

	if _, ok := contents.(io.ReadSeeker); !ok {
		data, err := io.ReadAll(contents)

		if err != nil {
			return storage.ObjectInfo{}, fmt.Errorf("read object %q: %w", key, err)
		}

		contents = bytes.NewReader(data)
	}

	if _, err := driver.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(driver.bucket),
		Key:    aws.String(physicalKey),
		Body:   contents,
	}); err != nil {
		return storage.ObjectInfo{}, err
	}

	return driver.statPhysical(ctx, key, physicalKey)
}

// Stat returns canonical S3 metadata for key. Missing objects return an error
// wrapping [storage.ErrNotFound].
// It applies the same logical key prefix as Open and Put without opening a body.
//
// Example:
//
//	info, err := driver.Stat(ctx, "reports/a.pdf")
//	if err != nil {
//		return err
//	}
//	_ = info.ModifiedAt
func (driver *Driver) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}

	physicalKey, err := driver.physicalKey(key)

	if err != nil {
		return storage.ObjectInfo{}, err
	}

	return driver.statPhysical(ctx, key, physicalKey)
}

// Delete removes key. S3 successful deletion is idempotent.
// Only the one prefixed key is removed, not objects below a similar path.
//
// Example:
//
//	if err := driver.Delete(ctx, "reports/retired.pdf"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	physicalKey, err := driver.physicalKey(key)

	if err != nil {
		return err
	}

	_, err = driver.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(driver.bucket),
		Key:    aws.String(physicalKey),
	})

	return err
}

// List returns objects whose portable logical keys match options.Prefix. Native
// objects outside that syntax are omitted. Continuation checks can read ahead
// through physical pages containing only omitted objects; cursors retain their
// native S3 representation and are empty when no further logical object exists.
// Concurrent native bucket changes can still affect subsequent requests.
//
// Example:
//
//	page, err := driver.List(ctx, storage.ListOptions{Prefix: "reports/", Limit: 20})
//	if err != nil {
//		return err
//	}
//	_ = page.Objects
//	_ = page.NextCursor // Continue with the same driver and logical prefix.
func (driver *Driver) List(ctx context.Context, options storage.ListOptions) (storage.Page, error) {
	if err := ctx.Err(); err != nil {
		return storage.Page{}, err
	}

	if err := storage.ValidatePrefix(options.Prefix); err != nil {
		return storage.Page{}, err
	}

	page, err := driver.listPage(ctx, options)

	if err != nil {
		return storage.Page{}, err
	}

	page.NextCursor, err = driver.logicalContinuation(ctx, options, page.NextCursor)

	if err != nil {
		return storage.Page{}, err
	}

	return page, nil
}

// listPage reads one physical S3 page and filters its keys into logical objects.
// Its cursor is still physical; List validates logical continuation separately.
func (driver *Driver) listPage(ctx context.Context, options storage.ListOptions) (storage.Page, error) {
	output, err := driver.client.ListObjectsV2(ctx, driver.listObjectsInput(options))

	if err != nil {
		return storage.Page{}, err
	}

	return storage.Page{
		Objects:    driver.storageObjects(options.Prefix, output.Contents),
		NextCursor: aws.ToString(output.NextContinuationToken),
	}, nil
}

// logicalContinuation skips physical pages without portable matching objects.
// It returns the input token of the next usable page, not the token after that
// page, so read-ahead never skips an object or changes deployed cursor encoding.
func (driver *Driver) logicalContinuation(ctx context.Context, options storage.ListOptions, cursor string) (string, error) {
	for cursor != "" {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		options.Cursor = cursor
		page, err := driver.listPage(ctx, options)

		if err != nil {
			return "", err
		}

		if len(page.Objects) != 0 {
			return cursor, nil
		}

		cursor = page.NextCursor
	}

	return "", nil
}

// listObjectsInput creates the S3 request for options.
func (driver *Driver) listObjectsInput(options storage.ListOptions) *s3.ListObjectsV2Input {
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(driver.bucket),
		Prefix: aws.String(driver.prefix + options.Prefix),
	}

	if options.Cursor != "" {
		input.ContinuationToken = aws.String(options.Cursor)
	}

	if options.Limit > 0 {
		input.MaxKeys = aws.Int32(int32(options.Limit))
	}

	return input
}

// storageObjects converts matching S3 objects to portable metadata.
func (driver *Driver) storageObjects(prefix string, contents []types.Object) []storage.ObjectInfo {
	objects := make([]storage.ObjectInfo, 0, len(contents))

	for _, item := range contents {
		key, valid := driver.logicalKey(aws.ToString(item.Key))

		if !valid {
			continue
		}

		if !strings.HasPrefix(key, prefix) {
			continue
		}

		objects = append(objects, storage.ObjectInfo{
			Key:        key,
			Size:       aws.ToInt64(item.Size),
			ModifiedAt: aws.ToTime(item.LastModified),
		})
	}

	return objects
}

// statPhysical returns metadata for a previously resolved physical key.
func (driver *Driver) statPhysical(ctx context.Context, key string, physicalKey string) (storage.ObjectInfo, error) {
	output, err := driver.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(driver.bucket),
		Key:    aws.String(physicalKey),
	})

	if err != nil {
		return storage.ObjectInfo{}, mapNotFound("stat", key, err)
	}

	return storage.ObjectInfo{
		Key:        key,
		Size:       aws.ToInt64(output.ContentLength),
		ModifiedAt: aws.ToTime(output.LastModified),
	}, nil
}

// physicalKey validates a logical key and adds the configured prefix.
func (driver *Driver) physicalKey(key string) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}

	return driver.prefix + key, nil
}

// logicalKey removes the configured prefix from a valid physical key.
func (driver *Driver) logicalKey(physicalKey string) (string, bool) {
	if !strings.HasPrefix(physicalKey, driver.prefix) {
		return "", false
	}

	key := strings.TrimPrefix(physicalKey, driver.prefix)

	return key, storage.ValidateKey(key) == nil
}

// validateConfig reports an error when bucket is empty or config.Prefix is not
// a valid storage prefix.
func validateConfig(bucket string, config Config) error {
	if bucket == "" {
		return errors.New("S3 bucket is empty")
	}

	return storage.ValidatePrefix(config.Prefix)
}

// mapNotFound maps S3 missing-object errors to the storage sentinel.
func mapNotFound(operation string, key string, err error) error {
	if s3NotFound(err) {
		return fmt.Errorf("%s %q: %w", operation, key, storage.ErrNotFound)
	}

	return err
}

// s3NotFound reports whether err represents an S3 missing-object response.
func s3NotFound(err error) bool {
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}

	if _, ok := errors.AsType[*types.NotFound](err); ok {
		return true
	}

	return s3ResponseNotFound(err)
}

// s3ResponseNotFound reports whether an AWS response has the not-found status.
func s3ResponseNotFound(err error) bool {
	responseError, ok := errors.AsType[*awshttp.ResponseError](err)

	if !ok {
		return false
	}

	if responseError.Response == nil {
		return false
	}

	return responseError.Response.StatusCode == http.StatusNotFound
}

// Client returns the underlying S3 client, or nil when the driver was
// built from a test double. Native calls bypass the logical Prefix, portable key
// validation, and facade observations; the client remains shared and borrowed.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		_ = native.Options().Region
//	}
func (driver *Driver) Client() *s3.Client {
	client, _ := driver.client.(*s3.Client)

	return client
}

var _ storage.Driver = (*Driver)(nil)
