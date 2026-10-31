package validate_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

// pngMagic is the signature http.DetectContentType recognizes as image/png.
const pngMagic = "\x89PNG\r\n\x1a\n"

// uploadForm parses a multipart body holding one file part per name/content
// pair, keeping at most maxMemory bytes in memory.
func uploadForm(t *testing.T, maxMemory int64, name, content string) *multipart.Form {
	t.Helper()

	var body bytes.Buffer

	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", name)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(maxMemory)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = form.RemoveAll()
	})

	return form
}

// uploadHeader returns the header of a single in-memory uploaded file.
func uploadHeader(t *testing.T, name, content string) *multipart.FileHeader {
	t.Helper()

	return uploadForm(t, 1<<20, name, content).File["file"][0]
}

// requireFileCode validates an uploaded file with the given name and content
// against schema and fails unless the result is code ("" for no violation).
func requireFileCode(t *testing.T, schema validate.FileSchema, filename, content, code string) {
	t.Helper()

	requireCode(t, schema.Validate(context.Background(), uploadHeader(t, filename, content)), code)
}

// TestFileRulesRequiredAbsent verifies the RequiredAbsent file rule case.
func TestFileRulesRequiredAbsent(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Required()
	}).Validate(context.Background(), nil), "required")
}

// TestFileRulesOptionalAbsent verifies the OptionalAbsent file rule case.
func TestFileRulesOptionalAbsent(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Min(10)
	}).Validate(context.Background(), nil), "")
}

// TestFileRulesRequiredPresent verifies the RequiredPresent file rule case.
func TestFileRulesRequiredPresent(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Required()
	}), "a.txt", "x", "")
}

// TestFileRulesMinPass verifies the MinPass file rule case.
func TestFileRulesMinPass(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Min(3)
	}), "a.txt", "abc", "")
}

// TestFileRulesMinFail verifies the MinFail file rule case.
func TestFileRulesMinFail(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Min(3)
	}), "a.txt", "ab", "file.min")
}

// TestFileRulesMaxPass verifies the MaxPass file rule case.
func TestFileRulesMaxPass(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Max(3)
	}), "a.txt", "abc", "")
}

// TestFileRulesMaxFail verifies the MaxFail file rule case.
func TestFileRulesMaxFail(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Max(3)
	}), "a.txt", "abcd", "file.max")
}

// TestFileRulesExtensionsPass verifies the ExtensionsPass file rule case.
func TestFileRulesExtensionsPass(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Extensions("png", "jpg")
	}), "a.jpg", "x", "")
}

// TestFileRulesExtensionsCaseInsensitive verifies the ExtensionsCaseInsensitive file rule case.
func TestFileRulesExtensionsCaseInsensitive(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Extensions("png")
	}), "A.PNG", "x", "")
}

// TestFileRulesExtensionsLeadingDot verifies the ExtensionsLeadingDot file rule case.
func TestFileRulesExtensionsLeadingDot(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Extensions(".PNG")
	}), "a.png", "x", "")
}

// TestFileRulesExtensionsLastOnly verifies the ExtensionsLastOnly file rule case.
func TestFileRulesExtensionsLastOnly(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Extensions("png")
	}), "a.png.exe", "x", "file.extensions")
}

// TestFileRulesExtensionsNone verifies the ExtensionsNone file rule case.
func TestFileRulesExtensionsNone(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Extensions("png")
	}), "png", "x", "file.extensions")
}

// TestFileRulesTypesExact verifies the TypesExact file rule case.
func TestFileRulesTypesExact(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("image/png")
	}), "a.png", pngMagic, "")
}

// TestFileRulesTypesWildcard verifies the TypesWildcard file rule case.
func TestFileRulesTypesWildcard(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("image/*")
	}), "a.png", pngMagic, "")
}

// TestFileRulesTypesWildcardFail verifies the TypesWildcardFail file rule case.
func TestFileRulesTypesWildcardFail(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("video/*")
	}), "a.png", pngMagic, "file.types")
}

