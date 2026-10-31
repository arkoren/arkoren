// Package itest holds the shared setup of the Restate integration tests that
// run against the restate service of driver/restate/compose.yaml.
package itest

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"arkoren.dev/driver/restate"
)

// Ports of the compose service. ARKOREN_RESTATE_INGRESS_URL and
// ARKOREN_RESTATE_ADMIN_URL override them.
const (
	defaultIngress = "http://127.0.0.1:18080"
	defaultAdmin   = "http://127.0.0.1:19070"
)

// env returns a nonempty environment override or the supplied fallback. Empty
// overrides intentionally keep the local integration defaults.
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

// Serve starts a host that serves the endpoint on port, registers it with the
// server, and returns a client-only host for the same server. Each test
// package must use its own port: the server runs in a container and reaches
// the endpoint at host.docker.internal:port, and a fixed port keeps the
// registered deployment the same between runs. register runs before the host
// starts and adds the services to serve. It skips the test in short mode.
// Cleanup cancels serving, closes the endpoint with a bounded grace period, and
// joins the serving worker. No fixture worker is left running between tests.
//
// Example:
//
//	_, client := itest.Serve(t, "19080", func(host *restate.Host) {
//		// Bind the test's native services before startup.
//	})
//	_ = client.Ingress()
func Serve(t *testing.T, port string, register func(*restate.Host)) (served, client *restate.Host) {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	ingress, admin := env("ARKOREN_RESTATE_INGRESS_URL", defaultIngress), env("ARKOREN_RESTATE_ADMIN_URL", defaultAdmin)

	served = restate.New(
		func(host *restate.Builder) {
			host.Addr("0.0.0.0:" + port)
		},
		func(host *restate.Builder) {
			host.AdvertisedURL("http://host.docker.internal:" + port)
		},
		func(host *restate.Builder) {
			host.IngressURL(ingress)
		},
		func(host *restate.Builder) {
			host.AdminURL(admin)
		},
		func(host *restate.Builder) {
			host.AllowInsecure(true)
		},
		func(host *restate.Builder) {
			host.Register(true)
		},
		func(host *restate.Builder) {
			host.RegisterForce(true)
		},
	)

	register(served)

	ctx, cancel := context.WithCancel(t.Context())

	if err := served.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- served.Serve(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := served.Stop(ctx); err != nil {
			t.Error(err)
		}

		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	client = restate.New(func(host *restate.Builder) {
		host.IngressURL(ingress)
	}, func(host *restate.Builder) {
		host.AdminURL(admin)
	}, func(host *restate.Builder) {
		host.ClientOnly(true)
	})

	return served, client
}

// PreflightRetention reads the expected disposable-cluster profile, defaulting
// to the legacy disabled profile. Invalid values fail the test. This is an
// assertion setting, not proof that the server applied a persisted feature;
// native cancellation outcomes must establish the behavior independently.
//
// Example:
//
//	t.Logf("expect retained preflight cancellation: %v", itest.PreflightRetention(t))
func PreflightRetention(t *testing.T) bool {
	t.Helper()

	retained, err := strconv.ParseBool(env("ARKOREN_RESTATE_PREFLIGHT_RETENTION", "false"))

	if err != nil {
		t.Fatal(err)
	}

	return retained
}

// WaitForService polls the admin API until the server knows service.
func WaitForService(t *testing.T, host *restate.Host, service string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for {
		response, err := host.Admin(t.Context(), http.MethodGet, "/services/"+service, nil)

		if err == nil {
			response.Body.Close()

			if response.StatusCode == http.StatusOK {
				return
			}
		}

		if time.Now().After(deadline) {
			t.Fatalf("service %s was not registered", service)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// KillInvocations kills the invocations of services that have not completed,
// now and when the test ends. A leftover invocation of an earlier run retries
// against a dead endpoint and blocks its virtual object queue. Call it after
// [Serve], so it runs before the endpoint stops.
func KillInvocations(t *testing.T, host *restate.Host, services ...string) {
	t.Helper()

	kill := func(ctx context.Context) {
		quoted := make([]string, len(services))

		for index, service := range services {
			quoted[index] = "'" + strings.ReplaceAll(service, "'", "''") + "'"
		}

		rows, err := host.Query(ctx, "SELECT id FROM sys_invocation WHERE status <> 'completed' AND target_service_name IN ("+strings.Join(quoted, ",")+")")

		if err != nil {
			t.Logf("list leftover invocations: %v", err)

			return
		}

		for _, row := range rows {
			id, _ := row["id"].(string)

			if id == "" {
				continue
			}

			response, err := host.Admin(ctx, http.MethodPatch, "/invocations/"+url.PathEscape(id)+"/kill", nil)

			if err != nil {
				t.Logf("kill %s: %v", id, err)

				continue
			}

			response.Body.Close()
		}
	}

	kill(t.Context())
	t.Cleanup(func() {
		kill(context.Background())
	})
}
