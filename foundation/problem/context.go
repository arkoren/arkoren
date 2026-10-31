package problem

import (
	"context"
	"maps"
	"net/http"
	"sync"
)

// responseContextKey identifies response contexts stored in request contexts.
type responseContextKey struct{}

// ResponseContext stores request-scoped RFC 9457 extension members and HTTP
// response headers. Methods protect outer maps and header slices for concurrent
// use; nested property values remain shared and require caller coordination,
// including while ServeHTTP encodes them. The zero value is usable. Do not copy
// a ResponseContext after first use, because it contains a mutex.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.SetProperty("request_id", "req-42")
//	ctx := problem.NewContext(context.Background(), metadata)
//	attached, found := problem.FromContext(ctx)
//	_, _ = attached, found
type ResponseContext struct {
	// mutex guards properties and headers.
	mutex sync.RWMutex

	// properties stores RFC 9457 extension members.
	properties map[string]any

	// headers stores HTTP response headers.
	headers http.Header
}

// NewResponseContext creates a request-local container with empty property and
// header maps. Attach it with NewContext; no global state or request is changed.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.SetHeader("Cache-Control", "no-store")
//	_ = metadata.Headers().Get("Cache-Control")
func NewResponseContext() *ResponseContext {
	return &ResponseContext{
		properties: make(map[string]any),
		headers:    make(http.Header),
	}
}

// AddHeader appends value to the header field named name.
// Names are canonicalized using net/http rules. Existing values retain their
// order; rendering may replace them with explicit problem headers and always
// owns Content-Type and the required Accept variance.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.AddHeader("Vary", "Accept-Language")
//	_ = metadata.Headers().Values("Vary")
func (responseContext *ResponseContext) AddHeader(name, value string) {
	responseContext.mutex.Lock()
	defer responseContext.mutex.Unlock()

	if responseContext.headers == nil {
		responseContext.headers = make(http.Header)
	}

	responseContext.headers.Add(name, value)
}

// SetProperty replaces the RFC 9457 extension member named name with value.
// The value is retained, not recursively copied or encoded. Use client-safe,
// JSON-encodable values; encoding failures follow ServeHTTP's negotiated fallback.
// Nil is stored, not removed. Standard names are ignored by HTTP rendering.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.SetProperty("request_id", "req-42")
//	_ = metadata.Properties()["request_id"]
func (responseContext *ResponseContext) SetProperty(name string, value any) {
	responseContext.mutex.Lock()
	defer responseContext.mutex.Unlock()

	if responseContext.properties == nil {
		responseContext.properties = make(map[string]any)
	}

	responseContext.properties[name] = value
}

// SetHeader replaces every value of the header field named name with value.
// It canonicalizes the name and owns its value slice. Empty values remain
// present; use DeleteHeader to remove the field instead.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.SetHeader("Cache-Control", "no-store")
//	_ = metadata.Headers().Get("Cache-Control")
func (responseContext *ResponseContext) SetHeader(name, value string) {
	responseContext.mutex.Lock()
	defer responseContext.mutex.Unlock()

	if responseContext.headers == nil {
		responseContext.headers = make(http.Header)
	}

	responseContext.headers.Set(name, value)
}

// DeleteProperty removes the RFC 9457 extension member named name.
// Removing an absent member is a no-op; retained nested values are not mutated
// or released by this container beyond dropping its own reference.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.SetProperty("debug", "temporary")
//	metadata.DeleteProperty("debug")
//	_, found := metadata.Properties()["debug"]
//	_ = found // false
func (responseContext *ResponseContext) DeleteProperty(name string) {
	responseContext.mutex.Lock()
	defer responseContext.mutex.Unlock()

	delete(responseContext.properties, name)
}

// DeleteHeader removes the header field named name.
// Names are canonicalized; removing an absent header is a no-op. It affects
// only this request's contextual headers, not a published problem's headers.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.SetHeader("Retry-After", "60")
//	metadata.DeleteHeader("retry-after")
//	_ = metadata.Headers().Get("Retry-After") // ""
func (responseContext *ResponseContext) DeleteHeader(name string) {
	responseContext.mutex.Lock()
	defer responseContext.mutex.Unlock()

	responseContext.headers.Del(name)
}

