---
title: Bindings and Security
description: Describe broker endpoints, authentication requirements, and protocol bindings without changing runtime infrastructure.
order: 40
summary:
  - Add credential-free broker endpoints and explicitly select servers for each channel.
  - Describe authentication alternatives without confusing metadata with authorization enforcement.
  - Attach protocol bindings while keeping provisioning and delivery guarantees outside the document.
---

# Bindings and Security

Payload schemas do not tell an integrator where to connect or which credentials
the broker requires. Add **servers** for broker endpoints, **security schemes**
for authentication methods, and **bindings** for protocol-specific details.
These declarations describe your deployment; they do not configure drivers,
provision resources, or enforce access.

The snippets use `arkoren.dev/core/asyncapi` inside the construction callbacks
introduced in [Queues and Operations](queues-and-operations.md). Keep credentials,
private keys, tokens, and sensitive example values out of every declaration:
the resulting metadata is included in the exported document.

## Broker Endpoints

To document the reporting worker's AMQP broker, add a server in the document
callback. Its local ID is separate from the host and protocol:

```go
definition.Server("primary", "broker.example:5672", "amqp", func(listener *asyncapi.ServerBuilder) {
	listener.Description("Reporting broker")
	listener.ProtocolVersion("0.9.1")
	listener.Pathname("/reports")
})
```

The host accepts a host with an optional port, including bracketed IPv6, not a
URL. Schemes, credentials, paths, queries, and fragments are rejected. Use
`Pathname` for the endpoint path. `ProtocolVersion` describes the protocol; it
does not negotiate one.

Once a document declares any servers, every channel must explicitly select at
least one. In the existing `work` channel callback, add:

```go
channel.Servers("primary")
```

If you also declare the completion channel, add its server selection too. With
no declared servers, channels can omit this setting for a portable document.
Server IDs may be declared later, but unresolved or duplicate selections fail
export. Defining a server does not connect to it or inspect its configuration.

## Broker Authentication

To describe username/password authentication, register a scheme in the document
callback and select it on the server. Replace the earlier `primary` declaration
with this one, retaining its optional endpoint metadata if needed:

```go
definition.SecurityScheme("login", asyncapi.RawObject(`{"type":"userPassword"}`))
definition.Server("primary", "broker.example:5672", "amqp", func(listener *asyncapi.ServerBuilder) {
	listener.Security("login")
})
```

The scheme describes a method, not a username or password. Configure actual
authentication and authorization on your broker and driver separately. The
exporter checks supported scheme type names, required fields for common scheme
families, and local references. It does not fully validate protocol rules or
OAuth flow semantics.

You can declare schemes after their selections. Duplicate scheme IDs and
unresolved local references fail export. Raw scheme aliases to local schemes
are checked within the security-scheme collection; external references remain
opaque and are never fetched.

## Requirement Alternatives

If a broker accepts either username/password or a client certificate, register
both schemes and select both on the server. This is an alternative to the
previous authentication declaration:

```go
definition.SecurityScheme("login", asyncapi.RawObject(`{"type":"userPassword"}`))
definition.SecurityScheme("certificate", asyncapi.RawObject(`{"type":"X509"}`))
definition.Server("primary", "broker.example:5671", "amqps", func(listener *asyncapi.ServerBuilder) {
	listener.Security("login", "certificate")
})
```

The server's security array means `login` **OR** `certificate`, not both.
`OperationBuilder.Security` likewise lists alternatives within an operation.
Server and operation requirements apply together (**AND** across the levels);
an operation's selection does not replace server authentication.

For example, after declaring a separate scheme named `publisher`, adding
`action.Security("publisher")` inside a sending operation documents an additional
requirement. It does not authorize that operation or define broker permissions.
Duplicate selections are errors, including across repeated `Security` calls.

Broker security metadata does not protect the HTTP endpoint that serves this
document. Apply separate [document access controls](serving.md#protected-access)
before exposing that endpoint.

## Protocol Bindings

A binding is a raw protocol-specific object attached to a server, channel,
operation, or message. Choose the level defined by that protocol's binding
specification. To describe the reporting work destination as an AMQP queue,
add this inside its channel callback:

```go
channel.Binding("amqp", asyncapi.RawObject(`{
  "is": "queue",
  "queue": {
    "name": "production.report-work",
    "durable": true
  },
  "bindingVersion": "0.3.0"
}`))
```

This exports the supplied queue metadata. It does not create the queue, make it
durable, or guarantee durable delivery. Configure and verify the deployed broker
independently; a successful export cannot establish that these details match it.
Server, operation, and message builders also expose `Binding` for their levels.

Bindings receive only JSON-object syntax checks, protocol-key ID checks, and
duplicate-key checks at their own level. Nested references and protocol-specific
semantics are opaque. Document graph references and local security aliases are
checked separately; they do not turn binding contents into resolved references.

No broker inventory, producer discovery, traffic sampling, or runtime address
parameter expansion occurs during export. Keep these declarations aligned with
your application's actual transport and infrastructure configuration.

For endpoint, binding, and security options, see the
[`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
