package request

import (
	"context"
	"errors"
	"io"
	"net/http"

	"arkoren.dev/core/http/internal/bodysize"
	"arkoren.dev/foundation/problem"
)

// ErrBodyTooLarge is returned when the request body exceeds
// the maximum allowed size. Arkoren exposes this
// condition as a problem response so handlers can return it directly.
//
// Example:
//
//	failure := request.ErrBodyTooLarge.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrBodyTooLarge = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/request:body-too-large")
	details.Title("Payload Too Large")
	details.Detail("The request body exceeds the allowed size.")
	details.Status(http.StatusRequestEntityTooLarge)
})

// ErrInvalidJSON is returned when the request body cannot be decoded as JSON.
//
// Example:
//
//	failure := request.ErrInvalidJSON.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrInvalidJSON = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/request:invalid-json")
	details.Title("Invalid JSON request body")
	details.Detail("The request body must contain valid JSON.")
	details.Status(http.StatusBadRequest)
})

// ErrInvalidXML is returned when the request body cannot be decoded as XML.
//
// Example:
//
//	failure := request.ErrInvalidXML.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrInvalidXML = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/request:invalid-xml")
	details.Title("Invalid XML request body")
	details.Detail("The request body must contain valid XML.")
	details.Status(http.StatusBadRequest)
})

// ErrValidationFailed is the problem for a body or value that fails schema
// validation. It is the base for [ValidationProblem], which adds the
// violations as the "errors" member keyed by JSON Pointer. Match it with
// errors.Is.
//
// Example:
//
//	failure := request.ErrValidationFailed.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrValidationFailed = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/request:validation-failed")
	details.Title("Validation failed")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrStrictUnsupported is returned by [Bytes], [String], [XML], [ParseXML]
// and [ValidateXML] when they are given the [Strict] option. Raw bodies have
// no members to reject and Arkoren does not provide a strict XML decoder. The
// check runs before the body is read, so the body stays unconsumed.
//
// Example:
//
//	_, err := request.XML[Feed](r, request.Strict())
//	if errors.Is(err, request.ErrStrictUnsupported) {
//		return err // choose a supported decoder option rather than reporting bad input
//	}
//	return err
var ErrStrictUnsupported = errors.New("request: Strict is not supported for XML, Bytes and String bodies")

// BodyOption changes how one call reads and decodes a request body. Options
// apply in order, so a later option overrides an earlier one of the same kind.
// Without options unknown JSON members are ignored and reads are limited to
// the size in the request context (bodylimit middleware), or [DefaultMaxBodySize].
// Options do not remove a ceiling already enforced by the request body's reader.
//
// Example:
//
//	options := []request.BodyOption{request.Strict(), request.MaxSize(1 << 20)}
//	input, err := request.JSON[CreateUser](r, options...)
//	if err != nil {
//		return err
//	}
//	_ = input
type BodyOption func(*bodyConfig)

// bodyConfig collects the settings that [BodyOption] values change.
type bodyConfig struct {
	// strict rejects unknown JSON members.
	strict bool

	// unlimited adds no per-call cap; an upstream reader's ceiling still holds.
	// It is set by [Unlimited] and cleared by a later [MaxSize].
	unlimited bool

	// sized reports whether maxSize was given explicitly and so overrides the
	// limit in the request context.
	sized bool

	// maxSize is the explicit byte limit when sized is set. A negative value
	// means [DefaultMaxBodySize].
	maxSize int64
}

// Strict makes JSON decoding reject members the target type does not define.
// The violations surface as "unknown" violations from [ValidateJSON] and
// [ParseJSON]. It is not supported for XML, [Bytes] or [String]: those return
// [ErrStrictUnsupported] before reading the body.
//
// Example:
//
//	input, err := request.JSON[CreateUser](r, request.Strict())
func Strict() BodyOption {
	return func(config *bodyConfig) {
		config.strict = true
	}
}

// MaxSize limits the body to n bytes for this call, overriding both the limit
// in the request context (the bodylimit middleware) and [DefaultMaxBodySize]. A
// larger read fails with [ErrBodyTooLarge]. A negative n selects
// [DefaultMaxBodySize]; zero allows only an empty body. The option cannot raise
// a ceiling already enforced by an upstream [http.MaxBytesReader]. It bounds
// input bytes read, not the total memory required by the decoded value.
//
// Example:
//
//	input, err := request.JSON[CreateUser](r, request.MaxSize(1<<20))
//	if err != nil {
//		return err
//	}
//	_ = input
func MaxSize(n int64) BodyOption {
	return func(config *bodyConfig) {
		config.unlimited = false
		config.sized = true
		config.maxSize = n
	}
}

