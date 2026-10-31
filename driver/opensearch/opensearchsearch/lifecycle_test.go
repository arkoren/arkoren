package opensearchsearch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/opensearch/opensearchsearch"
)

// lifecycleTransport substitutes a deterministic native HTTP boundary for
// lifecycle tests that need to block until request cancellation.
type lifecycleTransport func(*http.Request) (*http.Response, error)

// RoundTrip runs the test's context-aware exchange without real sockets. The
// SDK still owns its real routing, retry, discovery, and worker behavior.
func (transport lifecycleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// startupContextKey distinguishes the application's connectivity request from
// native background probes at the supported RoundTripper boundary.
type startupContextKey struct{}

// TestStopWaitsForCanceledStart verifies Stop cannot miss a concurrent acquired
// client, while request cancellation lets the serialized Start finish. It also
// verifies Stop honors release ownership despite its own canceled context.
func TestStopWaitsForCanceledStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		transport := lifecycleTransport(func(request *http.Request) (*http.Response, error) {
			if request.Context().Value(startupContextKey{}) != nil {
				close(entered)
				<-request.Context().Done()

				return nil, request.Context().Err()
			}

			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})

		driver, err := opensearchsearch.New("http://lifecycle.invalid", func(engine *opensearchsearch.Builder) {
			engine.Client(func(config *opensearchapi.Config) {
				config.Client.Transport = transport
				config.Client.DisableRetry = true
				config.Client.HealthCheckMaxRetries = -1
			})
		})

		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.WithValue(t.Context(), startupContextKey{}, true))
		started, stopped := make(chan error, 1), make(chan error, 1)
		go func() {
			started <- driver.Start(ctx)
		}()

		<-entered

		stopCtx, stopCancel := context.WithCancel(t.Context())
		stopCancel()

		go func() {
			stopped <- driver.Stop(stopCtx)
		}()

		synctest.Wait()

		select {
		case err := <-stopped:
			t.Fatalf("Stop returned before Start finished: %v", err)
		default:
		}

		cancel()

		if err := <-started; !errors.Is(err, context.Canceled) {
			t.Fatalf("Start error=%v; want context.Canceled", err)
		}

		if err := <-stopped; err != nil {
			t.Fatal(err)
		}

		if driver.Client() != nil {
			t.Fatal("Stop missed the concurrently acquired client")
		}

		synctest.Wait()
	})
}

// TestStopCancelsInitialNativeDiscovery covers the SDK's separately launched
// on-start discovery task, which must share the owned lifetime rather than an
// uncancelable background context. The task reports completion without sleeps.
func TestStopCancelsInitialNativeDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, exited := make(chan struct{}), make(chan struct{})
		transport := lifecycleTransport(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/_nodes/http" {
				close(entered)
				<-request.Context().Done()
				close(exited)

				return nil, request.Context().Err()
			}

			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})

		driver, err := opensearchsearch.New("http://lifecycle.invalid", func(engine *opensearchsearch.Builder) {
			engine.Client(func(config *opensearchapi.Config) {
				config.Client.Transport = transport
				config.Client.DisableRetry = true
				config.Client.DiscoverNodesOnStart = new(true)
				config.Client.DiscoverNodesInterval = -1
				config.Client.HealthCheckMaxRetries = -1
			})
		})

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		<-entered

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}

		<-exited
		synctest.Wait()
	})
}

// TestStopBeforeStartDoesNotAcquire verifies release is idempotent even before
// native construction. The accessor remains absent after both Stop calls.
func TestStopBeforeStartDoesNotAcquire(t *testing.T) {
	t.Parallel()

	driver, err := opensearchsearch.New("http://lifecycle.invalid")

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if driver.Client() != nil {
		t.Fatal("Stop-before-Start constructed a client")
	}
}

