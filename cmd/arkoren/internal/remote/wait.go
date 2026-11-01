package remote

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren/internal/console"
)

const (
	// waitDefaultTimeout bounds the complete condition-wait command, including
	// discovery. It is not the per-poll duration used by cursor tools.
	waitDefaultTimeout = 30 * time.Second
	// waitInitialBackoff keeps the first connection retry responsive.
	waitInitialBackoff = 50 * time.Millisecond

	// waitBackoffFactor grows connection retry delays without long pauses.
	waitBackoffFactor = 1.5

	// waitMaximumBackoff bounds latency when an inspector becomes available.
	waitMaximumBackoff = 500 * time.Millisecond
)

// WaitBudget belongs to one command, not one HTTP request.
type WaitBudget struct {
	// Timeout includes discovery, connection retries, and server-side waiting.
	Timeout time.Duration

	// Deadline is shared by every phase and every retry.
	Deadline time.Time
}

// prepareWait derives only a provisional discovery hint from conventional-looking
// flags. Normal advertised-schema parsing owns the authoritative request and its
// execution budget. Sources are materialized early only when their ownership is
// unambiguous throughout the argument list; other sources wait until discovery.
// Cursor follow has its own per-poll wait convention and never receives a
// condition-wait deadline, even when base input contains boolean wait=true.
func (session *Session) prepareWait(ctx context.Context, args []string) (*WaitBudget, []string, error) {
	args = append([]string(nil), args...)
	selected, err := session.waitArguments(ctx, args)

	if err != nil {
		return nil, nil, err
	}

	if session.flags.Follow {
		return nil, args, nil
	}

	intent, err := session.waitIntent(selected)

	if err != nil {
		return nil, nil, err
	}

	if intent["wait"] != true {
		return nil, args, nil
	}

	timeout, err := conditionWaitTimeout(intent["timeout"])

	if err != nil {
		// Until discovery, these names can belong to a non-wait tool. Only a
		// successfully parsed, schema-declared request can reject its duration.
		return nil, args, nil
	}

	return &WaitBudget{Timeout: timeout, Deadline: time.Now().Add(timeout)}, args, nil
}

// waitArguments materializes only the safely owned final discovery input.
// The pure hint scan cannot consume a schema-owned value or trigger source I/O;
// sources whose ownership is uncertain remain for normal operation parsing.
func (session *Session) waitArguments(ctx context.Context, args []string) ([]string, error) {
	selected, input, source := scanWaitHints(args)

	if input == nil {
		return selected, nil
	}

	value, err := session.materializeWaitInput(ctx, input, source)

	if err != nil {
		return nil, err
	}

	return append(selected, "-input="+value), nil
}

// scanWaitHints observes nonbinding connection hints without consuming the
// invocation's tokens. Unknown flags/positionals or bare wait make source
// ownership uncertain: no source is then read before schema-guided parsing.
// Conventional-looking values can at most affect discovery retry timing, never
// tool input, execution deadlines, or replay eligibility. Syntax failures are
// left to the authoritative schema parser after discovery.
func scanWaitHints(args []string) ([]string, []string, string) {
	selected := make([]string, 0, len(args))
	var input []string
	var source string
	uncertain := false

	for index := 0; index < len(args); index++ {
		start := index
		name, value, next, err := parseWaitArgument(args, index)

		if err != nil {
			return selected, nil, ""
		}

		index = next

		if name == "" {
			uncertain = true
			continue
		}

		bareWait := name == "wait" && !strings.Contains(args[start], "=")
		uncertain = uncertain || bareWait

		if name == "input" {
			input, source = args[start:index+1], value
			continue
		}

		if name == "wait" && !slices.Contains([]string{"true", "false"}, value) {
			continue
		}

		selected = append(selected, "-"+name+"="+value)
	}

	if uncertain {
		return selected, nil, ""
	}

	return selected, input, source
}

// parseWaitArgument recognizes one conventional flag without consuming unrelated
// operation flags or positionals. The returned index includes a consumed value;
// -- advances to the end so no following token can affect wait intent.
func parseWaitArgument(args []string, index int) (string, string, int, error) {
	arg := args[index]

	if arg == "--" {
		return "", "", len(args), nil
	}

	if !strings.HasPrefix(arg, "-") {
		return "", "", index, nil
	}

	name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")

	if !slices.Contains([]string{"wait", "timeout", "input"}, name) {
		return "", "", index, nil
	}

	if inline {
		return name, value, index, nil
	}

	if name == "wait" {
		if index+1 == len(args) {
			return name, "true", index, nil
		}

		if !slices.Contains([]string{"true", "false"}, args[index+1]) {
			return name, "true", index, nil
		}
	}

	if index+1 == len(args) {
		return "", "", index, console.UsageError("-%s needs a value", name)
	}

	return name, args[index+1], index + 1, nil
}

