package response_test

import (
	"errors"
	"fmt"
	htmltemplate "html/template"
	"net/http"
	"net/http/httptest"
	"testing"
	"text/template"

	"arkoren.dev/core/http/response"
	"arkoren.dev/foundation/problem"
)

// ExampleHTMLTemplate escapes user data before committing the response.
func ExampleHTMLTemplate() {
	page, err := htmltemplate.New("greeting").Parse("<p>Hello, {{.}}</p>")

	if err != nil {
		fmt.Println(err)

		return
	}

	writer := httptest.NewRecorder()

	if err := response.HTMLTemplate(writer, http.StatusOK, page, "<reader>"); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(writer.Header().Get("Content-Type"))
	fmt.Println(writer.Body.String())
	// Output:
	// text/html; charset=utf-8
	// <p>Hello, &lt;reader&gt;</p>
}

// ExampleStringTemplate renders plain text before commitment.
func ExampleStringTemplate() {
	page, err := template.New("greeting").Parse("Hello, {{.}}")

	if err != nil {
		fmt.Println(err)

		return
	}

	writer := httptest.NewRecorder()

	if err := response.StringTemplate(writer, http.StatusOK, page, "reader"); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(writer.Header().Get("Content-Type"))
	fmt.Println(writer.Body.String())
	// Output:
	// text/plain; charset=utf-8
	// Hello, reader
}

// TestRawSetsDefaultContentType verifies raw response type defaults.
func TestRawSetsDefaultContentType(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()

	if err := response.Raw(writer, http.StatusOK, []byte("data")); err != nil {
		t.Fatal(err)
	}

	if writer.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatal("missing default content type")
	}
}

// TestValidateRedirectTargetRejectsProtocolRelativeURL verifies unsafe redirect rejection.
func TestValidateRedirectTargetRejectsProtocolRelativeURL(t *testing.T) {
	t.Parallel()

	details := unsafeRedirectProblem(t, response.ValidateRedirectTarget("//evil.example"))

	assertUnsafeRedirectProblem(t, details)
}

// unsafeRedirectProblem returns redirect problem details or fails the test.
func unsafeRedirectProblem(t *testing.T, err error) problem.Details {
	t.Helper()

	var details problem.Details

	if !errors.As(err, &details) {
		t.Fatalf("expected problem details, got %T", err)
	}

	return details
}

// assertUnsafeRedirectProblem verifies unsafe redirect problem metadata.
func assertUnsafeRedirectProblem(t *testing.T, details problem.Details) {
	t.Helper()

	if details.Status() != http.StatusBadRequest {
		t.Fatalf("got %#v", details)
	}

	if details.Title() != "Invalid redirect target" {
		t.Fatalf("got %#v", details)
	}

	if details.Detail() != "The redirect target must be a safe relative path." {
		t.Fatalf("got %#v", details)
	}
}

// TestSafeRedirectWritesSafeRelativeTarget verifies safe redirect targets.
func TestSafeRedirectWritesSafeRelativeTarget(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()

	if err := response.SafeRedirect(writer, http.StatusFound, "/projects"); err != nil {
		t.Fatal(err)
	}

	if writer.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", writer.Code, http.StatusFound)
	}

	if target := writer.Header().Get("Location"); target != "/projects" {
		t.Errorf("Location = %q, want %q", target, "/projects")
	}
}

// TestValidateRedirectTargetRejectsBackslash checks that backslash-containing
// paths cannot bypass redirect-target validation through alternate slash
// interpretation.
func TestValidateRedirectTargetRejectsBackslash(t *testing.T) {
	t.Parallel()

	for _, target := range []string{`/\evil.com`, `/\/evil.com`} {
		if err := response.ValidateRedirectTarget(target); err == nil {
			t.Fatalf("target %q accepted", target)
		}
	}
}
