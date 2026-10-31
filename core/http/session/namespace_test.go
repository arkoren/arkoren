package session_test

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"arkoren.dev/core/http/session"
)

// TestNamespaceDefaultLiteralJoin verifies opaque bytes and the default colon
// without allowing normalization of spaces, embedded delimiters, or slashes.
func TestNamespaceDefaultLiteralJoin(t *testing.T) {
	t.Parallel()

	space := session.NewNamespace[int](" tenant:/ ")
	field := space.Field(" /user: ")

	if space.Name() != " tenant:/ " || space.Separator() != ":" || space.Err() != nil {
		t.Fatalf("namespace = %q, %q, %v", space.Name(), space.Separator(), space.Err())
	}

	if field.Name() != " tenant:/ : /user: " || field.Err() != nil {
		t.Fatalf("field = %q, %v", field.Name(), field.Err())
	}
}

// TestNamespaceMultibyteSeparator verifies separators are literal strings,
// rather than single-byte delimiters, and child overrides happen after joining.
func TestNamespaceMultibyteSeparator(t *testing.T) {
	t.Parallel()

	space := session.NewNamespace[string]("設定", func(fields *session.NamespaceBuilder) {
		fields.Separator(" → ")
	})

	child := space.Subnamespace("表示", func(fields *session.NamespaceBuilder) {
		fields.Separator("::")
	})

	if child.Name() != "設定 → 表示" || child.Field("色").Name() != "設定 → 表示::色" {
		t.Fatalf("child = %q, field = %q", child.Name(), child.Field("色").Name())
	}

	if child.Err() != nil || space.Separator() != " → " {
		t.Fatal("child construction changed parent or failed")
	}
}

// TestNamespaceEmptySeparatorSurvivesDerive verifies explicit concatenation
// remains configured through no-edit derivation and child publication.
func TestNamespaceEmptySeparatorSurvivesDerive(t *testing.T) {
	t.Parallel()

	space := session.NewNamespace[string]("app:", func(fields *session.NamespaceBuilder) {
		if fields.Separator("") != fields {
			t.Fatal("setting did not return the same draft")
		}
	})

	child := space.Derive().Subnamespace("display")

	if child.Separator() != "" || child.Field("theme").Name() != "app:displaytheme" {
		t.Fatalf("separator = %q, field = %q", child.Separator(), child.Field("theme").Name())
	}
}

// TestNamespaceInvalidAncestorsStayInvalid checks both missing roots and empty
// children remain invalid after nonempty compositions and separator overrides.
func TestNamespaceInvalidAncestorsStayInvalid(t *testing.T) {
	t.Parallel()

	for _, space := range []session.Namespace[int]{
		{}, session.NewNamespace[int](""), session.NewNamespace[int]("app").Subnamespace(""),
	} {
		child := space.Derive(func(fields *session.NamespaceBuilder) {
			fields.Separator("/")
		}).Subnamespace("valid")

		if !errors.Is(child.Err(), session.ErrInvalidField) {
			t.Fatalf("descendant error = %v", child.Err())
		}

		if !errors.Is(child.Field("value").Err(), session.ErrInvalidField) {
			t.Fatalf("leaf error = %v", child.Field("value").Err())
		}
	}
}

// TestNamespaceEmptyLeafIsInvalid verifies a nonempty composed physical name
// does not hide an empty local member declaration.
func TestNamespaceEmptyLeafIsInvalid(t *testing.T) {
	t.Parallel()

	field := session.NewNamespace[int]("app").Field("")

	if field.Name() != "app:" || !errors.Is(field.Err(), session.ErrInvalidField) {
		t.Fatalf("field = %q, %v", field.Name(), field.Err())
	}
}

