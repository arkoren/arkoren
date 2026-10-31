package request

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"

	"arkoren.dev/core/http/request/internal/decode"
	"arkoren.dev/foundation/problem"
)

// JSON decodes the request body as JSON into a value of type T. Unknown
// members are ignored unless [Strict] is given. A body over the [MaxSize]
// limit returns [ErrBodyTooLarge]; malformed JSON returns [ErrInvalidJSON].
// Decoding follows encoding/json/v2: by default member names are case-sensitive,
// duplicates are rejected, and integers reject fractions and exponents.
// Content-Type is not checked; use contenttype middleware for that policy.
// Errors from application unmarshaler methods retain their cause and are not
// automatically classified as bad input. Such methods must return an explicit
// problem or JSON syntax/type error when client-facing feedback is intended.
//
// Example:
//
//	input, err := request.JSON[CreateUser](r, request.Strict(), request.MaxSize(1<<20))
//	if err != nil {
//		return err
//	}
//	_ = input
func JSON[T any](request *http.Request, opts ...BodyOption) (value T, err error) {
	config := newBodyConfig(opts)

	if err := json.UnmarshalRead(config.body(request), &value, json.RejectUnknownMembers(config.strict)); err != nil {
		return value, invalidJSON(err)
	}

	return value, nil
}

// invalidJSON preserves operational failures and existing HTTP problems while
// classifying native JSON syntax, semantics, and body-limit failures.
func invalidJSON(err error) error {
	if _, ok := errors.AsType[problem.Details](err); ok {
		return err
	}

	if overLimit(err) {
		return ErrBodyTooLarge
	}

	var syntactic *jsontext.SyntacticError
	var semantic *json.SemanticError

	if errors.As(err, &semantic) && decode.CallbackError(semantic) {
		return err
	}

	if errors.Is(err, io.EOF) || errors.As(err, &syntactic) || errors.As(err, &semantic) {
		return ErrInvalidJSON.Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	}

	return err
}
