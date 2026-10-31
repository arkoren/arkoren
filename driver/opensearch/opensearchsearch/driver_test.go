package opensearchsearch_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/searchtest"
	"arkoren.dev/driver/opensearch/opensearchsearch"
)

// testAddress is the OpenSearch node of this module's compose.yaml. The
// environment variable OPENSEARCH_URL overrides it.
const testAddress = "http://127.0.0.1:19200"

// newTestDriver returns a started driver for the compose service that stops
// with the test. Node discovery is off because the node publishes its
// container address, which the host cannot reach.
func newTestDriver(t *testing.T, configure ...func(*opensearchsearch.Builder)) *opensearchsearch.Driver {
	t.Helper()

	address := testAddress

	if env := os.Getenv("OPENSEARCH_URL"); env != "" {
		address = env
	}

	configure = append(configure, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.DiscoverNodesOnStart = new(false)
			config.Client.DiscoverNodesInterval = -1
			config.Client.NodeStatsInterval = -1
		})
	})

	driver, err := opensearchsearch.New(address, configure...)

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(t.Context()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start (run docker compose up -d --wait in driver/opensearch): %v", err)
	}

	return driver
}

// TestConformance runs the search conformance suite against a real OpenSearch
// node.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the OpenSearch service of compose.yaml")
	}

	searchtest.Run(t, func(t *testing.T) search.Driver {
		return newTestDriver(t, func(engine *opensearchsearch.Builder) {
			engine.IndexPrefix("arkoren_test_")
		})
	})
}

// constructionTransport observes every native request, regardless of endpoint.
// It never claims network success or contacts a real service: unexpected probes
// are rejected at the application's supported native RoundTripper boundary.
type constructionTransport struct {
	// requests counts attempted seed-health, discovery, and periodic requests.
	requests atomic.Int64
}

// RoundTrip records the attempt and rejects it without fabricating a healthy
// node or an HTTP response. All requests are relevant to the no-I/O assertion.
func (constructionTransport *constructionTransport) RoundTrip(*http.Request) (*http.Response, error) {
	constructionTransport.requests.Add(1)

	return nil, errors.New("isolated construction fixture rejects network activity")
}

// TestNewMakesNoNativeRequests verifies the real public constructor cannot
// attempt immediate or delayed native I/O before Start. A minute of virtual time
// covers the pinned SDK's default discovery and statistics polling intervals.
func TestNewMakesNoNativeRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &constructionTransport{}
		hookCalls := 0
		driver, err := opensearchsearch.New("http://constructor.invalid:9200", func(engine *opensearchsearch.Builder) {
			engine.Client(func(config *opensearchapi.Config) {
				hookCalls++
				config.Client.Transport = transport
			})
		})

		if err != nil {
			t.Fatal(err)
		}

		if hookCalls != 1 {
			t.Fatalf("native hook calls=%d; want once during New", hookCalls)
		}

		if driver.Client() != nil {
			t.Fatal("New constructed a native client before Start")
		}

		time.Sleep(time.Minute) // Virtual time; no real network or wall-clock sleep.

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		if got := transport.requests.Load(); got != 0 {
			t.Fatalf("New attempted %d native requests before Start; want zero", got)
		}
	})
}

// TestNewWithMultipleNodesMakesNoNativeRequests covers the SDK's distinct
// multi-node pool and health-refresh path. Ten virtual minutes exceed its
// five-minute maximum refresh interval; Stop must retire every bubble worker.
func TestNewWithMultipleNodesMakesNoNativeRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &constructionTransport{}
		driver, err := opensearchsearch.New("http://constructor.invalid:9200", func(engine *opensearchsearch.Builder) {
			engine.Nodes("http://second-constructor.invalid:9200")
			engine.Client(func(config *opensearchapi.Config) {
				config.Client.Transport = transport
			})
		})

		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(10 * time.Minute) // Virtual time; no wall-clock wait or network.

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		if got := transport.requests.Load(); got != 0 {
			t.Fatalf("multi-node New attempted %d native requests before Start; want zero", got)
		}
	})
}

