package hatchetworkflow

import (
	"arkoren.dev/driver/hatchet/hatchetworkflow/internal/protocol"
)

// wire retains the native string-preserving protocol record at the adapter boundary.
type wire = protocol.Wire

// failure decodes native failure text without changing its error identity.
func failure(text string) error { return protocol.Failure(text) }

// outputOf decodes an exact facade payload from the native output record.
func outputOf(raw []byte) ([]byte, error) { return protocol.OutputOf(raw) }

// encodeFailure preserves the exact facade failure string at task boundaries.
func encodeFailure(err error) error { return protocol.EncodeFailure(err) }
