package session_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// TestFieldDeclarationPreservesLiteralName checks names are neither normalized
// nor escaped and valid declarations have no deferred construction error.
func TestFieldDeclarationPreservesLiteralName(t *testing.T) {
	t.Parallel()

	field := session.NewField[int]("tenant:user / exact")

	if field.Name() != "tenant:user / exact" || field.Err() != nil {
		t.Fatalf("name = %q, error = %v", field.Name(), field.Err())
	}
}

// TestInvalidFieldsDoNotMutate checks both zero and empty declarations fail
// before lookup or mutation, preserving data, ID, expiry, and dirty state.
func TestInvalidFieldsDoNotMutate(t *testing.T) {
	t.Parallel()

	for _, field := range []session.Field[int]{{}, session.NewField[int]("")} {
		current := session.New(time.Now().Add(time.Hour), map[string]any{"": 9})
		current.MarkAsUnchanged()

		id, expiry := current.ID(), current.ExpiresAt()

		_, err := current.Get(field)

		if !errors.Is(err, session.ErrInvalidField) {
			t.Fatalf("get error = %v", err)
		}

		if err := current.Put(field, 1); !errors.Is(err, session.ErrInvalidField) {
			t.Fatalf("put error = %v", err)
		}

		if err := current.Delete(field); !errors.Is(err, session.ErrInvalidField) {
			t.Fatalf("delete error = %v", err)
		}

		if current.HasChanged() || current.ID() != id || current.ExpiresAt() != expiry {
			t.Fatal("invalid operation changed session metadata")
		}

		if current.All()[""] != 9 || len(current.All()) != 1 {
			t.Fatal("invalid operation changed storage")
		}
	}
}

// TestFieldMissingAndTypeErrorsRedactIdentity checks error identities remain
// usable without disclosing the field's sensitive name or stored type/value.
func TestFieldMissingAndTypeErrorsRedactIdentity(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), map[string]any{"private-field": "private-value"})

	_, err := current.Get(session.NewField[int]("private-field"))

	if !errors.Is(err, session.ErrInvalidValueType) || strings.Contains(err.Error(), "private") {
		t.Fatalf("type error = %v", err)
	}

	_, err = current.Get(session.NewField[int]("private-missing"))

	if !errors.Is(err, session.ErrKeyNotFound) || strings.Contains(err.Error(), "private") {
		t.Fatalf("missing error = %v", err)
	}
}

// TestGetRejectsCompatibleInMemoryStruct checks JSON-compatible shapes do not
// bypass the typed in-memory boundary through an arbitrary serialization pass.
func TestGetRejectsCompatibleInMemoryStruct(t *testing.T) {
	t.Parallel()

	type original struct{ Count int }
	type other struct{ Count int }
	current := session.New(time.Now().Add(time.Hour), map[string]any{"counter": original{Count: 3}})

	_, err := current.Get(session.NewField[other]("counter"))

	if !errors.Is(err, session.ErrInvalidValueType) {
		t.Fatalf("compatible struct error = %v", err)
	}
}

// TestGetRejectsNamedInMemoryNumeric checks an integer with a different Go
// identity is not silently accepted even when its JSON representation matches.
func TestGetRejectsNamedInMemoryNumeric(t *testing.T) {
	t.Parallel()

	type count int64
	current := session.New(time.Now().Add(time.Hour), map[string]any{"counter": count(3)})

	_, err := current.Get(session.NewField[int64]("counter"))

	if !errors.Is(err, session.ErrInvalidValueType) {
		t.Fatalf("named number error = %v", err)
	}
}

// TestGetDecodesPersistedJSONForAny checks the raw-JSON branch precedes the
// generic assertion, which would otherwise return bytes for interface fields.
func TestGetDecodesPersistedJSONForAny(t *testing.T) {
	t.Parallel()

	current := readPersistedFields(t, `{"theme":"dark"}`)

	got, err := current.Get(session.NewField[any]("theme"))

	if err != nil || got != "dark" {
		t.Fatalf("decoded any = %v, %v", got, err)
	}
}

