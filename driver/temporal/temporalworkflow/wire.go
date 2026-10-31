package temporalworkflow

import (
	"arkoren.dev/driver/temporal/temporalworkflow/internal/protocol"
)

// payload retains the raw JSON converter used at the native adapter boundary.
type payload = protocol.Payload

// errTerminated retains the identity used by native result observations.
var errTerminated = protocol.ErrTerminated

// encodeInput forwards the native raw JSON envelope without decoding values.
func encodeInput(in payload, carrier map[string]string) (payload, error) {
	return protocol.EncodeInput(in, carrier)
}

// decodeInput accepts traced and legacy inputs at registration boundaries.
func decodeInput(in payload) (payload, map[string]string, error) {
	return protocol.DecodeInput(in)
}

// failure preserves the native failure envelope at adapter boundaries.
func failure(err error) error { return protocol.Failure(err) }

// fromTemporal maps native errors in client operations and observations.
func fromTemporal(err error) error { return protocol.FromTemporal(err) }
