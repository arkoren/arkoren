package problem

import (
	"fmt"
	"maps"
	"net/http"
)

// Details is an immutable RFC 9457 problem, an error, and an HTTP handler.
// Construct with New and customize occurrences with Derive. The zero value is
// usable for decoding and HTTP rendering, but has unspecified standard members.
// Only owned outer maps and header slices are detached, not nested extensions.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Status(http.StatusNotFound)
//	})
//	_ = failure.Title()
type Details struct {
	// data is the sole wire representation; named storage prevents field promotion.
	data detailsData
	// err is the optional cause, excluded from the wire representation.
	err error
	// headers holds independently owned response headers, excluded from JSON.
	headers http.Header
}

// detailsData is the tagged RFC 9457 representation delegated to JSON v2.
// Exported fields are required by the codec, not exposed through Details.
type detailsData struct {
	// Type identifies the problem category; New defaults empty to about:blank.
	Type string `json:"type"`
	// Title summarizes the category rather than an individual occurrence.
	Title string `json:"title"`
	// Status is advisory; zero represents absence outside HTTP rendering.
	Status int `json:"status,omitzero"`
	// Detail describes one occurrence and is omitted when empty.
	Detail string `json:"detail,omitzero"`
	// Instance identifies one occurrence and is omitted when empty.
	Instance string `json:"instance,omitzero"`
	// Additional embeds unknown top-level members. JSON v2 rejects collisions
	// with emitted standard members; MarshalJSONTo also protects omitted status.
	Additional map[string]any `json:",embed"`
}

// Type returns the stored problem-type URI reference.
// It does not apply defaults or mutate the problem.
//
// Example:
//
//	value := problem.NotFound().Type()
//	_ = value
func (problem Details) Type() string {
	return problem.data.Type
}

// Title returns the stored human-readable category summary.
// It does not apply defaults or mutate the problem.
//
// Example:
//
//	value := problem.NotFound().Title()
//	_ = value
func (problem Details) Title() string {
	return problem.data.Title
}

// Status returns the stored advisory status; zero means unspecified.
// It does not apply defaults or mutate the problem.
//
// Example:
//
//	value := problem.NotFound().Status()
//	_ = value
func (problem Details) Status() int {
	return problem.data.Status
}

// Detail returns the stored occurrence explanation; empty means absent.
// It does not apply defaults or mutate the problem.
//
// Example:
//
//	value := problem.NotFound().Detail()
//	_ = value
func (problem Details) Detail() string {
	return problem.data.Detail
}

// Instance returns the stored occurrence URI reference; empty means absent.
// It does not apply defaults or mutate the problem.
//
// Example:
//
//	value := problem.NotFound().Instance()
//	_ = value
func (problem Details) Instance() string {
	return problem.data.Instance
}

// Extension returns a retained extension value and whether it exists. Stored
// nil differs from absence. Nested values remain shared and caller-owned.
//
// Example:
//
//	value, found := problem.New().Extension("order_id")
//	_, _ = value, found
func (problem Details) Extension(name string) (any, bool) {
	value, found := problem.data.Additional[name]

	return value, found
}

// Extensions returns a copy of the outer extension map, or nil if none exists.
// Editing this map cannot change details; nested values are not copied.
//
// Example:
//
//	values := problem.New().Extensions()
//	_ = values["order_id"]
func (problem Details) Extensions() map[string]any {
	return maps.Clone(problem.data.Additional)
}

// Error returns a string representation of the [Details] for the [error] interface.
// It uses stored status and falls back to its status phrase for an empty title;
// a cause is appended for diagnostics, never for JSON rendering.
//
// Example:
//
//	message := problem.New().Error()
//	_ = message
func (problem Details) Error() string {
	title := problem.data.Title

	if title == "" {
		title = http.StatusText(problem.data.Status)
	}

	if problem.err != nil {
		return fmt.Sprintf("%d %s: %s", problem.data.Status, title, problem.err)
	}

	return fmt.Sprintf("%d %s", problem.data.Status, title)
}

// Unwrap returns the underlying error from the [Details]
// for use with [errors.Is] and [errors.As].
// A missing cause returns nil; the cause is not copied or serialized.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Error(io.EOF)
//	})
//	_ = errors.Is(failure.Unwrap(), io.EOF)
func (problem Details) Unwrap() error {
	return problem.err
}

// Is reports whether target is a [Details] with the same Type, Status and
// Title as problem, so [errors.Is] matches a problem against its template.
// Detail, extension members, headers and the wrapped cause are ignored. The
// cause is still searched by [errors.Is] through [Details.Unwrap].
//
// Example:
//
//	var ErrUserNotFound = problem.NotFound().Derive(func(details *problem.Builder) {
//		details.Title("User not found")
//	})
//
//	err := ErrUserNotFound.Derive(func(details *problem.Builder) {
//		details.Detail("no user 42").Extension("id", 42)
//	})
//	errors.Is(err, ErrUserNotFound) // true
//	errors.Is(err, problem.NotFound()) // false: the title differs
func (problem Details) Is(target error) bool {
	other, ok := target.(Details)

	if !ok {
		return false
	}

	return problem.data.Type == other.data.Type &&
		problem.data.Status == other.data.Status && problem.data.Title == other.data.Title
}

// defaultStandardMembers resolves empty type, status, and title fields.
func (problem Details) defaultStandardMembers() Details {
	if problem.data.Type == "" {
		problem.data.Type = "about:blank"
	}

	if problem.data.Status == 0 {
		problem.data.Status = http.StatusInternalServerError
	}

	if problem.data.Title == "" {
		problem.data.Title = http.StatusText(problem.data.Status)
	}

	return problem
}

// Defaulted returns a copy with zero-value fields resolved for request. Type
// defaults to "about:blank", Status to 500, Title to [http.StatusText] for the
// status, and Instance to the request URL path.
// A nil request leaves Instance untouched. The original value is unchanged.
//
// Example:
//
//	failure := (problem.Details{}).Defaulted(nil)
//	_ = failure.Status() // 500
func (problem Details) Defaulted(request *http.Request) Details {
	problem = problem.defaultStandardMembers()

	if problem.data.Instance == "" && request != nil {
		problem.data.Instance = request.URL.Path
	}

	return problem
}

// HTTPStatus returns the problem's HTTP status code for error handlers.
// It reports stored status, including zero and unvalidated values. ServeHTTP
// applies transport bounds and defaults to its response copy, not this value.
//
// Example:
//
//	status := problem.NotFound().HTTPStatus()
//	_ = status // 404
func (problem Details) HTTPStatus() int {
	return problem.data.Status
}