// materializeWaitInput reads a source once and replaces its original spelling
// in the owned slice containing only this flag and its optional value. Later
// Schema conversion sees the same JSON even for stdin, which cannot be rewound.
// Cancellation returns without closing stdin; its owner must release a blocked
// generic reader before that read goroutine can finish.
func (session *Session) materializeWaitInput(ctx context.Context, args []string, source string) (string, error) {
	body, err := session.readOperationSource(ctx, source)

	if err != nil {
		return "", err
	}

	value := string(body)
	args[len(args)-1] = value

	if len(args) == 1 {
		args[0] = "-input=" + value
	}

	return value, nil
}

// readOperationSource materializes a schema-owned input source without taking
// ownership of stdin. File and inline inputs retain readInputSource semantics;
// caller cancellation also interrupts waiting for a generic stdin reader.
func (session *Session) readOperationSource(ctx context.Context, source string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if source == "-" {
		return readCommandStdin(ctx, session.env.Stdin)
	}

	return readInputSource(source, session.env.Dir, session.env.Stdin)
}

// readCommandStdin waits for caller-owned input or command cancellation. It
// never closes the reader: Go cannot interrupt an arbitrary Reader. A canceled
// command leaves one read goroutine until the owner supplies EOF or an error;
// channel close publishes its result without leaving a blocked sender behind.
func readCommandStdin(ctx context.Context, input io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var body []byte
	var err error
	done := make(chan struct{})

	go func() {
		body, err = io.ReadAll(input)
		close(done)
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
		return body, err
	}
}

// waitIntent applies ordinary CLI input precedence to conventional fields only.
// It does not validate the eventual tool's schema or infer its semantic role.
// Other properties remain raw JSON, including numbers outside float64's range.
func (session *Session) waitIntent(selected []string) (map[string]any, error) {
	root := ParseSchema([]byte(`{"type":"object","properties":{"wait":{"type":"boolean"},"timeout":{"type":"string"}}}`))
	parsed, err := parseOpArgs(root, "wait", selected)

	if err != nil {
		return nil, err
	}

	if parsed.hasInput {
		// waitArguments supplied already-materialized bytes, never a new source.
		parsed.body = []byte(parsed.input)
	}

	input, err := buildInput(root, nil, parsed, session.env.Dir, session.env.Stdin)

	if err != nil {
		return nil, err
	}

	return decodeWaitIntent(input.body)
}

// decodeWaitIntent reads only the conventional members of an already encoded
// object. It neither traverses unrelated domain values nor validates tool input.
func decodeWaitIntent(body []byte) (map[string]any, error) {
	var fields struct {
		// Wait opts into the convention only for the exact JSON boolean true.
		Wait jsontext.Value `json:"wait"`
		// Timeout is interpreted only when it is a JSON string.
		Timeout jsontext.Value `json:"timeout"`
	}

	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, console.UsageError("wait input: %v", err)
	}

	intent := map[string]any{"wait": string(fields.Wait) == "true"}

	if fields.Timeout.Kind() != '"' {
		return intent, nil
	}

	var timeout string

	if err := json.Unmarshal(fields.Timeout, &timeout); err != nil {
		return nil, console.UsageError("wait timeout: %v", err)
	}

	intent["timeout"] = timeout

	return intent, nil
}

// conditionWaitTimeout normalizes omitted/zero timeouts to the shared default.
// A supplied string must be a nonnegative Go duration; non-string base JSON is
// left to the discovered tool's validator rather than rejected prematurely.
func conditionWaitTimeout(raw any) (time.Duration, error) {
	value, ok := raw.(string)

	if !ok || value == "" {
		return waitDefaultTimeout, nil
	}

	duration, err := time.ParseDuration(value)

	if err != nil || duration < 0 {
		return 0, console.UsageError("-timeout: want a non-negative duration")
	}

	return cmp.Or(duration, waitDefaultTimeout), nil
}

// discover retries only transport failures during SDK connection and tools/list.
// No HTTP transport retry policy can accidentally replay tools/call actions.
func (session *Session) discover(ctx context.Context, client *Client) (*Catalog, error) {
	delay := waitInitialBackoff

	for {
		manifest, err := client.FetchManifest(ctx)

		if err == nil {
			return manifest, nil
		}

		if session.wait == nil || !retryableTransport(err) {
			return nil, err
		}

		if err := waitRetry(ctx, delay); err != nil {
			return nil, err
		}

		delay = min(time.Duration(float64(delay)*waitBackoffFactor), waitMaximumBackoff)
	}
}

// retryableTransport rejects HTTP, protocol, tool, and certificate failures.
// Only broken connections can be retried by discovery or an explicit wait read.
func retryableTransport(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return false
	}

	if _, ok := errors.AsType[*APIError](err); ok {
		return false
	}

	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}

	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// waitRetry pauses between observation attempts and promptly honors the shared
