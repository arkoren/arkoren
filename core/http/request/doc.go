// Package request reads and validates HTTP request data: parameters, headers, cookies, bodies, and the user.
//
// The package focuses on safe extraction and decoding of request data:
// path/query parameters, headers/cookies, body parsing, lifecycle hooks, and
// the session and user ([User]) stored by middleware.
//
// # Behavior notes
//
// Body helpers return typed errors for malformed payloads and limit body
// size: an explicit [MaxSize], else the limit in the request context
// (the bodylimit middleware), else
// [DefaultMaxBodySize]. [Unlimited] adds no per-call cap but cannot remove an
// existing reader's ceiling. [Hooks] requires a request served by router.Handler
// and panics with [ErrNoHooksMiddleware] otherwise; there is no safe public lookup.
// A nil r.Body is empty input, including for requests constructed by
// http.NewRequest: raw readers succeed with no data and JSON/XML return their
// malformed-body problem. Decoders do not check Content-Type; use the
// contenttype middleware when a route requires a specific media type.
// Size options cannot remove a ceiling already enforced by r.Body's reader.
// Query scalar conversion follows strconv and does not execute OpenAPI schemas;
// use application validation for stricter numeric or URL requirements.
//
// [Session] and [MustSession] select the default session unless per-call
// [WithSessionHandle] options select another bridge. Later handles replace
// earlier ones; zero restores default lookup. These options contain no cookie
// or persistence settings. MustSession panics with session.ErrNoSession when
// that selected bridge is absent or nil; it never falls back to the default.
//
// Example:
//
//	admin := session.NewHandle()
//	current := request.MustSession(r, request.WithSessionHandle(admin))
//	if err := current.Put(session.NewField[string]("theme"), "dark"); err != nil {
//		return err
//	}
//
// # Multipart
//
// [Multipart] parses multipart/form-data once, with a size cap, and returns a
// [Form] of text fields and uploaded files. Combine it with
// validate.File and storage.Storage.PutFile for uploads.
//
// # Validation
//
// The Validate* and Parse* helpers decode a body like their plain
// counterparts ([JSON] and [XML]) and check it against a [validate.Schema];
// they take the same [BodyOption] values. T is inferred from
// the schema. Validate* returns the decoded value as is, Parse* returns the
// schema.Parse result with defaults applied. The locale and catalog come
// from the request context, see the locale package.
//
// Malformed JSON syntax, including duplicate names, stays [ErrInvalidJSON]
// (400); oversized bodies return [ErrBodyTooLarge] (413). Native JSON semantic
// failures become "decode" violations; strict unknown members retain "unknown".
// Both preserve the native JSON Pointer and merge with schema violations under
// [ErrValidationFailed] (422). Schema codes, including "type", are unchanged.
// [ValidationProblem] puts the [validate.Errors] in the problem's "errors"
// member. Its cause retains both the violations and native semantic error,
// without retaining an intermediate 400 problem identity.
//
// Native GoType metadata describes the output type, not the field's wire
// format. A "decode" violation therefore omits params.expected. Its optional
// params.default_representation is only a default hint, such as "integer" for
// an integer or "string" for binary, timestamp, and text-decoded values. For
// json:"count,string", "42" is valid and 42 is not; the integer hint is not
// advice to remove the quotes. Opaque custom JSON methods, unsupported Go kinds,
// and time.Duration have no hint. No JSON field resolver or tag parser is used
// to infer a field-specific expectation. The generic English message excludes
// native error text, submitted values, and Go type names; translate it with a
// "decode" entry in the validation catalog, not the schema's "type" entry.
//
// This Go 1.27 JSON-v2 API does not support format:* field-tag options such as
// format:array. time.Duration has no default JSON representation; use an
// application decoder or a DTO field with an explicit application conversion.
// Native XML syntax and scalar conversion errors carry no JSON Pointer and stay
// 400; ordinary application decoder failures are not assumed to be bad input.
// Native integer decoding rejects fractions, exponents, and overflow. Ordinary
// errors from custom unmarshaler methods, body reads, and multipart temporary-file
// I/O are not converted into input problems or decoding violations. Their causes
// remain available through errors.Is/errors.As for server-side handling.
//
// The router does not render a raw [validate.Errors]; it answers 500. Pass
// errors from schema.Validate or schema.Parse through [ValidationProblem], or
// register it once with the errormap middleware.
//
// encoding/json/v2 stops at its first semantic error and leaves the members
// after it undecoded, so at most one native decoding violation is reported.
// The schema can add its own violations on the partially decoded value, but
// "required" violations are dropped in that case because the missing
// members were never read. Fix the reported value and resubmit to see the
// rest.
package request
