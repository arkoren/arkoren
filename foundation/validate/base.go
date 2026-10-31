package validate

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"arkoren.dev/foundation/validate/internal/fields"
)

// opKind identifies the kind of an [op].
type opKind uint8

const (
	// opRule records an ordinary value constraint. It runs only when the current
	// value is present.
	opRule opKind = iota
	// opRequired records an unconditional presence requirement. Unlike value
	// constraints, it also observes absent values.
	opRequired
	// opRequiredIf requires presence when its condition holds. The condition
	// observes the enclosing object without changing it.
	opRequiredIf
	// opRequiredUnless requires presence when its condition does not hold. It
	// shares the ordinary conditional compilation path.
	opRequiredUnless
	// opRequiredWith requires presence when any selected sibling is present.
	// Sibling names are resolved during schema construction.
	opRequiredWith
	// opRequiredWithAll requires presence when all selected siblings are present.
	// Its sibling list is captured with the recorded operation.
	opRequiredWithAll
	// opRequiredWithout requires presence when any selected sibling is absent.
	// Presence follows the same rules as the current field.
	opRequiredWithout
	// opProhibitedIf rejects a present value when its condition holds. Absent
	// values do not produce a prohibition failure.
	opProhibitedIf
	// opProhibitedUnless rejects a present value when its condition does not
	// hold. The observation itself emits no violations.
	opProhibitedUnless
	// opExcludeIf clears a value when its condition holds. Later operations then
	// see the value as absent.
	opExcludeIf
	// opWhen runs an additional schema when its condition holds. The branch
	// remains in the recorded rule order.
	opWhen
	// opCompare compares the current value with a resolved sibling. The recorded
	// relation determines the violation code.
	opCompare
	// opEntries runs a collection assertion that can report entry-specific
	// failures. Its callback retains index or key paths for diagnostics.
	opEntries
)

// relation is the comparison of a field comparison op.
type relation uint8

const (
	// relEq compares two same-typed values for equality. It selects the eq_field
	// violation code.
	relEq relation = iota
	// relNe compares two same-typed values for inequality. It selects the
	// ne_field violation code.
	relNe
	// relGt requires a value above its sibling bound. It selects the gt_field
	// violation code.
	relGt
	// relGte requires a value at or above its sibling bound. It selects the
	// gte_field violation code.
	relGte
	// relLt requires a value below its sibling bound. It selects the lt_field
	// violation code.
	relLt
	// relLte requires a value at or below its sibling bound. It selects the
	// lte_field violation code.
	relLte
	// relAfter requires an instant after its sibling time. Comparison uses time
	// semantics rather than struct ordering.
	relAfter
	// relBefore requires an instant before its sibling time. Comparison uses time
	// semantics rather than struct ordering.
	relBefore
)

// relationCodes maps each relation to its violation code.
var relationCodes = [...]string{
	relEq:     "eq_field",
	relNe:     "ne_field",
	relGt:     "gt_field",
	relGte:    "gte_field",
	relLt:     "lt_field",
	relLte:    "lte_field",
	relAfter:  "after_field",
	relBefore: "before_field",
}

// op is one recorded builder call. Ops run in the order they were added.
type op[T any] struct {
	// kind selects the compilation path for this recorded builder operation.
	kind opKind
	// code overrides the violation code for a recorded presence operation.
	code string
	// cond is the read-only enclosing-object observation for conditional
	// operations.
	cond condition
	// fields retains sibling Go paths used by presence-dependent requirements.
	fields []string
	// field retains the sibling Go path used by a field comparison.
	field string
	// rel selects the comparison relation and its violation code.
	rel relation
	// rule checks a present value and returns a violation or operational error.
	rule func(context.Context, T) error
	// when retains the published branch schema applied when cond holds.
	when anySchema
	// entries records collection diagnostics at their individual entry paths.
	entries func(*state, T)
}

