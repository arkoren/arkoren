package problem

import (
	"maps"
	"net/http"
	"net/textproto"
	"slices"
)

// Builder configures one temporary draft. Publication copies outer maps and
// header slices; nested extension values and causes remain caller-owned.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Status(404).Title("Missing")
//	})
//	_ = failure.Title()
type Builder struct {
	// details holds the draft's independently owned outer containers.
	details Details
}

// BuilderFunc edits one temporary problem draft during New or Details.Derive.
// Callbacks run directly once in order on the same draft; nil entries panic.
// Publication detaches outer maps and header slices, not arbitrary nested values.
//
// Example:
//
//	policy := problem.BuilderFunc(func(details *problem.Builder) {
//		details.Status(http.StatusNotFound).Title("Order missing")
//	})
//	failure := problem.New(policy)
//	_ = failure.Title()
type BuilderFunc = func(*Builder)

// New invokes callbacks directly, synchronously, in order on one draft. Omitted
// callbacks use defaults; supplied nil callbacks panic. After callbacks, zero
// status becomes 500, empty type becomes about:blank, and empty title uses the
// final status phrase. Retained builders cannot modify the published value.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Status(http.StatusNotFound)
//	})
//	_ = failure.Title() // "Not Found"
func New(configure ...BuilderFunc) Details {
	return (Details{}).Derive(configure...).defaultStandardMembers()
}

// Derive applies callbacks in order on one detached draft and publishes a copy.
// It preserves absent fields and inherited titles, even when status changes.
// Supplied nil callbacks panic; expanded nil/empty slices supply no callbacks.
// Only outer extension maps and header slices are copied, not nested values.
//
// Example:
//
//	failure := problem.NotFound().Derive(func(details *problem.Builder) {
//		details.Detail("No order 42").Extension("order_id", 42)
//	})
//	_ = failure.Detail()
func (problem Details) Derive(configure ...BuilderFunc) Details {
	problem.data.Additional = maps.Clone(problem.data.Additional)
	problem.headers = problem.headers.Clone()
	draft := &Builder{
		details: problem,
	}

	for _, callback := range configure {
		callback(draft)
	}

	result := draft.details
	result.data.Additional = maps.Clone(result.data.Additional)
	result.headers = result.headers.Clone()

	return result
}

// Type replaces the URI reference without validation and returns the same draft.
// Empty defaults only in New, not Derive.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Type("urn:example:missing")
//	})
//	_ = failure.Type()
func (details *Builder) Type(value string) *Builder {
	details.details.data.Type = value

	return details
}

// Title replaces the summary and returns the same draft. Empty defaults from
// the final status only in New; Derive retains the supplied empty value.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Title("Order missing")
//	})
//	_ = failure.Title()
func (details *Builder) Title(value string) *Builder {
	details.details.data.Title = value

	return details
}

// Status replaces the advisory HTTP status and returns the same draft. New
// resolves zero to 500; Derive preserves it without changing the title.
// Values are stored without validation. ServeHTTP renders values outside
// net/http's three-digit range as 500, without modifying the stored identity.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Status(http.StatusConflict)
//	})
//	_ = failure.Status()
func (details *Builder) Status(value int) *Builder {
	details.details.data.Status = value

	return details
}

// Detail replaces the occurrence explanation and returns the same draft.
// Empty explanations are omitted from JSON.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Detail("Try another name.")
//	})
//	_ = failure.Detail()
func (details *Builder) Detail(value string) *Builder {
	details.details.data.Detail = value

	return details
}

// Instance replaces the occurrence URI reference and returns the same draft.
// Empty omits the JSON member until HTTP rendering supplies the request path.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Instance("/orders/42")
//	})
//	_ = failure.Instance()
func (details *Builder) Instance(value string) *Builder {
	details.details.data.Instance = value

	return details
}

// Error retains the cause for errors.Is/As traversal and returns the same draft.
// Nil clears the cause. Causes never become JSON members.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Error(io.EOF)
//	})
//	_ = errors.Is(failure, io.EOF)
func (details *Builder) Error(err error) *Builder {
	details.details.err = err

	return details
}

// Extension replaces one flattened extension and returns the same draft. Nil is
// stored, not removed. Values are retained without recursive cloning or encoding;
// unsupported JSON values fail at encoding time, not during registration.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Extension("order_id", 42)
//	})
//	value, found := failure.Extension("order_id")
//	_, _ = value, found
func (details *Builder) Extension(name string, value any) *Builder {
	if details.details.data.Additional == nil {
		details.details.data.Additional = make(map[string]any)
	}

	details.details.data.Additional[name] = value

	return details
}

// RemoveExtension deletes one extension if present and returns the same draft.
// Standard members with the same name are unaffected.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.RemoveExtension("debug")
//	})
//	_, found := failure.Extension("debug")
//	_ = found // false
func (details *Builder) RemoveExtension(name string) *Builder {
	delete(details.details.data.Additional, name)

	return details
}

// Header replaces a canonicalized header with copied values and returns the same
// draft. Zero values removes it. Rendering owns Content-Type regardless of this
// setting; headers never become JSON members.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Header("Retry-After", "60")
//	})
//	_ = failure
func (details *Builder) Header(name string, values ...string) *Builder {
	if len(values) == 0 {
		return details.RemoveHeader(name)
	}

	if details.details.headers == nil {
		details.details.headers = make(http.Header)
	}

	details.details.headers[textproto.CanonicalMIMEHeaderKey(name)] = slices.Clone(values)

	return details
}

// AddHeader appends one value to a canonicalized header and returns the same
// draft. Existing values retain their order; headers stay out of JSON.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.AddHeader("Vary", "Accept")
//	})
//	_ = failure
func (details *Builder) AddHeader(name, value string) *Builder {
	if details.details.headers == nil {
		details.details.headers = make(http.Header)
	}

	details.details.headers.Add(name, value)

	return details
}

// RemoveHeader removes all values for a canonicalized header and returns the
// same draft. Removing an absent header is a no-op.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.RemoveHeader("Retry-After")
//	})
//	_ = failure
func (details *Builder) RemoveHeader(name string) *Builder {
	details.details.headers.Del(name)

	return details
}