// Unlimited adds no per-call read limit, ignoring the stored request-context
// limit and [DefaultMaxBodySize]. It cannot remove a ceiling already enforced
// on r.Body by middleware. Use it only where memory use is bounded some other
// way; a hostile client can otherwise exhaust server memory.
//
// Example:
//
//	data, err := request.Bytes(r, request.Unlimited())
//	if err != nil {
//		return err
//	}
//	_ = data
func Unlimited() BodyOption {
	return func(config *bodyConfig) {
		config.unlimited = true
	}
}

// MaxBodySizeFromContext returns the limit stored in ctx by the bodylimit
// middleware (arkoren.dev/core/http/middleware/bodylimit) and whether there
// is one. When ok is false, bodies are limited to [DefaultMaxBodySize].
//
// Example:
//
//	limit, ok := request.MaxBodySizeFromContext(r.Context())
//	if !ok {
//		limit = request.DefaultMaxBodySize
//	}
//	_ = limit
func MaxBodySizeFromContext(ctx context.Context) (int64, bool) {
	return bodysize.FromContext(ctx)
}

// newBodyConfig applies opts in order to an empty configuration.
func newBodyConfig(opts []BodyOption) bodyConfig {
	var config bodyConfig

	for _, opt := range opts {
		opt(&config)
	}

	return config
}

// body returns the request body, capped at the resolved limit: an explicit
// [MaxSize], else the limit in the request context, else [DefaultMaxBodySize].
// [Unlimited] returns the body as is. Closing the result closes the original
// body. A nil body, as produced by http.NewRequest with no input, is treated
// as http.NoBody without mutating the request.
func (config bodyConfig) body(request *http.Request) io.ReadCloser {
	if request.Body == nil {
		return http.NoBody
	}

	if config.unlimited {
		return request.Body
	}

	maxSize := config.maxSize

	if !config.sized {
		var ok bool

		if maxSize, ok = MaxBodySizeFromContext(request.Context()); !ok {
			maxSize = DefaultMaxBodySize
		}
	}

	if maxSize < 0 {
		maxSize = DefaultMaxBodySize
	}

	return http.MaxBytesReader(nil, request.Body, maxSize)
}

// overLimit reports whether err comes from a body size limit, including an
// [http.MaxBytesReader] applied by middleware.
func overLimit(err error) bool {
	_, ok := errors.AsType[*http.MaxBytesError](err)

	return ok
}

// DefaultMaxBodySize is the body limit when neither [MaxSize] nor
// the bodylimit middleware gives one, and the size a negative limit selects (10 MiB).
// It is a sensible ceiling for JSON APIs; raise it explicitly for uploads or
// opt out with [Unlimited].
//
// Example:
//
//	data, err := request.Bytes(r, request.MaxSize(request.DefaultMaxBodySize))
//	if err != nil {
//		return err
//	}
//	_ = data
const DefaultMaxBodySize int64 = 10 << 20 // 10 MiB

// Bytes reads the request body and returns it as a byte slice. The body is
// consumed and cannot be read again. A body over the [MaxSize] limit returns
// [ErrBodyTooLarge]. [Strict] returns [ErrStrictUnsupported] without reading.
// Operational failures and problems already supplied by the reader retain
// their identity rather than being reclassified from an underlying cause.
//
// Example:
//
//	data, err := request.Bytes(r, request.MaxSize(1<<20))
//	if err != nil {
//		return err
//	}
//	_ = data
func Bytes(request *http.Request, opts ...BodyOption) ([]byte, error) {
	config := newBodyConfig(opts)

	if config.strict {
		return nil, ErrStrictUnsupported
	}

	data, err := io.ReadAll(config.body(request))

	if err != nil {
		_, classified := errors.AsType[problem.Details](err)

		if !classified && overLimit(err) {
			return nil, ErrBodyTooLarge
		}

		return nil, err
	}

	return data, nil
}

// String reads the request body and returns it as a string without UTF-8
// validation. It follows the rules of [Bytes], including the size limit and
// [ErrStrictUnsupported].
//
// Example:
//
//	text, err := request.String(r, request.MaxSize(4096))
//	if err != nil {
//		return err
//	}
//	_ = text
func String(request *http.Request, opts ...BodyOption) (string, error) {
	data, err := Bytes(request, opts...)

	if err != nil {
		return "", err
	}

	return string(data), nil
}
