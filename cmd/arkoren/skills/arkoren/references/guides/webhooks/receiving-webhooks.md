---
title: Receiving Webhooks
description: Authenticate incoming provider notifications and bind reusable event definitions to typed application handlers.
order: 10
summary:
  - Mount a verified receiver and distinguish acceptance from completed business work.
  - Define reusable sources and typed events without mixing credentials with application policy.
  - Preserve raw request bytes, classify verification failures, and share handler bindings safely.
---

# Receiving Webhooks

A webhook is an HTTP notification from another service. Unlike a browser request,
it usually has no logged-in user: verify the provider's credentials before acting
on the notification. The `webhook` package authenticates each delivery, decodes
the event you bind, and acknowledges application acceptance.

Three values separate those responsibilities:

- A **source** verifies requests for one provider account and returns its envelope,
  the provider metadata surrounding the still-encoded event payload.
- An **event** associates an exact provider event key with a payload decoder. It
  holds no credentials and works with multiple compatible sources.
- A **receiver** binds events to your application handlers and sets request policy.

Use a [provider adapter](provider-adapters.md) when one is shipped. Constructing
an outbound mail, model, or payment driver neither creates a source nor registers
an endpoint. Your application owns the public route and provider registration.

## Binding Handlers

For Resend delivery notifications, import `arkoren.dev/driver/resend/resendwebhook`
and `arkoren.dev/core/http/webhook`. Configure the endpoint in Resend and load its
signing secret—not the sending API key. See the
[adapter setup](provider-adapters.md#resend) for installation and verification.

```go
source, err := resendwebhook.New(signingSecret)
```

Construction checks the secret's format; verification happens on each request.
Focused fragments keep construction errors visible and assume success. Handle
those errors before using the returned values or registering routes.

With `context` imported and an existing application service `messages`, accept
only the delivered event:

```go
accept := func(ctx context.Context, delivery webhook.Delivery[resendwebhook.Email]) error {
	return messages.RecordDelivered(ctx, delivery.Payload.EmailID)
}
```

`messages.RecordDelivered` is application-owned and must tolerate duplicates.
Return nil only when you have accepted responsibility for the notification.
Every handler error answers 503, even if some work has already committed; core
does not roll back your work. A 503 permits redelivery but cannot make the
provider retry. Use [queued handling](delivery-policies.md#queued-handling) for
slow work rather than making the provider wait.

Bind that function while constructing the receiver:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Handle(resendwebhook.EmailDelivered, accept)
})
```

The handler receives the authenticated delivery ID, exact provider event type,
and decoded email metadata. It does not receive the raw request or credentials.
Each key has one binding; registering it twice is an error, not fan-out.

With your existing router `r`, mount the returned `router.Handler` directly:

```go
r.Post("/webhooks/resend", handler)
```

After acceptance returns nil, the receiver writes 200 with an empty body.
Authenticated unbound events also receive 200 without calling your handler.
Acceptance is not proof that downstream work finished. Choose
[delivery policies](delivery-policies.md) before relying on redelivery,
deduplication, or queue durability.

## Custom Sources

If no adapter implements your provider's protocol, supply a verifier with
`webhook.NewSource`. Core supplies no signature algorithm or replay protection
for custom sources. Prefer the provider's supported verifier rather than inventing
a signature format.

For a hypothetical shipping provider in your application, define the envelope
using `encoding/json/jsontext`:

```go
type Envelope struct {
	ID   string         `json:"id"`
	Kind string         `json:"kind"`
	Data jsontext.Value `json:"data"`
}
```

The provider-specific `verifyAndParse` function below is your code, not an Arkoren
helper. It must authenticate the exact received bytes, implement the provider's
timestamp/replay rules, and return an envelope only after authentication. For a
known invalid request, wrap both `webhook.ErrRejected` and your native sentinel,
for example `fmt.Errorf("%w: %w", webhook.ErrRejected, ErrBadSignature)`.
Leave transient verification failures unclassified: they answer 503 rather than
rejecting a potentially genuine notification.

With `net/http` imported and your account's `secret` already loaded:

```go
source := webhook.NewSource(func(r *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
	envelope, err := verifyAndParse(r, body, secret)

	if err != nil {
		return webhook.Delivery[Envelope]{}, err
	}

	return webhook.Delivery[Envelope]{
		ID:      envelope.ID,
		Type:    envelope.Kind,
		Payload: envelope,
	}, nil
})
```

The verifier receives the bounded body exactly as sent; `r.Body` is already spent.
Do not decode and re-encode the body before verifying a body signature. Headers,
method, URL, and `r.Context()` remain available for protocols that need them.
Do not select a business action or trust payload fields before verification.

The receiver reads at most 1 MiB by default and rejects larger bodies with 413
before the source runs. An outer body-limit middleware can lower that ceiling.
See [body limits](delivery-policies.md#body-limits) before increasing it.

`NewSource` retains the function without calling it or checking credentials.
Your provider constructor must validate credentials; `Source.Err` and `Handler`
only detect a missing verifier. Its function, credentials, and captured clients
are shared across requests and must support concurrent use.

## Typed Events

Keep reusable event definitions separate from configured sources. For the shipping
envelope above, use a small payload with the fields your handler needs:

```go
type Shipment struct {
	ID       string `json:"shipment_id"`
	Tracking string `json:"tracking_number"`
}
```

With `json` imported from `encoding/json/v2`, define the provider's exact key:

```go
var ShipmentDispatched = webhook.NewEvent("shipment.dispatched", func(envelope Envelope) (Shipment, error) {
	var shipment Shipment
	err := json.Unmarshal(envelope.Data, &shipment)

	return shipment, err
})
```

Only a bound, authenticated event reaches this decoder. Decoders must not perform
network I/O; keep remote lookups in acceptance or a worker. Ordinary decoder
errors answer 503, allowing a later schema fix to accept a redelivery. An
authenticated decoder can return `ErrSkip` to acknowledge without acceptance;
the [skip rules](delivery-policies.md#acknowledgement-outcomes) differ from errors
returned by handlers.

Event keys are case-sensitive provider strings, not queue names or diagnostic
categories. Empty keys, control characters, and nil decoders are invalid.
Types connect a handler to its payload, not to a particular account or secret.
Bind `ShipmentDispatched` through `receiver.Handle` in the same way as the shipped
Resend event above.

## Shared Bindings

A library can publish its source constructor and credential-free event
definitions. To share application bindings, return a `webhook.BuilderFunc` rather
than hiding endpoint policy inside the source. With an application-owned
`acceptShipment` of type `func(context.Context, webhook.Delivery[Shipment]) error`:

```go
bindings := func(receiver *webhook.Builder[Envelope]) {
	receiver.Handle(ShipmentDispatched, acceptShipment)
}
handler, err := webhook.Handler(source, bindings)
```

Additional callbacks run once in order on the same construction draft, so the
application can add receipt policy without redefining the binding. Publication
copies binding and rejection containers, not functions or captured dependencies.
There is no receiver lifecycle; the owners of those dependencies manage them.

`Handler` reports invalid sources, events, bindings, or policy with `ErrInvalid`.
At least one event must be bound. The first invalid setting is retained, later
callbacks still run, and a supplied nil callback panics. Fix construction errors
before serving requests rather than waiting for a provider delivery to reveal them.

For every function and option, see the
[`webhook` API reference](https://pkg.go.dev/arkoren.dev/core/http/webhook).
