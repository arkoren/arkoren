package push_test

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/core/service/push"
)

func ExampleNewMessage() {
	data := map[string]string{"order": "42"}
	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		notification.Body("Collect your order.").Data(data)
	})

	if err := message.Err(); err != nil {
		fmt.Println("invalid message")

		return
	}

	data["order"] = "caller mutation"
	fmt.Println(message.Title(), message.Data()["order"])
	// Output: Ready 42
}

// browserSubscription derives a deterministic valid browser public key from a
// fixed scalar. This is fixture material, never production key generation.
func browserSubscription(t *testing.T) push.Subscription {
	t.Helper()

	scalar := make([]byte, 32)
	scalar[31] = 1

	key, err := ecdh.P256().NewPrivateKey(scalar)

	if err != nil {
		t.Fatal(err)
	}

	return push.Subscription{
		Endpoint: "https://push.example/send/secret-registration?credential=secret",
		Keys: push.SubscriptionKeys{
			P256DH: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")),
		},
	}
}

// expectPanic verifies callback misuse without recovering outside its call
// scope. A missing panic fails immediately after the supplied function returns.
func expectPanic(t *testing.T, call func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	call()
}

func TestMessageAcceptsCanonicalSubscription(t *testing.T) {
	t.Parallel()

	registration := browserSubscription(t)
	message := push.NewMessage(registration, "Ready 🌍", func(notification *push.MessageBuilder) {
		notification.Body("Collect your order.").Data(map[string]string{"order": "42"})
	})

	if err := message.Validate(); err != nil {
		t.Fatal(err)
	}

	if message.To() != registration || message.Title() != "Ready 🌍" {
		t.Fatal("required fields changed")
	}

	if message.Body() != "Collect your order." || message.Data()["order"] != "42" {
		t.Fatal("optional fields changed")
	}

	registration.Endpoint = "https://changed.example"

	if message.To().(push.Subscription).Endpoint == registration.Endpoint {
		t.Fatal("message retained mutable subscription")
	}
}

func TestMessageAcceptsTokenAndOptionalEmptyContent(t *testing.T) {
	t.Parallel()

	message := push.NewMessage(push.Token("opaque-native-token"), "Ready")

	if err := message.Err(); err != nil {
		t.Fatal(err)
	}

	if message.To() != push.Token("opaque-native-token") || message.Body() != "" || message.Data() != nil {
		t.Fatal("unexpected message content")
	}
}

func TestMessageZeroInvalid(t *testing.T) {
	t.Parallel()

	message := push.Message{}

	if !errors.Is(message.Err(), push.ErrInvalidMessage) {
		t.Fatal("zero message accepted")
	}

	if !errors.Is(message.Validate(), push.ErrInvalidMessage) {
		t.Fatal("zero message validated")
	}
}

func TestMessageRejectsNoncanonicalRecipients(t *testing.T) {
	t.Parallel()

	registration := browserSubscription(t)
	token := push.Token("native")
	var missingSubscription *push.Subscription
	var missingToken *push.Token

	for _, recipient := range []push.Recipient{
		nil, missingSubscription, missingToken, &registration, &token, foreignRecipient{},
	} {
		message := push.NewMessage(recipient, "Ready")

		if !errors.Is(message.Err(), push.ErrInvalidMessage) {
			t.Fatalf("accepted noncanonical recipient %T", recipient)
		}

		if message.To() != nil {
			t.Fatalf("retained rejected recipient %T", recipient)
		}
	}
}

// foreignRecipient embeds the sealed interface to model a foreign lookalike.
// Message construction must reject it without invoking its nil embedded method.
type foreignRecipient struct {
	push.Recipient
}

func TestMessageRejectsInvalidNativeTokens(t *testing.T) {
	t.Parallel()

	for _, token := range []push.Token{"", "invalid-\xff"} {
		message := push.NewMessage(token, "Ready")

		if !errors.Is(message.Err(), push.ErrInvalidMessage) {
			t.Fatal("invalid token accepted")
		}
	}
}

func TestMessageRejectsInvalidTitles(t *testing.T) {
	t.Parallel()

	for _, title := range []string{"", "invalid-\xff"} {
		message := push.NewMessage(push.Token("native"), title)

		if !errors.Is(message.Err(), push.ErrInvalidMessage) {
			t.Fatal("invalid title accepted")
		}
	}
}

