package meilisearchsearch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Meilisearch error codes the driver reacts to.
const (
	codeIndexNotFound      = "index_not_found"
	codeIndexAlreadyExists = "index_already_exists"
)

// Bounds of the pause between two polls of a running task. The pause starts
// short, because most tasks of a small write finish within milliseconds, and
// doubles up to the maximum so a long reindex does not flood the server.
const (
	minPollInterval = 5 * time.Millisecond
	maxPollInterval = 200 * time.Millisecond
)

// maxErrorBody is how many bytes of an error response the driver reads.
const maxErrorBody = 1 << 20

// apiError is an error reported by Meilisearch, either as the response to a
// request or as the failure of an asynchronous task. Callers test it with
// [hasCode].
type apiError struct {
	// Status is the HTTP status of the response, or 0 for a failed task.
	Status int

	// Code is the machine-readable Meilisearch error code, such as
	// index_not_found. It is empty when the response was not a Meilisearch
	// error document.
	Code string

	// Message is the human-readable explanation from the server.
	Message string
}

// Error describes the failure with its code and status.
func (apiError *apiError) Error() string {
	return fmt.Sprintf("meilisearch: %s (code %q, status %d)", apiError.Message, apiError.Code, apiError.Status)
}

// hasCode reports whether err is a Meilisearch error with the given code.
func hasCode(err error, code string) bool {
	apiErr, ok := errors.AsType[*apiError](err)

	return ok && apiErr.Code == code
}

// failure is the JSON error document of Meilisearch, used in responses and
// in failed tasks.
type failure struct {
	// Message describes why the request or asynchronous task failed.
	Message string `json:"message"`

	// Code identifies the server failure independently of its message text.
	Code string `json:"code"`
}

// do sends one request and decodes the JSON response into out, which may be
// nil to discard it. A body that is not nil is encoded as JSON. A non-2xx
// response becomes an [*apiError]; transport errors and a canceled ctx are
// returned wrapped, so errors.Is(err, context.Canceled) works.
func (driver *Driver) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)

		if err != nil {
			return fmt.Errorf("meilisearch %s %s: encode request: %w", method, path, err)
		}

		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, driver.host+path, payload)

	if err != nil {
		return fmt.Errorf("meilisearch %s %s: %w", method, path, err)
	}

	request.Header.Set("Content-Type", "application/json")

	if driver.config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+driver.config.APIKey)
	}

	response, err := driver.client.Do(request)

	if err != nil {
		return fmt.Errorf("meilisearch %s %s: %w", method, path, err)
	}

	// Status and body processing determine the result; closing the reader is best-effort cleanup.
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		var failed failure
		// A proxy or load balancer may answer with a body that is not a
		// Meilisearch error document; the status alone still says what failed.
		_ = json.UnmarshalRead(io.LimitReader(response.Body, maxErrorBody), &failed)

		return fmt.Errorf("meilisearch %s %s: %w", method, path,
			&apiError{
				Status:  response.StatusCode,
				Code:    failed.Code,
				Message: cmp.Or(failed.Message, response.Status),
			})
	}

	if out == nil {
		_, err = io.Copy(io.Discard, response.Body)

		return err
	}

	if err := json.UnmarshalRead(response.Body, out); err != nil {
		return fmt.Errorf("meilisearch %s %s: decode response: %w", method, path, err)
	}

	return nil
}

// enqueue sends a request that Meilisearch answers with an asynchronous task
// and returns the task's uid without waiting for it.
func (driver *Driver) enqueue(ctx context.Context, method, path string, body any) (int64, error) {
	var accepted struct {
		// TaskUID identifies the queued operation that wait must poll.
		TaskUID int64 `json:"taskUid"`
	}

	if err := driver.do(ctx, method, path, body, &accepted); err != nil {
		return 0, err
	}

	return accepted.TaskUID, nil
}

// wait polls the task until it finishes. It returns an [*apiError] when the
// task failed and an error wrapping ctx's error when ctx ends first, in which
// case the task keeps running on the server.
func (driver *Driver) wait(ctx context.Context, taskUID int64) error {
	path := "/tasks/" + strconv.FormatInt(taskUID, 10)
	interval := minPollInterval

	for {
		var task struct {
			// Status determines whether polling continues or reports completion.
			Status string `json:"status"`

			// Error holds failure details when the task ends unsuccessfully.
			Error *failure `json:"error"`
		}

		if err := driver.do(ctx, http.MethodGet, path, nil, &task); err != nil {
			return err
		}

		switch task.Status {
		case "succeeded":
			return nil
		case "failed":
			message, code := "task failed", ""

			if task.Error != nil {
				message, code = task.Error.Message, task.Error.Code
			}

			return &apiError{
				Code:    code,
				Message: message,
			}
		case "canceled":
			return fmt.Errorf("meilisearch task %d was canceled", taskUID)
		}

		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("meilisearch task %d: %w", taskUID, ctx.Err())
		case <-timer.C:
		}

		interval = min(interval*2, maxPollInterval)
	}
}

// run sends a request that Meilisearch answers with a task and waits for the
// task to finish.
func (driver *Driver) run(ctx context.Context, method, path string, body any) error {
	taskUID, err := driver.enqueue(ctx, method, path, body)

	if err != nil {
		return err
	}

	return driver.wait(ctx, taskUID)
}
