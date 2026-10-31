package response_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/response"
	"arkoren.dev/foundation/collection"
)

// collectionWriter observes commitment separately from the recorder's default
// status, which is already 200 before a response has actually been written.
type collectionWriter struct {
	*httptest.ResponseRecorder

	// committed records any attempt to write a final response.
	committed bool
}

// WriteHeader records commitment before forwarding the response status.
func (writer *collectionWriter) WriteHeader(status int) {
	writer.committed = true
	writer.ResponseRecorder.WriteHeader(status)
}

// Write records implicit commitment before forwarding response bytes.
func (writer *collectionWriter) Write(data []byte) (int, error) {
	writer.committed = true

	return writer.ResponseRecorder.Write(data)
}

// TestLazyCollectionJSONMaterializesBeforeCommit verifies ordinary JSON remains
// a buffered document rather than progressively emitting collection elements.
func TestLazyCollectionJSONMaterializesBeforeCommit(t *testing.T) {
	t.Parallel()

	writer := &collectionWriter{ResponseRecorder: httptest.NewRecorder()}
	consumed := 0
	items := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for _, value := range []int{1, 2} {
			if writer.committed {
				t.Fatal("JSON response committed before source exhaustion")
			}

			consumed++

			if !yield(value, nil) {
				return
			}
		}
	})

	if err := response.JSON(writer, http.StatusOK, items); err != nil {
		t.Fatal(err)
	}

	if consumed != 2 || !writer.committed || writer.Body.String() != "[1,2]\n" {
		t.Fatalf("consumed=%d, committed=%v, body=%q", consumed, writer.committed, writer.Body.String())
	}
}

// TestLazyCollectionJSONFailureLeavesResponseUncommitted protects the router's
// opportunity to render an error instead of publishing an incomplete prefix.
func TestLazyCollectionJSONFailureLeavesResponseUncommitted(t *testing.T) {
	t.Parallel()

	want := errors.New("source failed")
	writer := &collectionWriter{ResponseRecorder: httptest.NewRecorder()}
	items := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		if !yield(1, nil) {
			return
		}

		yield(0, want)
	})

	err := response.JSON(writer, http.StatusOK, items)

	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want source failure", err)
	}

	if writer.committed || writer.Body.Len() != 0 || writer.Header().Get("Content-Type") != "" {
		t.Fatalf("committed=%v, body=%q, headers=%v", writer.committed, writer.Body.String(), writer.Header())
	}
}
