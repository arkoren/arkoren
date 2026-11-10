package totp_test

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"arkoren.dev/foundation/totp"
)

// RFC 6238 appendix B seeds: the ASCII digits repeated to each hash's block-sized key.
var (
	seedSHA1   = []byte("12345678901234567890")
	seedSHA256 = []byte("12345678901234567890123456789012")
	seedSHA512 = []byte("1234567890123456789012345678901234567890123456789012345678901234")
)

var vectorTimes = []int64{59, 1111111109, 1111111111, 1234567890, 2000000000, 20000000000}

func rfcScheme(algorithm totp.Algorithm) totp.Scheme {
	return totp.New(func(scheme *totp.Builder) {
		scheme.Algorithm(algorithm).Digits(8)
	})
}

func checkVectors(t *testing.T, algorithm totp.Algorithm, seed []byte, want []string) {
	t.Helper()
	scheme := rfcScheme(algorithm)
	for index, seconds := range vectorTimes {
		got, err := scheme.Generate(seed, time.Unix(seconds, 0))
		if err != nil {
			t.Fatal(err)
		}
		if got != want[index] {
			t.Fatalf("%s at %d = %s, want %s", algorithm, seconds, got, want[index])
		}
	}
}

func TestGenerateRFC6238SHA1(t *testing.T) {
	t.Parallel()
	checkVectors(t, totp.SHA1, seedSHA1, []string{"94287082", "07081804", "14050471", "89005924", "69279037", "65353130"})
}

func TestGenerateRFC6238SHA256(t *testing.T) {
	t.Parallel()
	checkVectors(t, totp.SHA256, seedSHA256, []string{"46119246", "68084774", "67062674", "91819424", "90698825", "77737706"})
}

func TestGenerateRFC6238SHA512(t *testing.T) {
	t.Parallel()
	checkVectors(t, totp.SHA512, seedSHA512, []string{"90693936", "25091201", "99943326", "93441116", "38618901", "47863826"})
}

