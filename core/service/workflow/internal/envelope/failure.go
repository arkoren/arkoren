package envelope

import "encoding/json/v2"

// Failure is the persisted failure representation, not a Go error identity.
type Failure struct {
	// Kind identifies the workflow error across driver boundaries.
	Kind string `json:"kind"`
	// Message is persisted application data, not safe telemetry text.
	Message string `json:"message"`
	// Permanent preserves the instruction not to retry the failure.
	Permanent bool `json:"permanent"`
}

// EncodeFailure preserves the wire field order and historical fallback bytes.
func EncodeFailure(failure Failure) []byte {
	out, err := json.Marshal(failure)

	if err != nil {
		return []byte(`{"kind":"","message":"encode error failed","permanent":false}`)
	}

	return out
}

// DecodeFailure parses a wire failure without interpreting application identity.
func DecodeFailure(data []byte) (Failure, error) {
	var failure Failure

	err := json.Unmarshal(data, &failure)

	return failure, err
}
