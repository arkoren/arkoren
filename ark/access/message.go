package access

import (
	"net/url"
	"time"
)

// Kind says which flow a [Message] belongs to.
//
// Example:
//
//	if message.Kind == access.KindReset {
//		// write the reset email
//	}
type Kind string

const (
	// KindVerify is the email verification of a new account.
	//
	// Example:
	//
	//	if message.Kind == access.KindVerify {
	//		// link to the verification page
	//	}
	KindVerify Kind = "verify"

	// KindReset is a password reset.
	//
	// Example:
	//
	//	if message.Kind == access.KindReset {
	//		// link to the reset page
	//	}
	KindReset Kind = "reset"

	// KindRegistration is the notice to the owner of an address that someone
	// tried to register with it. The token is a password reset token, as for
	// [KindReset]: the owner who did not register can set a new password, and an
	// owner who forgot they have an account can use it to log in. The response to
	// the registration is the same as for a new address, so this email is the only
	// place the owner learns of the attempt.
	//
	// Example:
	//
	//	if message.Kind == access.KindRegistration {
	//		// write "someone tried to register with this address"
	//	}
	KindRegistration Kind = "registration"
)

// Message is what the function given to [Builder.Deliver] receives: the secret
// link of a verification or reset email. The token is a credential until it is
// used or expires; never log it. Everything else the application wants to say
// about security (a changed password, disabled two-factor) is an event on
// [Builder.Events], which never carries a token.
//
// Example:
//
//	iam.Deliver(func(ctx context.Context, message access.Message) error {
//		base := "https://app.example.com/verify-email"
//		if message.Kind == access.KindReset {
//			base = "https://app.example.com/reset-password"
//		}
//		return mailer.Send(ctx, message.Identifier, message.Link(base))
//	})
type Message struct {
	// Kind says which flow sent the message.
	Kind Kind

	// Identifier is who to write to: the email stored with the user with the
	// built-in directory, or the address that the application's [Users.Lookup]
	// returned. It is never the text of a request, so a request cannot send a
	// victim's link elsewhere.
	Identifier string

	// Token is the single-use secret to put in the link.
	Token string

	// ExpiresAt is when the token stops working.
	ExpiresAt time.Time
}

// Link returns base with the token as its "token" query parameter, escaped.
//
// Example:
//
//	message := access.Message{Token: "abc"}
//	fmt.Println(message.Link("https://app.example.com/verify")) // https://app.example.com/verify?token=abc
func (message Message) Link(base string) string {
	return base + "?" + url.Values{"token": {message.Token}}.Encode()
}
