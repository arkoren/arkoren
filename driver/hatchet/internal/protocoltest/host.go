// Package protocoltest supplies isolated native protocol endpoints for public
// Hatchet adapter tests. Tokens are synthetic and never leave localhost; no
// Compose service or real provider credential is used.
//
//	host := protocoltest.Host(t, http.NotFoundHandler())
//	if err := host.Start(t.Context()); err != nil {
//		t.Fatal(err)
//	}
package protocoltest

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/driver/hatchet"
)

// Host constructs an unstarted client-only host against handler's local HTTP
// server. Later callbacks can replace its defaults. Tests remain serial because
// SDK endpoint configuration reads process environment variables. Cleanup stops
// the host before closing the server, even when the test exits early.
//
//	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
//		host.Namespace("stage")
//	})
//	if err := host.Start(t.Context()); err != nil {
//		t.Fatal(err)
//	}
func Host(t *testing.T, handler http.Handler, configure ...hatchet.BuilderFunc) *hatchet.Host {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	t.Setenv("HATCHET_CLIENT_SERVER_URL", server.URL)
	t.Setenv("HATCHET_CLIENT_HOST_PORT", "127.0.0.1:1")
	t.Setenv("HATCHET_CLIENT_TLS_STRATEGY", "none")
	t.Setenv("HATCHET_CLIENT_NAMESPACE", "")
	t.Setenv("HATCHET_CLIENT_EMBEDDED_DATABASE_URL", "")

	claims, err := json.Marshal(map[string]any{
		"sub": "44444444-4444-4444-4444-444444444444", "exp": 4102444800,
		"server_url": server.URL, "grpc_broadcast_address": "127.0.0.1:1",
	})

	if err != nil {
		t.Fatal(err)
	}

	host := hatchet.New(append([]hatchet.BuilderFunc{func(host *hatchet.Builder) {
		host.Token("e30." + base64.RawURLEncoding.EncodeToString(claims) + ".test")
		host.NoWorker(true)
		host.Logger(slog.New(slog.DiscardHandler))
	}}, configure...)...)
	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Errorf("stop protocol host: %v", err)
		}
	})

	return host
}
