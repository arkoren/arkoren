---
title: Localization
description: Choose the language of each request and translate validation messages, including your own rule codes, with catalogs that fall back safely.
order: 20
---

# Localization

## Introduction

A service with users in several countries should answer each of them in their
own language. In Arkoren, localization has two halves. The first decides which
language a request wants: the [locale middleware](locale.md)
reads a query parameter, a cookie, or the `Accept-Language` header and stores
the result in the request context. The second turns text into that language:
the `validate` package renders every validation message from a **catalog**, a
set of message templates per language.

Localization in Arkoren is deliberately this small. There is no general
translation function, no template engine integration, and no date or number
formatting. Validation messages are translated because Arkoren produces
them, and you cannot reach into its code to change them. Every other string
belongs to your application: your handlers can read the chosen language and
look up their own text in whatever way suits the service, whether that is a
`map[string]string` per language, a table in the database, or a third-party
i18n library.

Import `arkoren.dev/foundation/validate` and
`arkoren.dev/core/http/middleware/locale`. Registration snippets assume an
existing router `r`; handler snippets use `r` for the incoming request instead.
They also use `arkoren.dev/core/http/response` and `net/http`.

Focused snippets keep error results visible and assume success; application setup
must handle file-read and decoding errors before registering a catalog.

## How the language is chosen

