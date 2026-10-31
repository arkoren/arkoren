package link_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/key"
	"arkoren.dev/foundation/problem"
)

// secret is the fixed, sufficiently long signing key shared by deterministic
// link examples; it is not an application credential. Applications must load
// an unpredictable secret from secure configuration.
var secret = fixtureSecret([]byte("0123456789abcdef0123456789abcdef"))

// ExampleNew shows construction with an absolute origin, followed by signing
// and verifying an application link.
func ExampleNew() {
	signer, err := link.New(
		secret,
		func(signer *link.Builder) {
			signer.Origin("https://app.example.com")
		},
	)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Sign("/unsubscribe?user=42")

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(signer.Verify(signed))

	// An absolute signer rejects other origins.
	_, err = signer.Sign("https://evil.example.com/unsubscribe")
	fmt.Println(errors.Is(err, link.ErrInvalidURL))
	// Output:
	// <nil>
	// true
}

// ExampleNew_relative shows that an origin-free signer produces relative links
// suitable for applications that supply their own host.
func ExampleNew_relative() {
	signer, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Sign("/files/report.pdf")

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(signer.Verify(signed))

	// A relative signer accepts paths only.
	_, err = signer.Sign("https://app.example.com/files/report.pdf")
	fmt.Println(err)
	// Output:
	// <nil>
	// link URL is invalid
}

// ExampleNew_invalidSecret shows how callers inspect the construction error
// returned for an undersized signing secret.
func ExampleNew_invalidSecret() {
	_, err := link.New(key.Shared{})
	fmt.Println(err)
	// Output: link signing secret is invalid
}

// ExampleSigner_Verify shows that verification accepts an intact signed link
// and rejects a link whose application parameters changed.
func ExampleSigner_Verify() {
	signer, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Sign("/download?file=1")

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(signer.Verify(signed))

	// Changing any signed part invalidates the link.
	fmt.Println(signer.Verify("/download?file=2&" + signed[len("/download?file=1&"):]))
	// Output:
	// <nil>
	// link signature is invalid
}

// ExampleSigner_Temporary shows creating and verifying an expiring link, then
// checking the error returned for a nonpositive lifetime.
func ExampleSigner_Temporary() {
	signer, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Temporary("/reset?user=42", 15*time.Minute)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(signer.Verify(signed))

	_, err = signer.Temporary("/reset", 0)
	fmt.Println(errors.Is(err, link.ErrInvalidExpiry))
	// Output:
	// <nil>
	// true
}

// ExampleExpiredError shows how to distinguish an expired link from other
// verification failures and extract the expiration timestamp when present.
func ExampleExpiredError() {
	signer, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Temporary("/reset?user=42", time.Hour)

	if err != nil {
		fmt.Println(err)

		return
	}

	var expired *link.ExpiredError

	switch err := signer.Verify(signed); {
	case errors.As(err, &expired):
		fmt.Printf("link expired at %s, request a new one\n", expired.ExpiresAt.Format(time.RFC3339))
	case err != nil:
		fmt.Println("invalid link")
	default:
		fmt.Println("valid link")
	}
}

// ExampleNew_rotation shows accepting links signed with a previous secret
// while new links use the current secret.
func ExampleNew_rotation() {
	previous, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := previous.Sign("/unsubscribe?user=42")

	if err != nil {
		fmt.Println(err)

		return
	}

	replacement, err := key.NewShared([]byte("fedcba9876543210fedcba9876543210"))

	if err != nil {
		fmt.Println(err)

		return
	}

	rotated, err := link.New(
		replacement,
		func(signer *link.Builder) {
			signer.Previous(secret)
		},
	)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(rotated.Verify(signed))

	// Once the old secret is dropped, its links stop verifying.
	retired, err := link.New(replacement)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(retired.Verify(signed))
	// Output:
	// <nil>
	// link signature is invalid
}

// ExampleNew_ignoreQuery shows excluding tracking parameters from the
// signature while retaining verification of application parameters.
func ExampleNew_ignoreQuery() {
	signer, err := link.New(
		secret,
		func(signer *link.Builder) {
			signer.IgnoreQuery("utm_source")
		},
	)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Sign("/unsubscribe?user=42")

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(signer.Verify(signed + "&utm_source=newsletter"))
	// Output: <nil>
}