// TestFileRulesTypesIgnoresDetectedCharset verifies the TypesIgnoresDetectedCharset file rule case.
func TestFileRulesTypesIgnoresDetectedCharset(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("text/plain")
	}), "a.txt", "hello", "")
}

// TestFileRulesTypesIgnoresPatternParams verifies the TypesIgnoresPatternParams file rule case.
func TestFileRulesTypesIgnoresPatternParams(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("Text/Plain; charset=utf-8")
	}), "a.txt", "hello", "")
}

// TestFileRulesTypesIgnoresClaimedName verifies the TypesIgnoresClaimedName file rule case.
func TestFileRulesTypesIgnoresClaimedName(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("image/png")
	}), "a.png", "<html><body>hi", "file.types")
}

// TestFileRulesTypesEmptyFile verifies the TypesEmptyFile file rule case.
func TestFileRulesTypesEmptyFile(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("image/*")
	}), "a.png", "", "file.types")
}

// TestFileRulesTypesOfficeZip verifies the TypesOfficeZip file rule case.
func TestFileRulesTypesOfficeZip(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("application/zip")
		file.Extensions("docx")
	}), "a.docx", "PK\x03\x04rest", "")
}

// TestFileRulesTypesSVGIsNotImage verifies the TypesSVGIsNotImage file rule case.
func TestFileRulesTypesSVGIsNotImage(t *testing.T) {
	t.Parallel()

	requireFileCode(t, validate.File(func(file *validate.FileBuilder) {
		file.Types("image/*")
	}), "a.svg", `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`, "file.types")
}

// TestFileMessages verifies the English messages render the params.
func TestFileMessages(t *testing.T) {
	t.Parallel()

	header := uploadHeader(t, "a.txt", "hello")

	schema := validate.File(func(file *validate.FileBuilder) {
		file.Min(10)
		file.Max(1)
		file.Extensions("png", ".jpg")
		file.Types("image/*")
	})

	err := schema.Validate(context.Background(), header)

	got := err.Error()

	for _, want := range []string{
		"must be at least 10 bytes",
		"must be at most 1 bytes",
		"must have one of these extensions: png, jpg",
		"must be one of these types: image/*",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q lacks %q", got, want)
		}
	}
}

// TestFileOpenFailureIsNotAViolation verifies that a file that cannot be
// opened aborts validation with the underlying error.
func TestFileOpenFailureIsNotAViolation(t *testing.T) {
	t.Parallel()

	form := uploadForm(t, 0, "a.txt", "hello")
	header := form.File["file"][0]

	if err := form.RemoveAll(); err != nil {
		t.Fatal(err)
	}

	err := validate.File(func(file *validate.FileBuilder) {
		file.Types("text/plain")
	}).Validate(context.Background(), header)

	if err == nil {
		t.Fatal("Validate = nil, want an open error")
	}

	if _, isViolation := err.(validate.Errors); isViolation { //nolint:errorlint // asserting the concrete type is the point.
		t.Fatalf("error %v is a violation, want a plain error", err)
	}
}

// TestFileOnStruct verifies File works on *multipart.FileHeader fields and
// reports violations at the field pointer.
func TestFileOnStruct(t *testing.T) {
	t.Parallel()

	type upload struct {
		// Title provides an ordinary string sibling next to an uploaded file.
		Title string `json:"title"`
		// Avatar provides the uploaded file header checked by the child schema.
		Avatar *multipart.FileHeader `json:"avatar"`
	}

	schema := validate.Struct[upload](func(object *validate.StructBuilder[upload]) {
		object.Field("Title", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Avatar", func(file *validate.FileBuilder) {
			file.Required()
			file.Max(3)
		})
	})

	err := schema.Validate(context.Background(), upload{
		Title:  "t",
		Avatar: uploadHeader(t, "a.png", "toolong"),
	})

	requireCodes(t, err, map[string][]string{"/avatar": {"file.max"}})

	err = schema.Validate(context.Background(), upload{
		Title: "t",
	})

	requireCodes(t, err, map[string][]string{"/avatar": {"required"}})
}