// command deadline. The timer is always released, including cancellation.
func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Remote discovers and invokes an operation with an optional shared wait budget.
func (session *Session) Remote(ctx context.Context, command string, args []string) error {
	if session.flags.Help {
		conn, err := session.connect(ctx)

		if err != nil {
			return err
		}

		defer conn.close()

		return session.remoteHelp(conn, command)
	}

	startedAt := time.Now()
	budget, args, err := session.prepareWait(ctx, args)

	if err != nil {
		return err
	}

	active := *session
	active.wait = budget

	conn, err := active.connectForDiscovery(ctx)

	if err != nil {
		return WaitError(ctx, budget, "connecting to the inspector", err)
	}

	conn.configureRequests(ctx, session.requestTimeout())

	var cancel context.CancelFunc
	defer func() {
		conn.close()

		if cancel != nil {
			cancel()
		}
	}()

	_, operation, err := conn.resolve(command)

	if err != nil {
		return err
	}

	input, err := active.prepareOperation(ctx, operation, args)

	if err != nil {
		return err
	}

	budget, err = active.operationWait(operation, input, startedAt)

	if err != nil {
		return err
	}

	active.wait = budget
	ctx, cancel = budget.context(ctx)
	conn.configureRequests(ctx, active.requestTimeout())

	err = active.invoke(ctx, conn, operation, input)

	return WaitError(ctx, budget, "waiting for "+command, err)
}

// connectForDiscovery applies a known leading wait budget only to discovery.
// Its timer is released after connection establishment; it cannot accidentally
// shorten a tool call whose advertised input later proves to have other meaning.
func (session *Session) connectForDiscovery(ctx context.Context) (*connection, error) {
	ctx, cancel := session.wait.context(ctx)
	defer cancel()

	return session.connect(ctx)
}

// context gives one phase an owned descendant of the caller's context. A nil
// budget retains cancellation without imposing a deadline; a real budget uses
// its existing absolute deadline rather than granting another timeout period.
// The invocation owner releases it only after closing its SDK session, while
// discovery releases its separate phase context after catalog establishment.
func (budget *WaitBudget) context(ctx context.Context) (context.Context, context.CancelFunc) {
	if budget == nil {
		return context.WithCancel(ctx)
	}

	return context.WithDeadline(ctx, budget.Deadline)
}

// operationWait establishes the authoritative budget from normal schema-guided
// input parsing. Elapsed discovery and input time count against that one budget;
// actions, cursor follow, and non-boolean wait declarations never enable it.
func (session *Session) operationWait(operation *Operation, input *requestInput, startedAt time.Time) (*WaitBudget, error) {
	if session.flags.Follow || !WaitingRead(operation, input.root, input.body) {
		return nil, nil
	}

	intent, err := decodeWaitIntent(input.body)

	if err != nil {
		return nil, err
	}

	property := input.root.Properties["timeout"]

	if property == nil || property.kind() != "string" {
		delete(intent, "timeout")
	}

	timeout, err := conditionWaitTimeout(intent["timeout"])

	if err != nil {
		return nil, err
	}

	return &WaitBudget{Timeout: timeout, Deadline: startedAt.Add(timeout)}, nil
}

// WaitError distinguishes connection exhaustion from an unsatisfied condition,
// while preserving HTTP errors such as an explicit fail-fast response.
func WaitError(ctx context.Context, budget *WaitBudget, phase string, err error) error {
	_, response := errors.AsType[*APIError](err)
	expired := errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)

	if err != nil && !response && budget != nil && expired && !time.Now().Before(budget.Deadline) {
		return fmt.Errorf("timed out after %s %s: %w", budget.Timeout, phase, context.DeadlineExceeded)
	}

	return err
}

// remainingWait sets the operation timeout from the shared remaining budget.
// Only a declared string timeout participates; other types remain tool inputs.
// The input is already an object from buildInput; preserving its scalar JSON
// text prevents a timeout update from rounding unrelated numeric properties.
// Explicit non-string timeout values remain unchanged for server validation.
func remainingWait(root *Schema, input *requestInput, budget *WaitBudget) error {
	if budget == nil || root.Properties["timeout"] == nil {
		return nil
	}

	if root.Properties["timeout"].kind() != "string" {
		return nil
	}

	remaining := time.Until(budget.Deadline)

	if remaining <= 0 {
		return context.DeadlineExceeded
	}

	var body map[string]jsontext.Value

	if err := json.Unmarshal(input.body, &body); err != nil {
		return err
	}

	if value := body["timeout"]; len(value) > 0 && value.Kind() != '"' {
		return nil
	}

	body["timeout"] = jsontext.Value(strconv.Quote(remaining.String()))

	encoded, err := json.Marshal(body, json.Deterministic(true))

	if err != nil {
		return err
	}

	input.body = encoded

	return nil
}
