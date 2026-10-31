package restateworkflow

import (
	"arkoren.dev/driver/restate/restateworkflow/internal/protocol"
)

// traceHeaders encodes propagation separately from application metadata.
func traceHeaders(carrier map[string]string) map[string]string { return protocol.TraceHeaders(carrier) }
