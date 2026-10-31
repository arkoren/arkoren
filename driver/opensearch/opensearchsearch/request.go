package opensearchsearch

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"arkoren.dev/core/service/search"
)

// errIndexNotFound is the error type OpenSearch reports for a missing index.
const errIndexNotFound = "index_not_found_exception"

// response is one buffered HTTP response.
type response struct {
	// status is the HTTP status code.
	status int

	// body is the whole response body.
	body []byte
}

// failure is the error object of an OpenSearch error response or bulk item.
type failure struct {
	// Type is the error type, such as index_not_found_exception.
	Type string `json:"type"`

	// Reason is the human-readable cause.
	Reason string `json:"reason"`
}

// bulkItem is the outcome of one native bulk action, keyed by its action name
// (index or delete). Keeping the native shape permits a shared decoder for both
// write paths while preserving the failing document's identity.
type bulkItem map[string]struct {
	// ID identifies the document affected by this bulk action.
	ID string `json:"_id"`

	// Error is nil for a successful action and otherwise holds its cause.
	Error *failure `json:"error"`
}

// err reports this document's failed action with its identity and backend cause.
// Successful actions return nil; HTTP-level bulk failures are handled before
// decoding individual items by response.bulkError.
func (item bulkItem) err(operation string) error {
	for _, outcome := range item {
		if outcome.Error != nil {
			return outcome.Error.asError(fmt.Sprintf("%s: document %s", operation, outcome.ID), http.StatusBadRequest)
		}
	}

	return nil
}

// bulkError interprets one bulk response, rejecting HTTP and decoding failures
// before reporting the first failed document in native response order. Separating
// response interpretation keeps bulk chunking independent of wire error shapes.
func (response response) bulkError(operation string) error {
	if err := response.err(operation); err != nil {
		return err
	}

	var result struct {
		// Errors reports whether any action in this bulk response failed.
		Errors bool `json:"errors"`

		// Items preserves action outcomes so failures can name their document.
		Items []bulkItem `json:"items"`
	}

	if err := json.Unmarshal(response.body, &result); err != nil {
		return fmt.Errorf("opensearch: %s: decode bulk response: %w", operation, err)
	}

	if !result.Errors {
		return nil
	}

	for _, item := range result.Items {
		if err := item.err(operation); err != nil {
			return err
		}
	}

	return nil
}

// do sends one request through the client's transport, which picks a node,
// retries, and applies the client's authentication. A nil body sends none.
// An absent client fails without implicit acquisition. Cancellation is checked
// first. HTTP statuses remain for the caller to interpret with [response.err];
// readiness, request construction, transport, and body-read failures return here.
func (driver *Driver) do(ctx context.Context, method, path string, params url.Values, contentType string, body []byte) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, err
	}

	client := driver.Client()

	if client == nil || client.Client == nil {
		return response{}, fmt.Errorf("opensearch: driver not started")
	}

	target := path

	if len(params) > 0 {
		target += "?" + params.Encode()
	}

	var reader io.Reader

	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, method, target, reader)

	if err != nil {
		return response{}, fmt.Errorf("opensearch: build %s %s: %w", method, path, err)
	}

	if body != nil {
		request.Header.Set("Content-Type", contentType)
	}

	httpResponse, err := client.Client.Request(request)

	if err != nil {
		return response{}, fmt.Errorf("opensearch: %s %s: %w", method, path, err)
	}

	// Read errors are returned below; closing the response is best-effort reader cleanup.
	defer httpResponse.Body.Close()

	payload, err := io.ReadAll(httpResponse.Body)

	if err != nil {
		return response{}, fmt.Errorf("opensearch: read %s %s: %w", method, path, err)
	}

	return response{
		status: httpResponse.StatusCode,
		body:   payload,
	}, nil
}

// doJSON is [Driver.do] with value encoded as a JSON body.
func (driver *Driver) doJSON(ctx context.Context, method, path string, params url.Values, value any) (response, error) {
	body, err := json.Marshal(value)

	if err != nil {
		return response{}, fmt.Errorf("opensearch: encode %s %s: %w", method, path, err)
	}

	return driver.do(ctx, method, path, params, "application/json", body)
}

// err returns nil for a 2xx status. Otherwise it returns an error naming
// operation with the error type and reason OpenSearch reported, wrapping
// [search.ErrNotFound] when the cause is a missing index.
func (response response) err(operation string) error {
	if response.status >= 200 && response.status < 300 {
		return nil
	}

	var payload struct {
		// Error carries the server's typed failure for sentinel mapping.
		Error failure `json:"error"`
	}

	if json.Unmarshal(response.body, &payload) != nil || payload.Error.Type == "" {
		return fmt.Errorf("opensearch: %s: status %d: %s", operation, response.status, strings.TrimSpace(string(response.body)))
	}

	return payload.Error.asError(operation, response.status)
}

// asError converts an OpenSearch error object to a Go error naming operation.
func (failure failure) asError(operation string, status int) error {
	if failure.Type == errIndexNotFound {
		return fmt.Errorf("opensearch: %s: %w: %s", operation, search.ErrNotFound, failure.Reason)
	}

	return fmt.Errorf("opensearch: %s: status %d: %s: %s", operation, status, failure.Type, failure.Reason)
}
