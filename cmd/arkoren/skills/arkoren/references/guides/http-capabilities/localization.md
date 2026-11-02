---
title: Localization
description: Select a request language from client preferences and render validation messages with catalogs and explicit fallbacks.
order: 20
summary:
  - Build language catalogs and select a request language from query, cookie, and header preferences.
  - Render validation messages and application text with explicit regional and missing-message fallbacks.
  - Load translations before middleware construction and check language-dependent responses.
---

# Localization

A signup form can use the same validation rules for every visitor while showing
messages in each visitor's language. The `locale` middleware selects a language
once per request and puts it, with a **catalog** of message templates, in the
request context. Validation uses that context to render messages; your handlers
can use the language for their own text.

Arkoren does not provide a general translation engine, date/number formatting,
or plural handling. Keep application text in your own catalogs or translation
library. This page connects language selection to messages; the
[validation error guide](../validation/errors.md#translate-messages) owns error
conversion and relocalization.

## Build Catalogs

A `validate.Catalog` names a default language and maps supported language tags to
`validate.Messages`. Each message maps a stable violation code, such as
`string.min`, to plain text with optional `{param}` placeholders. Import
`arkoren.dev/foundation/validate` and define a small Spanish translation:

```go
spanish := validate.Messages{
	"required":     "es obligatorio",
	"string.min":   "debe tener al menos {min} caracteres",
	"string.email": "debe ser un correo electrónico válido",
}
```

For `string.min`, `{min}` comes from the violation's `min` parameter. A missing
parameter leaves its placeholder visible, so test spelling. Lists render with
commas and times use RFC 3339. Templates do not include the field name; the
validation error's path identifies the input. Use number-independent phrasing
when a language needs plural forms the template system cannot express.

Add English as the default, using the independently mutable copy returned by
`validate.English()`:

```go
catalog := validate.Catalog{
	Default: "en",
	Locales: map[string]validate.Messages{
		"en": validate.English(),
		"es": spanish,
	},
}
```

Catalog keys determine which languages clients can select. The Spanish catalog
can be incomplete: missing messages fall back rather than disabling the language.

## Select Languages

With an existing router `routes`, import
`arkoren.dev/core/http/middleware/locale` and register middleware before routes
that validate or use translated text:

```go
routes.Use(locale.Middleware(func(language *locale.Builder) {
	language.Catalog(catalog)
}))
```

This uses `Accept-Language`; query and cookie selectors are disabled by default.
For example, `Accept-Language: es-MX,es;q=0.9,en;q=0.5` selects the catalog key
`es` and sets `Content-Language: es`. Without locale middleware, validation uses
built-in English and `locale.FromContext` returns an empty string.

If links should force a language and your interface remembers a visitor's choice,
use this alternative registration:

```go
routes.Use(locale.Middleware(func(language *locale.Builder) {
	language.Catalog(catalog)
	language.QueryParam("lang")
	language.Cookie("language")
}))
```

The middleware tries the first supported choice in this order:

1. The named query parameter, such as `?lang=es`.
2. The named cookie, such as `language=es`.
3. `Accept-Language`, ordered by client preference.
4. The catalog default, or `en` if it is empty.

An absent or unsupported query value falls through rather than blocking the
cookie/header. Empty selector names disable those sources. Cookie detection only
reads: your application writes the preference cookie and chooses its security
attributes and lifetime. There is no locale cookie-lifetime API. Client language
preferences are presentation input, not proof of identity or permission.

Matching ignores case, surrounding whitespace, and `_` versus `-`, then tries
the base language. A request for `es-MX` can select `es`; a request for `es` does
not select an `es-MX`-only entry. Add base-language entries when they should be
selectable. If nothing matches, the default remains usable even without an entry
in `Locales`.

### Header Weights

Header candidates use descending `q` weights; omitted weights are `1` and ties
retain header order. Thus `fr;q=0.5, es;q=0.9, en;q=0.9` tries `es`, `en`, then
`fr`.

Weights accept HTTP decimal syntax: `0` or `1`, optionally a decimal point and
up to three digits; digits after `1` must be zero. `0.9` and `1.000` are valid;
`.9`, `+0.9`, `1.001`, `0.1234`, exponent/hexadecimal forms, and nonfinite values
are not. Missing or duplicate `q` parameters invalidate that entry. Malformed
language tags, wildcards, invalid weights, and `q=0` entries are skipped, not
request errors. Skipping a candidate does not veto the configured fallback.

### Configuration Checks

The completed catalog and nonempty cookie name are checked after every
construction callback has run. Tags require one to eight ASCII letters followed
by optional one-to-eight-character alphanumeric subtags, separated with `-` or
`_`; surrounding ASCII whitespace is ignored for matching. This is a shape
check, not full BCP 47 validation or a registry lookup.

Invalid tags, duplicate normalized keys such as `en-US` and `EN_us`, and cookie
names that fail `net/http.Cookie.Valid` panic during construction. An empty
catalog/default is valid. Callbacks run once in order, so a later callback can
replace an earlier invalid setting; nil callbacks panic. Finish loading messages
before construction: middleware copies both catalog map layers, and later edits
or retained builders do not reconfigure it.

## Read Languages

Use the selected language for application-owned text. In a handler with incoming
request `r`, `w http.ResponseWriter`, and imports `net/http` and
`arkoren.dev/core/http/response`:

```go
switch locale.FromContext(r.Context()) {
case "es":
	return response.String(w, http.StatusOK, "Hola")
default:
	return response.String(w, http.StatusOK, "Hello")
}
```

The Spanish request above receives `Hola`. The default branch also covers tests
or callers without middleware. For a larger catalog, use your translation
library with this language rather than growing a switch for every message.

A match preserves the original catalog spelling. A key `ES_mx` becomes context
value `ES_mx`, but response header `Content-Language: ES-mx`: headers trim
whitespace and replace underscores without changing case. The default uses its
catalog spelling when an exact normalized key exists; otherwise it preserves
`Catalog.Default`, or uses `en`. Your application text lookup must handle that
fallback even when it has no validation catalog entry.

The middleware adds `Accept-Language` to `Vary`, and `Cookie` when cookie
selection is enabled. Error responses carry these headers too. Query selection
is part of the URL, not an additional `Vary` header. Ensure any proxy cache also
keys on the relevant query values; these headers do not make personalized
responses safe for shared caching.

## Translate Messages

Existing request validation renders violations with the request's catalog and
language. Pass `r.Context()` to schemas called directly rather than substituting
a background context. An existing signup route rejecting a short password can
produce this Spanish violation:

```json
{
  "code": "string.min",
  "params": {
    "min": 12
  },
  "message": "debe tener al menos 12 caracteres"
}
```

Only the message changes. Codes, params, paths, and the HTTP problem title stay
stable for programmatic clients. See
[translated validation errors](../validation/errors.md#translate-messages) for
the response boundary and `Errors.Localize`; this page does not replace your
schema or [diagnostic ownership policy](../validation/errors.md#diagnostic-ownership).

For an application rule with stable code `username.reserved` and public parameter
`name`, add its message before middleware construction:

```go
catalog.Locales["es"]["username.reserved"] = "{name} está reservado"
```

The parameter is inserted into the visible message. Never put credentials or
other private values in message params. Use application-specific code prefixes
to avoid future built-in collisions; [custom rules](../validation/custom-rules.md)
owns producing the violation.

Conditional messages can use specialized keys such as `required_if:eq`. A plain
`required_if` translation is sufficient when every condition should use the same
wording: it takes precedence over the built-in English variant. Add specialized
translations when the condition needs a more precise sentence.

## Handle Fallbacks

Message lookup differs from language negotiation. For an ordinary code it tries:

1. The request language, then its base language.
2. The catalog default, then its base language.
3. Built-in English.
4. The code itself if no template exists.

A regional catalog can therefore override a few messages while its base catalog
supplies the rest. Lookup uses stored spellings, not negotiation's case folding:
keep regional/base keys consistent, such as `es-MX` with `es`. A partly translated
response can contain default-language or English messages even though its
`Content-Language` reports the selected language.

A missing custom translation can appear as `"message": "username.reserved"`.
The code remains valid for clients, but the reader sees an identifier. Test
custom codes as well as built-in ones rather than assuming English knows every
application message. Relocalizing existing errors preserves their original
message when no template is known.

## Load Translations

For larger catalogs, keep each language in a JSON object mapping codes to
templates. For example, the application file `locales/es.json` can contain:

```json
{
  "required": "es obligatorio",
  "string.min": "debe tener al menos {min} caracteres"
}
```

Embed the files with the standard `embed` package at package scope:

```go
//go:embed locales/*.json
var translations embed.FS
```

At startup, decode each file with `encoding/json/v2` before registering middleware:

```go
data, err := translations.ReadFile("locales/es.json")
if err != nil {
	return err
}
messages := validate.Messages{}
if err := json.Unmarshal(data, &messages); err != nil {
	return err
}
catalog.Locales["es"] = messages
```

Fail startup on missing or malformed files instead of silently dropping a
language. Middleware owns its copied catalog, not later mutations to these maps.

## Check Responses

Send requests through your configured router, not only directly to a schema, to
verify language selection and response headers together. With `net/http/httptest`
and the `/greeting` route using the handler above:

```go
incoming := httptest.NewRequest(http.MethodGet, "/greeting?lang=es", nil)
incoming.Header.Set("Accept-Language", "en")
result := routes.Record(incoming)
```

With query selection enabled, expect `Content-Language: es` and body `Hola`
despite the English header. The caller owns `result.Body` and must close it after
reading. Also test an unsupported query falling through to cookie/header, header
weights, and a missing translation. Compare your supported rule codes with each
language's templates; `validate.English()` supplies built-in keys, not your custom
code inventory.

For selection settings and message APIs, see the
[`locale` reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/locale)
and [`validate` reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
