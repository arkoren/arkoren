package gcpcaptcha

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrRejected identifies invalid tokens and hostname/action mismatches, not
// operational failures. Verify never rejects solely because of a low score.
//
// Example:
//
//	if errors.Is(err, gcpcaptcha.ErrRejected) {
//		return gcpcaptcha.ErrRejected
//	}
var ErrRejected = errors.New("gcpcaptcha rejected")

// ErrUnavailable identifies transport, quota and malformed provider responses.
// An unknown outcome must not authorize or trigger a replay of the same token.
//
// Example:
//
//	if errors.Is(err, gcpcaptcha.ErrUnavailable) {
//		return gcpcaptcha.ErrUnavailable
//	}
var ErrUnavailable = errors.New("gcpcaptcha unavailable")

// ErrConfiguration identifies invalid local settings and provider authentication
// failures. Fix server configuration rather than asking the user to retry.
//
// Example:
//
//	if errors.Is(err, gcpcaptcha.ErrConfiguration) {
//		return gcpcaptcha.ErrConfiguration
//	}
var ErrConfiguration = errors.New("gcpcaptcha configuration")

// Input supplies a single website token and optional explicit request signals.
// It is not retained. Never log it: Token is a single-use credential.
//
// Example:
//
//	input := gcpcaptcha.Input{Token: token, ExpectedAction: "login"}
type Input struct {
	// Token is the browser credential; empty or over 64 KiB rejects locally.
	// This is an application bound, not a documented Google maximum.
	// Example: input.Token = browserToken
	Token string
	// ExpectedAction is checked exactly and sent to Google. Empty skips the
	// local check for checkbox integrations without actions. Universal keys
	// require it; action-capable integrations should always supply it.
	// Example: input.ExpectedAction = "login"
	ExpectedAction string
	// RemoteIP is omitted when invalid. Supply only a trusted parsed client IP;
	// the package never trusts arbitrary proxy headers.
	// Example: input.RemoteIP = netip.MustParseAddr("192.0.2.4")
	RemoteIP netip.Addr
	// UserAgent is an optional user-device signal, omitted when empty.
	// Example: input.UserAgent = request.UserAgent()
	UserAgent string
}

// Assessment contains credential-free evidence from a valid website token.
// Scores range from zero (high risk) to one (low risk), not authorization.
// Unknown provider reason/challenge strings are retained for forward compatibility.
//
// Example:
//
//	assessment, err := verifier.Verify(ctx, input)
//	if err != nil {
//		return err
//	}
//	if assessment.Score < 0.7 {
//		return gcpcaptcha.ErrRejected
//	}
type Assessment struct {
	// Name is the provider resource name, usable for application-owned review.
	// Example: assessmentID := assessment.Name
	Name string
	// Score is Google's global legitimate-event score, not an SMS fraud score.
	// An omitted/null proto3 score decodes to zero; the wire format cannot
	// distinguish this default from a computed zero. RiskAnalysis must exist.
	// Example: accepted := assessment.Score >= 0.7
	Score float64
	// Reasons contains provider classification codes; it may be empty.
	// Example: reasons := assessment.Reasons
	Reasons []string
	// ExtendedVerdictReasons contains optional Enterprise-tier verdict codes.
	// Example: extended := assessment.ExtendedVerdictReasons
	ExtendedVerdictReasons []string
	// Challenge contains optional PASSED, FAILED, NOCAPTCHA or a future code.
	// Example: challengePassed := assessment.Challenge == "PASSED"
	Challenge string
	// Hostname is the verified, allowlisted website hostname.
	// Example: hostname := assessment.Hostname
	Hostname string
	// Action is the actual browser action; empty is possible for checkbox keys.
	// Example: action := assessment.Action
	Action string
	// CreatedAt is token generation time; zero means it was not supplied.
	// Example: createdAt := assessment.CreatedAt
	CreatedAt time.Time
	// ClientSignalsFailed records Google's signal-collection failure evidence.
	// Example: needsReview := assessment.ClientSignalsFailed
	ClientSignalsFailed bool
}

