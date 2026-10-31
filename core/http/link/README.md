# Accept signed actions without leaking their credentials

Use [link](https://pkg.go.dev/arkoren.dev/core/http/link) when possession of a
server-issued URL authorizes an application action. Load an independent
`key.Shared` through the key package before constructing the signer; do not
generate a replacement key to repair missing startup configuration.

For an existing validated `secret` and router `routes`:

```go
signer, err := link.New(secret, func(signer *link.Builder) {
	signer.Origin("https://app.example.com")
})
if err != nil {
	return err
}
routes.With(link.Middleware(signer)).Get("/download", download)
```

The configured origin, not the request Host header, determines absolute-link
verification. Without an origin, the signer accepts root-relative paths only.
The signature does not cover the HTTP method: register only the methods your
action permits. Signing and verification must see the same path; stripping a
prefix changes that path. Ignored query parameters are not authorization input.

## Spend a temporary link before applying its action

Configure a shared cache with `Builder.Cache`, create a positive-lifetime
temporary link, and call `ConsumeRequest` in the action handler before committing
the change. Middleware only checks validity and known consumption; it does not
spend links. A successful consume followed by a failed action leaves the link
spent. Issue a new link through your application's recovery flow.

A cache error does not prove that consumption failed: the marker may have been
committed before confirmation was lost. Stop the action after any error. A cache
that loses markers before their TTL permits replay, so choose retention and
availability appropriate to your action. Cache key identities and encoded URLs
are unchanged by event privacy protections.

`Bind` can revoke links when server-side state changes. A `BindRequest` lookup
runs before signature verification, so its URL inputs are untrusted. Validate
inputs and redact callback error causes before application-controlled logging.

## Observe outcomes without publishing URLs or causes

`Builder.Events` enables `Rejected` and `Consumed`. Their payloads contain only
the HTTP `Method`, empty for direct `Consume`/`ConsumeURL` calls. `Path` and the
rejection's raw `Err` were removed because paths and error messages can contain
credentials. The returned rejection problem still preserves its original cause
for `errors.Is`/`errors.As`; event listeners do not receive it as payload.

Event contexts retain caller values, and custom HTTP methods are client-controlled.
HTTP problem `instance` rendering is unchanged and can contain the request path.
`Inspection.URL` still contains application paths and query values even after the
signature and expiry are removed. Config formatting redacts rotation keys and
omits service pointers, but leaves Origin and ignored parameter names visible.
These interfaces are not general-purpose sanitizers: select safe data explicitly.
