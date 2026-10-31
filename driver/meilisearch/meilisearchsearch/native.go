package meilisearchsearch

import (
	"net/http"
)

// Client returns the HTTP client the driver sends requests with: the one made
// by [New], or the one given to [NewFrom]. Requests need the server URL and
// API key, which only the driver holds, so use the client for transport
// settings such as timeouts and proxies.
// The returned client and its transport remain shared; configure them before
// concurrent use. Stop closes idle connections, including for NewFrom clients.
//
// Example:
//
//	client := driver.Client()
//	_ = client.Timeout // Observe the native timeout without mutating a live client.
func (driver *Driver) Client() *http.Client {
	return driver.client
}
