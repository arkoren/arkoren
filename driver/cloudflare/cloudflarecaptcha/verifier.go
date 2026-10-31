package cloudflarecaptcha

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ErrRejected means the visitor must supply a fresh acceptable challenge.
// It includes invalid token input and hostname/action mismatches.
//
// Example:
//
//	if errors.Is(err, cloudflarecaptcha.ErrRejected) {
//		// Ask the visitor to complete a new challenge.
//	}
var ErrRejected = errors.New("turnstile verification rejected")

// ErrUnavailable means verification could not be established. Never authorize
// on this error or blindly retry a potentially consumed token.
//
// Example:
//
//	if errors.Is(err, cloudflarecaptcha.ErrUnavailable) {
//		// Stop processing and offer a fresh challenge later.
//	}
var ErrUnavailable = errors.New("turnstile verification unavailable")

// ErrConfiguration indicates invalid local settings or provider credentials.
// It is a server fault, not a visitor rejection.
//
// Example:
//
//	if errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
//		// Alert the operator without logging credentials.
//	}
var ErrConfiguration = errors.New("turnstile configuration invalid")

// Input describes one verification. Tokens are sensitive and must not be logged.
// ExpectedAction must come from server policy, not from untrusted request data.
//
// Example:
//
//	in := cloudflarecaptcha.Input{Token: token, ExpectedAction: "signup"}
//	_, err := verifier.Verify(ctx, in)
//	_ = err
type Input struct {
	// Token is the single-use widget response, from 1 through 2048 bytes.
	//
	// Example:
	//
	//	in := cloudflarecaptcha.Input{Token: token}
	Token string

	// ExpectedAction requires exact action equality when nonempty. Empty skips
	// only the action check, never the hostname check.
	//
	// Example:
	//
	//	in := cloudflarecaptcha.Input{Token: token, ExpectedAction: "login"}
	ExpectedAction string

	// RemoteIP is optional; an invalid address is omitted. Supply it only after
	// applying your trusted-proxy policy; no headers are interpreted here.
	//
	// Example:
	//
	//	in := cloudflarecaptcha.Input{Token: token, RemoteIP: netip.MustParseAddr("192.0.2.1")}
	RemoteIP netip.Addr
}

// Result is provider evidence returned only on successful verification. CData
// remains client-originated metadata, not an authorization claim or a score.
//
// Example:
//
//	evidence, err := verifier.Verify(ctx, cloudflarecaptcha.Input{Token: token})
//	if err != nil {
//		return err
//	}
//	_ = evidence.Hostname
type Result struct {
	// Hostname is the exact configured hostname matched by the response.
	//
	// Example:
	//
	//	hostname := evidence.Hostname
	Hostname string

	// Action is the provider action, possibly empty when not required.
	//
	// Example:
	//
	//	action := evidence.Action
	Action string

	// ChallengeAt is the provider's RFC3339 challenge timestamp, not local time.
	//
	// Example:
	//
	//	solvedAt := evidence.ChallengeAt
	ChallengeAt time.Time

	// CData is the provider's custom data; treat it as untrusted and sensitive.
	//
	// Example:
	//
	//	data := evidence.CData
	CData string
}

// Verifier is an immutable concurrent-safe Turnstile verifier constructed by
// New. Its zero value and a nil receiver fail closed with ErrConfiguration.
//
// Example:
//
//	v, err := cloudflarecaptcha.New("secret", []string{"example.com"})
//	if err != nil {
//		return err
//	}
//	_, err = v.Verify(ctx, cloudflarecaptcha.Input{Token: token})
//	return err
type Verifier struct {
	// secret authenticates server-side requests and never appears in errors.
	secret string
	// hostnames is an owned exact-match allowlist.
	hostnames []string
	// client snapshots borrowed client fields and forbids redirects.
	client http.Client
	// timeout bounds each request including response parsing.
	timeout time.Duration
}

// response is the bounded Siteverify wire format. Pointer success distinguishes
// absent/null success from a real provider rejection. Unknown fields are allowed
// for provider extensions, but unknown failure codes never authorize.
type response struct {
	// Success is mandatory, including on rejected responses.
	Success *bool `json:"success"`
	// Hostname identifies the widget's origin.
	Hostname string `json:"hostname"`
	// Action is optional evidence checked against server policy.
	Action string `json:"action"`
	// ChallengeAt must be a valid nonzero timestamp on success.
	ChallengeAt time.Time `json:"challenge_ts"`
	// CData carries provider-native custom data without interpretation.
	CData string `json:"cdata"`
	// Codes classify failure without exposing raw provider content.
	Codes []string `json:"error-codes"`
}

// failure retains classification and original causes for errors.Is/As while
// keeping error strings independent of sensitive transport or decoder text.
type failure struct {
	// class is one of the package's plain sentinels.
	class error
	// cause is retained, not rendered; callers must not log unwrapped causes.
	cause error
}

