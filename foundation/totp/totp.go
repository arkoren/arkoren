package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Algorithm names the HMAC hash function used to derive codes. The values are
// the spellings used by the otpauth URI "algorithm" parameter.
type Algorithm string

// Supported algorithms. Authenticator apps almost universally support only
// [SHA1]; choose another only when every client you target is known to honor
// the URI algorithm parameter.
const (
	// SHA1 is HMAC-SHA1, the RFC 6238 default and the only algorithm widely
	// supported by authenticator apps.
	SHA1 Algorithm = "SHA1"
	// SHA256 is HMAC-SHA256.
	SHA256 Algorithm = "SHA256"
	// SHA512 is HMAC-SHA512.
	SHA512 Algorithm = "SHA512"
)

// Defaults applied by [New] before configuration callbacks run.
const (
	// DefaultDigits is the code length used when [Builder.Digits] is not called.
	DefaultDigits = 6
	// DefaultPeriod is the time-step length used when [Builder.Period] is not called.
	DefaultPeriod = 30 * time.Second
	// DefaultWindow is the number of neighboring steps accepted on each side.
	DefaultWindow = 1
	// MaxWindow is the largest accepted verification window, in steps per side.
	MaxWindow = 1
)

const (
	minDigits = 6
	maxDigits = 8
)

var (
	// ErrInvalidCode reports that a code did not match any step in the
	// verification window, including codes of the wrong length or with
	// non-digit characters. Test for it with [errors.Is].
	ErrInvalidCode = errors.New("totp: invalid code")
	// ErrInvalidScheme reports unusable parameters: an unknown algorithm,
	// digits outside 6 to 8, a period that is not a positive whole number of
	// seconds, a window outside 0 to [MaxWindow], or an issuer that is empty or
	// contains a colon. Methods wrap it with the first offending setting.
	ErrInvalidScheme = errors.New("totp: invalid scheme")
	// ErrInvalidSecret reports an empty secret, an undecodable base32 string,
	// or an account name that cannot be placed in an otpauth URI.
	ErrInvalidSecret = errors.New("totp: invalid secret")
	// ErrInvalidTime reports a time before the Unix epoch, which has no
	// time step.
	ErrInvalidTime = errors.New("totp: time before Unix epoch")
)

// Scheme is an immutable set of TOTP parameters. Build it with [New]; the zero
// value is invalid and every method returns an error wrapping
// [ErrInvalidScheme]. A Scheme holds no secret and is safe for concurrent use.
type Scheme struct {
	issuer    string
	algorithm Algorithm
	digits    int
	period    time.Duration
	window    int
	err       error
}

// Builder is the draft passed to [New] callbacks. Each setting method returns
// the same draft. The first invalid value is kept and reported by
// [Scheme.Err]; later settings cannot clear it.
type Builder struct {
	scheme Scheme
}

// BuilderFunc configures a [Builder] during [New].
type BuilderFunc = func(*Builder)

// New builds a [Scheme] for issuer from the defaults (SHA1, 6 digits, 30
// seconds, a window of one step each side) and then runs each callback once, in
// order. A nil callback panics.
//
// The issuer is the service name authenticator apps show beside the code and
// is written to every provisioning URI. It has no default, so it is a required
// argument: it must be non-empty and contain no colon, which separates issuer
// and account in the URI label. Invalid settings, including the issuer, do not
// panic: the first one surfaces from [Scheme.Err] and from every method. The
// issuer is checked before any callback runs, so a bad issuer is reported first.
//
// Example:
//
//	scheme := totp.New("Acme", func(scheme *totp.Builder) {
//		scheme.Algorithm(totp.SHA256).Digits(8).Window(0)
//	})
//	if err := scheme.Err(); err != nil {
//		return err
//	}
func New(issuer string, configure ...BuilderFunc) Scheme {
	draft := &Builder{scheme: Scheme{
		issuer:    issuer,
		algorithm: SHA1,
		digits:    DefaultDigits,
		period:    DefaultPeriod,
		window:    DefaultWindow,
	}}
	if issuer == "" || strings.Contains(issuer, ":") {
		draft.fail("issuer %q must be non-empty and contain no colon", issuer)
	}
	for _, apply := range configure {
		apply(draft)
	}
	return draft.scheme
}

