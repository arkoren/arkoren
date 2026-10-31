// Package paypal contains the private protocol shared by the payment driver
// and webhook adapter. Only fixed PayPal origins receive account credentials;
// provider links are never followed. No constructor performs network I/O.
//
// Example:
//
//	client, err := paypal.New("client-id", "secret", paypal.Settings{})
//	if err != nil { return err }
//	_ = client.HTTPClient()
package paypal