// TestOwnedStartReusesClient observes the same native identity across repeated
// connectivity checks. The test lifecycle owner releases it through cleanup.
func TestOwnedStartReusesClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	driver, err := opensearchsearch.New(server.URL, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.DisableRetry = true
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	client := driver.Client()

	if client == nil {
		t.Fatal("Start did not publish its native client")
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if driver.Client() != client {
		t.Fatal("repeated Start replaced a running native client")
	}
}

// TestOwnedStopReleasesClientAndCanRestart verifies cancellation does not skip
// release and the next Start creates a fresh client without replaying the hook.
func TestOwnedStopReleasesClientAndCanRestart(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	hookCalls := 0
	driver, err := opensearchsearch.New(server.URL, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			hookCalls++
			config.Client.DisableRetry = true
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	client := driver.Client()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	if driver.Client() != nil {
		t.Fatal("Stop retained its native client")
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if driver.Client() == client || hookCalls != 1 {
		t.Fatal("reacquisition reused a closed client or replayed the hook")
	}
}

// TestCanceledStartDoesNotAcquire exercises cancellation before native
// construction, including a malformed URL that would otherwise fail validation.
func TestCanceledStartDoesNotAcquire(t *testing.T) {
	t.Parallel()

	driver, err := opensearchsearch.New("http://[malformed")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error=%v; want context.Canceled", err)
	}

	if driver.Client() != nil {
		t.Fatal("canceled Start acquired a native client")
	}

	if err := driver.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestAdministrationBeforeStartDoesNotAcquire checks readiness and index
// administration fail safely without implicitly constructing a native client.
func TestAdministrationBeforeStartDoesNotAcquire(t *testing.T) {
	t.Parallel()

	transport := &constructionTransport{}
	driver, err := opensearchsearch.New("http://constructor.invalid", func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.Transport = transport
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	schema := search.Schema{Name: "docs", Searchable: []string{"title"}}

	if err := driver.Ping(t.Context()); err == nil {
		t.Fatal("Ping before Start succeeded")
	}

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err == nil {
		t.Fatal("Sync before Start succeeded")
	}

	if err := driver.Check(t.Context(), []search.Schema{schema}); err == nil {
		t.Fatal("Check before Start succeeded")
	}

	if err := driver.Drop(t.Context(), schema); err == nil {
		t.Fatal("Drop before Start succeeded")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Ping error=%v; want context.Canceled", err)
	}

	if driver.Client() != nil || transport.requests.Load() != 0 {
		t.Fatal("pre-start request constructed a client or attempted I/O")
	}
}

// TestDocumentsBeforeStartDoNotAcquire covers document operations independently
// of index administration. Even empty writes must not acquire a native client.
func TestDocumentsBeforeStartDoNotAcquire(t *testing.T) {
	t.Parallel()

	transport := &constructionTransport{}
	driver, err := opensearchsearch.New("http://constructor.invalid", func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.Transport = transport
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	schema := search.Schema{Name: "docs", Searchable: []string{"title"}}

	if err := driver.Put(t.Context(), schema, nil); err == nil {
		t.Fatal("Put before Start succeeded")
	}

	if err := driver.Delete(t.Context(), schema, []string{"a"}); err == nil {
		t.Fatal("Delete before Start succeeded")
	}

	if _, err := driver.Search(t.Context(), schema, search.Spec{}, 10, 0); err == nil {
		t.Fatal("Search before Start succeeded")
	}

	if driver.Client() != nil || transport.requests.Load() != 0 {
		t.Fatal("pre-start document operation constructed a client or attempted I/O")
	}
}

// TestFailedStartWorkersRemainOwnedUntilStop verifies actual SDK workers on its
// multi-node path are retired even after acquisition's connectivity check fails.
// Synctest also rejects workers still alive when the bubble exits.
func TestFailedStartWorkersRemainOwnedUntilStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &constructionTransport{}
		driver, err := opensearchsearch.New("http://first.invalid", func(engine *opensearchsearch.Builder) {
			engine.Nodes("http://second.invalid")
			engine.Client(func(config *opensearchapi.Config) {
				config.Client.Transport = transport
				config.Client.DisableRetry = true
				config.Client.HealthCheckMaxRetries = -1
			})
		})

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Start(t.Context()); err == nil {
			t.Fatal("Start accepted the rejecting native transport")
		}

		if driver.Client() == nil || transport.requests.Load() == 0 {
			t.Fatal("failed connectivity did not retain the acquired client")
		}

		time.Sleep(time.Minute) // Virtual time exercises actual native workers.

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()
		requests := transport.requests.Load()
		time.Sleep(10 * time.Minute)
		synctest.Wait()

		if driver.Client() != nil || transport.requests.Load() != requests {
			t.Fatal("Stop left native work or a published client after failed Start")
		}
	})
}

// TestStartSnapshotsNativeAddressesAndHeaders observes copied native address and
// header containers through requests. Mutating the retained draft before Start
// cannot redirect acquisition or change its transmitted header values.
func TestStartSnapshotsNativeAddressesAndHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Snapshot") != "original" {
			t.Error("native header changed after publication")
		}

		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	var retained *opensearchapi.Config
	driver, err := opensearchsearch.New(server.URL, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			retained = config
			config.Client.Header = http.Header{"X-Snapshot": {"original"}}
			config.Client.DisableRetry = true
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	retained.Client.Addresses[0] = "http://[malformed"
	retained.Client.Header["X-Snapshot"][0] = "changed"

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestStartSnapshotsNativeRetryStatuses observes the copied retry-status slice
// at the SDK's public HTTP boundary. Backoff functions remain shared dependencies.
func TestStartSnapshotsNativeRetryStatuses(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/snapshot" && attempts.Add(1) == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
		}

		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	var retained *opensearchapi.Config
	driver, err := opensearchsearch.New(server.URL, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			retained = config
			config.Client.RetryOnStatus = []int{http.StatusTooManyRequests}
			config.Client.MaxRetries = 1
			config.Client.RetryBackoff = func(int) time.Duration {
				return 0
			}
			config.Client.DiscoverNodesInterval = -1
			config.Client.NodeStatsInterval = -1
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	retained.Client.RetryOnStatus[0] = http.StatusTeapot

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/snapshot", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := driver.Client().Client.Request(request)

	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()

	if attempts.Load() != 2 || response.StatusCode != http.StatusOK {
		t.Fatalf("native retry attempts=%d status=%d", attempts.Load(), response.StatusCode)
	}
}

// TestStartSnapshotsNativeDiscoveryPolicy verifies publication detaches a scalar
// pointer: a retained hook cannot enable on-start discovery after New returns.
func TestStartSnapshotsNativeDiscoveryPolicy(t *testing.T) {
	t.Parallel()

	var discovery atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/_nodes/http" {
			discovery.Add(1)
		}

		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	var retained *opensearchapi.Config
	driver, err := opensearchsearch.New(server.URL, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			retained = config
			config.Client.DiscoverNodesInterval = -1
			config.Client.NodeStatsInterval = -1
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	*retained.Client.DiscoverNodesOnStart = true

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if discovery.Load() != 0 {
		t.Fatal("retained scalar pointer enabled on-start discovery")
	}
}

// TestStartSnapshotsNativeCertificateBytes observes detached certificate bytes
// through native validation rather than through private configuration fields.
func TestStartSnapshotsNativeCertificateBytes(t *testing.T) {
	t.Parallel()

	var retained *opensearchapi.Config
	driver, err := opensearchsearch.New("https://constructor.invalid", func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			retained = config
			config.Client.CACert = []byte("not a PEM certificate")
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	clear(retained.Client.CACert)
	retained.Client.CACert = nil

	if err := driver.Start(t.Context()); !errors.Is(err, opensearch.ErrCreateTransport) {
		t.Fatalf("Start error=%v; want original native certificate failure", err)
	}
}

// TestConcurrentLifecycleAndNativeAccess exercises serialized acquisition and
// release together with lock-free observation and requests during shutdown.
func TestConcurrentLifecycleAndNativeAccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	driver, err := opensearchsearch.New(server.URL, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.DisableRetry = true
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			for range 10 {
				if err := driver.Start(t.Context()); err != nil {
					t.Error(err)
				}

				_ = driver.Client()
				// Shutdown can invalidate this request; either result is valid.
				_ = driver.Ping(t.Context())

				if err := driver.Stop(t.Context()); err != nil {
					t.Error(err)
				}
			}
		})
	}

	workers.Wait()

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if driver.Client() != nil {
		t.Fatal("final Stop retained a native client")
	}
}

// TestBorrowedLifecycleLeavesNativeClientUsable checks real native work after
// wrapper Stop, rather than treating pointer identity as proof of ownership.
func TestBorrowedLifecycleLeavesNativeClientUsable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	client, err := opensearchapi.NewClient(opensearchapi.Config{Client: opensearch.Config{
		Addresses:            []string{server.URL},
		DiscoverNodesOnStart: new(false),
	}})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})

	driver, err := opensearchsearch.NewFrom(client, func(engine *opensearchsearch.Builder) {
		engine.Client(func(*opensearchapi.Config) {
			t.Error("borrowed driver invoked the native hook")
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled borrowed Start error=%v", err)
	}

	if err := driver.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	if driver.Client() != client {
		t.Fatal("Stop unpublished a borrowed client")
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/borrowed", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := client.Client.Request(request)

	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()
}
