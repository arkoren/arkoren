package mailgunmail

import (
	"testing"

	"github.com/mailgun/mailgun-go/v5"
)

// TestConstructionCallbacksShareDraftOnce verifies order, same-draft returns,
// last-write replacement and isolation from retained construction pointers.
func TestConstructionCallbacksShareDraftOnce(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	driver, err := New("mg.example.com", "key", func(transport *Builder) {
		calls++
		retained = transport

		if transport.Endpoint("https://old.example.com") != transport {
			t.Fatal("Endpoint returned a different draft")
		}
	}, func(transport *Builder) {
		calls++

		if transport != retained || transport.config.endpoint != "https://old.example.com" {
			t.Fatal("callbacks did not share ordered settings")
		}

		transport.Endpoint("https://api.eu.mailgun.net")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Endpoint("https://changed.example.com")

	if calls != 2 || driver.Client().APIBase() != "https://api.eu.mailgun.net" {
		t.Fatalf("callbacks = %d, endpoint = %s", calls, driver.Client().APIBase())
	}
}

// TestConstructionEmptyCallbacksKeepSDKDefaults verifies nil and empty slice
// expansion both omit edits without changing the SDK's default endpoint.
func TestConstructionEmptyCallbacksKeepSDKDefaults(t *testing.T) {
	t.Parallel()

	var policies []func(*Builder)

	driver, err := New("mg.example.com", "key", policies...)

	if err != nil {
		t.Fatal(err)
	}

	other, err := New("mg.example.com", "key", []func(*Builder){}...)

	if err != nil {
		t.Fatal(err)
	}

	want := mailgun.NewMailgun("key").APIBase()

	if driver.Client().APIBase() != want || other.Client().APIBase() != want {
		t.Fatal("empty callbacks changed SDK defaults")
	}
}

// TestConstructionDraftsAreFresh verifies one constructor never reuses another
// constructor's draft or endpoint override.
func TestConstructionDraftsAreFresh(t *testing.T) {
	t.Parallel()

	var first *Builder

	_, err := New("mg.example.com", "key", func(transport *Builder) {
		first = transport
		transport.Endpoint("https://api.eu.mailgun.net")
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = New("mg.example.com", "key", func(transport *Builder) {
		if transport == first || transport.config.endpoint != "" {
			t.Fatal("draft reused")
		}
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestConstructionNilCallbackPanics verifies nil entries are invoked directly
// before checking even invalid required arguments.
func TestConstructionNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New("", "", nil)
}

// TestConstructionCallbackPanicPropagates verifies application panics are not
// converted into constructor errors.
func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "policy panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	_, _ = New("", "", func(transport *Builder) {
		panic("policy panic")
	})
}

// TestConstructionCallbacksPrecedeValidation verifies every callback runs once
// even when the positional sending domain is invalid.
func TestConstructionCallbacksPrecedeValidation(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := New("", "key", func(transport *Builder) {
		calls++
		transport.Endpoint("https://api.eu.mailgun.net")
	})

	if err == nil || calls != 1 {
		t.Fatalf("err = %v, callbacks = %d", err, calls)
	}
}

// TestConstructionRejectsEmptyAPIKey preserves required secret validation.
func TestConstructionRejectsEmptyAPIKey(t *testing.T) {
	t.Parallel()

	if _, err := New("mg.example.com", ""); err == nil {
		t.Fatal("empty API key accepted")
	}
}

// TestConstructionRejectsInvalidEndpoint preserves SDK endpoint validation
// without making a network request.
func TestConstructionRejectsInvalidEndpoint(t *testing.T) {
	t.Parallel()

	_, err := New("mg.example.com", "key", func(transport *Builder) {
		transport.Endpoint("https://api.mailgun.net/v3")
	})

	if err == nil {
		t.Fatal("invalid endpoint accepted")
	}
}

// TestNewFromBorrowsConfiguredClient verifies the settings-free constructor
// preserves native identity and leaves the owner's endpoint untouched.
func TestNewFromBorrowsConfiguredClient(t *testing.T) {
	t.Parallel()

	client := mailgun.NewMailgun("key")

	if err := client.SetAPIBase("https://api.eu.mailgun.net"); err != nil {
		t.Fatal(err)
	}

	driver, err := NewFrom(client, "mg.example.com")

	if err != nil {
		t.Fatal(err)
	}

	if driver.Client() != client || client.APIBase() != "https://api.eu.mailgun.net" {
		t.Fatal("NewFrom replaced or reconfigured caller client")
	}
}

// TestNewFromRejectsNilClient preserves validation without adding settings.
func TestNewFromRejectsNilClient(t *testing.T) {
	t.Parallel()

	if _, err := NewFrom(nil, "mg.example.com"); err == nil {
		t.Fatal("nil client accepted")
	}
}

// TestNewFromRejectsEmptyDomain preserves the required domain absent from the
// SDK client without adding a callback to this settings-free constructor.
func TestNewFromRejectsEmptyDomain(t *testing.T) {
	t.Parallel()

	if _, err := NewFrom(mailgun.NewMailgun("key"), ""); err == nil {
		t.Fatal("empty domain accepted")
	}
}

// TestConstructionValidatesCompletedDraft verifies an invalid early endpoint
// can be replaced before the SDK validates the completed construction state.
func TestConstructionValidatesCompletedDraft(t *testing.T) {
	t.Parallel()

	driver, err := New("mg.example.com", "key", func(transport *Builder) {
		transport.Endpoint("https://api.mailgun.net/v3")
	}, func(transport *Builder) {
		transport.Endpoint("")
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.Client().APIBase() != mailgun.NewMailgun("key").APIBase() {
		t.Fatal("empty replacement did not restore SDK default")
	}
}
