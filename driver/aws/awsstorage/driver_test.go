package awsstorage_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/storagetest"
	awsstorage "arkoren.dev/driver/aws/awsstorage"
)

// storageHTTPFunc substitutes the native SDK's HTTP boundary without replacing
// driver behavior or permitting a request to reach an external S3 endpoint.
type storageHTTPFunc func(*http.Request) (*http.Response, error)

// Do handles the SDK's serialized request with the scenario's isolated response.
// Blocking implementations must respect the request's context.
func (storageHTTPFunc storageHTTPFunc) Do(request *http.Request) (*http.Response, error) {
	return storageHTTPFunc(request)
}

// nativeStorageDriver borrows a real SDK client using synthetic credentials and
// the supplied HTTP transport; construction never resolves the AWS environment.
func nativeStorageDriver(t *testing.T, transport aws.HTTPClient, configure ...awsstorage.BuilderFunc) *awsstorage.Driver {
	t.Helper()

	native := s3.NewFromConfig(aws.Config{
		Region:           "eu-west-1",
		Credentials:      credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:       transport,
		RetryMaxAttempts: 1,
	})

	driver, err := awsstorage.NewFrom(native, "uploads", configure...)

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// observedReader detects consumption before a canceled request reaches the SDK.
// It intentionally lacks Seek so Put must consider its streaming-reader path.
type observedReader struct {
	// read records any attempt to consume the caller's stream.
	read bool
}

// Read reports an error distinct from cancellation and records the side effect.
// The test requires the driver to return before this method is called.
func (observedReader *observedReader) Read([]byte) (int, error) {
	observedReader.read = true

	return 0, errors.New("unexpected stream read")
}

// TestCanceledPutDoesNotConsumeStream checks the contract before reader and SDK
// side effects, not merely that a cooperating native transport returns an error.
func TestCanceledPutDoesNotConsumeStream(t *testing.T) {
	t.Parallel()

	driver := nativeStorageDriver(t, storageHTTPFunc(func(*http.Request) (*http.Response, error) {
		t.Error("canceled Put reached the native transport")

		return nil, context.Canceled
	}))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := &observedReader{}

	_, err := driver.Put(ctx, "report.json", reader)

	if !errors.Is(err, context.Canceled) || reader.read {
		t.Fatalf("Put error = %v, stream consumed = %t", err, reader.read)
	}
}

// TestCanceledOperationsPrecedeInvalidStorageKeys checks error precedence for
// all nonstreaming object operations without issuing any native request.
func TestCanceledOperationsPrecedeInvalidStorageKeys(t *testing.T) {
	t.Parallel()

	driver := nativeStorageDriver(t, storageHTTPFunc(func(*http.Request) (*http.Response, error) {
		t.Error("canceled operation reached the native transport")

		return nil, context.Canceled
	}))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, open := driver.Open(ctx, "invalid:")
	_, stat := driver.Stat(ctx, "invalid:")
	deleted := driver.Delete(ctx, "invalid:")
	_, listed := driver.List(ctx, storage.ListOptions{Prefix: "invalid:"})

	for _, err := range []error{open, stat, deleted, listed} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("operation error = %v, want context.Canceled", err)
		}
	}
}

