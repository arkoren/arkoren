package workflow

import (
	"context"
	"errors"

	"arkoren.dev/core/service/workflow/internal/envelope"
)

// EncodeValue encodes v with codec inside the envelope {"v": ...}. A nil
// codec uses [JSON]. The output of the codec must be valid JSON; it is not
// re-validated.
// Empty codec output is carried as JSON null; the returned envelope owns its
// bytes independently of the codec's buffer.
//
//	data, err := workflow.EncodeValue(nil, "welcome")
//	if err != nil {
//		return err
//	}
//	_ = data // {"v":"welcome"}
func EncodeValue(codec Codec, v any) ([]byte, error) {
	if codec == nil {
		codec = JSON{}
	}

	return envelope.Encode(codec, v)
}

// DecodeValue decodes an envelope made by [EncodeValue] into v, which is a
// pointer. Empty data leaves v unchanged. Data that is not an envelope, or
// has no "v" member, is an error. A nil codec uses [JSON].
// JSON null is a present value, including in whitespace-formatted envelopes.
//
//	var greeting string
//	if err := workflow.DecodeValue(nil, []byte(`{"v":"welcome"}`), &greeting); err != nil {
//		return err
//	}
//	fmt.Println(greeting)
func DecodeValue(codec Codec, data []byte, v any) error {
	if codec == nil {
		codec = JSON{}
	}

	return envelope.Decode(codec, data, v)
}

// EncodeError encodes err as {"kind","message","permanent"} so a driver can
// carry it across the wire. The kind and message come from the first [*Error]
// in the chain, so wrapping context is dropped. Without one, [context.Canceled]
// maps to the kind of [ErrCanceled] and [context.DeadlineExceeded] to the
// kind of [ErrTimeout]. EncodeError(nil) is nil.
// Only the serialized kind and message cross the boundary, not arbitrary Go
// error identities. Keep secrets out of messages before calling this function.
//
//	failure := workflow.NewError("billing.card-declined", "payment declined")
//	encoded := workflow.EncodeError(workflow.Permanent(failure))
//	_ = encoded // Store or transport this envelope, not the original cause.
func EncodeError(err error) []byte {
	if err == nil {
		return nil
	}

	failure := envelope.Failure{
		Message:   err.Error(),
		Permanent: errors.Is(err, ErrPermanent),
	}

	var typed *Error

	switch {
	case errors.As(err, &typed):
		failure.Kind = typed.Kind
		failure.Message = typed.Error()
	case errors.Is(err, context.Canceled):
		failure.Kind = ErrCanceled.Kind
	case errors.Is(err, context.DeadlineExceeded):
		failure.Kind = ErrTimeout.Kind
	}

	return envelope.EncodeFailure(failure)
}

// DecodeError restores an error from [EncodeError]. The result is an [*Error]
// that matches the sentinels such as [ErrCanceled] and [ErrTimeout] through
// [errors.Is], and is marked with [Permanent] when the original was. Data that
// is not an envelope becomes an [*Error] with the data as message.
// DecodeError of empty data is nil.
// Decoded messages remain persisted application data, not safe telemetry text.
//
//	failure := workflow.DecodeError(workflow.EncodeError(workflow.ErrTimeout))
//	if errors.Is(failure, workflow.ErrTimeout) {
//		return failure
//	}
func DecodeError(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	failure, err := envelope.DecodeFailure(data)

	if err != nil {
		return NewError("", string(data))
	}

	var restored error = NewError(failure.Kind, failure.Message)

	if failure.Permanent {
		restored = Permanent(restored)
	}

	return restored
}
