package account_test

import (
	"fmt"

	"arkoren.dev/ark/access/account"
)

// ExampleMessage_Link builds the verification link: the token is in the
// fragment.
func ExampleMessage_Link() {
	message := account.Message{Token: "a b"}
	fmt.Println(message.Link("https://app.example.com/verify"))
	// Output: https://app.example.com/verify#token=a+b
}

// ExampleMessage_QueryLink builds the query form for a server-rendered page.
func ExampleMessage_QueryLink() {
	message := account.Message{Token: "a b"}
	fmt.Println(message.QueryLink("https://app.example.com/verify"))
	// Output: https://app.example.com/verify?token=a+b
}