// base is the state shared by every builder. It is a value type: methods
// return a modified copy and never touch the receiver's backing arrays.
type base[T any] struct {
	// ops retains builder operations in execution order; updates detach its
	// backing array.
	ops []op[T]
	// norms retains pure normalizers in the order they run before presence
	// checks.
	norms []func(T) T
	// def produces the configured default when input is absent; nil leaves it
	// unchanged.
	def func() T
	// required records unconditional presence so Default and Required can reject
	// conflicts.
	required bool
	// bail stops this value at its first recorded rule failure when enabled.
	bail bool
	// custom marks arbitrary Check code, which derived count constraints forbid.
	custom bool
	// problems retains invalid declarations, including shared schemas and rule
	// arguments. Compilation reports them together with field context; later
	// settings never erase an earlier construction diagnostic.
	problems []string

	// keywords are the JSON Schema keywords recorded by rules, in call
	// order; description is the text set by Describe.
	keywords []keyword
	// description stores the human-readable JSON Schema annotation without
	// changing validation.
	description string
}

// with appends one operation to a copy of the rule sequence. Clipping before
// append prevents later drafts from overwriting a published backing array.
func (rules base[T]) with(o op[T]) base[T] {
	rules.ops = append(slices.Clip(rules.ops), o)

	return rules
}

// problemf records a definition diagnostic without interrupting construction
// callbacks. Copy-on-write preserves published errors when a draft is retained;
// build reports each diagnostic alongside other registration problems.
func (rules base[T]) problemf(format string, args ...any) base[T] {
	rules.problems = append(slices.Clip(rules.problems), fmt.Sprintf(format, args...))

	return rules
}

// addRule records a typed value check in declaration order. Compilation runs
// it only for values that count as present and preserves operational errors.
func (rules base[T]) addRule(rule func(context.Context, T) error) base[T] {
	return rules.with(op[T]{
		kind: opRule,
		rule: rule,
	})
}

// normalize appends a normalizer. Normalizers run in the order written,
// before the presence check, Default and every rule.
func (rules base[T]) normalize(fn func(T) T) base[T] {
	if fn == nil {
		invalidf("Normalize function is nil")
	}

	rules.norms = append(slices.Clip(rules.norms), fn)

	return rules
}

// markRequired records an unconditional presence check with the supplied
// violation code. It leaves the separate Default conflict check to requireValue.
func (rules base[T]) markRequired(code string) base[T] {
	return rules.with(op[T]{
		kind: opRequired,
		code: code,
	})
}

// requireValue records unconditional presence and marks the base required.
// It panics with ErrInvalidSchema when a default was already configured.
func (rules base[T]) requireValue() base[T] {
	if rules.def != nil {
		invalidf("Required cannot be combined with Default")
	}

	rules.required = true

	return rules.markRequired("required")
}

// setDefault retains the function that supplies an absent value's default.
// It panics with ErrInvalidSchema when unconditional Required was configured.
func (rules base[T]) setDefault(def func() T) base[T] {
	if rules.required {
		invalidf("Default cannot be combined with Required")
	}

	rules.def = def

	return rules
}

// setBail makes compiled execution stop this value at its first violation.
// Other independently registered values still report their own failures.
func (rules base[T]) setBail() base[T] {
	rules.bail = true

	return rules
}

// check records a custom value check and marks the base as containing user
// code. Nil functions panic; derived-count constraints reject the custom marker.
func (rules base[T]) check(fn func(context.Context, T) error) base[T] {
	if fn == nil {
		invalidf("Check function is nil")
	}

	rules.custom = true

	return rules.addRule(fn)
}

// when records an immutable conditional branch at the current rule position.
// A nil schema panics rather than producing a branch that fails during use.
func (rules base[T]) when(cond condition, schema Schema[T]) base[T] {
	if schema == nil {
		invalidf("When schema is nil")
	}

	return rules.with(op[T]{
		kind: opWhen,
		cond: cond,
		when: schema,
	})
}

// conditional records a presence or exclusion operation with its observation.
// The observation is bound to the enclosing struct during compilation.
func (rules base[T]) conditional(kind opKind, cond condition) base[T] {
	return rules.with(op[T]{
		kind: kind,
		cond: cond,
	})
}

