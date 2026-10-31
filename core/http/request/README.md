# HTTP Request Helpers

Use `arkoren.dev/core/http/request` from error-returning HTTP handlers to read
parameters and bodies. Return its input problems to the router; do not turn
operational failures into field-validation feedback.

- JSON uses Go's `encoding/json/v2` defaults, including duplicate-name rejection
  and case-sensitive member matching. `Strict` additionally rejects unknown names.
- XML decodes the first element only. Invalid numeric fields are input errors;
  trailing elements are deliberately not checked.
- A nil `r.Body` is empty input. Size options limit reads, not decoded memory,
  and cannot remove an upstream reader's body-size ceiling.
- Body decoding does not check `Content-Type`. Apply the content-type middleware
  when a route requires that policy.
- Query helpers parse Go scalars, not JSON Schema constraints. Apply application
  validation when finite floats or stricter URL syntax are required.

Custom JSON decoder methods must explicitly identify intended input failures
with an input problem or JSON syntax/type error. Ordinary callback, transport,
and multipart temporary-file failures retain their operational causes.
Resolve unsupported schema layouts before handling requests.

## JSON validation diagnostics

`ValidateJSON` and `ParseJSON` report native semantic failures as `decode`
violations under the existing 422 validation problem. They preserve JSON Pointer
paths and native causes for server-side inspection. Strict unknown members keep
`unknown`, and schema-generated codes, including `type`, are unchanged.

Native decoding feedback uses optional `params.default_representation`, not
`params.expected`. This is a default-type hint, not a field's required wire kind:
`json:"count,string"` accepts `"42"` and rejects `42`, even though the hint is
`integer`. Binary, timestamp, and text-decoded values retain a default `string`
hint. Opaque custom JSON methods, unsupported Go kinds, and `time.Duration` have
no hint. Update consumers of the former native `type`/`expected` diagnostic to
use `decode`/`default_representation` without prescribing a field format.

The generic message does not expose native error text, submitted values, or Go
type names. Add a `decode` entry to your validation catalog to translate it;
without one, the message stays English. Schema `type` translations do not apply.

The Go 1.27 JSON-v2 API does not support `format:*` field-tag options, including
`format:array`. `time.Duration` has no default JSON representation; use an
application decoder or convert an explicitly represented DTO field.

See the [package API and examples](https://pkg.go.dev/arkoren.dev/core/http/request)
for error identities, body options, and schema-validation behavior.