// TestStartValidatesNativeURLs verifies the deferred native validation boundary:
// the hook runs at New, while Start preserves the SDK's URL error identity.
func TestStartValidatesNativeURLs(t *testing.T) {
	t.Parallel()

	hookCalls := 0
	driver, err := opensearchsearch.New("http://[malformed", func(engine *opensearchsearch.Builder) {
		engine.Client(func(*opensearchapi.Config) {
			hookCalls++
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if hookCalls != 1 || driver.Client() != nil {
		t.Fatalf("New returned client=%v after %d hook calls; want nil and one", driver.Client(), hookCalls)
	}

	if err := driver.Start(t.Context()); !errors.Is(err, opensearch.ErrCreateClient) {
		t.Fatalf("Start error=%v; want native ErrCreateClient", err)
	}

	if hookCalls != 1 || driver.Client() != nil {
		t.Fatal("failed native creation published a client or replayed the hook")
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestStartValidatesNativeCertificates checks SDK validation beyond URL parsing.
// New remains network-free and Start preserves the native certificate error.
func TestStartValidatesNativeCertificates(t *testing.T) {
	t.Parallel()

	driver, err := opensearchsearch.New("https://constructor.invalid:9200", func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.CACert = []byte("not a PEM certificate")
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); !errors.Is(err, opensearch.ErrCreateTransport) {
		t.Fatalf("Start error=%v; want native ErrCreateTransport", err)
	}

	if driver.Client() != nil {
		t.Fatal("Start published a client despite an invalid native certificate")
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// ExampleNew demonstrates that the native hook runs during configuration, but
// native acquisition waits for Start. Stopping an unstarted driver needs no
// service and leaves the native accessor empty.
func ExampleNew() {
	hookCalls := 0
	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
		engine.IndexPrefix("shop_")
		engine.Client(func(config *opensearchapi.Config) {
			hookCalls++
			config.Client.DisableRetry = true
		})
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("native hook calls:", hookCalls)
	fmt.Println("native client before Start:", driver.Client())

	if err := driver.Stop(context.Background()); err != nil {
		fmt.Println(err)
	}

	// Output:
	// native hook calls: 1
	// native client before Start: <nil>
}

// TestIndexPrefixIsApplied verifies that Sync creates the prefixed index.
func TestIndexPrefixIsApplied(t *testing.T) {
	t.Parallel()

	engine, index, driver := integrationEngine(t)

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodHead, "/arkoren_extra_"+index.Name(), nil)

	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	response, err := driver.Client().Client.Request(request)

	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("prefixed index status = %d, want 200", response.StatusCode)
	}
}

// TestBuilderPoliciesAndNativeHookOrder checks same-draft once-only policies,
// copied node input, and native hook invocation after all base settings.
func TestBuilderPoliciesAndNativeHookOrder(t *testing.T) {
	t.Parallel()

	nodes := []string{"http://os2:9200"}
	var retained *opensearchsearch.Builder
	var addresses []string
	calls, hookCalls := 0, 0
	driver, err := opensearchsearch.New("http://os1:9200", func(engine *opensearchsearch.Builder) {
		calls++
		retained = engine
		engine.Nodes(nodes...)
		nodes[0] = "http://changed:9200"
		engine.Username("reader")
		engine.Password("password")
		engine.Client(func(config *opensearchapi.Config) {
			hookCalls++
			addresses = slices.Clone(config.Client.Addresses)

			if config.Client.Username != "reader" || config.Client.Password != "password" {
				t.Fatal("native hook ran before base settings")
			}
		})
	}, func(engine *opensearchsearch.Builder) {
		calls++

		if engine != retained {
			t.Fatal("callbacks received different drafts")
		}

		engine.IndexPrefix("shop_")
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	retained.Nodes("http://later:9200")

	if calls != 2 || hookCalls != 1 {
		t.Fatalf("policy calls=%d hook calls=%d", calls, hookCalls)
	}

	if !slices.Equal(addresses, []string{"http://os1:9200", "http://os2:9200"}) {
		t.Fatalf("native addresses = %v", addresses)
	}
}

// TestNewFromOnlyAppliesPrefix checks native hooks are not executed for a
// borrowed client and retained drafts cannot change the final request prefix.
func TestNewFromOnlyAppliesPrefix(t *testing.T) {
	t.Parallel()

	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			paths <- request.URL.Path
		}

		writer.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client, err := opensearchapi.NewClient(opensearchapi.Config{
		Client: opensearch.Config{
			Addresses:            []string{server.URL},
			DiscoverNodesOnStart: new(false),
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})

	var retained *opensearchsearch.Builder
	calls := 0
	driver, err := opensearchsearch.NewFrom(client, func(engine *opensearchsearch.Builder) {
		calls++
		retained = engine
		engine.IndexPrefix("shop_")
		engine.Nodes("not a URL")
		engine.Username("ignored")
		engine.Password("ignored")
		engine.Client(func(*opensearchapi.Config) {
			t.Fatal("NewFrom executed native construction hook")
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.IndexPrefix("changed_")

	if err := driver.Drop(t.Context(), search.Schema{
		Name: "products",
	}); err != nil {
		t.Fatal(err)
	}

	path := <-paths

	if calls != 1 || path != "/shop_products" || driver.Client() != client {
		t.Fatalf("calls=%d path=%q", calls, path)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestBuilderSettersReturnDraft verifies every setting returns the same zero
// draft; nil Client is a valid disabled native hook, not callback misuse.
func TestBuilderSettersReturnDraft(t *testing.T) {
	t.Parallel()

	engine := &opensearchsearch.Builder{}

	if engine.IndexPrefix("") != engine || engine.Nodes() != engine {
		t.Fatal("index settings changed draft")
	}

	if engine.Username("") != engine || engine.Password("") != engine {
		t.Fatal("authentication settings changed draft")
	}

	if engine.Client(nil) != engine {
		t.Fatal("native hook setting changed draft")
	}
}

// TestBuilderPoliciesRunBeforeAddressValidation verifies invalid addresses are
// reported after callbacks, with no native client hook invocation.
func TestBuilderPoliciesRunBeforeAddressValidation(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := opensearchsearch.New("", func(engine *opensearchsearch.Builder) {
		calls++
		engine.Client(func(*opensearchapi.Config) {
			t.Fatal("native hook ran despite invalid address")
		})
	})

	if calls != 1 || err == nil {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

// TestBuilderNilPolicyPanics checks a supplied nil entry is not filtered, even
// when normal address validation would return an error.
func TestBuilderNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = opensearchsearch.New("", nil)
}

// TestBuilderPanicPropagates checks application panics bypass address validation
// unchanged and stop policy execution before native client construction.
func TestBuilderPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := new(int)
	defer func() {
		if recover() != marker {
			t.Fatal("constructor changed the callback panic")
		}
	}()

	_, _ = opensearchsearch.New("", func(*opensearchsearch.Builder) {
		panic(marker)
	}, func(*opensearchsearch.Builder) {
		t.Fatal("callback ran after panic")
	})
}

// TestNewFromNilPolicyPanics checks nil wrapper policies are invoked directly;
// NewFrom does not filter them or execute any native Client hook.
func TestNewFromNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = opensearchsearch.NewFrom(nil, nil)
}

// TestBuilderEmptyPoliciesKeepDefaults verifies nil and empty slice expansions
// create fresh default settings without invoking native hooks or doing I/O.
func TestBuilderEmptyPoliciesKeepDefaults(t *testing.T) {
	t.Parallel()

	for _, configure := range [][]func(*opensearchsearch.Builder){nil, {}} {
		driver, err := opensearchsearch.New("http://localhost:9200", configure...)

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNewRequiresAddress verifies that an empty address is rejected.
func TestNewRequiresAddress(t *testing.T) {
	t.Parallel()

	if _, err := opensearchsearch.New(""); err == nil {
		t.Fatal("New with an empty address succeeded")
	}
}

// TestIndexPrefixValidation verifies that prefixes outside [a-z0-9_] or longer
// than 64 bytes fail in both constructors with ErrInvalidName.
func TestIndexPrefixValidation(t *testing.T) {
	t.Parallel()

	client, err := opensearchapi.NewClient(opensearchapi.Config{})

	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})

	for _, prefix := range []string{"Shop_", "shop-", "shop.", "shöp", strings.Repeat("a", 65)} {
		_, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
			engine.IndexPrefix(prefix)
		})

		if !errors.Is(err, search.ErrInvalidName) {
			t.Errorf("New with prefix %q error = %v, want ErrInvalidName", prefix, err)
		}

		_, err = opensearchsearch.NewFrom(client, func(engine *opensearchsearch.Builder) {
			engine.IndexPrefix(prefix)
		})

		if !errors.Is(err, search.ErrInvalidName) {
			t.Errorf("NewFrom with prefix %q error = %v, want ErrInvalidName", prefix, err)
		}
	}
}

// TestNewFromDoesNotOwnTheClient verifies that Client returns the wrapped
// client and Stop leaves it open.
func TestNewFromDoesNotOwnTheClient(t *testing.T) {
	t.Parallel()

	client, err := opensearchapi.NewClient(opensearchapi.Config{})

	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})

	driver, err := opensearchsearch.NewFrom(client, func(engine *opensearchsearch.Builder) {
		engine.IndexPrefix("shop_")
	})

	if err != nil {
		t.Fatalf("NewFrom: %v", err)
	}

	if driver.Client() != client {
		t.Fatal("Client is not the wrapped client")
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestUnderscoreIndexNeedsAPrefix verifies, without a server, that a name
// starting with an underscore fails before any request when there is no prefix.
func TestUnderscoreIndexNeedsAPrefix(t *testing.T) {
	t.Parallel()

	driver, err := opensearchsearch.New("http://127.0.0.1:1")

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(t.Context())
	})

	err = driver.Drop(t.Context(), search.Schema{
		Name: "_hidden",
	})

	if !errors.Is(err, search.ErrInvalidName) {
		t.Fatalf("Drop error = %v, want ErrInvalidName", err)
	}
}

// TestNewDisablesStartupDiscovery checks the hook's completed base settings:
// discovery stays opt-in when Start eventually constructs the native client.
func TestNewDisablesStartupDiscovery(t *testing.T) {
	t.Parallel()

	driver, err := opensearchsearch.New("http://127.0.0.1:1", func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			if config.Client.DiscoverNodesOnStart == nil || *config.Client.DiscoverNodesOnStart {
				t.Fatal("native defaults enable on-start discovery")
			}
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
}
