package resendmail

import "github.com/resend/resend-go/v3"

// Client returns the live borrowed Resend SDK client, not a snapshot. Configure
// native settings before concurrent use; neither this driver nor the mail facade
// closes the client. SDK fields can contain credentials and personal data; this
// accessor does not sanitize them or make the native object safe to log.
//
// Example:
//
//	native := resend.NewClient("api-key")
//	driver := resendmail.NewFrom(native)
//	_ = driver.Client().BaseURL // Same caller-owned configuration.
func (driver *Driver) Client() *resend.Client {
	return driver.client
}
