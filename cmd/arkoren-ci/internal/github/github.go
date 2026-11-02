// Package github confines authenticated GitHub operations to gh subprocesses.
package github

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// Repository is the sole automation repository.
const Repository = "arkoren/arkoren"

// API prefixes repository-scoped REST requests.
const API = "repos/" + Repository

// Client owns one scoped token; empty Token uses the existing read-only login.
type Client struct {
	// Root selects the reviewed working directory.
	Root string
	// Token is passed only through the child environment, never arguments.
	Token string
	// Command optionally supplies a process boundary for local fixtures.
	// Production callers leave it nil and always use native gh.
	Command func(context.Context, string, ...string) ([]byte, error)
}

// Run invokes native gh without leaking App or signing credentials.
func (client Client) Run(ctx context.Context, input string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if client.Command != nil {
		return client.Command(ctx, input, args...)
	}
	env := process.Environment(client.Token == "")
	if client.Token != "" {
		env = append(env, "GH_TOKEN="+client.Token)
	}

	return process.Execute(ctx, client.Root, env, "gh", input, args...)
}

// Call exchanges one JSON document. gh rejects unsuccessful HTTP responses.
func (client Client) Call(ctx context.Context, method, endpoint string, input, output any) error {
	args := []string{"api", "--method", method, endpoint}
	data := []byte(nil)
	if input != nil {
		var err error
		data, err = json.Marshal(input)
		if err != nil {
			return err
		}
		args = append(args, "--input", "-")
	}
	result, err := client.Run(ctx, string(data), args...)
	if err != nil {
		return err
	}
	if output == nil {
		return nil
	}

	return json.Unmarshal(result, output)
}

// Pages reads the entire bounded list before callers can mutate state.
func Pages[T any](ctx context.Context, client Client, endpoint string) ([]T, error) {
	items := []T{}
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	for page := 1; page <= 1000; page++ {
		batch := []T(nil)
		if err := client.Call(ctx, "GET", fmt.Sprintf("%s%sper_page=100&page=%d", endpoint, separator, page), nil, &batch); err != nil {
			return nil, err
		}
		if batch == nil {
			return nil, fmt.Errorf("expected paginated array")
		}
		items = append(items, batch...)
		if len(batch) < 100 {
			return items, nil
		}
	}

	return nil, fmt.Errorf("pagination limit exceeded; writes prohibited")
}

// VerifyApp requires the reviewed installation and repository-only access.
func (client Client) VerifyApp(ctx context.Context, slug, installation string) error {
	if slug != "arkoren-releases" || installation != "176874315" {
		return fmt.Errorf("unexpected release App installation")
	}
	var scope struct {
		Count        int `json:"total_count"`
		Repositories []struct {
			Name string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := client.Call(ctx, "GET", "installation/repositories?per_page=100", nil, &scope); err != nil {
		return err
	}
	if scope.Count != 1 || len(scope.Repositories) != 1 || scope.Repositories[0].Name != Repository {
		return fmt.Errorf("release App token must access only arkoren/arkoren")
	}

	return nil
}