// relate records a comparison against a sibling Go field path. Compilation
// resolves the path and verifies that its type supports the selected relation.
func (rules base[T]) relate(rel relation, field string) base[T] {
	return rules.with(op[T]{
		kind:  opCompare,
		rel:   rel,
		field: field,
	})
}

// presence records a sibling-dependent presence requirement. It copies the
// path list so later caller edits cannot change the recorded operation.
func (rules base[T]) presence(kind opKind, fields []string) base[T] {
	return rules.with(op[T]{
		kind:   kind,
		fields: slices.Clone(fields),
	})
}

// leafHooks are the type specific parts of compiling a builder.
type leafHooks[T any] struct {
	// typ is the exact validated Go type used for default and field checks.
	typ reflect.Type
	// get extracts the typed value from reflection for rules and normalizers.
	get func(reflect.Value) T
	// resolve maps Go field selectors to diagnostic names for object-level
	// checks.
	resolve func(string) (string, bool)
}

// build compiles the recorded ops into a node. Builders attach their
// children and then call verifyDefault.
func (rules base[T]) build(e *env, rep *report, where string, hooks leafHooks[T]) *node {
	for _, problem := range rules.problems {
		rep.addf(where, "%s", problem)
	}

	n := &node{
		bail: rules.bail,
	}

	for _, o := range rules.ops {
		current := compileOp(o, e, rep, where, hooks, n)

		if current != nil {
			n.steps = append(n.steps, current)
		}
	}

	if len(rules.norms) > 0 {
		norms := rules.norms

		n.mut = true
		n.norm = func(st *state, v reflect.Value) {
			value := hooks.get(v)

			for _, fn := range norms {
				if st.stopped() {
					return
				}

				value = fn(value)
			}

			if !st.stopped() {
				v.Set(reflect.ValueOf(&value).Elem())
			}
		}
	}

	if rules.def != nil {
		def := rules.def

		n.mut = true
		n.def = func(v reflect.Value) {
			value := def()
			v.Set(reflect.ValueOf(&value).Elem())
		}
	}

	return n
}

// verifyDefault checks a configured default using the same preparation as
// runtime insertion: children are prepared, but the node's own normalizers do
// not run again after a default is selected. It only executes during Struct
// construction; metadata export leaves rep.defaults false and never runs it.
func (rules base[T]) verifyDefault(n *node, rep *report, where string) {
	if rules.def == nil || !rep.defaults {
		return
	}

	probe := reflect.New(reflect.TypeFor[T]()).Elem()
	n.def(probe)

	st := &state{
		ctx: context.Background(),
	}

	if n.pre != nil {
		n.pre(st, probe)
	}

	n.runPrepared(st, probe, reflect.Value{}, true)

	switch {
	case st.fatal != nil:
		rep.addf(where, "default could not be checked: %v", st.fatal)
	case len(st.errs) > 0:
		rep.addf(where, "default %v fails its own schema: %v", probe.Interface(), st.errs)
	}
}

// compileOp compiles one op. It returns nil for ops that only affect the
// node, or after reporting a problem.
func compileOp[T any](
	o op[T],
	e *env,
	rep *report,
	where string,
	hooks leafHooks[T],
	n *node,
) step {
	if o.kind == opExcludeIf {
		n.mut = true
	}

	switch o.kind {
	case opEntries:
		return entryRuleStep(o.entries, hooks.get)
	case opRule:
		return valueRuleStep(o.rule, hooks)
	case opRequired:
		return requiredValueStep(o.code)
	case opWhen:
		return compileWhen(o, e, rep, where, n)
	case opCompare:
		return compileCompare(o, e, rep, where, hooks.typ)
	default:
		return compilePresenceCondition(o, e, rep, where)
	}
}

// entryRuleStep isolates collection reporting from scalar error conversion.
// An entry assertion may add several failures without producing a Check error.
func entryRuleStep[T any](entries func(*state, T), get func(reflect.Value) T) step {
	return func(st *state, c *cursor) bool {
		if !c.present {
			return false
		}

		before := st.count
		entries(st, get(c.v))

		return st.count > before
	}
}

