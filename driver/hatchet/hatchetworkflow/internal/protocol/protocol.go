// Package protocol owns Hatchet exact payload strings and physical signal identities.
package protocol

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"arkoren.dev/core/service/workflow"
)

// Wire is the JSON object that carries every input, output, and signal. The
// facade payload travels as a string, not as json.RawMessage: the SDK decodes
// a task input into map[string]any, which turns large numbers into float64 and
// would corrupt a nested payload.
type Wire struct {
	// Trace is the explicit start carrier, separate from AdditionalMetadata.
	// Durable orchestration forwards it as data; actual tasks extract it.
	Trace map[string]string `json:"trace,omitempty"`
	// Data is the facade envelope made by workflow.EncodeValue.
	Data string `json:"data"`

	// ID is the run identifier.
	ID string `json:"id,omitempty"`

	// Key is the idempotency key of the run.
	Key string `json:"key,omitempty"`

	// Marker is the id of the run that holds the first value of a signal.
	Marker string `json:"marker,omitempty"`

	// SignalVersion declares the physical-execution signal and durable-wait
	// protocol. Workflow inputs, markers, and events must use signalVersion;
	// older versions fail closed rather than changing recorded wait conditions
	// or replaying logical-only signal scopes during an incompatible upgrade.
	SignalVersion int `json:"signal_version,omitempty"`

	// ExecutionID owns a signal marker or event. It is the engine workflow-run
	// ID, never the reusable logical ID or a worker attempt ID. Workflow inputs
	// omit it because Hatchet assigns that ID only after submission.
	ExecutionID string `json:"execution_id,omitempty"`

	// Delay postpones the body of the run, in nanoseconds.
	Delay int64 `json:"delay,omitempty"`

	// Timeout overrides the step timeout, in nanoseconds.
	Timeout int64 `json:"timeout,omitempty"`

	// Attempts overrides the number of step attempts.
	Attempts int `json:"attempts,omitempty"`

	// RetryWithoutDelay carries a completed per-call zero backoff when the
	// registered policy is positive. Absence retains historical registration
	// behavior. Upgrade workers before producing this field; old workers ignore
	// it. Existing affected runs must drain before their replay payload changes.
	RetryWithoutDelay bool `json:"retry_without_delay,omitempty"`
}

// Failure turns the text of a failed run or task into a facade error. The
// text holds the envelope of workflow.EncodeError when the failure came from
// this driver; otherwise it is an engine message.
func Failure(text string) error {
	if raw, ok := Envelope(text); ok {
		return workflow.DecodeError(raw)
	}

	lower := strings.ToLower(text)

	if strings.Contains(lower, "timed out") || strings.Contains(lower, "timed_out") {
		return fmt.Errorf("%w: %s", workflow.ErrTimeout, text)
	}

	return workflow.NewError("", text)
}

// unescape reverses the JSON string quoting of an envelope that the engine
// embedded in a longer message.
var unescape = strings.NewReplacer(`\"`, `"`, `\\`, `\`)

// Envelope finds the error envelope inside text, which may quote it. It
// decodes the first JSON value that starts at the marker, so text after it
// does not matter.
func Envelope(text string) ([]byte, bool) {
	for _, marker := range []string{`{"kind":`, `{\"kind\":`} {
		for offset := 0; ; {
			index := strings.Index(text[offset:], marker)

			if index < 0 {
				break
			}

			rest := text[offset+index:]

			if strings.HasPrefix(marker, `{\`) {
				rest = unescape.Replace(rest)
			}

			if value, err := jsontext.NewDecoder(strings.NewReader(rest)).ReadValue(); err == nil {
				return slices.Clone(value), true
			}

			offset += index + 1
		}
	}

	return nil, false
}

// EncodeFailure returns an error whose text is the facade envelope of err.
func EncodeFailure(err error) error {
	return errors.New(string(workflow.EncodeError(err)))
}

// OutputOf decodes the output of a run into the facade payload.
func OutputOf(raw []byte) ([]byte, error) {
	var out Wire

	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode run output: %w", err)
		}
	}

	return []byte(out.Data), nil
}

// SignalVersion identifies private wire data that isolates signals by physical
// execution and uses a fixed historical-event boundary. Version one used a
// worker-relative cutoff; replaying its waits with new conditions is unsafe.
// Neither it nor version zero's reusable logical scopes are accepted.
const SignalVersion = 2

// SignalScope binds the verified logical key to one engine workflow-run ID.
// Senders use the ID proved by find; workers use DurableContext.WorkflowRunId,
// which remains stable across eviction/restart. Separate base64url components
// preserve the namespace boundary and exclude logical reuse after TTL expiry.
// Callers must supply a nonempty engine ID, never a task or invocation ID.
func SignalScope(key, executionID string) string {
	encode := base64.RawURLEncoding.EncodeToString

	return "arkoren.workflow.signal.v1." + encode([]byte(key)) + "." + encode([]byte(executionID))
}

// SignalMarkerKey identifies a single signal within a physical event scope.
// Encoding the name avoids delimiter ambiguity without depending on facade
// validation, since the raw driver can also be called directly.
func SignalMarkerKey(scope, signal string) string {
	return scope + "." + base64.RawURLEncoding.EncodeToString([]byte(signal))
}

// RunKey returns the versioned tenant-wide workflow idempotency key.
// Each identity component is encoded separately with a separator outside the
// base64url alphabet, so empty namespaces and punctuation cannot collide.
// The legacy colon-joined format is intentionally neither read nor written.
func RunKey(namespace, prefix, name, id string) string {
	encode := base64.RawURLEncoding.EncodeToString

	return "arkoren.workflow.v2." + encode([]byte(namespace)) + "." +
		encode([]byte(prefix)) + "." + encode([]byte(name)) + "." + encode([]byte(id))
}