func TestGenerateDefaultsToSixDigits(t *testing.T) {
	t.Parallel()
	got, err := totp.New().Generate(seedSHA1, time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got != "287082" {
		t.Fatalf("code = %s, want 287082", got)
	}
}

func TestVerifyReturnsMatchedStep(t *testing.T) {
	t.Parallel()
	step, err := totp.New().Verify(seedSHA1, "287082", time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if step != 1 {
		t.Fatalf("step = %d, want 1", step)
	}
}

func TestVerifyAcceptsNeighboringSteps(t *testing.T) {
	t.Parallel()
	scheme := totp.New()
	now := time.Unix(1111111111, 0)
	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := scheme.Generate(seedSHA1, now.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scheme.Verify(seedSHA1, code, now); err != nil {
			t.Fatalf("offset %s: %v", offset, err)
		}
	}
}

func TestVerifyRejectsOutsideWindow(t *testing.T) {
	t.Parallel()
	scheme := totp.New()
	now := time.Unix(1111111111, 0)
	for _, offset := range []time.Duration{-60 * time.Second, 60 * time.Second} {
		code, err := scheme.Generate(seedSHA1, now.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scheme.Verify(seedSHA1, code, now); !errors.Is(err, totp.ErrInvalidCode) {
			t.Fatalf("offset %s: err = %v, want ErrInvalidCode", offset, err)
		}
	}
}

func TestVerifyWindowZeroAcceptsOnlyCurrentStep(t *testing.T) {
	t.Parallel()
	scheme := totp.New(func(scheme *totp.Builder) { scheme.Window(0) })
	now := time.Unix(1111111111, 0)
	previous, err := scheme.Generate(seedSHA1, now.Add(-30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheme.Verify(seedSHA1, previous, now); !errors.Is(err, totp.ErrInvalidCode) {
		t.Fatalf("err = %v, want ErrInvalidCode", err)
	}
}

func TestVerifyRejectsMalformedCodes(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"", "28708", "2870822", "28708a", " 287082"} {
		if _, err := totp.New().Verify(seedSHA1, code, time.Unix(59, 0)); !errors.Is(err, totp.ErrInvalidCode) {
			t.Fatalf("code %q: err = %v, want ErrInvalidCode", code, err)
		}
	}
}

func TestVerifyNearEpochSkipsNegativeSteps(t *testing.T) {
	t.Parallel()
	code, err := totp.New().Generate(seedSHA1, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	step, err := totp.New().Verify(seedSHA1, code, time.Unix(10, 0))
	if err != nil || step != 0 {
		t.Fatalf("step, err = %d, %v", step, err)
	}
}

func TestTimeBeforeEpochIsRejected(t *testing.T) {
	t.Parallel()
	if _, err := totp.New().Generate(seedSHA1, time.Unix(-1, 0)); !errors.Is(err, totp.ErrInvalidTime) {
		t.Fatalf("err = %v, want ErrInvalidTime", err)
	}
}

func TestEmptySecretIsRejected(t *testing.T) {
	t.Parallel()
	if _, err := totp.New().Generate(nil, time.Unix(59, 0)); !errors.Is(err, totp.ErrInvalidSecret) {
		t.Fatalf("generate err = %v", err)
	}
	if _, err := totp.New().Verify(nil, "287082", time.Unix(59, 0)); !errors.Is(err, totp.ErrInvalidSecret) {
		t.Fatalf("verify err = %v", err)
	}
}

func TestInvalidSettingsAreReported(t *testing.T) {
	t.Parallel()
	settings := []func(*totp.Builder){
		func(scheme *totp.Builder) { scheme.Algorithm("MD5") },
		func(scheme *totp.Builder) { scheme.Digits(5) },
		func(scheme *totp.Builder) { scheme.Digits(9) },
		func(scheme *totp.Builder) { scheme.Period(1500 * time.Millisecond) },
		func(scheme *totp.Builder) { scheme.Period(0) },
		func(scheme *totp.Builder) { scheme.Window(2) },
		func(scheme *totp.Builder) { scheme.Window(-1) },
	}
	for index, setting := range settings {
		scheme := totp.New(setting)
		if !errors.Is(scheme.Err(), totp.ErrInvalidScheme) {
			t.Fatalf("setting %d: Err = %v", index, scheme.Err())
		}
		if _, err := scheme.Generate(seedSHA1, time.Unix(59, 0)); !errors.Is(err, totp.ErrInvalidScheme) {
			t.Fatalf("setting %d: Generate err = %v", index, err)
		}
	}
}

func TestFirstInvalidSettingWins(t *testing.T) {
	t.Parallel()
	scheme := totp.New(func(scheme *totp.Builder) {
		scheme.Digits(3).Window(5).Digits(6)
	})
	if err := scheme.Err(); err == nil || !errors.Is(err, totp.ErrInvalidScheme) {
		t.Fatalf("Err = %v", err)
	}
}

func TestZeroSchemeIsInvalid(t *testing.T) {
	t.Parallel()
	var scheme totp.Scheme
	if _, err := scheme.Generate(seedSHA1, time.Unix(59, 0)); !errors.Is(err, totp.ErrInvalidScheme) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewSecretMatchesHashSize(t *testing.T) {
	t.Parallel()
	sizes := map[totp.Algorithm]int{totp.SHA1: 20, totp.SHA256: 32, totp.SHA512: 64}
	for algorithm, size := range sizes {
		secret, err := rfcScheme(algorithm).NewSecret()
		if err != nil {
			t.Fatal(err)
		}
		if len(secret) != size {
			t.Fatalf("%s secret length = %d, want %d", algorithm, len(secret), size)
		}
	}
}

func TestSecretEncodingRoundTrips(t *testing.T) {
	t.Parallel()
	text := totp.EncodeSecret(seedSHA1)
	if text != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatalf("encoded = %s", text)
	}
	decoded, err := totp.DecodeSecret("gezd gnbv-GY3TQOJQ GEZDGNBVGY3TQOJQ==")
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(seedSHA1) {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestDecodeSecretRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "   ", "not base32!", "1890"} {
		if _, err := totp.DecodeSecret(text); !errors.Is(err, totp.ErrInvalidSecret) {
			t.Fatalf("%q: err = %v", text, err)
		}
	}
}

func TestURIWritesParameters(t *testing.T) {
	t.Parallel()
	scheme := totp.New(func(scheme *totp.Builder) {
		scheme.Algorithm(totp.SHA256).Digits(8).Period(60 * time.Second)
	})
	raw, err := scheme.URI("Example Co", "alice@example.com", seedSHA1)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" || parsed.Path != "/Example Co:alice@example.com" {
		t.Fatalf("uri = %s", raw)
	}
	want := map[string]string{
		"secret": totp.EncodeSecret(seedSHA1), "issuer": "Example Co",
		"algorithm": "SHA256", "digits": "8", "period": "60",
	}
	for key, value := range want {
		if query.Get(key) != value {
			t.Fatalf("%s = %q, want %q (uri %s)", key, query.Get(key), value, raw)
		}
	}
}

func TestURIRejectsBadLabels(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{{"", "alice"}, {"Example", ""}, {"Ex:ample", "alice"}, {"Example", "a:lice"}} {
		if _, err := totp.New().URI(pair[0], pair[1], seedSHA1); !errors.Is(err, totp.ErrInvalidSecret) {
			t.Fatalf("%v: err = %v", pair, err)
		}
	}
}
