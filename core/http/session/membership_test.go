package session_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// TestNamespaceFieldSharesStandaloneIdentity verifies composed definitions and
// exact standalone declarations address one member for reads, writes, deletes.
func TestNamespaceFieldSharesStandaloneIdentity(t *testing.T) {
	t.Parallel()

	field := session.NewNamespace[int]("app").Subnamespace("cart").Field("count")
	exact := session.NewField[int]("app:cart:count")
	current := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := current.Put(field, 7); err != nil {
		t.Fatal(err)
	}

	got, err := current.Get(exact)

	if err != nil || got != 7 {
		t.Fatalf("standalone read = %d, %v", got, err)
	}

	if err := current.Put(exact, 9); err != nil {
		t.Fatal(err)
	}

	got, err = current.Get(field)

	if err != nil || got != 9 {
		t.Fatalf("composed read = %d, %v", got, err)
	}

	if err := current.Delete(field); err != nil {
		t.Fatal(err)
	}

	_, err = current.Get(exact)

	if !errors.Is(err, session.ErrKeyNotFound) {
		t.Fatalf("deleted member error = %v", err)
	}
}

// TestNamespaceFieldPersistsExactMember verifies namespace composition changes
// only the deliberate JSON member identity, not the record codec or cache key.
func TestNamespaceFieldPersistsExactMember(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	field := session.NewNamespace[uint64]("app").Subnamespace("cart").Field("count")
	var id string
	handler := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		current := mustSession(request)
		id = current.ID()

		return current.Put(field, uint64(18446744073709551615))
	}))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	raw, err := driver.Get(t.Context(), "arkoren:session:"+id)

	if err != nil {
		t.Fatal(err)
	}

	// stored selects only the physical member payload from the existing record.
	var stored struct {
		// Storage is raw JSON to keep exact integer precision in this wire check.
		Storage jsontext.Value `json:"storage"`
	}

	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}

	if string(stored.Storage) != `{"app:cart:count":18446744073709551615}` {
		t.Fatalf("storage wire JSON = %s", stored.Storage)
	}

	read := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		current := mustSession(request)

		got, err := current.Get(session.NewField[uint64]("app:cart:count"))

		if err != nil || got != 18446744073709551615 {
			t.Fatalf("standalone persisted read = %d, %v", got, err)
		}

		return current.Delete(field)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(response.Cookies()[0])

	_ = read.Record(request)

	raw, err = driver.Get(t.Context(), "arkoren:session:"+id)

	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}

	if string(stored.Storage) != `{}` {
		t.Fatalf("storage after deletion = %s", stored.Storage)
	}
}

// TestNamespaceFieldDecodesPersistedNamedInteger verifies raw persisted members
// decode directly into a named type without losing precision through float64.
func TestNamespaceFieldDecodesPersistedNamedInteger(t *testing.T) {
	t.Parallel()

	// identifier retains a distinct Go identity across the raw JSON boundary.
	type identifier uint64
	field := session.NewNamespace[identifier]("app").Field("id")
	current := readPersistedFields(t, `{"app:id":18446744073709551615}`)

	got, err := current.Get(field)

	if err != nil || got != identifier(18446744073709551615) {
		t.Fatalf("persisted named identifier = %d, %v", got, err)
	}

	if err := current.Delete(field); err != nil {
		t.Fatal(err)
	}

	_, err = current.Get(session.NewField[identifier]("app:id"))

	if !errors.Is(err, session.ErrKeyNotFound) {
		t.Fatalf("persisted deletion error = %v", err)
	}
}

// TestNamespaceFieldRejectsInMemoryRetyping verifies named values are preserved
// in memory, not converted to a compatible numeric type through JSON.
func TestNamespaceFieldRejectsInMemoryRetyping(t *testing.T) {
	t.Parallel()

	// identifier distinguishes the application ID from a plain uint64.
	type identifier uint64
	current := session.New(time.Now().Add(time.Hour), map[string]any{})
	field := session.NewNamespace[identifier]("app").Field("id")

	if err := current.Put(field, identifier(18446744073709551615)); err != nil {
		t.Fatal(err)
	}

	got, err := current.Get(field)

	if err != nil || got != identifier(18446744073709551615) {
		t.Fatalf("named identifier = %d, %v", got, err)
	}

	_, err = current.Get(session.NewField[uint64]("app:id"))

	if !errors.Is(err, session.ErrInvalidValueType) {
		t.Fatalf("in-memory retyping error = %v", err)
	}
}

// TestNamespaceInvalidLeavesDoNotMutate verifies inherited errors precede all
// state access even when storage already contains the composed physical name.
func TestNamespaceInvalidLeavesDoNotMutate(t *testing.T) {
	t.Parallel()

	for _, field := range []session.Field[int]{
		session.NewNamespace[int]("").Subnamespace("app").Field("count"),
		session.NewNamespace[int]("app").Subnamespace("").Subnamespace("cart").Field("count"),
		session.NewNamespace[int]("app").Field(""),
	} {
		current := session.New(time.Now().Add(time.Hour), map[string]any{field.Name(): 7})
		current.MarkAsUnchanged()

		before := current.All()
		id, expiry := current.ID(), current.ExpiresAt()

		_, err := current.Get(field)

		if !errors.Is(err, session.ErrInvalidField) {
			t.Fatalf("read error = %v", err)
		}

		if err := current.Put(field, 9); !errors.Is(err, session.ErrInvalidField) {
			t.Fatalf("write error = %v", err)
		}

		if err := current.Delete(field); !errors.Is(err, session.ErrInvalidField) {
			t.Fatalf("delete error = %v", err)
		}

		if current.HasChanged() || current.ID() != id || current.ExpiresAt() != expiry {
			t.Fatal("invalid leaf changed session metadata")
		}

		if !reflect.DeepEqual(before, current.All()) {
			t.Fatal("invalid leaf changed session storage")
		}
	}
}