// fail records the first invalid setting.
func (builder *Builder) fail(format string, args ...any) {
	if builder.scheme.err == nil {
		builder.scheme.err = fmt.Errorf("%w: %s", ErrInvalidScheme, fmt.Sprintf(format, args...))
	}
}

// Algorithm selects the HMAC hash. An unknown value is an error.
func (builder *Builder) Algorithm(algorithm Algorithm) *Builder {
	if _, ok := hashFor(algorithm); !ok {
		builder.fail("unknown algorithm %q", algorithm)
		return builder
	}
	builder.scheme.algorithm = algorithm
	return builder
}

// Digits sets the code length, from 6 to 8. Authenticator apps usually
// support only 6 and 8.
func (builder *Builder) Digits(digits int) *Builder {
	if digits < minDigits || digits > maxDigits {
		builder.fail("digits %d outside %d to %d", digits, minDigits, maxDigits)
		return builder
	}
	builder.scheme.digits = digits
	return builder
}

// Period sets the time-step length. It must be a positive whole number of
// seconds so that it can be written to an otpauth URI.
func (builder *Builder) Period(period time.Duration) *Builder {
	if period < time.Second || period%time.Second != 0 {
		builder.fail("period %s is not a positive whole number of seconds", period)
		return builder
	}
	builder.scheme.period = period
	return builder
}

// Window sets how many steps on each side of the current step [Scheme.Verify]
// accepts, from 0 (current step only) to [MaxWindow]. An explicit 0 is
// honored; the default is [DefaultWindow].
func (builder *Builder) Window(steps int) *Builder {
	if steps < 0 || steps > MaxWindow {
		builder.fail("window %d outside 0 to %d", steps, MaxWindow)
		return builder
	}
	builder.scheme.window = steps
	return builder
}

// Err reports the first invalid setting, or nil. It wraps [ErrInvalidScheme].
func (scheme Scheme) Err() error {
	if scheme.err != nil {
		return scheme.err
	}
	if scheme.algorithm == "" {
		return fmt.Errorf("%w: zero value, use New", ErrInvalidScheme)
	}
	return nil
}

// Step returns the time step containing at: the whole number of periods since
// the Unix epoch. It is the counter RFC 6238 feeds to HOTP and the value
// [Scheme.Verify] returns for a match.
//
// It returns [ErrInvalidTime] for times before the epoch.
func (scheme Scheme) Step(at time.Time) (int64, error) {
	if err := scheme.Err(); err != nil {
		return 0, err
	}
	seconds := at.Unix()
	if seconds < 0 {
		return 0, ErrInvalidTime
	}
	return seconds / int64(scheme.period/time.Second), nil
}

// Generate returns the code for the time step containing at, zero-padded to
// the scheme's digit count. The secret is the raw key bytes (not base32) and
// must be non-empty.
//
// Example:
//
//	code, err := totp.New("Acme").Generate(secret, time.Now())
func (scheme Scheme) Generate(secret []byte, at time.Time) (string, error) {
	step, err := scheme.Step(at)
	if err != nil {
		return "", err
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("%w: empty", ErrInvalidSecret)
	}
	return scheme.code(secret, step), nil
}

// Verify checks code against the steps from the current step minus the window
// to the current step plus the window and returns the step that matched. It
// returns [ErrInvalidCode] when none match.
//
// Every candidate step is compared in constant time and all are always
// evaluated, so timing does not reveal which step matched. A successful match
// is not a replay check: the same code verifies again for as long as its step
// is inside the window. To reject replays, store the returned step per user and
// require each accepted step to be greater than the stored one.
//
// Example:
//
//	step, err := scheme.Verify(secret, submitted, time.Now())
//	if err != nil {
//		return err // errors.Is(err, totp.ErrInvalidCode) for a wrong code
//	}
//	if step <= lastStep {
//		return errReplayed
//	}
func (scheme Scheme) Verify(secret []byte, code string, at time.Time) (int64, error) {
	current, err := scheme.Step(at)
	if err != nil {
		return 0, err
	}
	if len(secret) == 0 {
		return 0, fmt.Errorf("%w: empty", ErrInvalidSecret)
	}
	matched := int64(-1)
	for step := max(current-int64(scheme.window), 0); step <= current+int64(scheme.window); step++ {
		expected := scheme.code(secret, step)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			matched = step
		}
	}
	if matched < 0 {
		return 0, ErrInvalidCode
	}
	return matched, nil
}