// ExampleMiddleware shows applying signature verification before a router
// handler so a valid signed request can proceed.
func ExampleMiddleware() {
	signer, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	routes := router.New()
	routes.With(link.Middleware(signer)).Get("/unsubscribe", func(writer http.ResponseWriter, _ *http.Request) error {
		_, err := fmt.Fprint(writer, "unsubscribed")

		return err
	})

	signed, err := signer.Sign("/unsubscribe?user=42")

	if err != nil {
		fmt.Println(err)

		return
	}

	for _, target := range []string{signed, "/unsubscribe?user=43"} {
		recorder := httptest.NewRecorder()

		routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		fmt.Println(recorder.Code)
	}
	// Output:
	// 200
	// 403
}

// ExampleBind shows binding a link to application state so verification
// requires the same state value.
func ExampleBind() {
	signer, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Sign("/reset?user=42", link.Bind([]byte("password-hash-1")))

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(signer.Verify(signed, link.Bind([]byte("password-hash-1"))))

	// After the password changes, the old link stops verifying.
	fmt.Println(signer.Verify(signed, link.Bind([]byte("password-hash-2"))))
	// Output:
	// <nil>
	// link signature is invalid
}

// testSecret is a valid signing secret.
var testSecret = fixtureSecret([]byte("current secret current secret 32"))

// oldSecret is a valid previous signing secret.
var oldSecret = fixtureSecret([]byte("old secret old secret old secret"))

// fixtureSecret converts deterministic fixture bytes, preserving invalid input
// as an invalid zero value so constructor-boundary rejection is exercised.
func fixtureSecret(raw []byte) key.Shared {
	secret, _ := key.NewShared(raw)

	return secret
}

// testOrigin is the public origin used by absolute signers in tests.
const testOrigin = "https://app.example.com"

// mustNewSigner creates an absolute signer or fails the test.
func mustNewSigner(t *testing.T, opts ...func(*link.Builder)) *link.Signer {
	t.Helper()

	base := []func(*link.Builder){func(signer *link.Builder) {
		signer.Origin(testOrigin)
	}}

	signer, err := link.New(testSecret, append(base, opts...)...)

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return signer
}

// mustNewRelativeSigner creates a relative signer or fails the test.
func mustNewRelativeSigner(t *testing.T) *link.Signer {
	t.Helper()

	signer, err := link.New(testSecret)

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return signer
}

// serve runs request through the Middleware and reports whether the
// protected handler ran along with the recorded response.
func serve(signer *link.Signer, target string) (*http.Response, bool) {
	called := false
	handler := link.Middleware(signer)(func(http.ResponseWriter, *http.Request) error {
		called = true

		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, target, nil))

	return response, called
}

// mustSign signs rawURL or fails the test.
func mustSign(t *testing.T, signer *link.Signer, rawURL string, opts ...link.CallOption) string {
	t.Helper()

	signedURL, err := signer.Sign(rawURL, opts...)

	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	return signedURL
}

// mustSignURL signs value or fails the test.
func mustSignURL(t *testing.T, signer *link.Signer, value *url.URL) *url.URL {
	t.Helper()

	signedURL, err := signer.SignURL(value)

	if err != nil {
		t.Fatalf("SignURL() error = %v", err)
	}

	return signedURL
}

// mustParseURL parses rawURL or fails the test.
func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()

	value, err := url.Parse(rawURL)

	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	return value
}

