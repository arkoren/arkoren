package restate_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/driver/restate"
)

// bindPing binds a service so the host has an endpoint to serve.
func bindPing(t *testing.T, host *restate.Host) {
	t.Helper()

	service := restatesdk.NewService("Ping").Handler("Ping", restatesdk.NewServiceHandler(
		func(restatesdk.Context, restatesdk.Void) (restatesdk.Void, error) {
			return restatesdk.Void{}, nil
		}))

	if err := host.Bind(service); err != nil {
		t.Fatal(err)
	}
}

// TestHostLifecycle verifies that the host binds its endpoint, registers
// services, rejects repeated startup, and releases the listener on Stop.
func TestHostLifecycle(t *testing.T) {
	t.Parallel()

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
		}
	}))
	defer admin.Close()

	host := restate.New(func(host *restate.Builder) {
		host.AllowInsecure(true)
	}, func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
	}, func(host *restate.Builder) {
		host.AdminURL(admin.URL)
	})

	if host.Addr() != nil {
		t.Fatal("Addr before Start should be nil")
	}

	bindPing(t, host)

	ctx := t.Context()

	if err := host.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if host.Addr() == nil {
		t.Fatal("Addr after Start is nil")
	}

	if err := host.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(ctx); !errors.Is(err, restate.ErrStarted) {
		t.Fatalf("second Start = %v", err)
	}

	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- host.Serve(serveCtx)
	}()
	cancel()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, time.Second)
	defer stopCancel()

	if err := host.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

// TestPingUnhealthy verifies that an unhealthy administrative endpoint makes
// host readiness fail.
func TestPingUnhealthy(t *testing.T) {
	t.Parallel()

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer admin.Close()

	if err := restate.New(func(host *restate.Builder) {
		host.AdminURL(admin.URL)
	}).Ping(t.Context()); err == nil {
		t.Fatal("expected error")
	}
}

// TestBindAfterStart verifies that native service definitions cannot be added
// after the endpoint starts serving.
func TestBindAfterStart(t *testing.T) {
	t.Parallel()

	host := restate.New(func(host *restate.Builder) {
		host.AllowInsecure(true)
	}, func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
	})
	bindPing(t, host)

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	defer host.Stop(t.Context()) //nolint:errcheck // test cleanup

	if err := host.Bind(nil); !errors.Is(err, restate.ErrStarted) {
		t.Fatalf("Bind after Start = %v", err)
	}
}

// TestRegisterSendsForceFalse verifies that default deployment registration
// explicitly avoids forcing an incompatible replacement.
func TestRegisterSendsForceFalse(t *testing.T) {
	t.Parallel()

	bodies := make(chan map[string]any, 1)
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		var body map[string]any

		if err := json.Unmarshal(raw, &body); err != nil || r.URL.Path != "/deployments" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		bodies <- body
		w.WriteHeader(http.StatusCreated)
	}))
	defer admin.Close()

	host := restate.New(
		func(host *restate.Builder) {
			host.AllowInsecure(true)
		},
		func(host *restate.Builder) {
			host.Addr("127.0.0.1:0")
		},
		func(host *restate.Builder) {
			host.AdminURL(admin.URL)
		},
		func(host *restate.Builder) {
			host.AdvertisedURL("http://example.test:9080")
		},
		func(host *restate.Builder) {
			host.Register(true)
		},
	)
	bindPing(t, host)

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- host.Serve(ctx)
	}()

	body := <-bodies
	cancel()

	// The cancel may land while the response is still in flight.
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if body["force"] != false || body["uri"] != "http://example.test:9080" {
		t.Fatalf("body = %v", body)
	}

	_ = host.Stop(t.Context()) //nolint:errcheck // test cleanup
}

// TestRegisterConflict verifies that an administrative deployment conflict
// remains visible as a startup failure.
func TestRegisterConflict(t *testing.T) {
	t.Parallel()

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer admin.Close()

	host := restate.New(func(host *restate.Builder) {
		host.AllowInsecure(true)
	}, func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
	}, func(host *restate.Builder) {
		host.AdminURL(admin.URL)
	}, func(host *restate.Builder) {
		host.Register(true)
	})
	bindPing(t, host)

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	defer host.Stop(t.Context()) //nolint:errcheck // test cleanup

	if err := host.Serve(t.Context()); err == nil {
		t.Fatal("expected conflict error")
	}
}

// TestRegisterPolicy verifies that explicit force and breaking-change
// registration settings reach the deployment request body.
func TestRegisterPolicy(t *testing.T) {
	t.Parallel()

	bodies := make(chan map[string]any, 1)
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		var body map[string]any
		_ = json.Unmarshal(raw, &body) //nolint:errcheck // checked by the test
		bodies <- body
		w.WriteHeader(http.StatusCreated)
	}))
	defer admin.Close()

	host := restate.New(
		func(host *restate.Builder) {
			host.AllowInsecure(true)
		},
		func(host *restate.Builder) {
			host.Addr("127.0.0.1:0")
		},
		func(host *restate.Builder) {
			host.AdminURL(admin.URL)
		},
		func(host *restate.Builder) {
			host.Register(true)
		},
		func(host *restate.Builder) {
			host.RegisterForce(true)
		},
		func(host *restate.Builder) {
			host.RegisterBreaking(true)
		},
	)
	bindPing(t, host)

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	defer host.Stop(t.Context()) //nolint:errcheck // test cleanup

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = host.Serve(ctx)
	}() //nolint:errcheck // ends with the test

	if body := <-bodies; body["force"] != true || body["breaking"] != true {
		t.Fatalf("body = %v", body)
	}
}

