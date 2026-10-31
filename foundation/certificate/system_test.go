package certificate

import (
	"crypto/x509"
	"errors"
	"testing"
)

// TestSystemPoolAdditive compares the real system roots with an additive export
// and the private-only export, without modifying the operating-system store.
func TestSystemPoolAdditive(t *testing.T) {
	t.Parallel()

	root, err := GenerateRoot("Additional test CA")

	if err != nil {
		t.Fatal(err)
	}

	system, err := x509.SystemCertPool()

	if err != nil {
		t.Fatal(err)
	}

	private, err := root.Bundle().Pool()

	if err != nil {
		t.Fatal(err)
	}

	combined, err := root.Bundle().SystemPool()

	if err != nil {
		t.Fatal(err)
	}

	want := x509.NewCertPool()
	want.AddCert(root.Bundle().Certificates()[0])

	if combined.Equal(system) || !private.Equal(want) {
		t.Fatal("additional root missing or private pool contains system roots")
	}

	system.AddCert(root.Bundle().Certificates()[0])

	if !combined.Equal(system) {
		t.Fatal("additive pool differs from system plus bundle")
	}
}

// TestSystemPoolDetached changes one export and confirms that a fresh export
// does not gain that trust anchor through either the bundle or system roots.
func TestSystemPoolDetached(t *testing.T) {
	t.Parallel()

	root, err := GenerateRoot("Additional test CA")

	if err != nil {
		t.Fatal(err)
	}

	combined, err := root.Bundle().SystemPool()

	if err != nil {
		t.Fatal(err)
	}

	want := combined.Clone()

	other, err := GenerateRoot("Other test CA")

	if err != nil {
		t.Fatal(err)
	}

	combined.AddCert(other.Bundle().Certificates()[0])

	fresh, err := root.Bundle().SystemPool()

	if err != nil {
		t.Fatal(err)
	}

	if !fresh.Equal(want) {
		t.Fatal("pool mutation affected the bundle or system roots")
	}
}

// TestSystemPoolLoadFailure retains the system loader cause instead of silently
// returning a usable private-only pool. The loader is local to this call.
func TestSystemPoolLoadFailure(t *testing.T) {
	t.Parallel()

	root, err := GenerateRoot("Test CA")

	if err != nil {
		t.Fatal(err)
	}

	want := errors.New("system roots unavailable")
	pool, err := root.Bundle().systemPool(func() (*x509.CertPool, error) {
		return nil, want
	})

	if pool != nil || !errors.Is(err, want) {
		t.Fatalf("pool = %v, error = %v", pool, err)
	}
}

// TestSystemPoolZero checks validity before any system loading takes place.
// The public entry point and injected boundary both reject the zero Bundle.
func TestSystemPoolZero(t *testing.T) {
	t.Parallel()

	public := Bundle{}
	_, err := public.systemPool(func() (*x509.CertPool, error) {
		t.Fatal("system loader called for invalid bundle")

		return nil, nil
	})

	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}

	if _, err := public.SystemPool(); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
