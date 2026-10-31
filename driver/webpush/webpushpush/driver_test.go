package webpushpush

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"arkoren.dev/core/service/push"
	"arkoren.dev/core/service/push/pushtest"
	"arkoren.dev/foundation/key"
)

// TestNewRejectsInvalidContacts ensures construction-only VAPID subjects
// accept no raw mailbox shorthand, embedded credentials or header injection.
func TestNewRejectsInvalidContacts(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	for _, contact := range []string{"", "push@example.com", "mailto:", "mailto:Name <a@example.com>",
		"http://example.com", "https://user:secret@example.com", "https://example.com/#secret", "mailto:a\r\nb@example.com"} {
		if _, err := newFixtureDriver(key, contact); err == nil {
			t.Fatal("accepted malformed contact")
		}
	}
}

// TestNewRejectsInvalidSettings verifies invalid timeout and origin syntax
// fail without exposing the configured secret path in error text.
func TestNewRejectsInvalidSettings(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	_, err = newFixtureDriver(key, "mailto:push@example.com", func(connection *Builder) {
		connection.Timeout(-time.Second)
	})

	if err == nil {
		t.Fatal("accepted negative timeout")
	}

	for _, origin := range []string{"https://fcm.googleapis.com/secret", "https://*.push.apple.com",
		"https://127.0.0.1", "https://fcm.googleapis.com?secret", "https://fcm.googleapis.com/"} {
		_, err := newFixtureDriver(key, "mailto:push@example.com", func(connection *Builder) {
			connection.AllowedOrigins(origin)
		})

		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("accepted invalid origin or exposed it")
		}
	}
}

// TestSendTTLOverflow rejects durations outside the portable integer range
// before the encrypted request can reach the provider boundary.
func TestSendTTLOverflow(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)

	_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{TTL: 1 << 62})

	if !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatal(err)
	}
}

// doFunc substitutes only the private provider I/O boundary. It cannot be
// installed through the public construction API.
type doFunc func(*http.Request) (*http.Response, error)

// newFixtureDriver validates native fixtures through the shared key boundary.
func newFixtureDriver(private *ecdsa.PrivateKey, contact string, configure ...BuilderFunc) (*Driver, error) {
	identity, err := key.NewSigning(private)

	if err != nil {
		return nil, err
	}

	return New(identity, contact, configure...)
}

// TestNewRejectsZeroIdentity checks the public constructor's zero-value guard.
func TestNewRejectsZeroIdentity(t *testing.T) {
	t.Parallel()

	if _, err := New(key.Signing{}, "mailto:push@example.com"); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("New(zero) = %v", err)
	}
}

// Do forwards a prepared, encrypted provider request to its test script.
func (do doFunc) Do(request *http.Request) (*http.Response, error) {
	return do(request)
}

// fixture constructs valid independent application and browser keys. The
// returned browser private key permits decryption outside the sending library.
func fixture(t *testing.T) (*Driver, push.Subscription, *ecdh.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	driver, err := newFixtureDriver(key, "mailto:push@example.com")

	if err != nil {
		t.Fatal(err)
	}

	browser, err := ecdh.P256().GenerateKey(rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	subscription := push.Subscription{
		Endpoint: "https://fcm.googleapis.com/push/secret%2Fpath?token=secret%2Bquery",
		Keys: push.SubscriptionKeys{
			P256DH: base64.RawURLEncoding.EncodeToString(browser.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")),
		},
	}

	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 201, Header: make(http.Header), Body: http.NoBody}, nil
	})

	return driver, subscription, browser
}

