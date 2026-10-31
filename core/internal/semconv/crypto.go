package semconv

const (
	// CryptoOperation labels the bounded encrypt and decrypt operations.
	// Example: telemetry.String(semconv.CryptoOperation, "decrypt").
	CryptoOperation = "arkoren.crypto.operation"
	// CryptoOutcome distinguishes success, successful fallback and final failure.
	// Example: telemetry.String(semconv.CryptoOutcome, "previous_key").
	CryptoOutcome = "arkoren.crypto.outcome"
	// CryptoOperations counts complete facade operations, not key attempts.
	// Example: meter.Counter(semconv.CryptoOperations).
	CryptoOperations = "arkoren.crypto.operations"
	// CryptoOperationDuration measures complete facade calls in seconds.
	// Example: meter.Histogram(semconv.CryptoOperationDuration, telemetry.Unit("s")).
	CryptoOperationDuration = "arkoren.crypto.operation.duration"
)