// Verifier holds immutable site and trust configuration. Reuse it concurrently;
// it owns no lifecycle resources and never closes a borrowed transport.
//
// Example:
//
//	v, err := gcpcaptcha.New(ctx, "shop-prod", "site-key", []string{"shop.example"})
//	if err != nil {
//		return err
//	}
//	_, err = v.Verify(ctx, gcpcaptcha.Input{Token: token, ExpectedAction: "login"})
//	if err != nil {
//		return err
//	}
type Verifier struct {
	// client is a private policy snapshot with a borrowed transport.
	client *http.Client
	// endpoint is fixed to Google's HTTPS assessment service.
	endpoint string
	// key is never placed in URLs or diagnostic messages.
	key string
	// siteKey binds all assessments to the configured website key.
	siteKey string
	// hostnames is a detached, exact-match allowlist.
	hostnames []string
}

// maxTokenBytes is an application memory/egress guard, deliberately more generous
// than legacy provider limits. Google does not document a token maximum here.
const maxTokenBytes = 64 << 10

// maxResponseBytes bounds provider data including ignored event echoes.
const maxResponseBytes = 1 << 20

// Verify performs exactly one assessment attempt, bounded to ten seconds and the
// caller's earlier deadline. Nil error guarantees a valid token, allowed hostname,
// matching nonempty ExpectedAction and a present risk-analysis message with a
// finite score. Omitted/null proto3 scalar scores mean zero. Low scores succeed;
// the caller owns score policy. Errors never include tokens, response bodies or
// native error text; errors.Is retains underlying transport/cancellation causes.
// Zero/nil verifiers report ErrConfiguration rather than authorizing.
// Native ADC refresh uses this request's context, not the construction context.
// Supplied HTTP transports/authentication must also honor request cancellation.
// Cancellation is checked before I/O and again before returning evidence, even
// if a transport or body reader reports success after cancellation.
//
// Example:
//
//	assessment, err := verifier.Verify(ctx, gcpcaptcha.Input{Token: token, ExpectedAction: "login"})
//	if err != nil {
//		return err
//	}
//	if assessment.Score < 0.7 {
//		return gcpcaptcha.ErrRejected
//	}
func (verifier *Verifier) Verify(ctx context.Context, input Input) (Assessment, error) {
	if verifier == nil || verifier.client == nil {
		return Assessment{}, ErrConfiguration
	}

	if input.Token == "" || len(input.Token) > maxTokenBytes {
		return Assessment{}, ErrRejected
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if err := ctx.Err(); err != nil {
		return Assessment{}, safeError(ErrUnavailable, err)
	}

	request, err := verifier.request(ctx, input)

	if err != nil {
		return Assessment{}, safeError(ErrConfiguration, err)
	}

	response, err := verifier.client.Do(request)

	if err != nil {
		return Assessment{}, safeError(ErrUnavailable, err)
	}

	defer func() {
		// Verification uses the bounded response read, not connection reuse.
		// A cleanup failure cannot change already received provider evidence.
		_ = response.Body.Close()
	}()

	assessment, err := verifier.assessment(response, input.ExpectedAction)

	if err := ctx.Err(); err != nil {
		return Assessment{}, safeError(ErrUnavailable, err)
	}

	return assessment, err
}

// request encodes only supported website signals; the browser token stays local
// to this request body and is never part of a URL or returned record.
func (verifier *Verifier) request(ctx context.Context, input Input) (*http.Request, error) {
	event := map[string]string{"token": input.Token, "siteKey": verifier.siteKey}

	if input.ExpectedAction != "" {
		event["expectedAction"] = input.ExpectedAction
	}

	if input.RemoteIP.IsValid() {
		event["userIpAddress"] = input.RemoteIP.Unmap().WithZone("").String()
	}

	if input.UserAgent != "" {
		event["userAgent"] = input.UserAgent
	}

	body, err := json.Marshal(map[string]map[string]string{"event": event})

	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, verifier.endpoint, bytes.NewReader(body))

	if err != nil {
		return nil, err
	}

	request.Header.Set("Content-Type", "application/json")
	// A replayable body can let net/http replay a POST on a stale connection.
	// Tokens have no documented assessment idempotency guarantee.
	request.GetBody = nil

	if verifier.key != "" {
		request.Header.Set("X-Goog-Api-Key", verifier.key)
	}

	return request, nil
}