// decrypt independently implements the receiver-side RFC 8291 derivation
// with standard-library HKDF and ECDH; no sending-library helpers are used.
func decrypt(t *testing.T, request *http.Request, browser *ecdh.PrivateKey) []byte {
	t.Helper()

	record, err := io.ReadAll(request.Body)

	if err != nil {
		t.Fatal(err)
	}

	if len(record) != 4096 || binary.BigEndian.Uint32(record[16:20]) != 4096 {
		t.Fatalf("invalid encrypted record length/header: %d", len(record))
	}

	mode, nonce := receiverCipher(t, record, browser)
	plaintext, err := mode.Open(nil, nonce, record[86:], nil)

	if err != nil {
		t.Fatal(err)
	}

	plaintext = bytes.TrimRight(plaintext, "\x00")

	if plaintext[len(plaintext)-1] != 2 {
		t.Fatal("missing final-record delimiter")
	}

	return plaintext[:len(plaintext)-1]
}

// receiverCipher derives receiver-side keying material independently of the
// sender library and returns the AEAD and nonce used by the single record.
func receiverCipher(t *testing.T, record []byte, browser *ecdh.PrivateKey) (cipher.AEAD, []byte) {
	t.Helper()

	peer, err := ecdh.P256().NewPublicKey(record[21:86])

	if err != nil {
		t.Fatal(err)
	}

	shared, err := browser.ECDH(peer)

	if err != nil {
		t.Fatal(err)
	}

	info := append([]byte("WebPush: info\x00"), browser.PublicKey().Bytes()...)
	info = append(info, peer.Bytes()...)
	material, err := hkdf.Key(sha256.New, shared, []byte("0123456789abcdef"), string(info), 32)

	if err != nil {
		t.Fatal(err)
	}

	key, err := hkdf.Key(sha256.New, material, record[:16], "Content-Encoding: aes128gcm\x00", 16)

	if err != nil {
		t.Fatal(err)
	}

	nonce, err := hkdf.Key(sha256.New, material, record[:16], "Content-Encoding: nonce\x00", 12)

	if err != nil {
		t.Fatal(err)
	}

	block, err := aes.NewCipher(key)

	if err != nil {
		t.Fatal(err)
	}

	mode, err := cipher.NewGCM(block)

	if err != nil {
		t.Fatal(err)
	}

	return mode, nonce
}

// TestDriver runs the portable suite against a deterministic private HTTP seam.
func TestDriver(t *testing.T) {
	t.Parallel()

	pushtest.Run(t, func(t *testing.T) (push.Driver, push.Recipient) {
		driver, subscription, _ := fixture(t)

		return driver, subscription
	})
}

// TestSendWire verifies encrypted plaintext, independent JWT verification,
// origin audience, opaque URL bytes and fractional TTL truncation together.
func TestSendWire(t *testing.T) {
	t.Parallel()

	driver, subscription, browser := fixture(t)
	driver.client = doFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != subscription.Endpoint || request.Header.Get("TTL") != "1" {
			t.Fatalf("endpoint or TTL changed: %s", request.Header.Get("TTL"))
		}

		if request.Header.Get("Content-Encoding") != "aes128gcm" || request.GetBody != nil {
			t.Fatal("encryption or replay policy incorrect")
		}

		if got := string(decrypt(t, request, browser)); got != `{"title":"Ready","body":"Collect now","data":{"order":"42"}}` {
			t.Fatalf("plaintext = %s", got)
		}

		verifyAuthorization(t, request, driver.public)

		return &http.Response{StatusCode: 201, Header: http.Header{"Location": {"https://secret.invalid/receipt"}}, Body: http.NoBody}, nil
	})

	message := push.NewMessage(subscription, "Ready", func(notification *push.MessageBuilder) {
		notification.Body("Collect now").Data(map[string]string{"order": "42"})
	})

	receipt, err := driver.Send(t.Context(), message, push.SendConfig{TTL: 1900 * time.Millisecond})

	if err != nil || receipt.ID != "" {
		t.Fatalf("Send = %#v, %v", receipt, err)
	}
}

