package request

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"

	"arkoren.dev/foundation/problem"
)

// XML decodes the request body as XML into a value of type T with a streaming
// decoder. A body over the [MaxSize] limit returns [ErrBodyTooLarge];
// malformed XML or invalid numeric fields return [ErrInvalidXML]. Operational
// read failures and ordinary application decoder errors retain their cause
// without becoming input problems. [Strict] returns
// [ErrStrictUnsupported] without reading the body.
//
// Only the first XML element is decoded; trailing data is ignored.
// [encoding/xml] does not expand DTD entities, so entity expansion attacks
// such as "Billion Laughs" do not apply.
//
// Example:
//
//	feed, err := request.XML[Feed](r, request.MaxSize(1<<20))
//	if err != nil {
//		return err
//	}
//	_ = feed
func XML[T any](request *http.Request, opts ...BodyOption) (value T, err error) {
	config := newBodyConfig(opts)

	if config.strict {
		return value, ErrStrictUnsupported
	}

	if err := xml.NewDecoder(config.body(request)).Decode(&value); err != nil {
		return value, invalidXML(err)
	}

	return value, nil
}

// invalidXML maps native XML syntax and scalar conversion errors to the input
// problem, preserving operational failures, existing problems, and size limits.
func invalidXML(err error) error {
	if _, ok := errors.AsType[problem.Details](err); ok {
		return err
	}

	if overLimit(err) {
		return ErrBodyTooLarge
	}

	var syntax *xml.SyntaxError
	var unmarshal xml.UnmarshalError
	var numeric *strconv.NumError
	invalid := errors.As(err, &syntax) || errors.As(err, &unmarshal) || errors.As(err, &numeric)

	if errors.Is(err, io.EOF) || invalid {
		return ErrInvalidXML.Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	}

	return err
}