// mustVerify verifies rawURL or fails the test.
func mustVerify(t *testing.T, signer *link.Signer, rawURL string) {
	t.Helper()

	if err := signer.Verify(rawURL); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

// mustVerifyURL verifies value or fails the test.
func mustVerifyURL(t *testing.T, signer *link.Signer, value *url.URL) {
	t.Helper()

	if err := signer.VerifyURL(value); err != nil {
		t.Fatalf("VerifyURL() error = %v", err)
	}
}

// TestSignerSignsAbsoluteURL verifies absolute URL signing.
func TestSignerSignsAbsoluteURL(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signedURL := mustSign(t, signer, "/unsubscribe/user_123")

	if !strings.HasPrefix(signedURL, testOrigin+"/unsubscribe/user_123?") {
		t.Fatalf("Sign() = %q, want configured absolute origin", signedURL)
	}

	mustVerify(t, signer, signedURL)
}

// TestSignerSignsURLWithoutMutation verifies input URL preservation.
func TestSignerSignsURLWithoutMutation(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	value := mustParseURL(t, "/unsubscribe/user_123?tag=first&tag=second")
	original := value.String()
	signedURL := mustSignURL(t, signer, value)

	if value.String() != original {
		t.Fatalf("SignURL() mutated input to %q, want %q", value.String(), original)
	}

	mustVerifyURL(t, signer, signedURL)
}

// checkNewError fails the test unless link.New rejects secret and opts with an
// error matching want.
func checkNewError(t *testing.T, secret key.Shared, opts []func(*link.Builder), want error) {
	t.Helper()

	if _, err := link.New(secret, opts...); !errors.Is(err, want) {
		t.Fatalf("New() error = %v, want %v", err, want)
	}
}

// TestNewRejectsMissingSecret verifies construction fails for a missing secret.
func TestNewRejectsMissingSecret(t *testing.T) {
	t.Parallel()

	checkNewError(t, key.Shared{}, nil, link.ErrInvalidSecret)
}

// TestNewRejectsShortSecret verifies construction fails for a short secret.
func TestNewRejectsShortSecret(t *testing.T) {
	t.Parallel()

	checkNewError(t, fixtureSecret([]byte("too short")), nil, link.ErrInvalidSecret)
}

// TestNewRejectsShortPreviousSecret verifies construction fails for a short previous secret.
func TestNewRejectsShortPreviousSecret(t *testing.T) {
	t.Parallel()

	checkNewError(t, testSecret, []func(*link.Builder){func(signer *link.Builder) {
		signer.Previous(oldSecret, key.Shared{})
	}}, link.ErrInvalidSecret)
}

// TestNewRejectsOriginWithPath verifies construction fails for an origin with a path.
func TestNewRejectsOriginWithPath(t *testing.T) {
	t.Parallel()

	checkNewError(t, testSecret, []func(*link.Builder){func(signer *link.Builder) {
		signer.Origin(testOrigin + "/path")
	}}, link.ErrInvalidOrigin)
}

// TestNewRejectsOriginWithBadScheme verifies construction fails for an origin with a bad scheme.
func TestNewRejectsOriginWithBadScheme(t *testing.T) {
	t.Parallel()

	checkNewError(t, testSecret, []func(*link.Builder){func(signer *link.Builder) {
		signer.Origin("ftp://app.example.com")
	}}, link.ErrInvalidOrigin)
}

// TestNewRejectsUnparsableOrigin verifies construction fails for an unparsable origin.
func TestNewRejectsUnparsableOrigin(t *testing.T) {
	t.Parallel()

	checkNewError(t, testSecret, []func(*link.Builder){func(signer *link.Builder) {
		signer.Origin("https://%zz")
	}}, link.ErrInvalidOrigin)
}

// TestNewRejectsEmptyIgnoredName verifies construction fails for an empty ignored query name.
func TestNewRejectsEmptyIgnoredName(t *testing.T) {
	t.Parallel()

	checkNewError(t, testSecret, []func(*link.Builder){func(signer *link.Builder) {
		signer.IgnoreQuery("utm", "")
	}}, link.ErrInvalidIgnoreQuery)
}

// TestNewRejectsReservedSignature verifies construction fails for the reserved signature name.
func TestNewRejectsReservedSignature(t *testing.T) {
	t.Parallel()

	checkNewError(t, testSecret, []func(*link.Builder){func(signer *link.Builder) {
		signer.IgnoreQuery("signature")
	}}, link.ErrInvalidIgnoreQuery)
}

// TestNewRejectsReservedExpires verifies construction fails for the reserved expires name.
func TestNewRejectsReservedExpires(t *testing.T) {
	t.Parallel()

	checkNewError(t, testSecret, []func(*link.Builder){func(signer *link.Builder) {
		signer.IgnoreQuery("expires")
	}}, link.ErrInvalidIgnoreQuery)
}

// TestConstructionCallbacksReplaceSettings verifies later origin selection and
// query exclusions are published together without a mutable Config input.
func TestConstructionCallbacksReplaceSettings(t *testing.T) {
	t.Parallel()

	signer, err := link.New(testSecret,
		func(signer *link.Builder) {
			signer.Origin("https://ignored.example.com")
		},
		func(signer *link.Builder) {
			signer.Origin(testOrigin)
		},
		func(signer *link.Builder) {
			signer.IgnoreQuery("utm")
		},
	)

	if err != nil {
		t.Fatal(err)
	}

	signed, err := signer.Sign("/files")

	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(signed, testOrigin+"/") {
		t.Fatalf("signed = %q, want final origin", signed)
	}

	if err := signer.Verify(signed + "&utm=changed"); err != nil {
		t.Fatal(err)
	}
}

// TestNewCopiesInputs verifies the signer does not alias caller slices.
func TestNewCopiesInputs(t *testing.T) {
	t.Parallel()

	secret := []byte("mutable secret mutable secret 32")

	signer, err := link.New(fixtureSecret(secret))

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	signedURL := mustSign(t, signer, "/a")
	clear(secret)

	mustVerify(t, signer, signedURL)
}

// TestSignerRejectsInvalidTarget verifies invalid target rejection.
func TestSignerRejectsInvalidTarget(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	_, err := signer.Sign("https://other.example.com/unsubscribe/user_123")

	if !errors.Is(err, link.ErrInvalidURL) {
		t.Fatalf("Sign() error = %v, want ErrInvalidURL", err)
	}
}

// TestSignerRejectsAlteredPath verifies altered path rejection.
func TestSignerRejectsAlteredPath(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signedURL := mustSign(t, signer, "/unsubscribe/user_123")

	err := signer.Verify(strings.Replace(signedURL, "user_123", "user_456", 1))

	if !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() error = %v, want ErrInvalidSignature", err)
	}
}