// verifyAuthorization verifies the library's signature and registered claims
// using only the public application key and an independent JWT parser.
func verifyAuthorization(t *testing.T, request *http.Request, public string) {
	t.Helper()

	header := request.Header.Get("Authorization")
	token, suffix, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")

	if !ok || suffix != public || !strings.HasPrefix(header, "vapid t=") {
		t.Fatal("not an RFC 8292 vapid authorization")
	}

	encoded, err := base64.RawURLEncoding.DecodeString(public)

	if err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), encoded)

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) {
		return key, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("https://fcm.googleapis.com"))

	if err != nil {
		t.Fatal(err)
	}

	claims := parsed.Claims.(jwt.MapClaims)

	if claims["sub"] != "mailto:push@example.com" {
		t.Fatal("incorrect contact claim")
	}

	expiration, err := claims.GetExpirationTime()

	if err != nil || expiration.After(time.Now().Add(24*time.Hour)) {
		t.Fatal("invalid expiration")
	}
}

// TestSendNormalizesHTTPSContact exercises case-insensitive URI schemes at
// the signing boundary. The native library recognizes only lowercase https:
// and would otherwise turn a valid HTTPS contact into a mailto subject.
func TestSendNormalizesHTTPSContact(t *testing.T) {
	t.Parallel()

	_, subscription, _ := fixture(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	normalized, err := newFixtureDriver(key, "HTTPS://example.com/contact")

	if err != nil {
		t.Fatal(err)
	}

	normalized.client = doFunc(func(request *http.Request) (*http.Response, error) {
		header := request.Header.Get("Authorization")
		token, _, _ := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
		parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) {
			return &key.PublicKey, nil
		}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("https://fcm.googleapis.com"))

		if err != nil {
			t.Fatal(err)
		}

		if parsed.Claims.(jwt.MapClaims)["sub"] != "https://example.com/contact" {
			t.Fatal("HTTPS contact was changed into a mailto subject")
		}

		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	})

	if _, err := normalized.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{}); err != nil {
		t.Fatal(err)
	}
}

// TestSendNoStore checks a subsecond positive retention never rounds upward,
// and omitted optional fields remain absent from the flat wire object.
func TestSendNoStore(t *testing.T) {
	t.Parallel()

	driver, subscription, browser := fixture(t)
	driver.client = doFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("TTL") != "0" || string(decrypt(t, request, browser)) != `{"title":"Ready"}` {
			t.Fatal("no-store wire representation is incorrect")
		}

		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	})

	for _, retention := range []time.Duration{0, time.Millisecond} {
		_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{TTL: retention})

		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestSendStandardPortAudience ensures an explicit :443 does not leak into
// RFC 8292's serialized-origin audience or change opaque path/query bytes.
func TestSendStandardPortAudience(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	subscription.Endpoint = "https://fcm.googleapis.com:443/push/secret%2Fpath?token=secret%2Bquery"
	driver.client = doFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.RequestURI() != "/push/secret%2Fpath?token=secret%2Bquery" {
			t.Fatal("opaque endpoint bytes changed")
		}

		verifyAuthorization(t, request, driver.public)

		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	})

	if _, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{}); err != nil {
		t.Fatal(err)
	}
}

// TestSendInvalidSubscriptionKeys rejects malformed browser input before
// the encryption library or transport can process it.
func TestSendInvalidSubscriptionKeys(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid subscription reached I/O")

		return nil, nil
	})

	subscription.Keys.Auth = "secret-invalid-key"

	_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{})

	if !errors.Is(err, push.ErrInvalidMessage) || strings.Contains(err.Error(), "secret-invalid-key") {
		t.Fatal("invalid key accepted or exposed")
	}
}

// TestAuthorizationCapacityFailsBeforeDispatch bounds an attacker-controlled
// Apple origin fanout without evicting live credentials and refreshing early.
func TestAuthorizationCapacityFailsBeforeDispatch(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)

	for index := range 256 {
		driver.authorizations[string(rune(index))] = authorization{header: "cached", expiresAt: time.Now().Add(time.Hour)}
	}

	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("capacity failure performed I/O")

		return nil, nil
	})

	_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{})

	if !errors.Is(err, push.ErrUnavailable) || errors.Is(err, push.ErrOutcomeUnknown) {
		t.Fatalf("capacity failure class = %v", err)
	}
}