// NewSecret returns a random key of the hash's output size (20 bytes for SHA1),
// the length RFC 6238 recommends, read from crypto/rand. Generate it once at
// enrollment and store it encrypted; it is the credential.
func (scheme Scheme) NewSecret() ([]byte, error) {
	if err := scheme.Err(); err != nil {
		return nil, err
	}
	newHash, _ := hashFor(scheme.algorithm)
	secret := make([]byte, newHash().Size())
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("totp: generate secret: %w", err)
	}
	return secret, nil
}

// secretEncoding is the unpadded base32 alphabet used by authenticator apps.
var secretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeSecret returns the unpadded base32 form of secret, as shown for manual
// entry and carried in the otpauth URI.
func EncodeSecret(secret []byte) string {
	return secretEncoding.EncodeToString(secret)
}

// DecodeSecret parses base32 text, ignoring case, spaces, hyphens and padding,
// so a user-typed secret such as "jbsw y3dp-ehpk 3pxp" decodes. It returns
// [ErrInvalidSecret] for invalid or empty input.
func DecodeSecret(text string) ([]byte, error) {
	cleaned := strings.NewReplacer(" ", "", "-", "", "=", "").Replace(strings.ToUpper(text))
	secret, err := secretEncoding.DecodeString(cleaned)
	if err != nil || len(secret) == 0 {
		return nil, fmt.Errorf("%w: not base32", ErrInvalidSecret)
	}
	return secret, nil
}

// URI returns the otpauth:// provisioning URI that authenticator apps read from
// a QR code, with the scheme's issuer, algorithm, digits and period written
// explicitly. Account identifies the user (an email address or username). It
// must be non-empty and contain no colon, which separates issuer and account in
// the URI label; a violation returns [ErrInvalidSecret]. An invalid scheme,
// including an invalid issuer, returns its [Scheme.Err].
//
// The URI contains the secret. Render it only to the enrolling user and never
// log it.
//
// Example:
//
//	uri, err := totp.New("Acme").URI("alice@example.com", secret)
//	// otpauth://totp/Acme:alice@example.com?algorithm=SHA1&digits=6&issuer=Acme&period=30&secret=...
func (scheme Scheme) URI(account string, secret []byte) (string, error) {
	if err := scheme.Err(); err != nil {
		return "", err
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("%w: empty", ErrInvalidSecret)
	}
	if account == "" || strings.Contains(account, ":") {
		return "", fmt.Errorf("%w: account must be non-empty and contain no colon", ErrInvalidSecret)
	}
	query := url.Values{
		"secret":    {EncodeSecret(secret)},
		"issuer":    {scheme.issuer},
		"algorithm": {string(scheme.algorithm)},
		"digits":    {strconv.Itoa(scheme.digits)},
		"period":    {strconv.FormatInt(int64(scheme.period/time.Second), 10)},
	}
	uri := url.URL{
		Scheme:   "otpauth",
		Host:     "totp",
		Path:     "/" + scheme.issuer + ":" + account,
		RawQuery: query.Encode(),
	}
	return uri.String(), nil
}

// hashFor returns the hash constructor for algorithm.
func hashFor(algorithm Algorithm) (func() hash.Hash, bool) {
	switch algorithm {
	case SHA1:
		return sha1.New, true
	case SHA256:
		return sha256.New, true
	case SHA512:
		return sha512.New, true
	}
	return nil, false
}

// code computes the HOTP value for one counter (RFC 4226 section 5.3): an HMAC
// of the big-endian counter, dynamically truncated to 31 bits and reduced to
// the configured digits. The scheme must already be valid.
func (scheme Scheme) code(secret []byte, counter int64) string {
	newHash, _ := hashFor(scheme.algorithm)
	mac := hmac.New(newHash, secret)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))
	mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	modulus := uint32(1)
	for range scheme.digits {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", scheme.digits, truncated%modulus)
}
