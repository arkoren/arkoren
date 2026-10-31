package semconv

// WorkflowOperation identifies a fixed client operation or step attempt, never
// a workflow/run identifier.
//
// Example: telemetry.String(semconv.WorkflowOperation, "start")
const WorkflowOperation = "arkoren.workflow.operation"

// WorkflowOutcome distinguishes accepted, already-started and failed calls.
// A duplicate start is an expected outcome, not a failed operation.
//
// Example: telemetry.String(semconv.WorkflowOutcome, "already_started")
const WorkflowOutcome = "arkoren.workflow.outcome"

// WorkflowOperationCount counts client calls and actual step-handler attempts.
// Its labels exclude names and identifiers to bound cardinality.
//
// Example: meter.Counter(semconv.WorkflowOperationCount)
const WorkflowOperationCount = "arkoren.workflow.operation.count"

// WorkflowOperationDuration measures observed call/attempt duration in seconds,
// not durable workflow execution time.
//
// Example: meter.Histogram(semconv.WorkflowOperationDuration, telemetry.Unit("s"))
const WorkflowOperationDuration = "arkoren.workflow.operation.duration"
