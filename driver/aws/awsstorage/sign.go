package awsstorage

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"arkoren.dev/core/service/storage"
)

// SignURL returns an AWS Signature Version 4 presigned URL that grants GET or
// PUT on key until options.TTL elapses. Validation follows
// [storage.Driver.SignURL]; the TTL may not exceed seven days, the SigV4
// limit, and a longer one returns an error wrapping [storage.ErrInvalidTTL].
// [Config.Prefix] applies, so the URL addresses the object that [Driver.Put]
// would store for key. A driver built by [NewFrom] from a client that cannot
// presign returns an error wrapping [storage.ErrUnsupported].
//
// For PUT, a nonempty options.ContentType and a positive options.ContentLength
// bind the Content-Type and Content-Length headers. Zero leaves length unbound;
// a type can be bound independently. If the presigner drops or changes the
// requested type, signing returns an error wrapping [storage.ErrUnsupported]
// instead of a URL with a weaker binding. This can occur when a borrowed native
// client's middleware computes zero length before the SDK removes Content-Type.
// The driver does not modify the client's middleware to override that behavior.
// Custom presigners must report the exact requested type as the sole signed
// Content-Type value in v4.PresignedHTTPRequest.SignedHeader.
// Set both bindings for user uploads: without them the URL holder picks the type
// (for example text/html) and the size. S3 presigned PUT can bind only an exact
// length, never a maximum; size ranges need a presigned POST policy, which this
// driver does not implement. The client must send exactly the signed headers.
//
// A URL signed with temporary credentials, such as STS or IRSA, stops working
// when that session expires, even if the TTL is longer.
//
// Example:
//
//	link, err := store.SignURL(ctx, "avatars/ada.png", storage.SignOptions{
//		Method:        http.MethodPut,
//		TTL:           10 * time.Minute,
//		ContentType:   "image/png",
//		ContentLength: 2048,
//	})
//	if err != nil {
//		return err
//	}
//	_ = link // Give the uploader this URL and the bound header values.
func (driver *Driver) SignURL(ctx context.Context, key string, options storage.SignOptions) (*url.URL, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	physicalKey, err := driver.physicalKey(key)

	if err != nil {
		return nil, err
	}

	if err := validateSignOptions(key, options); err != nil {
		return nil, err
	}

	if driver.presigner == nil {
		return nil, fmt.Errorf("sign %q: S3 client cannot presign: %w", key, storage.ErrUnsupported)
	}

	var signed *v4.PresignedHTTPRequest

	if options.Method == http.MethodPut {
		signed, err = driver.presignPutObject(ctx, physicalKey, options)
	} else {
		signed, err = driver.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(driver.bucket),
			Key:    aws.String(physicalKey),
		}, s3.WithPresignExpires(options.TTL))
	}

	if err != nil {
		return nil, fmt.Errorf("presign %s %q: %w", cmp.Or(options.Method, http.MethodGet), key, err)
	}

	signedURL, err := url.Parse(signed.URL)

	if err != nil {
		return nil, fmt.Errorf("parse presigned URL for %q: %w", key, err)
	}

	return signedURL, nil
}

// validateSignOptions checks the portable signing policy after context and key
// validation. It preserves TTL, method, then binding error precedence, including
// the S3 seven-day cap, before SignURL checks for an available presigner.
func validateSignOptions(key string, options storage.SignOptions) error {
	if options.TTL <= 0 || options.TTL > maxSignTTL {
		return fmt.Errorf("sign %q: ttl %s must be positive and at most %s: %w",
			key, options.TTL, maxSignTTL, storage.ErrInvalidTTL)
	}

	if options.Method != "" && options.Method != http.MethodGet && options.Method != http.MethodPut {
		return fmt.Errorf("sign %q: method %q: %w", key, options.Method, storage.ErrUnsupported)
	}

	if err := options.ValidateBinding(); err != nil {
		return fmt.Errorf("sign %q: %w", key, err)
	}

	return nil
}

// presignPutObject builds the upload request from validated options and checks
// the presigner's signed headers before exposing a URL. Native middleware may
// remove Content-Type for an empty signing body; that profile is unsupported
// when a type was requested. The borrowed client and its middleware are unchanged.
// Errors retain their cause and are wrapped with the logical key by SignURL.
func (driver *Driver) presignPutObject(
	ctx context.Context,
	physicalKey string,
	options storage.SignOptions,
) (*v4.PresignedHTTPRequest, error) {
	input := &s3.PutObjectInput{
		Bucket: aws.String(driver.bucket),
		Key:    aws.String(physicalKey),
	}

	if options.ContentType != "" {
		input.ContentType = aws.String(options.ContentType)
	}

	if options.ContentLength > 0 {
		input.ContentLength = aws.Int64(options.ContentLength)
	}

	signed, err := driver.presigner.PresignPutObject(ctx, input, s3.WithPresignExpires(options.TTL))

	if err != nil {
		return nil, err
	}
	// contentType retains every signed value so extra values cannot pass the
	// exact binding check merely because the first value matches.
	contentType := signed.SignedHeader.Values("Content-Type")

	if options.ContentType != "" && !slices.Equal(contentType, []string{options.ContentType}) {
		return nil, fmt.Errorf("S3 presigner did not bind requested content type: %w", storage.ErrUnsupported)
	}

	return signed, nil
}
