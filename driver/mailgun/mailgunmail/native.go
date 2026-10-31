package mailgunmail

import "github.com/mailgun/mailgun-go/v5"

// Client returns the live borrowed Mailgun SDK client, not a snapshot. Configure
// it before concurrent use; neither the driver nor the mail facade closes it.
// Native clients and their diagnostics can expose credentials: this accessor
// does not sanitize the SDK object or make it safe to log.
//
// Example:
//
//	driver, err := mailgunmail.New("mg.example.com", "private-key")
//	if err != nil {
//		panic(err)
//	}
//	driver.Client().SetHTTPClient(&http.Client{Timeout: 10 * time.Second})
func (driver *Driver) Client() *mailgun.Client {
	return driver.client
}