// valueRuleStep preserves optional absence and operational Check errors while
// adapting a typed scalar rule to the reflection-backed execution cursor.
func valueRuleStep[T any](rule func(context.Context, T) error, hooks leafHooks[T]) step {
	return func(st *state, c *cursor) bool {
		if !c.present {
			return false
		}

		return st.handle(rule(st.ctx, hooks.get(c.v)), hooks.resolve)
	}
}

// requiredValueStep checks unconditional presence independently of an enclosing
// object, which also permits Required on standalone scalar schemas.
func requiredValueStep(code string) step {
	return func(st *state, c *cursor) bool {
		if c.present {
			return false
		}

		st.violate(code, nil)

		return true
	}
}

// compilePresenceCondition separates sibling-presence lists from explicit
// condition expressions, keeping their existing localized parameters intact.
func compilePresenceCondition[T any](o op[T], e *env, rep *report, where string) step {
	if o.kind >= opRequiredWith && o.kind <= opRequiredWithout {
		return compilePresence(o, e, rep, where)
	}

	return compileConditional(o, e, rep, where)
}

// conditionalCodes identifies the localized presence rule without coupling
// condition evaluation to validation diagnostics.
var conditionalCodes = [...]string{
	opRequiredIf:       "required_if",
	opRequiredUnless:   "required_unless",
	opProhibitedIf:     "prohibited_if",
	opProhibitedUnless: "prohibited_unless",
}

// compileConditional binds a pure observation, then selects the presence or
// exclusion effect. Unless complements the observation, not its diagnostics.
func compileConditional[T any](o op[T], e *env, rep *report, where string) step {
	observation := o.cond.bound(e, rep, where)
	cond := observation

	if o.kind == opRequiredUnless || o.kind == opProhibitedUnless {
		cond = func(object reflect.Value) bool {
			return !observation(object)
		}
	}

	if o.kind == opExcludeIf {
		return excludedWhen(cond)
	}

	code := conditionalCodes[o.kind]
	params := o.cond.params(e)

	if o.kind == opRequiredIf || o.kind == opRequiredUnless {
		return requiredWhen(cond, code, params)
	}

	return prohibitedWhen(cond, code, params)
}

// excludedWhen clears a writable present value and affects only later steps.
// Missing enclosing context never activates an exclusion.
func excludedWhen(cond condFunc) step {
	return func(_ *state, c *cursor) bool {
		if !c.obj.IsValid() || !cond(c.obj) {
			return false
		}

		if c.present && c.v.CanSet() {
			c.v.SetZero()
		}

		c.present = false

		return false
	}
}

// requiredWhen builds a step that reports a missing value when cond holds.
func requiredWhen(cond condFunc, code string, params map[string]any) step {
	return func(st *state, c *cursor) bool {
		if c.present || !c.obj.IsValid() || !cond(c.obj) {
			return false
		}

		st.violate(code, params)

		return true
	}
}

// prohibitedWhen builds a step that reports a present value when cond
// holds.
func prohibitedWhen(cond condFunc, code string, params map[string]any) step {
	return func(st *state, c *cursor) bool {
		if !c.present || !c.obj.IsValid() || !cond(c.obj) {
			return false
		}

		st.violate(code, params)

		return true
	}
}

// compilePresence compiles RequiredWith, RequiredWithAll and
// RequiredWithout.
func compilePresence[T any](o op[T], e *env, rep *report, where string) step {
	if e == nil {
		rep.addf(where, "conditions and field references need an enclosing Struct")

		return nil
	}

	infos := make([]*fields.Field, 0, len(o.fields))
	names := make([]string, 0, len(o.fields))

	for _, field := range o.fields {
		info := e.lookupField(field, rep, where)

		if info != nil {
			infos = append(infos, info)
			names = append(names, info.JSONName)
		}
	}

	var (
		code string
		test func(count int) bool
	)

	switch o.kind {
	case opRequiredWith:
		code = "required_with"
		test = func(present int) bool {
			return present > 0
		}
	case opRequiredWithAll:
		code = "required_with_all"
		test = func(present int) bool {
			return present == len(infos) && len(infos) > 0
		}
	default:
		code = "required_without"
		test = func(present int) bool {
			return present < len(infos)
		}
	}

	return func(st *state, c *cursor) bool {
		if c.present || !c.obj.IsValid() {
			return false
		}

		present := 0

		for _, info := range infos {
			if presentValue(info.Value(c.obj)) {
				present++
			}
		}

		if !test(present) {
			return false
		}

		st.violate(code, map[string]any{"fields": slices.Clone(names)})

		return true
	}
}