// TestNamespaceCallbacksPublishDetached verifies callback order and once-only
// execution on one shared draft, with independent root and derived publication.
func TestNamespaceCallbacksPublishDetached(t *testing.T) {
	t.Parallel()

	var retained *session.NamespaceBuilder
	calls := ""
	space := session.NewNamespace[int]("app", func(fields *session.NamespaceBuilder) {
		calls += "a"
		retained = fields
		fields.Separator("/")
	}, func(fields *session.NamespaceBuilder) {
		calls += "b"

		if fields != retained {
			t.Fatal("callbacks received different drafts")
		}

		fields.Separator("|")
	})
	retained.Separator("changed")

	derived := space.Derive(func(fields *session.NamespaceBuilder) {
		calls += "c"
		retained = fields
		fields.Separator("~")
	})

	leaf := derived.Field("value")
	retained.Separator("changed")

	if calls != "abc" || space.Separator() != "|" || derived.Separator() != "~" {
		t.Fatalf("calls = %q, separators = %q, %q", calls, space.Separator(), derived.Separator())
	}

	if leaf.Name() != "app~value" {
		t.Fatalf("leaf changed: %q", leaf.Name())
	}
}

// TestNamespaceInvalidConstructionRunsCallbacks verifies invalid domain input
// does not skip synchronous construction edits, including child callbacks.
func TestNamespaceInvalidConstructionRunsCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	edit := func(fields *session.NamespaceBuilder) {
		calls++
		fields.Separator("/")
	}

	space := session.NewNamespace[int]("", edit).Subnamespace("", edit).Derive(edit)

	if calls != 3 || !errors.Is(space.Err(), session.ErrInvalidField) {
		t.Fatalf("calls = %d, error = %v", calls, space.Err())
	}
}

// TestNamespaceNilExpansionsOmitEdits verifies nil callback slices are not
// interpreted as a supplied nil callback and do not reset inherited settings.
func TestNamespaceNilExpansionsOmitEdits(t *testing.T) {
	t.Parallel()

	var edits []session.NamespaceBuilderFunc
	space := session.NewNamespace[int]("app", edits...).Derive(edits...).Subnamespace("child", edits...)
	space = space.Derive([]session.NamespaceBuilderFunc{}...)

	if space.Err() != nil || space.Field("value").Name() != "app:child:value" {
		t.Fatalf("field = %q, error = %v", space.Field("value").Name(), space.Err())
	}
}

// TestNamespaceApplicationPanicPropagates verifies callback failures are not
// converted into domain validation errors, even for an invalid root.
func TestNamespaceApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application panic" {
			t.Fatalf("panic = %v", got)
		}
	}()
	_ = session.NewNamespace[int]("", func(*session.NamespaceBuilder) {
		panic("application panic")
	})
}

// TestNamespaceNilCallbackPanics verifies supplied nil entries retain ordinary
// Go misuse semantics rather than silently acting like omitted edits.
func TestNamespaceNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	_ = session.NewNamespace[int]("app").Subnamespace("child", nil)
}

// TestNamespaceFormattingRedactsIdentity checks every formatting verb and slog
// resolution redacts both name and separator, while Name remains explicit.
func TestNamespaceFormattingRedactsIdentity(t *testing.T) {
	t.Parallel()

	space := session.NewNamespace[int]("private-prefix", func(fields *session.NamespaceBuilder) {
		fields.Separator("private-separator")
	})

	text := fmt.Sprintf("%v %+v %#v %s %q", space, space, space, space, space)

	if strings.Contains(text, "private") || strings.Contains(slog.AnyValue(space).Resolve().String(), "private") {
		t.Fatal("namespace formatting exposed identity")
	}
}

// TestNamespaceRetypingRejected compiles invalid callers to keep namespace
// assignment, conversion, and leaf operations bound to the exact Go type.
func TestNamespaceRetypingRejected(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "-gcflags=-e", "./testdata/namespaceretyping")

	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("namespace retyping compiled")
	}

	for _, diagnostic := range []string{
		"cannot convert session.NewNamespace[string]",
		"cannot use session.NewNamespace[string]",
		"argument to current.Put[int]",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Fatalf("missing %q diagnostic: %s", diagnostic, output)
		}
	}
}