The locale middleware runs once per request. It looks at three sources in
order and takes the first language your catalog supports: an optional query
parameter such as `?lang=es`, an optional cookie, and then the
`Accept-Language` header, ordered by the client's preferences. Query parameter
and cookie are off until you name them; the
[locale middleware page](locale.md#how-the-language-is-chosen)
shows how to enable them. The cookie is read-only here; your application sets
it and chooses its lifetime. There is no locale cookie-lifetime API.

Header weights use strict HTTP `q` syntax, not general floating-point syntax.
Malformed entries and `q=0` are skipped; equal weights preserve header order.
See [Accept-Language weights](locale.md#accept-language-weights) for the grammar
and examples.

The supported languages are the keys of the catalog's `Locales` map. A client
language matches a key after ignoring case, trimming surrounding whitespace,
and treating `_` as `-`, or by its base language. Thus `es-MX` matches `es` when
the catalog has no `es-MX`. The reverse does not
hold: a client asking for `pt` does not get a catalog entry named `pt-BR`, so
list the base language when you want it to match.

When nothing matches, the request gets the catalog's `Default` language, or
`en` if the catalog names none. In other words, a request is never left without
a language, and a client that sends `Accept-Language: fr` to a service without
French is answered in the default.

The middleware also sets `Content-Language` on the response, trimming whitespace
and replacing `_` with `-` without changing case. It adds
`Accept-Language` to `Vary`, plus `Cookie` when cookie detection is enabled,
so caches distinguish the request headers used to choose a language.
Error responses carry both headers too.

## Writing a catalog

A catalog is a `validate.Catalog`: a default language and the messages for each
language. The messages are a `validate.Messages` map from a violation **code**
to a template. Codes are the stable identifiers that already appear in error
responses, such as `required` or `string.min`; templates may contain
`{param}` placeholders that are replaced with the violation's params:

```go
func spanish() validate.Messages {
	return validate.Messages{
		"required":     "es obligatorio",
		"string.min":   "debe tener al menos {min} caracteres",
		"string.email": "debe ser un correo electrónico válido",
	}
}
```

The names in braces are the keys of the violation's `params`, the same ones a
client sees in the response. `string.min` carries `min`, `number.between`
carries `min` and `max`, and `eq_field` carries `field`. A placeholder with no
matching param is left in the text as written, so a typo in a template shows
up in the response instead of disappearing. Lists, such as the values of
`one_of`, are joined with commas, and times are written in RFC 3339.

To see the codes and params a rule produces, start from the English messages.
`validate.English()` returns a copy of the built-in templates that you can
read, print, or use as the base of a translation. The copy is yours to modify.

Templates are plain text with placeholders. There is no plural handling: the
messages say "at least 3 characters" and "at least 1 characters" alike.
When a language needs separate forms, phrase the template so that it does not
depend on the number, for example "minimum length: {min}".

> [!NOTE]
> Messages describe the field, not the sentence around it. A template does not
> contain the field's name: the JSON Pointer key of the response says which
> field failed, and clients show the message next to that input.

## Registering the catalog

A catalog does not need a translation for every code: missing templates fall
back to the default language or built-in English. Put your languages in the
`Locales` map and give the middleware the result:

```go
catalog := validate.Catalog{
	Default: "en",
	Locales: map[string]validate.Messages{
		"en": validate.English(),
		"es": spanish(),
	},
}

r.Use(locale.Middleware(func(language *locale.Builder) {
	language.Catalog(catalog)
}))
```

Register the middleware before the routes that validate, so the request
context carries the catalog and language when validation runs. Without it,
messages are English. The middleware copies the catalog and each message map;
finish loading and editing translations before registration. Later edits to
your maps do not change an already constructed middleware.

The final catalog is checked after all construction callbacks. Invalid
language-tag shapes or keys that normalize to the same spelling, such as
`en-US` and `EN_us`, panic. An empty catalog and an empty default remain valid;
the default need not have messages. These are basic shape checks, not full
BCP 47 validation. See [Validating configuration](locale.md#validating-configuration)
for the accepted shapes and cookie-name constraints.

### Keeping catalogs in files

Once a catalog has more than a handful of messages, keep each language in a
JSON file, embed the files in the binary, and decode them at startup. Because
`Messages` is a `map[string]string`, a file is only the map:

```json
{
  "required": "es obligatorio",
  "string.min": "debe tener al menos {min} caracteres"
}
```

Embedding makes the files part of the binary. Declare the embedded files at
package scope, using the `embed` package:

```go
//go:embed locales/*.json
var translations embed.FS
```

At startup, decode a file with `encoding/json/v2` and add its messages to the
existing `catalog`:

```go
data, err := translations.ReadFile("locales/es.json")
messages := validate.Messages{}
err = json.Unmarshal(data, &messages)
catalog.Locales["es"] = messages
```

Handle both errors before registering the catalog; fail startup if a file
cannot be read or parsed. A catalog that silently loses a
language is much harder to notice than a service that does not start.

## Translating validation messages

With the middleware and a catalog in place, nothing changes in the handlers.
`request.ParseJSON` renders each violation's message in the request's
language when it creates the violation. A client that sends
`Accept-Language: es-MX,es;q=0.9,en;q=0.5` and an invalid body receives the
Spanish messages, with `Content-Language: es`:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/request:validation-failed",
  "title": "Validation failed",
  "status": 422,
  "instance": "/signup",
  "errors": {
    "/email": [
      { "code": "string.email", "message": "debe ser un correo electrónico válido" }
    ],
    "/password": [
      {
        "code": "string.min",
        "params": { "min": 12 },
        "message": "debe tener al menos 12 caracteres"
      }
    ]
  }
}
```

Only the `message` changes. The `title` of the problem document and the
`code` of each violation stay the same in every language, because programs
branch on them, and clients that want to build their own text can do so from
the code and params.

The same holds when you call a schema yourself. `schema.Validate` and
`schema.Parse` render messages from the context they are given, so pass the
request's context, and convert the error with `request.ValidationProblem`
as described in [Validation](../validation/overview.md#handling-failures).

### Conditional rules

The conditional rules, such as `RequiredIf`, have a more specific message for
each kind of condition. English says "is required when delivery is shipping"
for an equality condition and a different sentence for each of the others. In
the catalog these variants are keyed `code:condition`, for example
`required_if:eq`, and a catalog may translate them one by one.

You do not have to. If your catalog translates only the plain code,
`required_if`, that one message is used for every condition, so a Spanish
catalog never shows a half-English sentence. Translate the variants when you
want the precise wording.

## Messages for your own rules

A custom rule reports a violation with a code of your choosing, as described in
[Custom rules](../validation/advanced.md#custom-checks). Add that code to every
catalog and the message is translated like a built-in one. The params you pass
to `validate.Fail` become the placeholders:

```go
return validate.Fail("username.reserved", map[string]any{"name": name})
```

For example, before registering the middleware, add the entry to your existing
Spanish message map:

```go
catalog.Locales["es"]["username.reserved"] = "{name} está reservado"
```

Prefix the codes of your own rules with the area they belong to, as the
built-in ones are, so they never collide with codes Arkoren adds later.

## When a translation is missing

A message is looked up in a fixed order, and the first template found is used:

1. the request's language, such as `es-MX`;
2. its base language, `es`;
3. the catalog's `Default`, and its base language;
4. the built-in English message;
5. the code itself.

Template lookup uses the stored catalog spellings, not negotiation's case
folding. Keep regional and base keys consistent, such as `es-MX` alongside `es`,
so a missing regional template can find its base-language translation.

Steps 1 and 2 let a regional catalog contain only the words that differ. An
`es-MX` entry can override `string.min` and leave the rest to `es`. Steps 3
and 4 mean a partly translated language still produces complete responses:
the untranslated codes appear in the default language, or in the built-in
English when the catalog has none.

The last step is the one to watch. A code that no catalog knows, such as a
custom rule you forgot to add, is shown to the client as the code itself, for
example `"message": "username.reserved"`. The response is still valid and the
`code` is still right, so clients keep working, but the user sees an
identifier instead of a sentence. Tests, below, are how you catch it.

## Using the language in your own code

The middleware stores the chosen language in the context, and
`locale.FromContext` reads it. It is the key for your own text:

```go
switch locale.FromContext(r.Context()) {
case "es":
	return response.String(w, http.StatusOK, "Hola")
default:
	return response.String(w, http.StatusOK, "Hello")
}
```

When a client language matches, the result is the catalog key with its original
spelling. The fallback uses that spelling too when the default has an exact
normalized key match; otherwise it is `Catalog.Default`, or `en` when no default
is set. The fallback
need not have an entry in `Locales`. Ensure your own text lookup
handles it too. For example, a catalog key `ES_mx` is returned as `ES_mx`, while
the response header is `Content-Language: ES-mx`. Without the middleware the
result is the empty string, so handle that case when a handler also runs
elsewhere, for example in tests that skip the middleware.

Arkoren does not translate or format anything else for you. Use the language
with `golang.org/x/text` or a similar library for dates, numbers, and plurals,
and keep your own strings in your own catalogs. To make a language selectable
from client preferences, add it to `Locales` as well as your own text tables.
The configured default remains a possible fallback even without a catalog entry.

If you have `validate.Errors` produced earlier, in a context that had no
language, `errs.Localize(ctx)` renders them again for the context you give it.
Violations whose code no catalog knows keep their original message.

## Testing

Send requests with different `Accept-Language` headers through a router that
has the middleware, as in [HTTP tests](../testing/http-tests.md), and check
the message and the `Content-Language` header. This fragment assumes a configured
`/signup` route requiring a 12-character password, a test's `t *testing.T`, and
imports `net/http/httptest` and `strings`:

```go
req := httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(`{"password": "short"}`))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Accept-Language", "es")

res := r.Record(req)
t.Cleanup(func() {
	if err := res.Body.Close(); err != nil {
		t.Error(err)
	}
})
if got := res.Header.Get("Content-Language"); got != "es" {
	t.Fatalf("Content-Language = %q, want es", got)
}
```

Also assert that the `/password` message reads
`debe tener al menos 12 caracteres`. A useful test walks the codes your service can produce, built-in and
custom, and checks that each language has a template for each of them, so a
missing translation fails the build instead of reaching a user. To compare
languages, take the keys of `validate.English()` for the built-in codes and
add your own list for the custom ones.

For the middleware's options and the full list of catalog functions, see the
[`locale` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/locale) and the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