// TestAuthorizationRefreshAndExpiredEviction verifies refresh depends on the
// signed expiration, not a sliding TTL, and expired origins release capacity.
func TestAuthorizationRefreshAndExpiredEviction(t *testing.T) {
	t.Parallel()

	driver, _, _ := fixture(t)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://fcm.googleapis.com/path", nil)

	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Authorization", "fresh-library-token")
	driver.authorizations["https://fcm.googleapis.com"] = authorization{header: "stale", expiresAt: time.Now().Add(time.Minute)}
	driver.authorizations["https://expired.example"] = authorization{header: "expired", expiresAt: time.Unix(0, 0)}

	header, err := driver.authorization(request, time.Now().Add(12*time.Hour))

	if err != nil || header != "fresh-library-token" {
		t.Fatal("near-expiration credential not refreshed")
	}

	if _, ok := driver.authorizations["https://expired.example"]; ok {
		t.Fatal("expired origin retained")
	}
}

// TestSendEncodedBoundary exercises the exact encoded plaintext budget and
// the immediately larger message, not just raw field lengths.
func TestSendEncodedBoundary(t *testing.T) {
	t.Parallel()

	driver, subscription, browser := fixture(t)
	calls := 0
	driver.client = doFunc(func(request *http.Request) (*http.Response, error) {
		calls++

		if len(decrypt(t, request, browser)) != 3993 {
			t.Fatal("wrong encoded boundary")
		}

		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	})

	message := push.NewMessage(subscription, strings.Repeat("a", 3981))

	if _, err := driver.Send(t.Context(), message, push.SendConfig{}); err != nil {
		t.Fatal(err)
	}

	message = push.NewMessage(subscription, strings.Repeat("a", 3982))

	if _, err := driver.Send(t.Context(), message, push.SendConfig{}); !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("oversized error = %v", err)
	}

	if calls != 1 {
		t.Fatalf("dispatches = %d", calls)
	}
}

// TestSendEscapedBoundary proves JSON escaping, not input byte count, sets
// the limit; native token recipients are independently unsupported.
func TestSendEscapedBoundary(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	message := push.NewMessage(subscription, strings.Repeat("\"", 2000))

	if _, err := driver.Send(t.Context(), message, push.SendConfig{}); !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("escaped oversize error = %v", err)
	}
}

// TestSendUnsupportedToken checks that native registrations cannot be
// accidentally routed to a browser endpoint.
func TestSendUnsupportedToken(t *testing.T) {
	t.Parallel()

	driver, _, _ := fixture(t)

	_, err := driver.Send(t.Context(), push.NewMessage(push.Token("native-secret"), "Ready"), push.SendConfig{})

	if !errors.Is(err, push.ErrUnsupported) {
		t.Fatal(err)
	}
}

// TestSendTransportError preserves native secret-bearing causes without
// rendering them and never hides uncertain acceptance behind an ordinary error.
func TestSendTransportError(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	calls := 0
	native := &url.Error{Op: "Post", URL: subscription.Endpoint, Err: context.Canceled}
	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		calls++

		return nil, native
	})

	receipt, err := driver.Send(t.Context(), push.NewMessage(subscription, "secret-title"), push.SendConfig{})

	if !errors.Is(err, push.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error identity lost: %v", err)
	}

	var found *url.Error

	if !errors.As(err, &found) || found != native {
		t.Fatal("native cause lost")
	}

	if strings.Contains(err.Error(), "secret") || receipt != (push.Receipt{}) || calls != 1 {
		t.Fatal("secret leaked, receipt returned, or request repeated")
	}
}

// TestSendCanceledPreflight forbids even entry into the private HTTP seam
// after a context has already been canceled.
func TestSendCanceledPreflight(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled send performed I/O")

		return nil, nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.Send(ctx, push.NewMessage(subscription, "Ready"), push.SendConfig{})

	if !errors.Is(err, context.Canceled) || errors.Is(err, push.ErrOutcomeUnknown) {
		t.Fatal("preflight cancellation misclassified")
	}
}

