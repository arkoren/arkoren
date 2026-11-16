package access_test

import (
	"fmt"

	"arkoren.dev/ark/access"
)

// ExampleMessage_Link builds the link of a verification email: the token is in
// the fragment.
func ExampleMessage_Link() {
	message := access.Message{Kind: access.KindVerify, Token: "a b"}
	fmt.Println(message.Link("https://app.example.com/verify-email"))
	// Output: https://app.example.com/verify-email#token=a+b
}

// ExampleMessage_QueryLink builds the query form for a server-rendered page.
func ExampleMessage_QueryLink() {
	message := access.Message{Kind: access.KindVerify, Token: "a b"}
	fmt.Println(message.QueryLink("https://app.example.com/verify-email"))
	// Output: https://app.example.com/verify-email?token=a+b
}
