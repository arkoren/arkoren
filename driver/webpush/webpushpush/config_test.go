package webpushpush_test

import (
	"fmt"
	"time"

	"arkoren.dev/core/service/push"
	"arkoren.dev/driver/webpush/webpushpush"
	"arkoren.dev/foundation/key"
)

// ExampleNew constructs a ready sender without network I/O. Production
// applications load a persistent key matching their browser subscriptions.
func ExampleNew() {
	identity, err := key.GenerateSigning()

	if err != nil {
		fmt.Println("key generation failed")

		return
	}

	driver, err := webpushpush.New(identity, "mailto:push@example.com", func(connection *webpushpush.Builder) {
		connection.Timeout(5 * time.Second)
	})

	if err != nil {
		fmt.Println("configuration failed")

		return
	}

	sender := push.New(driver)
	fmt.Println(sender != nil)
	// Output: true
}
