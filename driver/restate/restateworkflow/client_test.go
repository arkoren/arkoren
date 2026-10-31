package restateworkflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"arkoren.dev/driver/restate"
)

// hostileID tries to address the Cancel handler of another run key.
const hostileID = "victim/Cancel?x#y%"

// ingressPaths serves a fake Restate ingress and returns the escaped request
// paths it receives.
func ingressPaths(t *testing.T) (hostGateway, func() []string) {
	t.Helper()

	var (
		mu    sync.Mutex
		paths []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.EscapedPath()+"|query="+r.URL.RawQuery)
		mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	host := restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
	})

	return hostGateway{
		host: host,
	}, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), paths...)
	}
}

// TestGatewayEscapesRunIDInIngressPath verifies that reserved URL characters in
// a logical run identifier cannot select another native handler or workflow
// key.
func TestGatewayEscapesRunIDInIngressPath(t *testing.T) {
	t.Parallel()

	gateway, got := ingressPaths(t)
	ctx := context.Background()

	_, _ = gateway.start(ctx, "svc", hostileID, []byte(`"x"`), startRequest{})
	_, _ = gateway.attach(ctx, "svc", hostileID)
	_, _ = gateway.output(ctx, "svc", hostileID)
	_ = gateway.signal(ctx, "svc", hostileID, signalRequest{
		Name: "s",
	})
	_ = gateway.interrupt(ctx, "svc", hostileID)

	want := []string{
		"/svc/victim%2FCancel%3Fx%23y%25/Run/send|query=",
		"/restate/workflow/svc/victim%2FCancel%3Fx%23y%25/attach|query=",
		"/restate/workflow/svc/victim%2FCancel%3Fx%23y%25/output|query=",
		"/svc/victim%2FCancel%3Fx%23y%25/Signal|query=",
		"/svc/victim%2FCancel%3Fx%23y%25/Cancel|query=",
	}

	paths := got()

	if len(paths) != len(want) {
		t.Fatalf("requests = %q; want %d", paths, len(want))
	}

	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("request %d = %q; want %q", i, paths[i], want[i])
		}
	}
}
