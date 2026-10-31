package cloudflarecaptcha

import (
	"context"
	"errors"
	"net/http"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// rejectedProblem is the HTTP-only identity for visitor failures. Its safe
// presentation never includes extraction, provider or transport diagnostics.
var rejectedProblem = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/cloudflare:cloudflarecaptcha:rejected")
	details.Title("Verification Rejected")
	details.Status(http.StatusForbidden)
	details.Detail("Complete a new verification challenge before trying again.")
})

// unavailableProblem reports an uncertain provider result without encouraging
// automatic token replay. Applications can offer a fresh challenge later.
var unavailableProblem = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/cloudflare:cloudflarecaptcha:unavailable")
	details.Title("Verification Unavailable")
	details.Status(http.StatusServiceUnavailable)
	details.Detail("Verification is temporarily unavailable.")
})

// configurationProblem reports server configuration faults without disclosing
// credentials, hostname policies or native error text to the visitor.
var configurationProblem = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/cloudflare:cloudflarecaptcha:configuration")
	details.Title("Verification Configuration Error")
	details.Status(http.StatusInternalServerError)
	details.Detail("The server could not complete verification.")
})

// ExtractFunc obtains a sensitive token using application-owned request rules.
// Middleware does not provide a default extractor or inspect the body. Ordinary
// extraction errors mean ErrRejected. Errors wrapping context.Canceled or
// context.DeadlineExceeded mean ErrUnavailable, not visitor rejection. Errors
// wrapping ErrUnavailable or ErrConfiguration retain that classification;
// configuration takes precedence. Error strings are not rendered. Extractors
// shared by requests must be concurrency-safe.
//
// Example:
//
//	var extract cloudflarecaptcha.ExtractFunc = func(request *http.Request) (string, error) {
//		return request.Header.Get("X-Turnstile-Token"), nil
//	}
//	protect := cloudflarecaptcha.Middleware(verifier, extract)
//	_ = protect
type ExtractFunc = func(*http.Request) (string, error)

// MiddlewareBuilder selects the immutable request policy. It owns no request
// data; mutating a retained draft after construction does not affect middleware.
//
// Example:
//
//	protect := cloudflarecaptcha.Middleware(verifier, extract, func(policy *cloudflarecaptcha.MiddlewareBuilder) {
//		policy.Action("signup")
//	})
//	_ = protect
type MiddlewareBuilder struct {
	// action is the server-selected action required on every protected request.
	action string
}

// MiddlewareBuilderFunc edits a fresh policy directly once in order. Nil
// callbacks panic; callback panics propagate without being recovered.
//
// Example:
//
//	var signup cloudflarecaptcha.MiddlewareBuilderFunc = func(policy *cloudflarecaptcha.MiddlewareBuilder) {
//		policy.Action("signup")
//	}
//	protect := cloudflarecaptcha.Middleware(verifier, extract, signup)
//	_ = protect
type MiddlewareBuilderFunc = func(*MiddlewareBuilder)

// Action replaces the exact expected provider action. Empty disables only the
// action comparison. Choose it from server policy, not from request input.
//
// Example:
//
//	protect := cloudflarecaptcha.Middleware(verifier, extract, func(policy *cloudflarecaptcha.MiddlewareBuilder) {
//		policy.Action("login")
//	})
//	_ = protect
func (policy *MiddlewareBuilder) Action(action string) *MiddlewareBuilder {
	policy.action = action

	return policy
}

// Middleware extracts once, verifies once, then invokes next only on success.
// It returns safe router problems: rejection 403, unavailable 503, configuration
// 500. Returned errors preserve classification and original causes for errors.Is
// and errors.As; their text and HTTP bodies omit sensitive diagnostics.
// Extraction cancellation or deadline errors skip verification and next, return
// unavailable 503, and do not add ErrRejected. ErrConfiguration takes precedence.
// No body sniffing, proxy header trust, remote-IP forwarding or evidence context
// storage is performed. Use Verify directly after decoding JSON once in a handler.
// Nil verifier, extractor, next handler or construction callback is programmer
// misuse and panics. A zero verifier instead returns a configuration failure on
// requests. Callbacks run once directly in order and publication detaches drafts.
//
// Example:
//
//	protect := cloudflarecaptcha.Middleware(verifier, func(request *http.Request) (string, error) {
//		return request.Header.Get("X-Turnstile-Token"), nil
//	}, func(policy *cloudflarecaptcha.MiddlewareBuilder) {
//		policy.Action("signup")
//	})
//	protected := protect(handler)
//	_ = protected
func Middleware(verifier *Verifier, extract ExtractFunc, configure ...MiddlewareBuilderFunc) router.Middleware {
	if verifier == nil || extract == nil {
		panic("cloudflarecaptcha: verifier and extractor required")
	}

	policy := &MiddlewareBuilder{}

	for _, configure := range configure {
		configure(policy)
	}

	action := policy.action

	return func(next router.Handler) router.Handler {
		if next == nil {
			panic("cloudflarecaptcha: next handler required")
		}

		return func(writer http.ResponseWriter, request *http.Request) error {
			token, err := extract(request)

			if err != nil {
				return httpFailure(err)
			}

			_, err = verifier.Verify(request.Context(), Input{Token: token, ExpectedAction: action})

			if err != nil {
				return httpFailure(err)
			}

			return next(writer, request)
		}
	}
}

// httpFailure wraps causes in safe text before attaching them to the immutable
// HTTP problem. Configuration takes precedence if a cause matches two classes.
func httpFailure(err error) error {
	class := ErrRejected
	details := rejectedProblem

	if errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		class, details = ErrUnavailable, unavailableProblem
	}

	if errors.Is(err, ErrConfiguration) {
		class, details = ErrConfiguration, configurationProblem
	}

	return details.Derive(func(details *problem.Builder) {
		details.Error(failure{class, err})
	})
}