// TestSendCancellationAfterDispatch preserves a real canceled request context
// while conservatively refusing to assert whether the provider accepted it.
func TestSendCancellationAfterDispatch(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver.client = doFunc(func(request *http.Request) (*http.Response, error) {
		cancel()

		return nil, request.Context().Err()
	})

	_, err := driver.Send(ctx, push.NewMessage(subscription, "Ready"), push.SendConfig{})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, push.ErrOutcomeUnknown) {
		t.Fatal("dispatch cancellation identity or uncertainty lost")
	}
}

// TestSendFiniteTimeout exercises the operation deadline without wall-clock
// waiting. The private provider blocks until the bounded send context expires.
func TestSendFiniteTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, subscription, _ := fixture(t)
		driver.client = doFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()

			return nil, request.Context().Err()
		})

		_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{})

		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, push.ErrOutcomeUnknown) {
			t.Fatal("finite operation deadline missing or misclassified")
		}
	})
}

// TestSendRejectsProtocolUpgradeWithoutReading exercises net/http's actual
// upgraded-connection body. HTTP 101 transfers socket ownership to the caller,
// so neither client timeouts nor context cancellation can bound a body read.
// A hostile upgrade must be rejected and closed without waiting for payload.
func TestSendRejectsProtocolUpgradeWithoutReading(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, subscription, _ := fixture(t)
		client, server := net.Pipe()
		var workers sync.WaitGroup
		t.Cleanup(func() {
			client.Close()
			server.Close()
			workers.Wait()
		})

		provider := newClient(time.Second)
		provider.Transport.(*http.Transport).DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		}
		driver.client = provider

		workers.Go(func() {
			request, err := http.ReadRequest(bufio.NewReader(server))

			if err != nil {
				t.Error(err)

				return
			}

			_, _ = io.Copy(io.Discard, request.Body)
			request.Body.Close()
			_, _ = io.WriteString(server, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: unexpected\r\n\r\n")
		})

		result := make(chan error, 1)
		workers.Go(func() {
			_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{})
			result <- err
		})

		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()

		select {
		case err := <-result:
			var rejected *ProviderError

			if !errors.As(err, &rejected) || rejected.StatusCode != http.StatusSwitchingProtocols {
				t.Fatal("protocol upgrade was not rejected")
			}
		default:
			t.Error("protocol upgrade bypassed the response timeout")
		}
	})
}

// TestSendReusesAuthorization checks concurrent sends publish exactly one
// wire token even though the library produces independent signatures.
func TestSendReusesAuthorization(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	var mutex sync.Mutex
	var first string
	driver.client = doFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		defer mutex.Unlock()

		if first == "" {
			first = request.Header.Get("Authorization")
		}

		if first != request.Header.Get("Authorization") {
			t.Error("authorization refreshed per send")
		}

		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	})

	var workers sync.WaitGroup

	for range 8 {
		workers.Go(func() {
			if _, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{}); err != nil {
				t.Error(err)
			}
		})
	}

	workers.Wait()
}

// TestNewSnapshotsKey verifies caller mutations cannot invalidate signing
// material after successful construction.
func TestNewSnapshotsKey(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	driver, err := newFixtureDriver(key, "mailto:push@example.com")

	if err != nil {
		t.Fatal(err)
	}

	private, public := driver.private, driver.public
	*key = ecdsa.PrivateKey{}

	if driver.private != private || driver.public != public {
		t.Fatal("driver retained mutable caller key")
	}
}

// TestNewRejectsMalformedKeys covers missing coordinates, wrong curve,
// inconsistent pairs and absent scalars without panicking.
func TestNewRejectsMalformedKeys(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	key.PublicKey = other.PublicKey

	for _, key := range []*ecdsa.PrivateKey{
		nil, {}, {PublicKey: ecdsa.PublicKey{Curve: elliptic.P384()}},
		{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256()}},
		{PublicKey: other.PublicKey}, key,
	} {
		if _, err := newFixtureDriver(key, "mailto:push@example.com"); err == nil {
			t.Fatal("accepted malformed key")
		}
	}
}
