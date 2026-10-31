package awsstorage_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"arkoren.dev/core/service/storage"
	awsstorage "arkoren.dev/driver/aws/awsstorage"
)

// fakePresigningClient is a [fakeClient] that also implements the presigning
// operations, recording which one ran and the expiry it received.
type fakePresigningClient struct {
	fakeClient

	// method is the HTTP method of the last presign call.
	method string

	// bucket and key identify the object of the last presign call.
	bucket, key string

	// expires is the expiry the last presign call was given.
	expires time.Duration

	// putInput is the input of the last PresignPutObject call.
	putInput *s3.PutObjectInput

	// err is returned by both presign operations when set.
	err error
}

// PresignGetObject records the request and returns a URL for the object.
func (client *fakePresigningClient) PresignGetObject(_ context.Context, input *s3.GetObjectInput, options ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return client.record(http.MethodGet, aws.ToString(input.Bucket), aws.ToString(input.Key), options)
}

// PresignPutObject records the upload request and returns a URL for the object.
// Its result advertises the requested Content-Type as signed, matching the SDK
// contract. Native SDK tests exercise middleware that can remove that header.
func (client *fakePresigningClient) PresignPutObject(
	_ context.Context,
	input *s3.PutObjectInput,
	options ...func(*s3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) {
	client.putInput = input

	signed, err := client.record(http.MethodPut, aws.ToString(input.Bucket), aws.ToString(input.Key), options)

	if err != nil {
		return nil, err
	}

	if input.ContentType != nil {
		signed.SignedHeader = http.Header{"Content-Type": {*input.ContentType}}
	}

	return signed, nil
}

// record stores the call and builds the presigned request.
func (client *fakePresigningClient) record(method, bucket, key string, options []func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	var applied s3.PresignOptions

	for _, option := range options {
		option(&applied)
	}

	client.method, client.bucket, client.key, client.expires = method, bucket, key, applied.Expires

	if client.err != nil {
		return nil, client.err
	}

	return &v4.PresignedHTTPRequest{
		URL:    "https://" + bucket + ".example.com/" + key + "?sig=1",
		Method: method,
	}, nil
}

// requireSignedWithMethod signs a key under a prefixed driver with the given
// method and verifies the presigner received the prefixed key, the want method
// and the TTL as expiry.
func requireSignedWithMethod(t *testing.T, method, want string) {
	t.Helper()

	fake := &fakePresigningClient{}
	driver, err := awsstorage.NewFrom(fake, "bucket", func(store *awsstorage.Builder) {
		store.Prefix("tenant-a")
	})

	if err != nil {
		t.Fatal(err)
	}

	signed, err := driver.SignURL(t.Context(), "reports/a.pdf", storage.SignOptions{
		Method: method,
		TTL:    90 * time.Second,
	})

	if err != nil {
		t.Fatal(err)
	}

	if fake.method != want || fake.bucket != "bucket" || fake.key != "tenant-a/reports/a.pdf" || fake.expires != 90*time.Second {
		t.Fatalf("presign call = %s %s/%s expires %s", fake.method, fake.bucket, fake.key, fake.expires)
	}

	if got, want := signed.String(), "https://bucket.example.com/tenant-a/reports/a.pdf?sig=1"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

// TestSignURLEmptyMethodSignsGet verifies an empty method presigns a GET.
func TestSignURLEmptyMethodSignsGet(t *testing.T) {
	t.Parallel()

	requireSignedWithMethod(t, "", http.MethodGet)
}

// TestSignURLGet verifies GET presigns a GET with the prefixed key and TTL.
func TestSignURLGet(t *testing.T) {
	t.Parallel()

	requireSignedWithMethod(t, http.MethodGet, http.MethodGet)
}

// TestSignURLPut verifies PUT presigns a PUT with the prefixed key and TTL.
func TestSignURLPut(t *testing.T) {
	t.Parallel()

	requireSignedWithMethod(t, http.MethodPut, http.MethodPut)
}

// requireSignRejected verifies SignURL fails with want before presigning.
func requireSignRejected(t *testing.T, ctx context.Context, key string, options storage.SignOptions, want error) {
	t.Helper()

	fake := &fakePresigningClient{}
	driver, err := awsstorage.NewFrom(fake, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.SignURL(ctx, key, options); !errors.Is(err, want) || fake.method != "" {
		t.Fatalf("SignURL error = %v, presigned %q, want %v without presigning", err, fake.method, want)
	}
}

// TestSignURLCanceledContextFailsBeforePresigning verifies a canceled context wins.
func TestSignURLCanceledContextFailsBeforePresigning(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	requireSignRejected(t, canceled, "", storage.SignOptions{}, context.Canceled)
}

// TestSignURLInvalidKeyFailsBeforePresigning verifies key validation.
func TestSignURLInvalidKeyFailsBeforePresigning(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a//b", storage.SignOptions{}, storage.ErrInvalidKey)
}

// TestSignURLZeroTTLFailsBeforePresigning verifies a zero TTL is rejected.
func TestSignURLZeroTTLFailsBeforePresigning(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a", storage.SignOptions{}, storage.ErrInvalidTTL)
}

// TestSignURLNegativeTTLFailsBeforePresigning verifies a negative TTL is rejected.
func TestSignURLNegativeTTLFailsBeforePresigning(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a", storage.SignOptions{
		TTL: -1,
	}, storage.ErrInvalidTTL)
}

// TestSignURLTTLOverCapFailsBeforePresigning verifies the seven day cap.
func TestSignURLTTLOverCapFailsBeforePresigning(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a", storage.SignOptions{
		TTL: signURLCap + time.Second,
	}, storage.ErrInvalidTTL)
}

