package semconv

const (
	// HashOperation labels the bounded hash, check and needs_rehash operations.
	// Example: telemetry.String(semconv.HashOperation, "check").
	HashOperation = "arkoren.hash.operation"
	// HashOutcome distinguishes success, mismatch, policy observations and errors.
	// Example: telemetry.String(semconv.HashOutcome, "mismatch").
	HashOutcome = "arkoren.hash.outcome"
	// HashOperations counts complete facade operations, not nested driver calls.
	// Example: meter.Counter(semconv.HashOperations).
	HashOperations = "arkoren.hash.operations"
	// HashOperationDuration measures complete facade calls in seconds.
	// Example: meter.Histogram(semconv.HashOperationDuration, telemetry.Unit("s")).
	HashOperationDuration = "arkoren.hash.operation.duration"
)
