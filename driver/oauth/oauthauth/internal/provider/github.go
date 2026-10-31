package provider

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
)

// githubIssuer is the issuer reported in the [Identity] of GitHub users.
const githubIssuer = "https://github.com"

// GitHubUser loads the user and the primary verified email from the API at apiURL.
// A positive numeric ID is required before requesting emails or returning an
// identity; login is only a display-name fallback. Failures return a zero identity
// with the original wrapped HTTP/decoder cause. The authenticated client is borrowed.
func GitHubUser(ctx context.Context, client *http.Client, apiURL string) (Record, error) {
	var user struct {
		// ID is the stable numeric subject identifier, independent of login changes.
		ID int64 `json:"id"`
		// Login supplies the display-name fallback when Name is empty.
		Login string `json:"login"`
		// Name is the optional public display name reported by GitHub.
		Name string `json:"name"`
	}

	if err := githubGet(ctx, client, apiURL+"/user", &user); err != nil {
		return Record{}, err
	}

	if user.ID <= 0 {
		return Record{}, fmt.Errorf("GitHub identity lacks a positive numeric ID")
	}

	var emails []struct {
		// Email is retained only when this address is both primary and verified.
		Email string `json:"email"`
		// Primary selects the account's preferred address.
		Primary bool `json:"primary"`
		// Verified prevents unverified addresses from becoming identity email.
		Verified bool `json:"verified"`
	}

	if err := githubGet(ctx, client, apiURL+"/user/emails", &emails); err != nil {
		return Record{}, err
	}

	identity := Record{
		Issuer:  githubIssuer,
		Subject: strconv.FormatInt(user.ID, 10),
		Name:    user.Name,
	}

	if identity.Name == "" {
		identity.Name = user.Login
	}

	for _, email := range emails {
		if email.Primary && email.Verified {
			identity.Email = email.Email

			break
		}
	}

	return identity, nil
}

// githubGet decodes the JSON response of a GitHub API GET request into target.
// It binds the request to ctx and accepts only HTTP 200. Native request and JSON
// errors retain their causes; response bodies are closed best effort without
// replacing the authoritative response/decoding result. The client stays caller-owned.
func githubGet(ctx context.Context, client *http.Client, url string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	if err != nil {
		return fmt.Errorf("build GitHub request: %w", err)
	}

	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	response, err := client.Do(request)

	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}

	defer func() {
		// Response classification and decoded evidence are authoritative;
		// best-effort cleanup cannot undo the completed provider response.
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: status %d", url, response.StatusCode)
	}

	if err := json.UnmarshalRead(response.Body, target); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}

	return nil
}