// TestSignURLUnsupportedMethodFailsBeforePresigning verifies methods other than GET and PUT are rejected.
func TestSignURLUnsupportedMethodFailsBeforePresigning(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a", storage.SignOptions{
		Method: http.MethodDelete,
		TTL:    time.Minute,
	}, storage.ErrUnsupported)
}

// signURLCap is the longest TTL SignURL accepts: seven days.
const signURLCap = 7 * 24 * time.Hour

// TestSignURLTTLAtCapIsAccepted verifies the seven day cap itself is allowed.
func TestSignURLTTLAtCapIsAccepted(t *testing.T) {
	t.Parallel()

	fake := &fakePresigningClient{}
	driver, err := awsstorage.NewFrom(fake, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.SignURL(t.Context(), "a", storage.SignOptions{
		TTL: signURLCap,
	}); err != nil || fake.expires != signURLCap {
		t.Fatalf("SignURL at the cap error = %v, expires %s", err, fake.expires)
	}
}

// TestSignURLWrapsPresignerErrors verifies that presigner failures name the operation and keep their cause.
func TestSignURLWrapsPresignerErrors(t *testing.T) {
	t.Parallel()

	cause := errors.New("no credentials")
	driver, err := awsstorage.NewFrom(&fakePresigningClient{
		err: cause,
	}, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.SignURL(t.Context(), "a", storage.SignOptions{
		Method: http.MethodPut,
		TTL:    time.Minute,
	})

	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want wrapped cause", err)
	}

	if got, want := err.Error(), `presign PUT "a": no credentials`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestSignURLWithoutPresignerIsUnsupported verifies that a client that cannot presign yields ErrUnsupported.
func TestSignURLWithoutPresignerIsUnsupported(t *testing.T) {
	t.Parallel()

	driver, err := awsstorage.NewFrom(&fakeClient{}, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.SignURL(t.Context(), "a", storage.SignOptions{
		TTL: time.Minute,
	}); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}

	if _, err := driver.SignURL(t.Context(), "", storage.SignOptions{
		TTL: time.Minute,
	}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("invalid key error = %v, want ErrInvalidKey", err)
	}
}

// TestSignURLPutBindsContentTypeAndLength verifies the PutObject input carries
// the requested type and length. Separate native SDK tests verify the signature;
// passing an input alone is not evidence that middleware preserves its binding.
func TestSignURLPutBindsContentTypeAndLength(t *testing.T) {
	t.Parallel()

	fake := &fakePresigningClient{}
	driver, err := awsstorage.NewFrom(fake, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	options := storage.SignOptions{
		Method:        http.MethodPut,
		TTL:           time.Minute,
		ContentType:   "image/png",
		ContentLength: 2048,
	}

	if _, err := driver.SignURL(t.Context(), "a.png", options); err != nil {
		t.Fatal(err)
	}

	if got := aws.ToString(fake.putInput.ContentType); got != "image/png" {
		t.Fatalf("ContentType = %q, want image/png", got)
	}

	if got := aws.ToInt64(fake.putInput.ContentLength); got != 2048 {
		t.Fatalf("ContentLength = %d, want 2048", got)
	}
}

// TestSignURLPutSignsContentTypeWithoutLength checks the real SDK's SigV4
// output, not just its input. Static fake credentials keep presigning offline;
// a requested type must remain bound when zero leaves the size unrestricted.
func TestSignURLPutSignsContentTypeWithoutLength(t *testing.T) {
	t.Parallel()

	client := s3.NewFromConfig(aws.Config{
		Region:      "eu-west-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})

	driver, err := awsstorage.NewFrom(client, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	signed, err := driver.SignURL(t.Context(), "a.png", storage.SignOptions{
		Method:      http.MethodPut,
		TTL:         time.Minute,
		ContentType: "image/png",
	})

	if err != nil {
		t.Fatal(err)
	}

	if got := signed.Query().Get("X-Amz-SignedHeaders"); got != "content-type;host" {
		t.Fatalf("signed headers = %q, want content-type;host", got)
	}

	if !strings.Contains(signed.Query().Get("X-Amz-Credential"), "/eu-west-1/s3/aws4_request") {
		t.Fatal("presigned URL no longer uses the S3 SigV4 credential scope")
	}
}

// TestSignURLPutSignsContentTypeAndLength checks both actual SigV4 bindings
// with the same middleware that drops the type for zero-length signing bodies.
// A positive exact length must remain supported without changing URL format.
func TestSignURLPutSignsContentTypeAndLength(t *testing.T) {
	t.Parallel()

	client := s3.NewFromConfig(aws.Config{
		Region:      "eu-west-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		APIOptions: []func(*middleware.Stack) error{func(stack *middleware.Stack) error {
			return stack.Build.Insert(&smithyhttp.ComputeContentLength{}, "RemoveContentTypeHeader", middleware.Before)
		}},
	})

	driver, err := awsstorage.NewFrom(client, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	signed, err := driver.SignURL(t.Context(), "a.png", storage.SignOptions{
		Method:        http.MethodPut,
		TTL:           time.Minute,
		ContentType:   "image/png",
		ContentLength: 2048,
	})

	if err != nil {
		t.Fatal(err)
	}

	if got := signed.Query().Get("X-Amz-SignedHeaders"); got != "content-length;content-type;host" {
		t.Fatalf("signed headers = %q, want content-length;content-type;host", got)
	}
}

// TestSignURLPutRejectsDroppedContentType checks a native client's optional
// content-length middleware, which computes zero for the empty signing body.
// The SDK then removes Content-Type; SignURL must reject that weakened profile
// instead of returning a URL that does not bind the requested type.
func TestSignURLPutRejectsDroppedContentType(t *testing.T) {
	t.Parallel()

	client := s3.NewFromConfig(aws.Config{
		Region:      "eu-west-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		APIOptions: []func(*middleware.Stack) error{func(stack *middleware.Stack) error {
			return stack.Build.Insert(&smithyhttp.ComputeContentLength{}, "RemoveContentTypeHeader", middleware.Before)
		}},
	})

	request, err := s3.NewPresignClient(client).PresignPutObject(t.Context(), &s3.PutObjectInput{
		Bucket:      aws.String("bucket"),
		Key:         aws.String("a.png"),
		ContentType: aws.String("image/png"),
	})

	if err != nil {
		t.Fatal(err)
	}

	if got := request.SignedHeader.Get("Content-Type"); got != "" {
		t.Fatalf("SDK content type = %q, want omitted type for a computed zero length", got)
	}

	driver, err := awsstorage.NewFrom(client, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	signed, err := driver.SignURL(t.Context(), "a.png", storage.SignOptions{
		Method:      http.MethodPut,
		TTL:         time.Minute,
		ContentType: "image/png",
	})

	if signed != nil {
		t.Fatalf("unbound URL signed headers = %q", signed.Query().Get("X-Amz-SignedHeaders"))
	}

	if !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported for the unsupported binding profile", err)
	}
}

// TestSignURLPutRejectsChangedContentType checks the presigner result's value,
// not only header presence. A native client's header override must not grant
// uploads for a type other than the one requested through SignURL.
func TestSignURLPutRejectsChangedContentType(t *testing.T) {
	t.Parallel()

	client := s3.NewFromConfig(aws.Config{
		Region:      "eu-west-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		APIOptions:  []func(*middleware.Stack) error{smithyhttp.SetHeaderValue("Content-Type", "text/html")},
	})

	driver, err := awsstorage.NewFrom(client, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	signed, err := driver.SignURL(t.Context(), "a.png", storage.SignOptions{
		Method:      http.MethodPut,
		TTL:         time.Minute,
		ContentType: "image/png",
	})

	if signed != nil || !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("SignURL returned a URL: %t, error = %v; want nil URL and ErrUnsupported", signed != nil, err)
	}
}

// TestSignURLPutUnboundLeavesInputEmpty verifies unset options are not signed.
func TestSignURLPutUnboundLeavesInputEmpty(t *testing.T) {
	t.Parallel()

	fake := &fakePresigningClient{}
	driver, err := awsstorage.NewFrom(fake, "bucket")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.SignURL(t.Context(), "a.png", storage.SignOptions{
		Method: http.MethodPut,
		TTL:    time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	if fake.putInput.ContentType != nil || fake.putInput.ContentLength != nil {
		t.Fatalf("input = %+v, want no ContentType or ContentLength", fake.putInput)
	}
}

// TestSignURLGetRejectsBinding verifies a GET with a bound type fails before presigning.
func TestSignURLGetRejectsBinding(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a.png", storage.SignOptions{
		TTL:         time.Minute,
		ContentType: "image/png",
	}, storage.ErrInvalidSignOptions)
}

// TestSignURLPutRejectsNegativeLengthBeforePresigning preserves binding
// validation before any SDK middleware or returned-header checks can run.
func TestSignURLPutRejectsNegativeLengthBeforePresigning(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a.png", storage.SignOptions{
		Method:        http.MethodPut,
		TTL:           time.Minute,
		ContentType:   "image/png",
		ContentLength: -1,
	}, storage.ErrInvalidSignOptions)
}

// TestSignURLUnsupportedMethodPrecedesBindingValidation preserves the portable
// error precedence even when a request also has an invalid length and type.
func TestSignURLUnsupportedMethodPrecedesBindingValidation(t *testing.T) {
	t.Parallel()

	requireSignRejected(t, t.Context(), "a.png", storage.SignOptions{
		Method:        http.MethodDelete,
		TTL:           time.Minute,
		ContentType:   "image/png",
		ContentLength: -1,
	}, storage.ErrUnsupported)
}
