package validate

import (
	"context"
	"reflect"
)

// cursor is the mutable view of the value a node is checking.
type cursor struct {
	// v is the current reflected value, copied before normalization when
	// necessary.
	v reflect.Value
	// obj is the enclosing object used by sibling conditions, or an invalid Value
	// outside objects.
	obj reflect.Value
	// present tracks presence after preparation, defaults and exclusions.
	present bool
	// force treats zero collection elements and pointees as present.
	force bool
}

// step is one compiled rule. It reports whether it recorded a violation.
type step func(st *state, c *cursor) bool

// node is a compiled schema. Nodes are immutable and shared between
// concurrent calls; all per-call data lives in the state.
type node struct {
	// steps holds compiled operations in the original builder order.
	steps []step
	// def assigns a configured default to an absent, settable value; nil means no
	// default.
	def func(v reflect.Value)
	// pre prepares child values before the parent normalization and presence
	// checks.
	pre func(st *state, v reflect.Value)
	// norm applies the compiled normalizer chain to a settable value.
	norm func(st *state, v reflect.Value)
	// bail stops rule execution for this value after its first violation.
	bail bool
	// always runs child checks even when the container value would otherwise
	// count as absent.
	always bool
	// mut marks nodes that need copied storage before preparation changes a
	// value.
	mut bool
	// child executes nested child validation after the current node rules.
	child func(st *state, v reflect.Value)
}

// applyDefault sets the default on v when v is absent.
func (node *node) applyDefault(st *state, v reflect.Value) {
	node.prepare(st, v)

	if st.stopped() {
		return
	}

	if node.def != nil && isAbsent(v) && v.CanSet() {
		node.def(v)

		if node.pre != nil {
			node.pre(st, v)
		}
	}
}

// prepare normalizes v once: first the elements or values it holds (pre),
// then the node's own normalizers.
func (node *node) prepare(st *state, v reflect.Value) {
	if st.stopped() {
		return
	}

	if node.pre != nil && v.CanSet() {
		node.pre(st, v)
	}

	node.applyNorm(st, v)
}

// applyNorm runs the normalizers on v before presence is decided.
func (node *node) applyNorm(st *state, v reflect.Value) {
	if st.stopped() {
		return
	}

	if node.norm != nil && v.CanSet() {
		node.norm(st, v)
	}
}

// run checks v. obj is the enclosing struct, or the zero Value outside an
// object. force marks v present regardless of its value, which is how
// elements and pointees are treated. Fatal state stops execution before any
// preparation callback or mutation; otherwise preparation runs exactly once.
func (node *node) run(st *state, v, obj reflect.Value, force bool) {
	if st.stopped() {
		return
	}

	node.prepare(st, v)
	node.runPrepared(st, v, obj, force)
}

// runPrepared checks v after its caller has prepared it. It never repeats child
// preparation or normalization; defaults, presence, rules, and child checks
// retain their normal execution order. Fatal state skips all execution.
func (node *node) runPrepared(st *state, v, obj reflect.Value, force bool) {
	if st.stopped() {
		return
	}

	st.cursors = append(st.cursors, cursor{
		v:       v,
		obj:     obj,
		force:   force,
		present: force || !isAbsent(v),
	})
	depth := len(st.cursors) - 1

	node.runSteps(st, &st.cursors[depth])

	st.cursors = st.cursors[:depth]
}

// runSteps runs the compiled steps and the child check against c, which
// lives on the state's cursor stack to avoid a heap allocation per node.
func (node *node) runSteps(st *state, c *cursor) {
	v := c.v

	if !c.present && node.def != nil && v.CanSet() {
		node.def(v)

		if node.pre != nil {
			node.pre(st, v)
		}

		c.present = true
	}

	if !node.runRules(st, c) {
		return
	}

	if node.child != nil && (c.present || node.always) {
		node.child(st, c.v)
	}
}

// runRules executes the declared rules until completion, cancellation, an
// operational failure or Bail. The boolean permits child validation only when
// all rules completed; cancellation is observed even when a Check returns nil.
func (node *node) runRules(st *state, c *cursor) bool {
	for _, run := range node.steps {
		if st.stopped() {
			return false
		}

		failed := run(st, c)

		if st.stopped() || (failed && node.bail) {
			return false
		}
	}

	return !st.stopped()
}

// execute validates value with the compiled node and returns the value
// with defaults applied, or the error describing the failure.
func execute[T any](ctx context.Context, n *node, value T) (T, error) {
	st := &state{
		ctx: ctx,
	}

	n.run(st, reflect.ValueOf(&value).Elem(), reflect.Value{}, false)

	if err := st.result(); err != nil {
		var zero T

		return zero, err
	}

	return value, nil
}

// compileStandalone compiles a schema used outside a Struct, panicking
// when it is invalid.
func compileStandalone[T any](s anySchema) *node {
	rep := &report{}
	n := s.compile(nil, rep, "")

	rep.check("schema for " + reflect.TypeFor[T]().String())

	return n
}
