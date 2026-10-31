package smtpmail_test

import (
	"fmt"
	"strings"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/driver/smtp/smtpmail"
)

// ExampleBuilderFunc constructs an SMTP mailer without connecting to a server.
// The default timeout policy is reusable and is applied only at construction.
func ExampleBuilderFunc() {
	policies := []smtpmail.BuilderFunc{func(transport *smtpmail.Builder) {
		transport.Timeout(smtpmail.DefaultTimeout)
	}}

	driver, err := smtpmail.New("smtp.example.com", 587, policies...)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(mail.New(driver).Driver() == driver)
	// Output: true
}

// ExampleConfig demonstrates all reporting fields and the three safe formatting
// protocols without using the record as constructor input or sending mail.
func ExampleConfig() {
	config := smtpmail.Config{
		Username: "user",
		Password: "secret",
		TLSMode:  smtpmail.TLSModeStartTLS,
		Timeout:  smtpmail.DefaultTimeout,
	}

	fmt.Println(strings.Contains(config.String(), "[REDACTED]"))
	fmt.Println(strings.Contains(config.GoString(), "[REDACTED]"))
	fmt.Println(strings.Contains(config.LogValue().String(), "[REDACTED]"))
	// Output:
	// true
	// true
	// true
}
