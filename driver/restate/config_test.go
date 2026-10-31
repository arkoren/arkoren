package restate

import (
	"testing"
)

// TestHostConstructionCallbacks verifies ordered, once-only edits and
// publication isolation from retained construction drafts.
func TestHostConstructionCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *Builder
	component := New(func(host *Builder) {
		calls++
		retained = host

		if host.Addr("first") != host {
			t.Fatal("setting returned a different draft")
		}
	}, func(host *Builder) {
		if calls != 1 || host != retained {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		host.Addr("127.0.0.1:0")
	})

	retained.Addr("changed")

	if calls != 2 || component.config.Addr != "127.0.0.1:0" {
		t.Fatalf("published settings changed or callback replayed: calls=%d settings=%+v", calls, component.config)
	}
}

// TestHostEmptyConstructionCallbacks verifies that omitted policies and
// expanded nil and empty policy slices all preserve the same defaults.
func TestHostEmptyConstructionCallbacks(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	component := New(absent...)
	empty := []func(*Builder){}
	other := New(empty...)

	if component.config.Addr != other.config.Addr {
		t.Fatal("empty construction policies changed defaults")
	}
}

// TestHostNilConstructionCallback verifies that supplied nil callbacks are
// programmer errors, not silently ignored optional settings.
func TestHostNilConstructionCallback(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_ = New(nil)
}

// TestHostCopiesIdentityKeys verifies that both input slices and a retained
// construction draft are detached before the host can serve requests.
func TestHostCopiesIdentityKeys(t *testing.T) {
	t.Parallel()

	keys := []string{"publickeyv1_first"}
	var retained *Builder
	host := New(func(host *Builder) {
		retained = host
		host.IdentityKeys(keys...)
		keys[0] = "input changed"
	})

	retained.config.IdentityKeys[0] = "draft changed"

	if host.config.IdentityKeys[0] != "publickeyv1_first" {
		t.Fatal("identity keys were not detached at capture and publication")
	}
}

// TestHostNormalizesEmptySettings verifies completed drafts receive the existing
// endpoint defaults without creating a listener or changing security policy.
func TestHostNormalizesEmptySettings(t *testing.T) {
	t.Parallel()

	host := New(func(host *Builder) {
		host.Addr("")
		host.IngressURL("")
		host.AdminURL("")
	})

	if host.config.Addr != ":9080" || host.config.AdvertisedURL != "http://localhost:9080" {
		t.Fatal("empty endpoint settings did not use defaults")
	}

	if host.config.IngressURL != "http://localhost:8080" || host.config.AdminURL != "http://localhost:9070" {
		t.Fatal("empty API URLs did not use defaults")
	}

	if host.config.AllowInsecure || host.Addr() != nil {
		t.Fatal("construction changed endpoint security or opened a listener")
	}
}
