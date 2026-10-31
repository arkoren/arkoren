package validate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"arkoren.dev/foundation/validate/internal/fields"
)

// segment is one step of the path to the value being validated.
type segment struct {
	// name is the raw struct-field or map-key segment before JSON Pointer
	// escaping.
	name string
	// index is the numeric slice index when array is true.
	index int
	// array selects numeric index rendering instead of named-segment rendering.
	array bool
}

// state carries the data of a single Validate or Parse call. It is never
// shared between calls.
type state struct {
	// ctx provides cancellation, localization and clock values for this
	// execution.
	ctx context.Context
	// segments is the active path stack used to build diagnostic JSON Pointers.
	segments []segment
	// cursors is the per-call cursor stack, avoiding an allocation for each node.
	cursors []cursor
	// errs collects localized violations by JSON Pointer during this call.
	errs Errors
	// count counts recorded violations so steps can detect whether they added a
	// failure.
	count int
	// fatal retains the first operational error and stops further validation.
	fatal error
}

// stopped records request cancellation without replacing an earlier operational
// failure. Every execution and preparation boundary checks it before invoking
// more application code; cancellation discards partial private results.
func (state *state) stopped() bool {
	if state.fatal == nil {
		state.fatal = state.ctx.Err()
	}

	return state.fatal != nil
}

// push enters a named child such as a struct field or map key.
func (state *state) push(name string) {
	state.segments = append(state.segments, segment{
		name: name,
	})
}

// pushIndex enters a slice element.
func (state *state) pushIndex(index int) {
	state.segments = append(state.segments, segment{
		index: index,
		array: true,
	})
}

// pop leaves the child entered by the last push.
func (state *state) pop() {
	state.segments = state.segments[:len(state.segments)-1]
}

// pointer builds the RFC 6901 JSON Pointer of the current path, plus an
// optional extra field name. Paths are only built when a violation is
// recorded.
func (state *state) pointer(extra string) string {
	var builder strings.Builder

	for _, current := range state.segments {
		builder.WriteByte('/')

		if current.array {
			builder.WriteString(strconv.Itoa(current.index))
		} else {
			builder.WriteString(fields.EscapePointer(current.name))
		}
	}

	if extra != "" {
		builder.WriteByte('/')
		builder.WriteString(fields.EscapePointer(extra))
	}

	return builder.String()
}

// violate records a violation at the current path. Parameter containers are
// detached by violateAt; opaque values retain Fail's documented copying limits.
func (state *state) violate(code string, params map[string]any) {
	state.violateAt("", code, params)
}

// violateAt records a violation at the current path extended by the JSON
// name of a child field. It snapshots supported parameter containers before
// rendering and publication so mutable diagnostics cannot expose schema state
// or reusable failure parameters. Nil maps remain nil; opaque values retain
// Fail's documented copying limits. Callers may retain the supplied containers
// but must not mutate them concurrently with this call.
func (state *state) violateAt(extra, code string, params map[string]any) {
	if state.errs == nil {
		state.errs = make(Errors)
	}

	params = cloneParams(params)
	state.errs.Add(state.pointer(extra), Violation{
		Code:    code,
		Params:  params,
		Message: message(state.ctx, code, params),
	})
	state.count++
}

// handle converts the error returned by a check function. It reports
// whether a violation was recorded. Errors that are not failures abort
// validation and are returned to the caller unchanged. Validation failures
// reach violateAt's common publication boundary, which detaches supported
// containers from reusable Fail and At errors before exposing diagnostics.
// At requires an object resolver; without one its field is unresolved and
// returns ErrInvalidSchema rather than becoming a root violation.
func (state *state) handle(err error, resolve func(string) (string, bool)) bool {
	if err == nil {
		return false
	}

	fail, ok := errors.AsType[*failure](err)

	if !ok {
		state.fatal = err

		return true
	}

	if fail.field == "" {
		state.violate(fail.code, fail.params)

		return true
	}

	var name string

	ok = false

	if resolve != nil {
		name, ok = resolve(fail.field)
	}

	if !ok {
		state.fatal = fmt.Errorf("%w: At(%q) does not name a field of the enclosing object", ErrInvalidSchema, fail.field)

		return true
	}

	state.violateAt(name, fail.code, fail.params)

	return true
}

// result returns the outcome of the call as an error, or a literal nil.
func (state *state) result() error {
	if state.fatal != nil {
		return state.fatal
	}

	if len(state.errs) > 0 {
		return state.errs
	}

	return nil
}

// report collects every problem found while building a schema, so a
// single panic can list them all.
type report struct {
	// lines collects construction diagnostics until one combined schema error is
	// reported.
	lines []string
	// defaults enables construction-time verification of configured defaults.
	defaults bool
}

// addf records a problem found at where.
func (report *report) addf(where, format string, args ...any) {
	text := fmt.Sprintf(format, args...)

	if where != "" {
		text = where + ": " + text
	}

	report.lines = append(report.lines, text)
}

// check panics with an error wrapping [ErrInvalidSchema] when problems
// were recorded.
func (report *report) check(subject string) {
	if len(report.lines) == 0 {
		return
	}

	panic(fmt.Errorf("%w: %s:\n  - %s", ErrInvalidSchema, subject, strings.Join(report.lines, "\n  - ")))
}

// invalidf panics with an error wrapping [ErrInvalidSchema].
func invalidf(format string, args ...any) {
	panic(fmt.Errorf("%w: %s", ErrInvalidSchema, fmt.Sprintf(format, args...)))
}
