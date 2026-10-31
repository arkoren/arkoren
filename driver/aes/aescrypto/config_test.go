package aescrypto

import (
	"bytes"
	"context"
	"testing"

	"arkoren.dev/foundation/key"
)

// encryptionFixture creates deterministic typed material for builder checks.
func encryptionFixture(t *testing.T) key.Shared {
	t.Helper()

	material, err := key.NewShared(make([]byte, 32))

	if err != nil {
		t.Fatal(err)
	}

	return material
}

// TestConstructionCallbacksOrderedAndDetached verifies once-only ordered edits,
// immediate AAD capture, detached publication, and reusable callback policies.
func TestConstructionCallbacksOrderedAndDetached(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	data := []byte("context")
	policy := func(encrypter *Builder) {
		calls++
		retained = encrypter

		if encrypter.AdditionalData(data) != encrypter {
			t.Fatal("setting returned a different draft")
		}
	}

	first, err := New(encryptionFixture(t), policy, func(encrypter *Builder) {
		if encrypter != retained || calls != 1 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		data[0] = 'X'
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.additionalData[0] = 'Y'
	retained.AdditionalData([]byte("replacement"))
	firstDraft := retained

	second, err := New(encryptionFixture(t), policy)

	if err != nil {
		t.Fatal(err)
	}

	if retained == firstDraft || calls != 3 {
		t.Fatal("policy was replayed or draft was reused")
	}

	if string(first.additionalData) != "context" || string(second.additionalData) != "Xontext" {
		t.Fatal("AAD aliased caller data or retained draft")
	}
}

// TestAdditionalDataReplacesAndClears verifies repeated calls replace rather
// than append context, with nil explicitly restoring the no-AAD default.
func TestAdditionalDataReplacesAndClears(t *testing.T) {
	t.Parallel()

	crypto := &Builder{}
	crypto.AdditionalData([]byte("first"))
	crypto.AdditionalData([]byte("second"))

	if string(crypto.additionalData) != "second" {
		t.Fatal("AAD was not replaced")
	}

	crypto.AdditionalData(nil)

	if len(crypto.additionalData) != 0 {
		t.Fatal("nil did not clear AAD")
	}

	crypto.AdditionalData([]byte("third"))
	crypto.AdditionalData([]byte{})

	if len(crypto.additionalData) != 0 {
		t.Fatal("empty input did not clear AAD")
	}
}

// TestConstructionOmissionDefaults verifies zero callbacks, including expanded
// nil and empty slices, preserve the key and default empty AAD.
func TestConstructionOmissionDefaults(t *testing.T) {
	t.Parallel()

	var policies []func(*Builder)

	for _, configure := range [][]func(*Builder){nil, policies, {}} {
		driver, err := New(encryptionFixture(t), configure...)

		if err != nil {
			t.Fatal(err)
		}

		if len(driver.additionalData) != 0 {
			t.Fatal("default AAD was not empty")
		}
	}
}

// TestConstructionValidatesKeyAfterCallbacks verifies invalid required inputs
// still invoke each callback once before returning the original key error.
func TestConstructionValidatesKeyAfterCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := New(key.Shared{}, func(*Builder) {
		calls++
	}, func(*Builder) {
		calls++
	})

	if err == nil || calls != 2 {
		t.Fatalf("New = %v, callbacks = %d", err, calls)
	}
}

// TestConstructionNilCallbackPanics verifies nil entries remain ordinary
// function-call misuse, even when the required key would be invalid.
func TestConstructionNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New(key.Shared{}, nil)
}

// TestConstructionCallbackPanicPropagates verifies the constructor does not
// translate application callback panics into key validation errors.
func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recovered := recover(); recovered != "policy" {
			t.Fatalf("panic = %v", recovered)
		}
	}()

	_, _ = New(key.Shared{}, func(*Builder) {
		panic("policy")
	})
}

// TestConstructionKeyOwnershipAndZeroing verifies input keys are copied before
// retention and Stop clears only the driver's owned material.
func TestConstructionKeyOwnershipAndZeroing(t *testing.T) {
	t.Parallel()

	raw := bytes.Repeat([]byte{42}, 32)
	material, err := key.NewShared(raw)

	if err != nil {
		t.Fatal(err)
	}

	driver, err := New(material)

	if err != nil {
		t.Fatal(err)
	}

	raw[0] = 1

	if driver.key[0] != 42 {
		t.Fatal("driver retained caller key")
	}

	if err := driver.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(driver.key, make([]byte, 32)) || raw[0] != 1 {
		t.Fatal("Stop did not exclusively clear the owned key")
	}
}