// TestSignerRejectsAddedQueryValue verifies added query rejection.
func TestSignerRejectsAddedQueryValue(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signedURL := mustSign(t, signer, "/unsubscribe/user_123")

	err := signer.Verify(signedURL + "&campaign=altered")

	if !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() error = %v, want ErrInvalidSignature", err)
	}
}

// TestSignerRejectsAlteredSignature verifies altered signature rejection.
func TestSignerRejectsAlteredSignature(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signedURL := mustSign(t, signer, "/unsubscribe/user_123")
	parsedURL := mustParseURL(t, signedURL)
	query := parsedURL.Query()
	query.Set("signature", "altered")

	parsedURL.RawQuery = query.Encode()

	err := signer.Verify(parsedURL.String())

	if !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() error = %v, want ErrInvalidSignature", err)
	}
}

// TestSignerDerivesMACKey verifies the MAC key is not the raw secret.
func TestSignerDerivesMACKey(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signedURL := mustSign(t, signer, "/a")
	signature := mustParseURL(t, signedURL).Query().Get("signature")

	canonical := "arkoren.dev/core/http/link/v1\nabsolute\n" + testOrigin + "/a"
	mac := hmac.New(sha256.New, testSecret.Bytes())
	mac.Write([]byte(canonical))

	rawSignature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if signature == rawSignature {
		t.Fatal("signature equals HMAC under the raw secret, want HKDF-derived key")
	}
}

// TestSignerVerifiesPreviousSecret verifies signature rotation support.
func TestSignerVerifiesPreviousSecret(t *testing.T) {
	t.Parallel()

	oldSigner, err := link.New(oldSecret, func(signer *link.Builder) {
		signer.Origin(testOrigin)
	})

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	oldURL := mustSign(t, oldSigner, "/unsubscribe/user_123")

	rotated := mustNewSigner(t, func(signer *link.Builder) {
		signer.Previous(oldSecret)
	})

	mustVerify(t, rotated, oldURL)

	newURL := mustSign(t, rotated, "/unsubscribe/user_123")

	if err := oldSigner.Verify(newURL); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("old signer Verify() error = %v, want ErrInvalidSignature", err)
	}

	if err := mustNewSigner(t).Verify(oldURL); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("signer without previous secret Verify() error = %v, want ErrInvalidSignature", err)
	}
}