func TestMessageRejectsUnsafeEndpointStructureWithoutLeakingIt(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"http://push.example/secret", "/secret", "https:///secret", "https://user:secret@push.example",
		"https://push.example/#secret", "https://push.example:secret", "https://push.example/%secret",
	} {
		registration := browserSubscription(t)
		registration.Endpoint = endpoint

		err := push.NewMessage(registration, "Ready").Err()

		if !errors.Is(err, push.ErrInvalidMessage) {
			t.Fatal("invalid endpoint accepted")
		}

		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "push.example") {
			t.Fatal("endpoint leaked through validation")
		}
	}
}

func TestMessageDoesNotApplyEndpointNetworkPolicy(t *testing.T) {
	t.Parallel()

	registration := browserSubscription(t)
	registration.Endpoint = "https://127.0.0.1/send"

	if err := push.NewMessage(registration, "Ready").Err(); err != nil {
		t.Fatalf("structural validation applied network policy: %v", err)
	}
}

// TestMessageRejectsEmptyEndpointFragment keeps an empty fragment delimiter
// subject to the same secret-safe rejection policy as nonempty fragments.
func TestMessageRejectsEmptyEndpointFragment(t *testing.T) {
	t.Parallel()

	registration := browserSubscription(t)
	registration.Endpoint += "#"
	message := push.NewMessage(registration, "Ready")

	err := message.Err()

	if !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("empty fragment error = %v, want invalid message", err)
	}

	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "push.example") {
		t.Fatal("fragment rejection disclosed the subscription endpoint")
	}
}

// TestMessagePreservesEncodedHashInEndpoint distinguishes encoded path/query
// data from a literal fragment delimiter without normalizing the subscription.
func TestMessagePreservesEncodedHashInEndpoint(t *testing.T) {
	t.Parallel()

	registration := browserSubscription(t)
	registration.Endpoint = "https://push.example/send/%23registration?key=%23secret"
	message := push.NewMessage(registration, "Ready")

	if err := message.Err(); err != nil {
		t.Fatal(err)
	}

	if message.To() != registration {
		t.Fatal("encoded path or query changed during publication")
	}
}

func TestMessageRejectsInvalidPublicKeysWithoutLeakingThem(t *testing.T) {
	t.Parallel()

	registration := browserSubscription(t)
	valid := registration.Keys.P256DH

	for _, key := range []string{
		"", "secret-invalid", valid + "=", valid + "\n",
		base64.RawURLEncoding.EncodeToString(make([]byte, 65)),
		base64.RawURLEncoding.EncodeToString(make([]byte, 33)),
	} {
		registration.Keys.P256DH = key

		err := push.NewMessage(registration, "Ready").Err()

		if !errors.Is(err, push.ErrInvalidMessage) {
			t.Fatal("invalid public key accepted")
		}

		if strings.Contains(err.Error(), "secret-invalid") {
			t.Fatal("public key leaked")
		}
	}
}

func TestMessageRejectsInvalidAuthenticationSecrets(t *testing.T) {
	t.Parallel()

	registration := browserSubscription(t)
	valid := registration.Keys.Auth

	for _, secret := range []string{"", "secret-invalid", valid + "=", valid + "\n", "AA"} {
		registration.Keys.Auth = secret

		err := push.NewMessage(registration, "Ready").Err()

		if !errors.Is(err, push.ErrInvalidMessage) {
			t.Fatal("invalid auth accepted")
		}

		if strings.Contains(err.Error(), "secret-invalid") {
			t.Fatal("auth secret leaked")
		}
	}
}

func TestMessageCallbacksRunOnceInOrderAfterInvalidInput(t *testing.T) {
	t.Parallel()

	var retained *push.MessageBuilder
	var order []int
	policies := []func(*push.MessageBuilder){
		func(notification *push.MessageBuilder) {
			retained = notification
			order = append(order, 1)
			notification.Body("first")
		},
		func(notification *push.MessageBuilder) {
			if retained != notification {
				t.Fatal("callbacks used different drafts")
			}

			order = append(order, 2)
			notification.Body("second")
		},
	}

	message := push.NewMessage(nil, "", policies...)

	if !reflect.DeepEqual(order, []int{1, 2}) || message.Body() != "second" {
		t.Fatal("callbacks not applied once in order")
	}

	if !errors.Is(message.Err(), push.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "recipient") {
		t.Fatal("required-input validation precedence lost")
	}
}