// requireClientOnly verifies host runs client-only: Start does not listen, a
// second Start fails with [restate.ErrStarted], Serve returns cleanly once its
// context ends, and Stop succeeds.
func requireClientOnly(t *testing.T, host *restate.Host) {
	t.Helper()

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if host.Addr() != nil {
		t.Fatal("client-only host listens")
	}

	if err := host.Start(t.Context()); !errors.Is(err, restate.ErrStarted) {
		t.Fatalf("second Start = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- host.Serve(ctx)
	}()
	cancel()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestClientOnlyOption verifies that the explicit client-only option suppresses
// endpoint startup while keeping ingress access available.
func TestClientOnlyOption(t *testing.T) {
	t.Parallel()

	requireClientOnly(t, restate.New(func(host *restate.Builder) {
		host.ClientOnly(true)
	}, func(host *restate.Builder) {
		host.Register(true)
	}))
}

// TestClientOnlyNothing verifies that disabling registration and listening
// leaves a usable ingress-only host.
func TestClientOnlyNothing(t *testing.T) {
	t.Parallel()

	requireClientOnly(t, restate.New(func(host *restate.Builder) {
		host.Register(true)
	}))
}

// TestClientOnlyNoListen verifies that an empty listening address yields
// client-only behavior without opening a listener.
func TestClientOnlyNoListen(t *testing.T) {
	t.Parallel()

	requireClientOnly(t, restate.New(func(host *restate.Builder) {
		host.Addr("256.0.0.1:99999")
	}))
}

// TestClientOnlyWithAdmin verifies that configuring administrative access does
// not force a client-only host to listen.
func TestClientOnlyWithAdmin(t *testing.T) {
	t.Parallel()

	requireClientOnly(t, restate.New(func(host *restate.Builder) {
		host.ClientOnly(true)
	}, func(host *restate.Builder) {
		host.AdminAuthKey("k")
	}))
}

// TestClientOnlyBind verifies that binding a service on an explicitly client-
// only host returns the client-only sentinel.
func TestClientOnlyBind(t *testing.T) {
	t.Parallel()

	host := restate.New(func(host *restate.Builder) {
		host.ClientOnly(true)
	})

	if err := host.Bind(nil); !errors.Is(err, restate.ErrClientOnly) {
		t.Fatalf("Bind = %v", err)
	}
}

// TestAdminAuthKey verifies that administrative requests use the configured
// administrative credential rather than the ingress credential.
func TestAdminAuthKey(t *testing.T) {
	t.Parallel()

	var seen []string

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer admin.Close()

	host := restate.New(func(host *restate.Builder) {
		host.AdminURL(admin.URL)
	}, func(host *restate.Builder) {
		host.AdminAuthKey("secret")
	})

	if err := host.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 1 || seen[0] != "Bearer secret" {
		t.Fatalf("Authorization = %v", seen)
	}
}

// TestQuery verifies that the administrative introspection response is decoded
// into column-keyed rows.
func TestQuery(t *testing.T) {
	t.Parallel()

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler

		if r.URL.Path != "/query" || r.Method != http.MethodPost || r.Header.Get("Accept") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		var body map[string]string

		if err := json.Unmarshal(raw, &body); err != nil || body["query"] != "SELECT 1" {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		_, _ = w.Write([]byte(`{"rows":[{"id":"inv_1","n":2}]}`)) //nolint:errcheck // test handler
	}))
	defer admin.Close()

	rows, err := restate.New(func(host *restate.Builder) {
		host.AdminURL(admin.URL)
	}).Query(t.Context(), "SELECT 1")

	if err != nil {
		t.Fatal(err)
	}

	if len(rows) != 1 || rows[0]["id"] != "inv_1" {
		t.Fatalf("rows = %v", rows)
	}
}

// TestQueryError verifies that an unsuccessful administrative query response
// becomes an error instead of returning empty rows.
func TestQueryError(t *testing.T) {
	t.Parallel()

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad sql")) //nolint:errcheck // test handler
	}))
	defer admin.Close()

	if _, err := restate.New(func(host *restate.Builder) {
		host.AdminURL(admin.URL)
	}).Query(t.Context(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

// TestStartRequiresIdentityKeys verifies that a host with bound services and
// neither identity keys nor AllowInsecure refuses to open a listener.
func TestStartRequiresIdentityKeys(t *testing.T) {
	t.Parallel()

	host := restate.New(func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
	})
	bindPing(t, host)

	if err := host.Start(t.Context()); !errors.Is(err, restate.ErrNoIdentityKeys) {
		t.Fatalf("Start = %v, want ErrNoIdentityKeys", err)
	}

	if host.Addr() != nil {
		t.Fatal("Start opened a listener without identity keys")
	}
}

// TestStartWithIdentityKeys verifies that a configured identity key lets the
// host start and that the endpoint rejects an unsigned request.
func TestStartWithIdentityKeys(t *testing.T) {
	t.Parallel()

	host := restate.New(
		func(host *restate.Builder) {
			host.Addr("127.0.0.1:0")
		},
		func(host *restate.Builder) {
			host.IdentityKeys("publickeyv1_w7YHemBctH5Ck2nQRQ47iBBqhNHy4FV7t2Usbye2A6f")
		},
	)
	bindPing(t, host)

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	go func() {
		_ = host.Serve(ctx)
	}()

	t.Cleanup(func() {
		_ = host.Stop(context.WithoutCancel(ctx))
	})

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host.Addr().String()+"/discover", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := http.DefaultClient.Do(request)

	if err != nil {
		t.Fatal(err)
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned discover status = %d, want 401", response.StatusCode)
	}
}