// Headers returns an independent snapshot of response headers.
// Both the outer map and value slices are copied. Mutating them cannot change
// this container; an untouched zero ResponseContext returns nil.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.AddHeader("Vary", "Accept-Language")
//	headers := metadata.Headers()
//	headers.Set("Vary", "Origin") // Does not modify metadata.
func (responseContext *ResponseContext) Headers() http.Header {
	responseContext.mutex.RLock()
	defer responseContext.mutex.RUnlock()

	return responseContext.headers.Clone()
}

// Properties copies the outer RFC 9457 extension map, not nested values.
// Editing map entries cannot change this container; mutations through a nested
// reference can. Coordinate such mutations with all readers and HTTP rendering.
// An untouched zero ResponseContext returns nil.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	metadata.SetProperty("request_id", "req-42")
//	properties := metadata.Properties()
//	properties["request_id"] = "local" // Does not modify metadata.
func (responseContext *ResponseContext) Properties() map[string]any {
	responseContext.mutex.RLock()
	defer responseContext.mutex.RUnlock()

	return maps.Clone(responseContext.properties)
}

// snapshot copies outer property and header maps under one read lock, including
// detached header slices. Nested property values keep their shared ownership;
// the lock does not protect their use after this method returns.
func (responseContext *ResponseContext) snapshot() (map[string]any, http.Header) {
	responseContext.mutex.RLock()
	defer responseContext.mutex.RUnlock()

	return maps.Clone(responseContext.properties), responseContext.headers.Clone()
}

// NewContext returns a context containing responseContext. A nil
// responseContext leaves ctx unchanged. The container is retained, not copied;
// later edits are visible to ServeHTTP when it snapshots this request's context.
// A nonnil container requires a nonnil parent, as context.WithValue does.
//
// Example:
//
//	metadata := problem.NewResponseContext()
//	ctx := problem.NewContext(context.Background(), metadata)
//	attached, found := problem.FromContext(ctx)
//	_, _ = attached, found
func NewContext(ctx context.Context, responseContext *ResponseContext) context.Context {
	if responseContext == nil {
		return ctx
	}

	return context.WithValue(ctx, responseContextKey{}, responseContext)
}

// FromContext retrieves the response context from ctx without
// panicking when one is absent or has an unexpected type.
// Nil contexts and missing containers return nil, false. A successful result
// is the same shared request-local container passed to NewContext.
//
// Example:
//
//	ctx := problem.NewContext(context.Background(), problem.NewResponseContext())
//	if metadata, found := problem.FromContext(ctx); found {
//		metadata.SetProperty("request_id", "req-42")
//	}
func FromContext(ctx context.Context) (*ResponseContext, bool) {
	if ctx == nil {
		return nil, false
	}

	responseContext, ok := ctx.Value(responseContextKey{}).(*ResponseContext)

	if !ok || responseContext == nil {
		return nil, false
	}

	return responseContext, true
}

// isStandardMember reports whether key is an RFC 9457 standard member.
func isStandardMember(key string) bool {
	switch key {
	case "type", "title", "detail", "status", "instance":
		return true
	default:
		return false
	}
}

// mergeResponseProperties adds non-standard properties absent from additional.
func mergeResponseProperties(additional map[string]any, properties map[string]any) map[string]any {
	additional = cloneResponseAdditional(additional, len(properties))

	for key, value := range properties {
		if isStandardMember(key) {
			continue
		}

		if _, exists := additional[key]; !exists {
			additional[key] = value
		}
	}

	return additional
}

// cloneResponseAdditional returns a writable copy sized for properties.
func cloneResponseAdditional(additional map[string]any, propertyCount int) map[string]any {
	cloned := maps.Clone(additional)

	if cloned == nil {
		return make(map[string]any, propertyCount)
	}

	return cloned
}

// withResponseContext merges properties into details and returns contextual
// headers from one response-context snapshot.
func (problem Details) withResponseContext(ctx context.Context) (Details, http.Header) {
	responseContext, ok := FromContext(ctx)

	if !ok {
		return problem, nil
	}

	properties, headers := responseContext.snapshot()

	if len(properties) == 0 {
		return problem, headers
	}

	problem.data.Additional = mergeResponseProperties(problem.data.Additional, properties)

	return problem, headers
}