// TestListOmitsCursorWhenOnlyNonportableKeysRemain models a bucket shared with
// native callers. Physical continuation must not promise another logical object.
func TestListOmitsCursorWhenOnlyNonportableKeysRemain(t *testing.T) {
	t.Parallel()

	driver := nativeStorageDriver(t, storageHTTPFunc(func(request *http.Request) (*http.Response, error) {
		body := `<ListBucketResult><Contents><Key>tenant/a</Key><Size>1</Size></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>native-next</NextContinuationToken></ListBucketResult>`

		if request.URL.Query().Get("continuation-token") == "native-next" {
			body = `<ListBucketResult><Contents><Key>tenant/bad:</Key><Size>1</Size></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}), func(store *awsstorage.Builder) {
		store.Prefix("tenant")
	})

	page, err := driver.List(t.Context(), storage.ListOptions{Limit: 1})

	if err != nil {
		t.Fatal(err)
	}

	if len(page.Objects) != 1 || page.Objects[0].Key != "a" {
		t.Fatalf("logical objects = %+v, want only a", page.Objects)
	}

	if page.NextCursor != "" {
		t.Fatalf("cursor = %q, but no further portable object exists", page.NextCursor)
	}
}

// TestListPreservesNativeCursorBeforeNextLogicalPage protects resume identity
// when physical pages with invalid keys lie between two portable object pages.
func TestListPreservesNativeCursorBeforeNextLogicalPage(t *testing.T) {
	t.Parallel()

	driver := nativeStorageDriver(t, storageHTTPFunc(func(request *http.Request) (*http.Response, error) {
		var body string

		switch request.URL.Query().Get("continuation-token") {
		case "native-second":
			body = `<ListBucketResult><Contents><Key>bad:</Key></Contents><NextContinuationToken>native-third</NextContinuationToken></ListBucketResult>`
		case "native-third":
			body = `<ListBucketResult><Contents><Key>c</Key></Contents></ListBucketResult>`
		default:
			body = `<ListBucketResult><Contents><Key>a</Key></Contents><NextContinuationToken>native-second</NextContinuationToken></ListBucketResult>`
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}))

	first, err := driver.List(t.Context(), storage.ListOptions{Limit: 1})

	if err != nil {
		t.Fatal(err)
	}

	if first.NextCursor != "native-third" {
		t.Fatalf("cursor = %q, want native-third", first.NextCursor)
	}

	next, err := driver.List(t.Context(), storage.ListOptions{Limit: 1, Cursor: first.NextCursor})

	if err != nil {
		t.Fatal(err)
	}

	if len(next.Objects) != 1 || next.Objects[0].Key != "c" {
		t.Fatalf("resumed logical page = %+v, want c", next.Objects)
	}

	if next.NextCursor != "" {
		t.Fatal("final logical page retained a cursor")
	}
}

const (
	// minioTestEndpoint selects the local MinIO service for integration tests.
	minioTestEndpoint = "http://localhost:9001"

	// minioTestRootUser is the disposable MinIO account configured by Compose.
	minioTestRootUser = "000000000000"

	// minioTestRootSecret is the matching non-production MinIO credential.
	minioTestRootSecret = "arkoren-integration"

	// miniStackTestEndpoint selects the alternate local S3 emulator.
	miniStackTestEndpoint = "http://localhost:14566"

	// s3TestAccessKeyID authenticates against the disposable S3 emulator.
	s3TestAccessKeyID = "000000000000"

	// s3TestSecretKey is a non-production credential accepted by the emulator.
	s3TestSecretKey = "test"

	// s3TestRegion supplies the signing region for local S3 requests.
	s3TestRegion = "us-east-1"

	// s3TestOperationTimeout bounds setup and object operations in tests.
	s3TestOperationTimeout = 10 * time.Second

	// s3TestCleanupTimeout bounds resource release after a test has finished.
	s3TestCleanupTimeout = 10 * time.Second

	// s3TestContents is the deterministic body used by integration assertions.
	s3TestContents = "S3 integration contents"

	// s3TestNameAllowedCharacters contains characters permitted in test resource names.
	s3TestNameAllowedCharacters = "0123456789abcdefghijklmnopqrstuvwxyz"
)

// fakeClient records S3 requests and supplies configurable responses.
type fakeClient struct {
	// getInput retains the bucket and physical key selected by Open.
	getInput *s3.GetObjectInput

	// putInput retains the upload body and destination without reading the body.
	putInput *s3.PutObjectInput

	// headInput retains metadata requests made by Stat and after Put.
	headInput *s3.HeadObjectInput

	// deleteInput retains the last deletion request for key-mapping assertions.
	deleteInput *s3.DeleteObjectInput

	// listInput retains the initial list request, independently of read-ahead
	// used to validate logical continuation in a shared native bucket.
	listInput *s3.ListObjectsV2Input

	// getErr injects native Open failures; nil returns a fixed four-byte body.
	getErr error

	// headErr injects native Stat failures; nil returns fixed object metadata.
	headErr error
}

// GetObject records the request and returns a configured object response.
func (client *fakeClient) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	client.getInput = input

	if client.getErr != nil {
		return nil, client.getErr
	}

	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader([]byte("body"))),
		ContentLength: aws.Int64(4),
	}, nil
}

// PutObject records the request and returns a successful response.
func (client *fakeClient) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	client.putInput = input

	return &s3.PutObjectOutput{}, nil
}

// HeadObject records the request and returns a configured metadata response.
func (client *fakeClient) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	client.headInput = input

	if client.headErr != nil {
		return nil, client.headErr
	}

	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(4),
		LastModified:  aws.Time(time.Unix(1, 0)),
	}, nil
}

// DeleteObject records the request and returns a successful response.
func (client *fakeClient) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	client.deleteInput = input

	return &s3.DeleteObjectOutput{}, nil
}

// ListObjectsV2 records the initial request and returns a prefixed object page.
func (client *fakeClient) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if client.listInput == nil {
		client.listInput = input
	}

	return &s3.ListObjectsV2Output{
		Contents: []types.Object{
			{
				Key:  aws.String("tenant-a/reports/a.pdf"),
				Size: aws.Int64(4),
			},
		},
		NextContinuationToken: aws.String("next"),
	}, nil
}

// TestNewFromValidatesConfig verifies that invalid S3 configuration is rejected.
func TestNewFromValidatesConfig(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{}

	if _, err := awsstorage.NewFrom(fake, ""); err == nil {
		t.Fatal("NewFrom accepted empty bucket")
	}

	if _, err := awsstorage.NewFrom(fake, "bucket", func(store *awsstorage.Builder) {
		store.Prefix("../")
	}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientMapsPrefixAndHeadsAfterPut verifies physical key construction and post-write metadata lookup.
func TestClientMapsPrefixAndHeadsAfterPut(t *testing.T) {
	t.Parallel()

	ctx := newS3OperationContext(t)

	fake := &fakeClient{}
	client, err := awsstorage.NewFrom(fake, "bucket", func(store *awsstorage.Builder) {
		store.Prefix("tenant-a")
	})

	if err != nil {
		t.Fatal(err)
	}

	putS3ClientObject(t, client, ctx)

	assertS3ClientPhysicalKeys(t, fake)

	page := s3ClientListPage(t, client, ctx)

	assertS3ClientListRequest(t, page, fake.listInput)
	assertS3ClientListPage(t, page, fake.listInput)
}

// TestClientPassesBodyAndMapsOnlyNotFound verifies object-body ownership and missing-object mapping.
func TestClientPassesBodyAndMapsOnlyNotFound(t *testing.T) {
	t.Parallel()

	ctx := newS3OperationContext(t)

	fake := &fakeClient{}
	client, err := awsstorage.NewFrom(fake, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	object := openS3ClientObject(t, client, ctx, "a")

	assertS3ClientObjectBody(t, object)

	closeS3ClientObject(t, object)

	assertS3ClientMissingObject(t, client, ctx, fake)
	assertS3ClientDeniedObject(t, client, ctx, fake)
	assertS3ClientDeleteRequest(t, client, ctx, fake)
}

// TestClientMapsMissingObjectErrorsThroughStorage verifies facade preservation
// of mapped S3 missing errors.
func TestClientMapsMissingObjectErrorsThroughStorage(t *testing.T) {
	t.Parallel()

	ctx := newS3OperationContext(t)
	fake := &fakeClient{
		getErr:  &types.NoSuchKey{},
		headErr: &types.NotFound{},
	}
	client, err := awsstorage.NewFrom(fake, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	store := storage.New(client)

	if _, err := store.Open(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v", err)
	}

	if _, err := store.Stat(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat error = %v", err)
	}
}

// TestMinIOPutOpensBytes verifies MinIO object write and read behavior.
func TestMinIOPutOpensBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, operationContext := newMinIOClient(t)
	key := fmt.Sprintf("reports/%s.txt", s3TestName(t))
	contents := []byte(s3TestContents)

	info := putS3TestObject(t, client, operationContext, key, contents, "S3")

	assertS3ObjectInfo(t, info, key, int64(len(contents)))

	object := openS3TestObject(t, client, operationContext, key)

	assertS3ObjectContents(t, object, contents)
}

// TestMinIOStat verifies MinIO metadata retrieval.
func TestMinIOStat(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, operationContext := newMinIOClient(t)
	key := fmt.Sprintf("reports/%s.txt", s3TestName(t))
	contents := []byte(s3TestContents)

	putS3TestObject(t, client, operationContext, key, contents, "S3")

	info := s3TestObjectInfo(t, client, operationContext, key)

	assertS3ObjectInfoWithTimestamp(t, info, key, int64(len(contents)))
}

// TestMinIOListsPrefixedObjects verifies MinIO prefix filtering.
func TestMinIOListsPrefixedObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, operationContext := newMinIOClient(t)
	matchingKey := fmt.Sprintf("reports/%s.txt", s3TestName(t))
	otherKey := fmt.Sprintf("other/%s.txt", s3TestName(t))

	putS3TestObject(t, client, operationContext, matchingKey, []byte(s3TestContents), "matching S3")
	putS3TestObject(t, client, operationContext, otherKey, []byte(s3TestContents), "nonmatching S3")

	page := s3TestListPage(t, client, operationContext)

	assertS3TestListPage(t, page, matchingKey)
}

// TestMinIODeleteMapsMissingObject verifies MinIO missing-object error mapping.
func TestMinIODeleteMapsMissingObject(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, operationContext := newMinIOClient(t)
	key := fmt.Sprintf("reports/%s.txt", s3TestName(t))

	if _, err := client.Put(operationContext, key, bytes.NewReader([]byte(s3TestContents))); err != nil {
		t.Fatalf("put S3 object: %v", err)
	}

	if err := client.Delete(operationContext, key); err != nil {
		t.Fatalf("delete S3 object: %v", err)
	}

	if _, err := client.Stat(operationContext, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("stat missing S3 object error = %v", err)
	}
}

// TestMiniStackBucketLifecycle verifies MiniStack object lifecycle behavior.
func TestMiniStackBucketLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, operationContext := newMiniStackClient(t)
	key := fmt.Sprintf("reports/%s.txt", s3TestName(t))

	if _, err := client.Put(operationContext, key, bytes.NewReader([]byte(s3TestContents))); err != nil {
		t.Fatalf("put MiniStack object: %v", err)
	}

	if err := client.Delete(operationContext, key); err != nil {
		t.Fatalf("delete MiniStack object: %v", err)
	}

	if _, err := client.Stat(operationContext, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("stat missing MiniStack object error = %v", err)
	}
}

// newMinIOClient creates an isolated S3 driver on the shared MinIO server.
func newMinIOClient(t *testing.T) (*awsstorage.Driver, context.Context) {
	t.Helper()

	operationContext := newS3OperationContext(t)
	provider := credentials.NewStaticCredentialsProvider(minioTestRootUser, minioTestRootSecret, "")
	bucket := s3TestName(t)
	sdkClient := newS3SDKClient(t, operationContext, minioTestEndpoint, provider)
	createS3TestBucket(t, operationContext, sdkClient, bucket, "MinIO")
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), s3TestCleanupTimeout)
		defer cancel()

		cleanupS3Bucket(t, cleanupContext, sdkClient, bucket)
	})

	return newS3TestStorageClient(t, operationContext, bucket, minioTestEndpoint, provider, "MinIO"), operationContext
}

// newMiniStackClient creates an isolated S3 driver on the shared MiniStack server.
func newMiniStackClient(t *testing.T) (*awsstorage.Driver, context.Context) {
	t.Helper()

	operationContext := newS3OperationContext(t)
	provider := credentials.NewStaticCredentialsProvider(s3TestAccessKeyID, s3TestSecretKey, "")
	bucket := s3TestName(t)
	sdkClient := newS3SDKClient(t, operationContext, miniStackTestEndpoint, provider)
	createS3TestBucket(t, operationContext, sdkClient, bucket, "MiniStack")
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), s3TestCleanupTimeout)
		defer cancel()

		cleanupS3Bucket(t, cleanupContext, sdkClient, bucket)
	})

	return newS3TestStorageClient(t, operationContext, bucket, miniStackTestEndpoint, provider, "MiniStack"), operationContext
}

// newS3OperationContext creates a bounded context for one S3 test.
func newS3OperationContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), s3TestOperationTimeout)
	t.Cleanup(cancel)

	return ctx
}

// newS3SDKClient creates a path-style S3 SDK client for a test endpoint.
func newS3SDKClient(
	t *testing.T,
	ctx context.Context,
	endpoint string,
	provider aws.CredentialsProvider,
) *s3.Client {
	t.Helper()

	config, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(s3TestRegion),
		awsconfig.WithBaseEndpoint(endpoint),
		awsconfig.WithCredentialsProvider(provider),
	)

	if err != nil {
		t.Fatalf("load S3 SDK configuration: %v", err)
	}

	return s3.NewFromConfig(config, func(options *s3.Options) {
		options.UsePathStyle = true
	})
}

// cleanupS3Bucket removes all objects and the bucket created for a test.
func cleanupS3Bucket(t *testing.T, ctx context.Context, client *s3.Client, bucket string) {
	t.Helper()

	objects, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})

	if err != nil {
		t.Errorf("list S3 bucket %q for cleanup: %v", bucket, err)

		return
	}

	deleteS3TestObjects(t, ctx, client, objects, bucket)
	deleteS3TestBucket(t, ctx, client, bucket)
}

// s3TestName creates a unique, S3-compatible name for a test resource.
func s3TestName(t *testing.T) string {
	t.Helper()

	randomBytes := make([]byte, 8)

	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("generate S3 test name: %v", err)
	}

	name := strings.Map(s3TestNameCharacter, t.Name())
	name = strings.Trim(name, "-")

	if name == "" {
		name = "test"
	}

	return fmt.Sprintf("arkoren-%s-%s", name[:min(len(name), 38)], hex.EncodeToString(randomBytes))
}

// putS3ClientObject stores the fake-client object used to inspect request keys.
func putS3ClientObject(t *testing.T, client *awsstorage.Driver, ctx context.Context) {
	t.Helper()

	if _, err := client.Put(ctx, "reports/a.pdf", bytes.NewReader([]byte("body"))); err != nil {
		t.Fatal(err)
	}
}

// assertS3ClientPhysicalKeys verifies put and stat use the configured prefix.
func assertS3ClientPhysicalKeys(t *testing.T, client *fakeClient) {
	t.Helper()

	if aws.ToString(client.putInput.Key) != "tenant-a/reports/a.pdf" || aws.ToString(client.headInput.Key) != "tenant-a/reports/a.pdf" {
		t.Fatalf("inputs = %#v %#v", client.putInput, client.headInput)
	}
}

// s3ClientListPage returns the fake-client page used to inspect list inputs.
func s3ClientListPage(t *testing.T, client *awsstorage.Driver, ctx context.Context) storage.Page {
	t.Helper()

	page, err := client.List(ctx, storage.ListOptions{
		Prefix: "reports/",
		Cursor: "token",
		Limit:  2,
	})

	if err != nil {
		t.Fatalf("page = %#v, input = %#v, err = %v", page, nil, err)
	}

	return page
}

// assertS3ClientListRequest verifies the fake client received the expected list input.
func assertS3ClientListRequest(t *testing.T, page storage.Page, input *s3.ListObjectsV2Input) {
	t.Helper()

	if aws.ToString(input.Prefix) != "tenant-a/reports/" {
		t.Fatalf("page = %#v, input = %#v, err = %v", page, input, nil)
	}

	if aws.ToString(input.ContinuationToken) != "token" {
		t.Fatalf("page = %#v, input = %#v, err = %v", page, input, nil)
	}

	if aws.ToInt32(input.MaxKeys) != 2 {
		t.Fatalf("page = %#v, input = %#v, err = %v", page, input, nil)
	}
}

// assertS3ClientListPage verifies the fake-client page is mapped to portable metadata.
func assertS3ClientListPage(t *testing.T, page storage.Page, input *s3.ListObjectsV2Input) {
	t.Helper()

	if page.NextCursor != "next" {
		t.Fatalf("page = %#v, input = %#v, err = %v", page, input, nil)
	}

	if len(page.Objects) != 1 {
		t.Fatalf("page = %#v, input = %#v, err = %v", page, input, nil)
	}

	if page.Objects[0].Key != "reports/a.pdf" {
		t.Fatalf("page = %#v, input = %#v, err = %v", page, input, nil)
	}
}

// openS3ClientObject opens key through the fake S3 client.
func openS3ClientObject(t *testing.T, client *awsstorage.Driver, ctx context.Context, key string) storage.Object {
	t.Helper()

	object, err := client.Open(ctx, key)

	if err != nil {
		t.Fatal(err)
	}

	return object
}

// assertS3ClientObjectBody verifies opening an S3 object returns its body.
func assertS3ClientObjectBody(t *testing.T, object storage.Object) {
	t.Helper()

	if object.Contents == nil {
		t.Fatal("Open did not return S3 body")
	}
}

// closeS3ClientObject closes an opened fake-client object body.
func closeS3ClientObject(t *testing.T, object storage.Object) {
	t.Helper()

	if err := object.Contents.Close(); err != nil {
		t.Fatal(err)
	}
}

// assertS3ClientMissingObject verifies S3 Open and Stat missing errors map to
// the storage sentinel.
func assertS3ClientMissingObject(t *testing.T, client *awsstorage.Driver, ctx context.Context, fake *fakeClient) {
	t.Helper()

	fake.getErr = &types.NoSuchKey{}

	if _, err := client.Open(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v", err)
	}

	fake.headErr = &types.NotFound{}

	if _, err := client.Stat(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat error = %v", err)
	}
}

// assertS3ClientDeniedObject verifies non-missing S3 errors remain distinct.
func assertS3ClientDeniedObject(t *testing.T, client *awsstorage.Driver, ctx context.Context, fake *fakeClient) {
	t.Helper()

	fake.getErr = errors.New("permission denied")

	if _, err := client.Open(ctx, "denied"); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("permission error = %v", err)
	}
}

// assertS3ClientDeleteRequest verifies delete uses the logical key without a prefix.
func assertS3ClientDeleteRequest(t *testing.T, client *awsstorage.Driver, ctx context.Context, fake *fakeClient) {
	t.Helper()

	if err := client.Delete(ctx, "a"); err != nil || aws.ToString(fake.deleteInput.Key) != "a" {
		t.Fatalf("Delete err = %v, input = %#v", err, fake.deleteInput)
	}
}

// putS3TestObject stores contents and returns the resulting S3 metadata.
func putS3TestObject(t *testing.T, client *awsstorage.Driver, ctx context.Context, key string, contents []byte, name string) storage.ObjectInfo {
	t.Helper()

	info, err := client.Put(ctx, key, bytes.NewReader(contents))

	if err != nil {
		t.Fatalf("put %s object: %v", name, err)
	}

	return info
}

// assertS3ObjectInfo verifies the stored object metadata matches key and size.
func assertS3ObjectInfo(t *testing.T, info storage.ObjectInfo, key string, size int64) {
	t.Helper()

	if info.Key != key || info.Size != size {
		t.Fatalf("put info = %#v", info)
	}
}

// openS3TestObject opens a stored object and fails the test if it cannot be read.
func openS3TestObject(t *testing.T, client *awsstorage.Driver, ctx context.Context, key string) storage.Object {
	t.Helper()

	object, err := client.Open(ctx, key)

	if err != nil {
		t.Fatalf("open S3 object: %v", err)
	}

	return object
}

// assertS3ObjectContents verifies an opened object body contains want and closes it.
func assertS3ObjectContents(t *testing.T, object storage.Object, want []byte) {
	t.Helper()

	got, err := io.ReadAll(object.Contents)

	if err != nil {
		t.Fatalf("read S3 object: %v", err)
	}

	if err := object.Contents.Close(); err != nil {
		t.Fatalf("close S3 object: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("open contents = %q, want %q", got, want)
	}
}

// s3TestObjectInfo returns canonical S3 metadata for key.
func s3TestObjectInfo(t *testing.T, client *awsstorage.Driver, ctx context.Context, key string) storage.ObjectInfo {
	t.Helper()

	info, err := client.Stat(ctx, key)

	if err != nil {
		t.Fatalf("stat S3 object: %v", err)
	}

	return info
}

// assertS3ObjectInfoWithTimestamp verifies S3 metadata includes a timestamp.
func assertS3ObjectInfoWithTimestamp(t *testing.T, info storage.ObjectInfo, key string, size int64) {
	t.Helper()

	if info.Key != key || info.Size != size || info.ModifiedAt.IsZero() {
		t.Fatalf("stat info = %#v", info)
	}
}

// s3TestListPage returns the reports page for the MinIO list test.
func s3TestListPage(t *testing.T, client *awsstorage.Driver, ctx context.Context) storage.Page {
	t.Helper()

	page, err := client.List(ctx, storage.ListOptions{
		Prefix: "reports/",
	})

	if err != nil {
		t.Fatalf("list S3 objects: %v", err)
	}

	return page
}

// assertS3TestListPage verifies one matching object is returned by the list test.
func assertS3TestListPage(t *testing.T, page storage.Page, key string) {
	t.Helper()

	if len(page.Objects) != 1 || page.Objects[0].Key != key {
		t.Fatalf("list page = %#v", page)
	}
}

// createS3TestBucket creates bucket and fails the test if creation fails.
func createS3TestBucket(t *testing.T, ctx context.Context, client *s3.Client, bucket string, name string) {
	t.Helper()

	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("create %s bucket: %v", name, err)
	}
}

// newS3TestStorageClient creates a configured storage client for an integration endpoint.
func newS3TestStorageClient(
	t *testing.T,
	ctx context.Context,
	bucket string,
	endpoint string,
	provider aws.CredentialsProvider,
	name string,
) *awsstorage.Driver {
	t.Helper()

	client, err := awsstorage.New(ctx, bucket, func(store *awsstorage.Builder) {
		store.Region(s3TestRegion)
		store.Endpoint(endpoint)
		store.Prefix(fmt.Sprintf("integration/%s", s3TestName(t)))
		store.Credentials(provider)
	})

	if err != nil {
		t.Fatalf("create %s storage client: %v", name, err)
	}

	return client
}

// deleteS3TestObjects deletes the listed objects before deleting their bucket.
func deleteS3TestObjects(
	t *testing.T,
	ctx context.Context,
	client *s3.Client,
	output *s3.ListObjectsV2Output,
	bucket string,
) {
	t.Helper()

	for _, object := range output.Contents {
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    object.Key,
		}); err != nil {
			t.Errorf("delete S3 object %q during cleanup: %v", aws.ToString(object.Key), err)
		}
	}
}

// deleteS3TestBucket deletes bucket after its objects are removed.
func deleteS3TestBucket(t *testing.T, ctx context.Context, client *s3.Client, bucket string) {
	t.Helper()

	if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Errorf("delete S3 bucket %q: %v", bucket, err)
	}
}

// s3TestNameCharacter converts unsupported characters to hyphens.
func s3TestNameCharacter(character rune) rune {
	lowercaseCharacter := character | ('a' - 'A')

	if strings.ContainsRune(s3TestNameAllowedCharacters, lowercaseCharacter) {
		return lowercaseCharacter
	}

	return '-'
}

// TestMinIOConformance runs the storage conformance suite against MinIO.
func TestMinIOConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	storagetest.Run(t, func(t *testing.T) storage.Driver {
		client, _ := newMinIOClient(t)

		return client
	})
}
