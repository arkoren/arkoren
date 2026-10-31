package openaillm

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// TestBuilderDefaultsAndExplicitZero checks endpoint defaults separately from
// explicit false and zero overrides, which must not be normalized away.
func TestBuilderDefaultsAndExplicitZero(t *testing.T) {
	t.Parallel()

	driver, err := New()

	if err != nil {
		t.Fatal(err)
	}

	if !driver.strict || driver.config.MaxRetries != 0 || driver.config.EmbedBatchSize != 2048 {
		t.Fatal("default settings changed")
	}

	driver, err = New(func(driver *Builder) {
		driver.StrictSchemas(false)
		driver.MaxRetries(0)
		driver.EmbedBatchSize(0)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.strict || driver.config.MaxRetries != 0 || driver.config.EmbedBatchSize != 0 {
		t.Fatal("explicit false or zero was overwritten")
	}
}

// TestBuilderMethodsReturnDraft checks receiver identity without invoking native
// hooks, which are runtime request policies rather than construction callbacks.
func TestBuilderMethodsReturnDraft(t *testing.T) {
	t.Parallel()

	driver := &Builder{}
	drafts := []*Builder{
		driver.APIKey("key"), driver.BaseURL("https://api.example/v1"),
		driver.API(ChatCompletions), driver.MaxRetries(0), driver.HTTPClient(http.DefaultClient),
		driver.Organization("org"), driver.Project("project"), driver.StrictSchemas(false),
		driver.EmbedBatchSize(1), driver.ResponsesHook(nil), driver.ChatHook(nil),
	}

	for _, draft := range drafts {
		if draft != driver {
			t.Fatal("setting returned another draft")
		}
	}
}

// TestNewFromPublishesCompletedDraft checks one ordered callback execution and
// native-hook deferral while preserving the injected SDK client's identity.
func TestNewFromPublishesCompletedDraft(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	hooks := 0
	client := openai.NewClient()
	driver, err := NewFrom(client, func(driver *Builder) {
		retained = driver
		calls++
		driver.API(ChatCompletions)
		driver.ResponsesHook(func(*responses.ResponseNewParams) {
			hooks++
		})
	}, func(driver *Builder) {
		calls++

		if driver != retained || calls != 2 {
			t.Fatal("callbacks were replayed or reordered")
		}

		driver.ChatHook(func(*openai.ChatCompletionNewParams) {
			hooks++
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.API(Responses).ChatHook(nil)

	if driver.config.API != ChatCompletions || driver.config.ChatHook == nil || hooks != 0 {
		t.Fatal("publication retained a mutable root or invoked a request hook")
	}
}

// TestConstructorsOmitEmptyCallbackSlices verifies both expansion forms supply
// zero callbacks and preserve the ordinary constructor defaults.
func TestConstructorsOmitEmptyCallbackSlices(t *testing.T) {
	t.Parallel()

	var configure []func(*Builder)

	if _, err := New(configure...); err != nil {
		t.Fatal(err)
	}

	if _, err := NewFrom(openai.NewClient(), []func(*Builder){}...); err != nil {
		t.Fatal(err)
	}

	if _, err := New([]func(*Builder){}...); err != nil {
		t.Fatal(err)
	}

	if _, err := NewFrom(openai.NewClient(), configure...); err != nil {
		t.Fatal(err)
	}
}

// TestNewNilCallbackPanics verifies supplied nil is application misuse, not a
// default configuration or validation error.
func TestNewNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New(nil)
}

// TestNewFromNilCallbackPanics checks the injected-client constructor applies
// the same direct-call misuse semantics as New.
func TestNewFromNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = NewFrom(openai.NewClient(), nil)
}

// TestInvalidSettingsStillRunAllCallbacks verifies final validation follows all
// policies, including when a prior policy supplied an invalid connection value.
func TestInvalidSettingsStillRunAllCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := NewFrom(openai.NewClient(), func(driver *Builder) {
		calls++
		driver.MaxRetries(-1)
	}, func(driver *Builder) {
		calls++
		driver.Project("ignored")
	})

	if err == nil || calls != 2 {
		t.Fatal("validation ran before all callbacks")
	}
}

// TestBuilderApplicationPanicPropagates checks constructors do not recover or
// replace application panics from construction callbacks.
func TestBuilderApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	_, _ = New(func(driver *Builder) {
		panic("application panic")
	})
}

// TestBuilderReusablePolicyGetsFreshDraft checks shared construction policies
// never reuse a retained root across independently constructed components.
func TestBuilderReusablePolicyGetsFreshDraft(t *testing.T) {
	t.Parallel()

	var drafts []*Builder
	configure := func(driver *Builder) {
		drafts = append(drafts, driver)
		driver.APIKey("key")
	}

	if _, err := New(configure); err != nil {
		t.Fatal(err)
	}

	if _, err := New(configure); err != nil {
		t.Fatal(err)
	}

	if len(drafts) != 2 || drafts[0] == drafts[1] {
		t.Fatal("construction reused a draft")
	}
}

// TestAPIJSONUsesNames guards the configuration representation of both endpoint
// families, independently of the SDK's native endpoint types.
func TestAPIJSONUsesNames(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]API{Responses, ChatCompletions})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["responses","chat_completions"]` {
		t.Fatalf("API JSON = %s", data)
	}

	var decoded API

	if err := json.Unmarshal([]byte(`"chat_completions"`), &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded != ChatCompletions {
		t.Fatalf("decoded API = %q", decoded)
	}
}

// TestStrictModeJSONUsesNames guards explicit strict schema policy names rather
// than relying on numeric ordinals that change when constants are reordered.
func TestStrictModeJSONUsesNames(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]StrictMode{StrictDefault, StrictOn, StrictOff})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["default","on","off"]` {
		t.Fatalf("strict mode JSON = %s", data)
	}
}

// TestEmptyAPISelectsResponses verifies an explicit empty configuration retains
// the formerly numeric-zero endpoint default, including strict schemas.
func TestEmptyAPISelectsResponses(t *testing.T) {
	t.Parallel()

	driver, err := New(func(driver *Builder) {
		driver.API("")
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.config.API != Responses || driver.config.StrictSchemas != StrictDefault || !driver.strict {
		t.Fatal("empty endpoint did not retain Responses and strict schemas")
	}
}

// TestChatDefaultDisablesStrictSchemas verifies normalization does not apply the
// Responses strict default to compatible Chat Completions servers.
func TestChatDefaultDisablesStrictSchemas(t *testing.T) {
	t.Parallel()

	driver, err := New(func(driver *Builder) {
		driver.API(ChatCompletions)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.config.StrictSchemas != StrictDefault || driver.strict {
		t.Fatal("Chat Completions enabled strict schemas by default")
	}
}