// compileWhen compiles a When op, running the inner schema's node when
// the condition holds.
func compileWhen[T any](o op[T], e *env, rep *report, where string, n *node) step {
	cond := o.cond.bound(e, rep, where)
	inner := o.when.compile(e, rep, where)

	n.mut = n.mut || inner.mut

	return func(st *state, c *cursor) bool {
		if !c.obj.IsValid() || !cond(c.obj) {
			return false
		}

		before := st.count
		inner.run(st, c.v, c.obj, c.force)
		c.present = c.force || !isAbsent(c.v)

		return st.count > before
	}
}

// compileCompare compiles a field comparison between this value and a
// sibling field of the same type.
func compileCompare[T any](o op[T], e *env, rep *report, where string, typ reflect.Type) step {
	if e == nil {
		rep.addf(where, "conditions and field references need an enclosing Struct")

		return nil
	}

	info := e.lookupField(o.field, rep, where)

	if info == nil {
		return nil
	}

	code := relationCodes[o.rel]

	if info.Type != typ {
		rep.addf(where, "%s(%q): field has type %s, want %s", code, o.field, info.Type, typ)

		return nil
	}

	holds := comparison(typ, o.rel)

	if holds == nil {
		rep.addf(where, "%s(%q): type %s does not support this comparison", code, o.field, typ)

		return nil
	}

	params := map[string]any{"field": info.JSONName}

	return func(st *state, c *cursor) bool {
		if !c.present || !c.obj.IsValid() {
			return false
		}

		target := info.Value(c.obj)

		if target.IsValid() && holds(c.v, target) {
			return false
		}

		st.violate(code, params)

		return true
	}
}

// comparison returns the test for rel on values of typ, or nil when typ
// does not support it. Ordered floating predicates reject NaN on either side;
// ordering's total order is appropriate for sorting, not numeric constraints.
func comparison(typ reflect.Type, rel relation) func(left, right reflect.Value) bool {
	if rel == relEq || rel == relNe {
		equal := equality(typ)

		if equal == nil {
			return nil
		}

		if rel == relEq {
			return equal
		}

		return func(left, right reflect.Value) bool {
			return !equal(left, right)
		}
	}

	order := ordering(typ)

	if order == nil {
		return nil
	}

	floats := typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64

	return func(left, right reflect.Value) bool {
		if floats && (math.IsNaN(left.Float()) || math.IsNaN(right.Float())) {
			return false
		}

		result := order(left, right)

		switch rel {
		case relGt, relAfter:
			return result > 0
		case relGte:
			return result >= 0
		case relLt, relBefore:
			return result < 0
		default:
			return result <= 0
		}
	}
}

// ordering returns a three-way comparison for ordered types, or nil.
func ordering(typ reflect.Type) func(left, right reflect.Value) int {
	if typ == timeType {
		return func(left, right reflect.Value) int {
			leftTime, _ := left.Interface().(time.Time)
			rightTime, _ := right.Interface().(time.Time)

			return leftTime.Compare(rightTime)
		}
	}

	switch typ.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(left, right reflect.Value) int {
			return cmp.Compare(left.Int(), right.Int())
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return func(left, right reflect.Value) int {
			return cmp.Compare(left.Uint(), right.Uint())
		}
	case reflect.Float32, reflect.Float64:
		return func(left, right reflect.Value) int {
			return cmp.Compare(left.Float(), right.Float())
		}
	case reflect.String:
		return func(left, right reflect.Value) int {
			return strings.Compare(left.String(), right.String())
		}
	default:
		return nil
	}
}
