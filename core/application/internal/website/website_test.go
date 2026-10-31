package website

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWelcomeParseFailure checks that a damaged embedded template produces a
// returned error rather than a panic or page bytes. This test is serial because
// it temporarily replaces the package-owned embedded source.
func TestWelcomeParseFailure(t *testing.T) {
	original := welcomeHTML
	t.Cleanup(func() {
		welcomeHTML = original
	})

	welcomeHTML = `{{`
	recorder := httptest.NewRecorder()

	err := Handler("")(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if err == nil {
		t.Fatal("malformed template returned no error")
	}

	if recorder.Body.Len() != 0 {
		t.Fatalf("partial response = %q", recorder.Body.String())
	}
}

// TestWelcomeRenderFailure checks that template execution is buffered even when
// failure occurs after rendering some text. Serial source replacement exercises
// the failure boundary without introducing production injection hooks.
func TestWelcomeRenderFailure(t *testing.T) {
	original := welcomeHTML
	t.Cleanup(func() {
		welcomeHTML = original
	})

	welcomeHTML = `partial page {{template "missing" .}}`
	recorder := httptest.NewRecorder()

	err := Handler("")(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if err == nil {
		t.Fatal("incomplete template returned no error")
	}

	if recorder.Body.Len() != 0 {
		t.Fatalf("partial response = %q", recorder.Body.String())
	}

	if recorder.Header().Get("Content-Type") != "" {
		t.Fatal("render failure set an HTML content type before router error handling")
	}
}
