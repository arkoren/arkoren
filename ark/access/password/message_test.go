package password_test

import (
	"fmt"

	"arkoren.dev/ark/access/password"
)

// ExampleMessage_Link builds the reset link: the token is in the fragment.
func ExampleMessage_Link() {
	message := password.Message{Token: "a b"}
	fmt.Println(message.Link("https://app.example.com/reset"))
	// Output: https://app.example.com/reset#token=a+b
}

// ExampleMessage_QueryLink builds the query form for a server-rendered page.
func ExampleMessage_QueryLink() {
	message := password.Message{Token: "a b"}
	fmt.Println(message.QueryLink("https://app.example.com/reset"))
	// Output: https://app.example.com/reset?token=a+b
}
