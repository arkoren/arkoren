package webpushpush

import (
	"fmt"
	"net/url"

	"arkoren.dev/core/service/push"
	"arkoren.dev/driver/webpush/webpushpush/internal/network"
)

// checkEndpoint maps malformed or unauthorized subscription destinations to
// invalid messages without echoing capability URLs in error text.
func (driver *Driver) checkEndpoint(endpoint string) (*url.URL, error) {
	parsed, allowed, err := network.Endpoint(endpoint, driver.origins)

	if err != nil {
		return nil, fmt.Errorf("web push endpoint is invalid: %w", push.ErrInvalidMessage)
	}

	if !allowed {
		return nil, fmt.Errorf("web push endpoint origin is not approved: %w", push.ErrInvalidMessage)
	}

	return parsed, nil
}
