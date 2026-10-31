package gcpcaptcha

import (
	"context"
	"errors"
	"net/http"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ExtractFunc reads the application-selected token location exactly once.
// Ordinary errors reject the request. ErrUnavailable, context.Canceled and
// context.DeadlineExceeded produce unavailable responses; ErrConfiguration takes
// precedence over all other categories. Original causes remain inspectable but
// their text is hidden. Shared extractors must be concurrency-safe. Middleware
// does not parse forms, inspect JSON or buffer request bodies.
//
// Example:
//
//	var extract gcpcaptcha.ExtractFunc = func(request *http.Request) (string, error) {
//		return request.Header.Get("X-Captcha-Token"), nil
//	}
type ExtractFunc = func(*http.Request) (string, error)

// MiddlewareBuilder is a construction-only score policy. Publication copies
// its settings, so retained drafts cannot change a running route's policy.
//
// Example:
//
//	configure := func(policy *gcpcaptcha.MiddlewareBuilder) {
//		policy.Action("login")
//		policy.MinScore(0.7)
//	}
type MiddlewareBuilder struct {
	// action is the expected browser interaction; empty skips the action check.
	action string
	// score is the explicitly selected minimum acceptable global score.
	score float64
	// scoreSet distinguishes omission from the valid explicit zero threshold.
	scoreSet bool
}

// MiddlewareBuilderFunc edits a shared fresh draft once in argument order.
// Nil callbacks panic; an expanded nil slice makes no edits.
//
// Example:
//
//	var configure gcpcaptcha.MiddlewareBuilderFunc = func(policy *gcpcaptcha.MiddlewareBuilder) {
//		policy.MinScore(0.7)
//	}
type MiddlewareBuilderFunc = func(*MiddlewareBuilder)

// Action sends and locally verifies the exact browser action. Empty is allowed
// only when the application intentionally skips actions (for example, checkbox
// keys rendered explicitly). Universal keys require a nonempty action.
//
// Example:
//
//	configure := func(policy *gcpcaptcha.MiddlewareBuilder) {
//		policy.Action("login")
//	}
func (policy *MiddlewareBuilder) Action(action string) *MiddlewareBuilder {
	policy.action = action

	return policy
}

// MinScore requires a finite threshold in [0,1], inclusive. Explicit zero is
// valid but still requires token authenticity, origin and a present risk-analysis
// message. Omitted/null proto3 score scalars count as zero, just like explicit 0.
// There is no implicit default. The last call supplies the final threshold.
//
// Example:
//
//	configure := func(policy *gcpcaptcha.MiddlewareBuilder) {
//		policy.MinScore(0.7)
//	}
func (policy *MiddlewareBuilder) MinScore(score float64) *MiddlewareBuilder {
	policy.score = score
	policy.scoreSet = true

	return policy
}

// rejectedProblem is the safe public denial response; causes stay internal.
var rejectedProblem = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/gcp:gcpcaptcha:rejected")
	details.Title("CAPTCHA rejected")
	details.Status(http.StatusForbidden)
})

// unavailableProblem exposes no provider or transport diagnostic data.
var unavailableProblem = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/gcp:gcpcaptcha:unavailable")
	details.Title("CAPTCHA unavailable")
	details.Status(http.StatusServiceUnavailable)
})

// configurationProblem fails closed when route policy or credentials are invalid.
var configurationProblem = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/gcp:gcpcaptcha:configuration")
	details.Title("CAPTCHA configuration error")
	details.Status(http.StatusInternalServerError)
})

// Middleware extracts a token, verifies it once and admits only scores at least
// the explicit MinScore. It never falls back to another location or fails open.
// Missing/invalid threshold, nil verifier or nil extractor produces a handler
// returning a safe 500 without extraction or verification. Callback panics and
// nil callback misuse propagate during construction. All callbacks run in order.
// Ordinary extraction and authenticity denials return safe 403 problems;
// unavailable/cancellation/deadline failures return 503 and configuration failures
// return 500, retaining errors.Is causes. Configuration takes precedence over
// unavailable, which takes precedence over rejection. Extraction operational
// failures do not acquire a spurious ErrRejected identity.
// No IP or user-agent signal is inferred; use Verify for explicit signals or
// richer risk policy. The original body reaches the next handler unchanged
// unless the application's own extractor consumes it.
//
// Example:
//
//	protect := gcpcaptcha.Middleware(verifier, func(request *http.Request) (string, error) {
//		return request.Header.Get("X-Captcha-Token"), nil
//	}, func(policy *gcpcaptcha.MiddlewareBuilder) {
//		policy.Action("login")
//		policy.MinScore(0.7)
//	})
//	routes.Use(protect)
func Middleware(verifier *Verifier, extract ExtractFunc, configure ...MiddlewareBuilderFunc) router.Middleware {
	policy := &MiddlewareBuilder{}

	for _, configure := range configure {
		configure(policy)
	}

	settings := *policy
	configurationErr := middlewareConfiguration(verifier, extract, settings)

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			if configurationErr != nil {
				return httpError(configurationErr)
			}

			if err := verifyRequest(request, verifier, extract, settings); err != nil {
				return httpError(err)
			}

			return next(writer, request)
		}
	}
}

// middlewareConfiguration checks required objects separately from score policy.
func middlewareConfiguration(verifier *Verifier, extract ExtractFunc, policy MiddlewareBuilder) error {
	if verifier == nil || extract == nil {
		return ErrConfiguration
	}

	if !policy.scoreSet || !validScore(policy.score) {
		return ErrConfiguration
	}

	return nil
}

// verifyRequest applies the application's extraction and score policy in order.
func verifyRequest(request *http.Request, verifier *Verifier, extract ExtractFunc, policy MiddlewareBuilder) error {
	token, err := extract(request)

	if err != nil {
		return err
	}

	assessment, err := verifier.Verify(request.Context(), Input{Token: token, ExpectedAction: policy.action})

	if err != nil {
		return err
	}

	if assessment.Score < policy.score {
		return ErrRejected
	}

	return nil
}

// httpError classifies extraction and verification errors at the HTTP boundary.
// Configuration wins over unavailable/cancellation, which wins over rejection.
// The safe wrapper adds only the selected class and retains original causes.
func httpError(err error) error {
	class, details := ErrRejected, rejectedProblem

	if errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		class, details = ErrUnavailable, unavailableProblem
	}

	if errors.Is(err, ErrConfiguration) {
		class, details = ErrConfiguration, configurationProblem
	}

	return details.Derive(func(occurrence *problem.Builder) {
		occurrence.Error(safeError(class, err))
	})
}