func TestMessageCopiesDataAtInputPublicationAndInspection(t *testing.T) {
	t.Parallel()

	data := map[string]string{"order": "42"}
	var retained *push.MessageBuilder
	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		retained = notification
		notification.Data(data)

		data["order"] = "source mutation"
	})
	retained.Body("later").Data(map[string]string{"order": "draft mutation"})

	inspected := message.Data()
	inspected["order"] = "getter mutation"

	if message.Data()["order"] != "42" || message.Body() != "" {
		t.Fatal("published message shares mutable state")
	}
}

func TestMessageDataReplacesInsteadOfMerging(t *testing.T) {
	t.Parallel()

	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		if notification.Data(map[string]string{"old": "value"}) != notification {
			t.Fatal("Data did not return the same draft")
		}

		notification.Data(map[string]string{"new": "value"})
	})

	if !reflect.DeepEqual(message.Data(), map[string]string{"new": "value"}) {
		t.Fatal("Data merged instead of replacing")
	}
}

func TestMessageDataNilClearsMetadata(t *testing.T) {
	t.Parallel()

	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		notification.Data(map[string]string{"old": "value"}).Data(nil)
	})

	if message.Data() != nil || message.Err() != nil {
		t.Fatal("Data nil did not clear metadata")
	}
}

func TestMessageBodyErrorStickyAfterReplacement(t *testing.T) {
	t.Parallel()

	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		if notification.Body("\xff") != notification {
			t.Fatal("Body did not return same draft")
		}

		notification.Body("valid").Data(map[string]string{"\xff": "also invalid"})
	})

	if !errors.Is(message.Err(), push.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "body") {
		t.Fatal("first invalid optional input was not preserved")
	}
}

func TestMessageRejectsInvalidMetadataKeys(t *testing.T) {
	t.Parallel()

	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		notification.Data(map[string]string{"\xff": "valid"}).Data(nil)
	})

	if !errors.Is(message.Err(), push.ErrInvalidMessage) {
		t.Fatal("metadata key error not sticky")
	}
}

func TestMessageRejectsInvalidMetadataValues(t *testing.T) {
	t.Parallel()

	message := push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
		notification.Data(map[string]string{"key": "\xff"}).Data(map[string]string{"key": "valid"})
	})

	if !errors.Is(message.Err(), push.ErrInvalidMessage) {
		t.Fatal("metadata value error not sticky")
	}
}

func TestMessageKeepsRequiredTitleErrorBeforeOptionalErrors(t *testing.T) {
	t.Parallel()

	message := push.NewMessage(push.Token("native"), "", func(notification *push.MessageBuilder) {
		notification.Body("\xff").Data(map[string]string{"key": "\xff"})
	})

	if !errors.Is(message.Err(), push.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "title") {
		t.Fatal("title error precedence lost")
	}
}

func TestMessageNilCallbackPanics(t *testing.T) {
	t.Parallel()

	expectPanic(t, func() {
		push.NewMessage(nil, "", nil)
	})
}

func TestMessageCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	expectPanic(t, func() {
		push.NewMessage(push.Token("native"), "Ready", func(notification *push.MessageBuilder) {
			panic("application callback")
		})
	})
}

func TestMessageConstructorAndMethodsHaveExactAliasTypes(t *testing.T) {
	t.Parallel()

	constructor := (func(push.Recipient, string, ...func(*push.MessageBuilder)) push.Message)(push.NewMessage)
	alias := push.MessageBuilderFunc(func(notification *push.MessageBuilder) {
		body := (func(string) *push.MessageBuilder)(notification.Body)
		data := (func(map[string]string) *push.MessageBuilder)(notification.Data)
		body("body")
		data(map[string]string{"order": "42"})
	})

	policies := ([]func(*push.MessageBuilder))([]push.MessageBuilderFunc{alias})

	if err := constructor(push.Token("native"), "Ready", policies...).Err(); err != nil {
		t.Fatal(err)
	}

	var omitted []push.MessageBuilderFunc

	if err := constructor(push.Token("native"), "Ready", omitted...).Err(); err != nil {
		t.Fatal(err)
	}
}