// Error renders only the safe classification, never the cause's text.
// A valid failure has a nonnil package sentinel; do not format an unwrapped
// transport cause, which can contain a token, secret or provider response.
//
// Example:
//
//	err := failure{class: ErrUnavailable, cause: context.Canceled}
//	_ = err.Error() // Classification only, never native diagnostic text.
func (failure failure) Error() string {
	return failure.class.Error()
}

// Unwrap preserves class and cause for errors.Is/As inspection.
// The returned slice is new; the errors themselves remain shared. Explicit
// inspection retains cancellation identity without making native text log-safe.
//
// Example:
//
//	err := failure{class: ErrUnavailable, cause: context.Canceled}
//	_ = errors.Is(err, context.Canceled)
func (failure failure) Unwrap() []error {
	return []error{failure.class, failure.cause}
}

// Verify makes one POST to Cloudflare's fixed HTTPS Siteverify endpoint. A nil
// error means provider success, a nonzero timestamp, a trusted hostname, and
// the requested action matched. Every failure returns a zero Result. Empty or
// oversized tokens reject before I/O. Responses are limited to 64 KiB; malformed,
// contradictory and unknown failure responses fail closed as ErrUnavailable.
// Context cancellation/deadlines and native causes remain inspectable through
// errors.Is/As, but Error text never includes token, secret or raw response.
// Do not log unwrapped causes: custom transports may put secrets in errors.
// Nil context is programmer misuse and panics. No retry or idempotency key is used.
//
// Example:
//
//	evidence, err := verifier.Verify(ctx, cloudflarecaptcha.Input{Token: token, ExpectedAction: "login"})
//	if err != nil {
//		return err
//	}
//	_ = evidence.Hostname
func (verifier *Verifier) Verify(ctx context.Context, input Input) (Result, error) {
	if verifier == nil || verifier.secret == "" {
		return Result{}, ErrConfiguration
	}

	if len(input.Token) == 0 || len(input.Token) > 2048 {
		return Result{}, ErrRejected
	}

	ctx, cancel := context.WithTimeout(ctx, verifier.timeout)
	defer cancel()

	form := url.Values{"secret": {verifier.secret}, "response": {input.Token}}

	if input.RemoteIP.IsValid() {
		form.Set("remoteip", input.RemoteIP.String())
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))

	if err != nil {
		return Result{}, failure{ErrUnavailable, err}
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GetBody is deliberately absent: even standard transports must not replay
	// this one-use token after an ambiguous connection failure.
	request.GetBody = nil

	result, err := verifier.exchange(request)

	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, failure{ErrUnavailable, err}
	}

	return verifier.accept(result, input.ExpectedAction)
}

// exchange owns and closes the response body, never the borrowed transport.
// Provider status failures are classified before parsing untrusted body data.
func (verifier *Verifier) exchange(request *http.Request) (response, error) {
	reply, err := verifier.client.Do(request)

	if err != nil {
		return response{}, failure{ErrUnavailable, err}
	}

	defer func() {
		// Closing is best effort after classification; it cannot authorize a token.
		_ = reply.Body.Close()
	}()

	if reply.StatusCode == http.StatusUnauthorized || reply.StatusCode == http.StatusForbidden {
		return response{}, ErrConfiguration
	}

	if reply.StatusCode != http.StatusOK {
		return response{}, ErrUnavailable
	}

	// limit caps provider JSON at 64 KiB before decoding or retaining evidence.
	const limit = 64 << 10

	body, err := io.ReadAll(io.LimitReader(reply.Body, limit+1))

	if err != nil {
		return response{}, failure{ErrUnavailable, err}
	}

	if len(body) > limit {
		return response{}, ErrUnavailable
	}

	var result response

	if err := json.Unmarshal(body, &result); err != nil {
		return response{}, failure{ErrUnavailable, err}
	}

	return result, nil
}

// accept separates wire validity and provider failure from local authorization
// policy. Evidence is published only after all checks succeed.
func (verifier *Verifier) accept(result response, action string) (Result, error) {
	if result.Success == nil {
		return Result{}, ErrUnavailable
	}

	if !*result.Success {
		return Result{}, classify(result.Codes)
	}

	if len(result.Codes) != 0 || result.ChallengeAt.IsZero() {
		return Result{}, ErrUnavailable
	}

	if !slices.Contains(verifier.hostnames, result.Hostname) {
		return Result{}, ErrRejected
	}

	if action != "" && result.Action != action {
		return Result{}, ErrRejected
	}

	return Result{Hostname: result.Hostname, Action: result.Action,
		ChallengeAt: result.ChallengeAt, CData: result.CData}, nil
}

// classify gives server faults precedence over visitor rejection. Unknown or
// absent codes are unavailable rather than an invented visitor diagnosis.
func classify(codes []string) error {
	class := ErrRejected

	if len(codes) == 0 {
		return ErrUnavailable
	}

	for _, code := range codes {
		switch code {
		case "missing-input-secret", "invalid-input-secret", "bad-request":
			return ErrConfiguration
		case "missing-input-response", "invalid-input-response", "timeout-or-duplicate":
		default:
			class = ErrUnavailable
		}
	}

	return fmt.Errorf("turnstile provider response: %w", class)
}