// TestTemporaryRejectsNonPositiveTTL verifies ttl validation.
func TestTemporaryRejectsNonPositiveTTL(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := signer.Temporary("/a", ttl); !errors.Is(err, link.ErrInvalidExpiry) {
			t.Fatalf("Temporary(%v) error = %v, want ErrInvalidExpiry", ttl, err)
		}

		if _, err := signer.TemporaryURL(mustParseURL(t, "/a"), ttl); !errors.Is(err, link.ErrInvalidExpiry) {
			t.Fatalf("TemporaryURL(%v) error = %v, want ErrInvalidExpiry", ttl, err)
		}
	}
}

// TestTemporaryLinkExpires verifies validity before expiry and ExpiredError after.
func TestTemporaryLinkExpires(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		signer := mustNewSigner(t)
		start := time.Now()

		signedURL, err := signer.Temporary("/unsubscribe/user_123", time.Minute)

		if err != nil {
			t.Fatalf("Temporary() error = %v", err)
		}

		mustVerify(t, signer, signedURL)

		time.Sleep(time.Minute - time.Second)

		mustVerify(t, signer, signedURL)

		time.Sleep(time.Second)

		err = signer.Verify(signedURL)

		if !errors.Is(err, link.ErrExpired) {
			t.Fatalf("Verify() error = %v, want ErrExpired", err)
		}

		var expired *link.ExpiredError

		if !errors.As(err, &expired) {
			t.Fatalf("Verify() error = %v, want *ExpiredError", err)
		}

		if want := start.Add(time.Minute).Truncate(time.Second); !expired.ExpiresAt.Equal(want) {
			t.Fatalf("ExpiresAt = %v, want %v", expired.ExpiresAt, want)
		}
	})
}

// TestTemporaryURLWithoutMutation verifies TemporaryURL preserves its input.
func TestTemporaryURLWithoutMutation(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	value := mustParseURL(t, "/a?x=1")

	signedURL, err := signer.TemporaryURL(value, time.Hour)

	if err != nil {
		t.Fatalf("TemporaryURL() error = %v", err)
	}

	if value.String() != "/a?x=1" {
		t.Fatalf("TemporaryURL() mutated input to %q", value.String())
	}

	mustVerifyURL(t, signer, signedURL)
}

// TestExpiredLinkWithBadSignatureIsInvalidSignature verifies signature precedes expiry.
func TestExpiredLinkWithBadSignatureIsInvalidSignature(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		signer := mustNewSigner(t)

		signedURL, err := signer.Temporary("/a", time.Second)

		if err != nil {
			t.Fatalf("Temporary() error = %v", err)
		}

		time.Sleep(time.Minute)

		err = signer.Verify(strings.Replace(signedURL, ".com/a", ".com/b", 1))

		if !errors.Is(err, link.ErrInvalidSignature) || errors.Is(err, link.ErrExpired) {
			t.Fatalf("Verify() error = %v, want only ErrInvalidSignature", err)
		}
	})
}

// TestIgnoreQuery verifies ignored parameters can change freely.
func TestIgnoreQuery(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t, func(signer *link.Builder) {
		signer.IgnoreQuery("utm", "ref")
	})

	signedURL := mustSign(t, signer, "/a?id=1&utm=mail")

	if !strings.Contains(signedURL, "utm=mail") {
		t.Fatalf("Sign() = %q, want ignored parameter kept", signedURL)
	}

	mustVerify(t, signer, signedURL)
	mustVerify(t, signer, signedURL+"&ref=home")
	mustVerify(t, signer, strings.Replace(signedURL, "utm=mail", "utm=other", 1))
	mustVerify(t, signer, strings.Replace(signedURL, "&utm=mail", "", 1))
	mustVerify(t, signer, strings.Replace(signedURL, "id=1", "id=1&utm=extra", 1))

	err := signer.Verify(strings.Replace(signedURL, "id=1", "id=2", 1))

	if !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("Verify() with changed id error = %v, want ErrInvalidSignature", err)
	}
}

