# AsyncAPI documents

Use `arkoren.dev/core/asyncapi` to describe the messages your application sends
and receives without connecting to a broker or changing runtime behavior.
Install the package with `go get arkoren.dev/core` in your application module.

For application wiring, schema limits, and envelope examples, start with the
[AsyncAPI overview](../../cmd/arkoren/skills/arkoren/references/guides/asyncapi/overview.md).
The package does not include a viewer or enforce authentication or broker policy.

## Describe a protocol binding

Import `arkoren.dev/core/asyncapi`, then pass the protocol name separately from
its individual binding object:

```go
document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
	definition.Channel("invoices", "billing.invoices", func(channel *asyncapi.ChannelBuilder) {
		channel.Binding("kafka", asyncapi.RawObject(`{"bindingVersion":"0.5.0"}`))
	})
})
data, err := document.JSON()
```

Here, `data` contains the detached document bytes to write or serve. The exported path is
`channels.invoices.bindings.kafka.bindingVersion`. Do not wrap the supplied
object in another `kafka` member. Protocol metadata is opaque: the package checks
JSON object syntax, not the protocol's complete binding specification.

## Export and serve safely

Construction callbacks run once in order, including invalid declarations; nil
callbacks panic as caller misuse. Schema and graph failures are deferred to
`JSON` or `Handler`, and the first export caches validation success or failure.
Retaining drafts or mutating returned bytes cannot change the document.

Register `document.Handler()` explicitly on your router and apply your
application's access policy. The handler serves the cached JSON, returns export
errors before committing a response, and sends no body for HEAD. Schema defaults,
examples, annotations, and bindings do not validate runtime message instances.
Raw schemas use a self-contained Draft 7 subset with no reference resolution.

See the [AsyncAPI API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