// wireAssessment reads selected evidence, ignoring event echoes and additive
// API fields as Google recommends. Message pointers preserve message presence;
// nonoptional proto3 scalars use their protocol defaults for omitted/null values.
type wireAssessment struct {
	// Name is the provider resource identifier.
	Name string `json:"name"`
	// Risk must be a present, non-null message before publishing a score.
	Risk *struct {
		// Score defaults to zero for omission/null, as required by ProtoJSON.
		Score float64 `json:"score"`
		// Reasons preserves Google's extensible classification codes.
		Reasons []string `json:"reasons"`
		// Extended preserves optional Enterprise reason codes.
		Extended []string `json:"extendedVerdictReasons"`
		// Challenge is optional challenge outcome evidence.
		Challenge string `json:"challenge"`
	} `json:"riskAnalysis"`
	// Token contains no echoed credential, only website validity evidence.
	Token *struct {
		// Valid defaults to false; only explicit true affirms token validity.
		Valid bool `json:"valid"`
		// Hostname identifies the token's origin.
		Hostname string `json:"hostname"`
		// Action identifies the browser interaction.
		Action string `json:"action"`
		// CreatedAt decodes optional RFC3339 token time.
		CreatedAt time.Time `json:"createTime"`
		// ClientSignalsFailed is nonauthorizing collection evidence.
		ClientSignalsFailed bool `json:"clientSignalsFailed"`
	} `json:"tokenProperties"`
}

// assessment maps HTTP outcomes separately from invalid token evidence, then
// applies local trust checks before publishing any usable score.
func (verifier *Verifier) assessment(response *http.Response, action string) (Assessment, error) {
	switch response.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return Assessment{}, ErrConfiguration
	case http.StatusOK:
		// Continue only for a complete assessment response.
	default:
		return Assessment{}, fmt.Errorf("gcpcaptcha assessment HTTP %d: %w", response.StatusCode, ErrUnavailable)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))

	if err != nil {
		return Assessment{}, safeError(ErrUnavailable, err)
	}

	if len(body) > maxResponseBytes {
		return Assessment{}, ErrUnavailable
	}

	var evidence wireAssessment

	if err := json.Unmarshal(body, &evidence); err != nil {
		return Assessment{}, safeError(ErrUnavailable, err)
	}

	return verifier.evaluate(evidence, action)
}

// evaluate separates authenticity from score policy. Both enclosing messages
// must exist before publishing evidence; their scalar defaults follow proto3.
func (verifier *Verifier) evaluate(evidence wireAssessment, action string) (Assessment, error) {
	if evidence.Token == nil {
		return Assessment{}, ErrUnavailable
	}

	if !evidence.Token.Valid {
		return Assessment{}, ErrRejected
	}

	if !verifier.allowed(evidence.Token.Hostname) {
		return Assessment{}, ErrRejected
	}

	if action != "" && evidence.Token.Action != action {
		return Assessment{}, ErrRejected
	}

	if evidence.Risk == nil {
		return Assessment{}, ErrUnavailable
	}

	return publishAssessment(evidence)
}

// allowed checks exact DNS names without treating suffixes as a trust grant.
func (verifier *Verifier) allowed(hostname string) bool {
	for _, allowed := range verifier.hostnames {
		if strings.EqualFold(hostname, allowed) {
			return true
		}
	}

	return false
}

// publishAssessment validates score range and creates credential-free evidence.
// Its caller has established nonnil token and risk messages. Score omission/null
// has already become the proto3 default zero, not an absence error.
func publishAssessment(evidence wireAssessment) (Assessment, error) {
	score := evidence.Risk.Score

	if !validScore(score) {
		return Assessment{}, ErrUnavailable
	}

	return Assessment{Name: evidence.Name, Score: score, Reasons: evidence.Risk.Reasons,
		ExtendedVerdictReasons: evidence.Risk.Extended, Challenge: evidence.Risk.Challenge,
		Hostname: evidence.Token.Hostname, Action: evidence.Token.Action,
		CreatedAt: evidence.Token.CreatedAt, ClientSignalsFailed: evidence.Token.ClientSignalsFailed}, nil
}

// validScore enforces the shared provider/policy finite inclusive unit interval.
func validScore(score float64) bool {
	return score >= 0 && score <= 1 && !math.IsNaN(score)
}

// redactedError preserves causes without rendering secret-bearing diagnostics.
// Callers must not log unwrapped native errors.
type redactedError struct {
	// class is the safe public category.
	class error
	// cause is retained for errors.Is/As, never interpolated.
	cause error
}

// Error renders only the public category, not native error data.
func (redactedError redactedError) Error() string { return redactedError.class.Error() }

// Unwrap exposes identity and cause for cancellation-aware application policy.
func (redactedError redactedError) Unwrap() []error {
	return []error{redactedError.class, redactedError.cause}
}

// safeError wraps operational causes without disclosing URLs or provider text.
func safeError(class, cause error) error { return redactedError{class: class, cause: cause} }