// TestRelativeSignerSignsRootRelativeURL verifies relative URL signing.
func TestRelativeSignerSignsRootRelativeURL(t *testing.T) {
	t.Parallel()

	signer := mustNewRelativeSigner(t)
	signedURL := mustSign(t, signer, "/unsubscribe/user_123")
	parsedURL := mustParseURL(t, signedURL)

	if parsedURL.Scheme != "" || parsedURL.Host != "" {
		t.Fatalf("Sign() = %q, want relative URL", signedURL)
	}

	mustVerify(t, signer, signedURL)
}

// TestRelativeSignerRejectsInvalidTargets verifies absolute and protocol-relative rejection.
func TestRelativeSignerRejectsInvalidTargets(t *testing.T) {
	t.Parallel()

	signer := mustNewRelativeSigner(t)

	for _, target := range []string{
		testOrigin + "/unsubscribe/user_123",
		"//app.example.com/unsubscribe/user_123",
	} {
		if _, err := signer.Sign(target); !errors.Is(err, link.ErrInvalidURL) {
			t.Fatalf("Sign(%q) error = %v, want ErrInvalidURL", target, err)
		}
	}
}

// TestSignedMiddlewarePermitsValidRequest verifies valid request acceptance.
func TestSignedMiddlewarePermitsValidRequest(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	parsedURL := mustParseURL(t, mustSign(t, signer, "/unsubscribe/user_123"))

	called := false
	handler := link.Middleware(signer)(func(http.ResponseWriter, *http.Request) error {
		called = true

		return nil
	})

	request := httptest.NewRequest(http.MethodGet, parsedURL.RequestURI(), nil)
	request.Header.Set("Forwarded", "host=attacker.example.com;proto=http")
	request.Header.Set("X-Forwarded-Host", "attacker.example.com")

	response := router.Handler(handler).Record(request)
	t.Cleanup(func() {
		response.Body.Close()
	})

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	if !called {
		t.Fatal("protected handler was not called")
	}
}

// TestSignedMiddlewareRejectsModifiedRequest verifies the generic rejection response.
func TestSignedMiddlewareRejectsModifiedRequest(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)
	signedURL := mustSign(t, signer, "/unsubscribe/user_123")

	response, called := serve(signer, strings.Replace(signedURL, "user_123", "user_456", 1))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if called {
		t.Fatal("protected handler was called")
	}

	assertProblem(t, response, link.ErrLinkRejected)
}

// TestSignedMiddlewareRejectsExpiredRequest verifies the expired response.
func TestSignedMiddlewareRejectsExpiredRequest(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		signer := mustNewSigner(t)

		signedURL, err := signer.Temporary("/unsubscribe/user_123", time.Minute)

		if err != nil {
			t.Fatalf("Temporary() error = %v", err)
		}

		time.Sleep(time.Minute)

		response, called := serve(signer, signedURL)
		defer response.Body.Close()

		if called {
			t.Fatal("protected handler was called")
		}

		assertProblem(t, response, link.ErrLinkExpired)
	})
}

// assertProblem verifies response carries the status and title of want.
func assertProblem(t *testing.T, response *http.Response, want problem.Details) {
	t.Helper()

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if !strings.Contains(string(body), want.Title()) {
		t.Fatalf("body = %s, want title %q", body, want.Title())
	}
}

// TestVerifyIgnoresDefaultPort verifies default ports are not significant.
func TestVerifyIgnoresDefaultPort(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	signed, err := signer.Sign(testOrigin + ":443/download")

	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	mustVerify(t, signer, signed)
}

// TestSignMalformedQueryIsInvalidURL verifies malformed query rejection.
func TestSignMalformedQueryIsInvalidURL(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	if _, err := signer.Sign("/download?a=%zz"); !errors.Is(err, link.ErrInvalidURL) {
		t.Fatalf("error = %v, want ErrInvalidURL", err)
	}
}

// TestVerifyMalformedQueryIsInvalidSignature verifies malformed verified query rejection.
func TestVerifyMalformedQueryIsInvalidSignature(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	if err := signer.Verify(testOrigin + "/download?a=%zz"); !errors.Is(err, link.ErrInvalidSignature) {
		t.Fatalf("error = %v, want ErrInvalidSignature", err)
	}
}
