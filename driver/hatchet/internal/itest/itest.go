// Package itest holds the shared setup of the Hatchet integration tests that
// run against the hatchet service of driver/hatchet/compose.yaml.
package itest

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sync"
	"testing"
	"time"

	hatchethost "arkoren.dev/driver/hatchet"
)

// Ports of the compose service. ARKOREN_HATCHET_API and
// ARKOREN_HATCHET_GRPC override them.
const (
	defaultAPI  = "localhost:18888"
	defaultGRPC = "127.0.0.1:17077"
)

// Credentials of the admin user that hatchet-lite creates on first start.
const (
	adminEmail    = "admin@example.com"
	adminPassword = "Admin123!!"
)

var (
	// tokenOnce limits local integration credential creation to one attempt per
	// test process.
	tokenOnce sync.Once
	// token retains the shared local integration credential after tokenOnce
	// completes.
	token string
	// tokenErr retains credential-creation failure so every caller observes the
	// same setup result.
	tokenErr error
)

// env returns a nonempty environment override or the supplied fallback. Empty
// overrides intentionally keep the local integration defaults.
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

// Options returns ordered host construction callbacks for the compose service.
// It skips short tests before obtaining credentials. Caller policies follow the
// test defaults, so they can replace individual settings.
//
// Example:
//
//	host := hatchethost.New(Options(t)...)
//	_ = host
func Options(t *testing.T, extra ...hatchethost.BuilderFunc) []hatchethost.BuilderFunc {
	t.Helper()

	token := Token(t)

	return append([]hatchethost.BuilderFunc{
		func(host *hatchethost.Builder) {
			host.Token(token)
			host.HostPort(env("ARKOREN_HATCHET_GRPC", defaultGRPC))
			host.TLSStrategy("none")
		},
	}, extra...)
}

// Token obtains the compose-service credential once per test process. It skips
// short tests and fails the calling test if token creation fails. Native REST
// integration assertions use this test-only seam, not host configuration access.
//
// Example:
//
//	request.Header.Set("Authorization", "Bearer "+Token(t))
func Token(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	tokenOnce.Do(func() {
		token, tokenErr = createToken(t.Context())
	})

	if tokenErr != nil {
		t.Fatalf("hatchet API token: %v", tokenErr)
	}

	return token
}

// createToken logs in as the seeded admin and creates an API token for the
// default tenant.
func createToken(ctx context.Context) (string, error) {
	jar, err := cookiejar.New(nil)

	if err != nil {
		return "", err
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
	}
	base := "http://" + env("ARKOREN_HATCHET_API", defaultAPI) + "/api/v1"

	post := func(path string, in, out any) error {
		body, err := json.Marshal(in)

		if err != nil {
			return err
		}

		return do(ctx, client, http.MethodPost, base+path, body, out)
	}

	if err := post("/users/login", map[string]string{"email": adminEmail, "password": adminPassword}, nil); err != nil {
		return "", err
	}

	var memberships struct {
		// Rows contains the rows decoded from the server response.
		Rows []struct {
			// Tenant contains the tenant associated with one membership response.
			Tenant struct {
				// Metadata contains the server-assigned tenant identity.
				Metadata struct {
					// ID identifies the tenant used to create the integration API credential.
					ID string `json:"id"`
				} `json:"metadata"`
			} `json:"tenant"`
		} `json:"rows"`
	}

	if err := do(ctx, client, http.MethodGet, base+"/users/memberships", nil, &memberships); err != nil {
		return "", err
	}

	if len(memberships.Rows) == 0 {
		return "", fmt.Errorf("admin user has no tenant")
	}

	var created struct {
		// Token contains the newly created credential returned by the local test
		// service.
		Token string `json:"token"`
	}

	name := fmt.Sprintf("arkoren-it-%d", time.Now().UnixNano())

	if err := post("/tenants/"+memberships.Rows[0].Tenant.Metadata.ID+"/api-tokens", map[string]string{"name": name}, &created); err != nil {
		return "", err
	}

	return created.Token, nil
}

// do sends one JSON request to the local test API and decodes a successful
// response when out is non-nil. Transport, status, and decoding failures are
// returned to setup.
func do(ctx context.Context, client *http.Client, method, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))

	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s", method, url, resp.Status)
	}

	if out == nil {
		return nil
	}

	return json.UnmarshalRead(resp.Body, out)
}