// TestGetPersistedJSONRetainsIntegerPrecision checks decoding never passes
// signed or unsigned values through the float64 dynamic JSON representation.
func TestGetPersistedJSONRetainsIntegerPrecision(t *testing.T) {
	t.Parallel()

	current := readPersistedFields(t, `{"signed":9007199254740993,"unsigned":18446744073709551615}`)

	got, err := current.Get(session.NewField[int64]("signed"))

	if err != nil || got != 9007199254740993 {
		t.Fatalf("signed = %d, %v", got, err)
	}

	unsigned, err := current.Get(session.NewField[uint64]("unsigned"))

	if err != nil || unsigned != 18446744073709551615 {
		t.Fatalf("unsigned = %d, %v", unsigned, err)
	}
}

// TestGetPersistedJSONMismatchRetainsErrorIdentity checks persisted mismatches
// fail without leaking the decoder's diagnostic text or the incompatible value.
func TestGetPersistedJSONMismatchRetainsErrorIdentity(t *testing.T) {
	t.Parallel()

	for _, raw := range []jsontext.Value{jsontext.Value(`"private-value"`), jsontext.Value(`{"private":"value"}`)} {
		current := readPersistedFields(t, `{"private-field":`+string(raw)+`}`)

		_, err := current.Get(session.NewField[int]("private-field"))

		if !errors.Is(err, session.ErrInvalidValueType) || strings.Contains(err.Error(), "private") {
			t.Fatalf("decode error = %v", err)
		}
	}
}

// TestFieldFormattingRedactsIdentity covers ordinary, detailed, quoted, and
// structured formatting; explicit Name remains the intentional escape hatch.
func TestFieldFormattingRedactsIdentity(t *testing.T) {
	t.Parallel()

	field := session.NewField[int]("private-field")
	text := fmt.Sprintf("%v %+v %#v %s %q", field, field, field, field, field)

	if strings.Contains(text, "private") || strings.Contains(slog.AnyValue(field).Resolve().String(), "private") {
		t.Fatal("field formatting exposed identity")
	}
}

// TestFieldRetypingRejected compiles intentionally invalid caller code against
// the public API so assignment, conversions, and operation type mismatches stay
// compile-time errors rather than becoming runtime checks.
func TestFieldRetypingRejected(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "-gcflags=-e", "./testdata/retyping")

	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("field retyping compiled")
	}

	for _, diagnostic := range []string{
		"cannot convert session.NewField[string]",
		"cannot use session.NewField[string]",
		"argument to current.Get[int]",
		"argument to current.Put[int]",
		"cannot use 42",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Fatalf("missing %q diagnostic: %s", diagnostic, output)
		}
	}
}

// TestFieldPersistsLiteralJSON checks field declarations do not alter physical
// cache identities or wrap stored JSON members with type/schema metadata.
func TestFieldPersistsLiteralJSON(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	space := cache.NewNamespace[jsontext.Value]("wire")
	var id string
	handler := session.Middleware(cache.New(driver), func(cookies *session.Builder) {
		cookies.Namespace(space)
	})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		current := mustSession(request)
		id = current.ID()

		return current.Put(session.NewField[uint64]("literal:user / id"), 18446744073709551615)
	}))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	raw, err := driver.Get(t.Context(), "wire:"+id)

	if err != nil {
		t.Fatal(err)
	}

	var stored struct {
		Storage jsontext.Value `json:"storage"`
	}

	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}

	if string(stored.Storage) != `{"literal:user / id":18446744073709551615}` {
		t.Fatalf("storage wire JSON = %s", stored.Storage)
	}

	read := session.Middleware(cache.New(driver), func(cookies *session.Builder) {
		cookies.Namespace(space)
	})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		got := requireGet[uint64](t, mustSession(request), "literal:user / id")

		if got != 18446744073709551615 {
			t.Fatalf("persisted uint64 = %d", got)
		}

		return nil
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(response.Cookies()[0])

	_ = read.Record(request)
}
