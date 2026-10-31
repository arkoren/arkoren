---
title: Bindings and Security
description: Describe broker endpoints, protocol-specific metadata, and authentication requirements without configuring runtime security.
order: 40
---

# Bindings and Security

## Introduction

A message schema does not tell another service which broker endpoint to use or
how clients authenticate. Add server metadata, protocol bindings, and security
requirements to describe those integration details. These declarations document
the connection contract; they do not change your driver's configuration.

This page extends the channel and operation declarations from the
[AsyncAPI overview](overview.md). All snippets belong inside construction
callbacks and use the `arkoren.dev/core/asyncapi` import.

## Describing a broker endpoint

Keep the server's local ID separate from its host. Replace an existing server
declaration with a configured one rather than declaring its ID twice:

```go
definition.Server("primary", "broker.example:5672", "amqp", func(listener *asyncapi.ServerBuilder) {
	listener.Description("Primary billing broker")
	listener.ProtocolVersion("0.9.1")
	listener.Pathname("/billing")
})
```

The host accepts a host and optional port, including bracketed IPv6, not a URL.
Hosts reject schemes, credentials, paths, queries, and fragments. Use `Pathname`
separately if the endpoint needs a path. `ProtocolVersion` is documentation, not
protocol negotiation. If any servers are declared, each channel must explicitly
select its servers with `channel.Servers("primary")`.

Never put credentials in a host, example, or binding. These values become part
of the exported document and may be distributed beyond your service.

## Documenting broker authentication

To describe a broker that requires a username and password, register a security
scheme and select it on the relevant server. In a document callback:

```go
definition.SecurityScheme("login", asyncapi.RawObject(`{"type":"userPassword"}`))
definition.Server("primary", "broker.example:5672", "amqp", func(listener *asyncapi.ServerBuilder) {
	listener.Security("login")
})
```

Use this instead of the earlier `primary` declaration, or add `Security` to
that existing server callback. The scheme contains the authentication method,
not real credentials. These declarations neither authenticate clients nor
enforce authorization. Configure those controls on the actual broker and driver.

Security schemes receive limited structural checks: supported type names,
required fields for common scheme families, and local scheme aliases. They are
not fully validated against protocol or OAuth flow semantics. External scheme
references remain opaque and are never fetched.

## Combining security requirements

A server or operation can name alternative security schemes. For a broker
accepting either username/password or client certificates, register both schemes
and select them in one server callback:

```go
definition.SecurityScheme("login", asyncapi.RawObject(`{"type":"userPassword"}`))
definition.SecurityScheme("certificate", asyncapi.RawObject(`{"type":"X509"}`))
definition.Server("primary", "broker.example:5671", "amqps", func(listener *asyncapi.ServerBuilder) {
	listener.Security("login", "certificate")
})
```

The server's array means `login` **OR** `certificate`, not both. An operation's
security array also means alternatives within that level. If both the server
and operation declare requirements, both levels apply (**AND**): the operation's
alternatives do not replace the server's requirements.

For example, after registering an additional scheme named `publisher`, add
`action.Security("publisher")` to the sending operation callback. This documents
an additional operation requirement, not an authorization check. Scheme IDs may
be declared later, but duplicates and unresolved local references are export
errors.

Broker security declarations do not protect an HTTP documentation route.
[Serving AsyncAPI Documentation](serving.md#serving-a-protected-document)
shows where to apply your existing HTTP middleware.

## Adding protocol bindings

A **binding** is protocol-specific metadata attached to a server, channel,
operation, or message. Use the level that the protocol's binding specification
defines. For example, within an existing channel callback, declare the AMQP
queue metadata for the billing channel:

```go
channel.Binding("amqp", asyncapi.RawObject(`{
  "is": "queue",
  "queue": {
    "name": "production.billing",
    "durable": true
  },
  "bindingVersion": "0.3.0"
}`))
```

`Binding` also exists on the server, operation, and message builders. Each call
captures a caller-supplied `RawObject` at that level. It does not create the
queue, make it durable, use a broker SDK, or change the transport.

Bindings are opaque: only JSON object syntax, the protocol key's ID syntax, and
duplicate protocol keys at that level are checked. Nested references are not
resolved or validated. Actual document graph edges and local security-scheme
aliases are checked separately. You remain responsible for protocol-specific
binding semantics and for matching the deployed infrastructure.

The exporter supplies no broker inventory, producer discovery, traffic sampling,
or runtime parameter expansion. Exporting valid JSON is not proof that a binding
matches a broker's configuration.

For every server, binding, and security option, see the [`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
