---
title: Locale Detection
nav: Locale
description: Pick each client's language from a query parameter, a cookie, or Accept-Language, so validation messages are rendered in it.
order: 25
---

# Locale Detection

## Introduction

When a service has users who speak different languages, each request needs to
know which language to answer in. The `locale` middleware works this out once
per request and stores the result in the request context, where the validation
package and your own code find it.

## Usage

Import `arkoren.dev/core/http/middleware/locale` and
`arkoren.dev/foundation/validate`. With an existing
[router](../http-fundamentals/routing.md) `r`, give the middleware a **catalog**:
the messages for each language you support.
The validation package provides English messages; `spanish` below is your
existing `validate.Messages` map. [Localization](localization.md#writing-a-catalog)
shows how to write one. Register the middleware before routes that validate input:

```go
catalog := validate.Catalog{
	Default: "en",
	Locales: map[string]validate.Messages{
		"en": validate.English(),
		"es": spanish,
	},
}

r.Use(locale.Middleware(func(language *locale.Builder) {
	language.Catalog(catalog)
}))
```

Validation uses the selected language and the catalog's fallback messages.
For example, `Accept-Language: es` selects the `es` catalog; a missing Spanish
template falls back as described in
[Localization](localization.md#when-a-translation-is-missing).

## How the language is chosen

The middleware looks at three sources, in order, and takes the first language
the catalog supports:

1. a query parameter, when you set `QueryParam("lang")` on the locale builder;
2. a cookie, when you set `Cookie("language")` on the locale builder;
3. the `Accept-Language` header, in the order of the client's preferences.

A language matches a catalog entry exactly after ignoring case, trimming
surrounding whitespace, and treating `_` as `-`, or by its base language:
a client asking for `es-MX` gets `es` when there is no `es-MX`. The reverse does
not hold: `es` does not select an `es-MX` entry. When nothing matches, the
catalog's default is used, or `en` if no default is set.
Neither query nor cookie detection is enabled by default.

The query parameter is useful for links that force a language, and the cookie
for remembering a choice the user made in your interface. To enable both on
the catalog above, add these settings in its registration callback:

```go
language.QueryParam("lang")
language.Cookie("language")
```

An absent or unsupported query value falls through to the cookie, then the
header. Empty selector names disable that source. `Cookie` only reads a cookie:
your application writes it and chooses its security attributes and lifetime.
The locale middleware has no cookie-lifetime or duration API.

### Accept-Language weights

Header entries are tried in descending `q` weight; equal weights keep their
original header order. An omitted weight is `1`. For example,
`fr;q=0.5, es;q=0.9, en;q=0.9` tries `es`, then `en`, then `fr`.

Weights use HTTP decimal syntax: `0` or `1`, optionally followed by a decimal
point and at most three digits. Only zeros may follow `1`. Thus `0.9` and
`1.000` are valid; `.9`, `+0.9`, `1.001`, `0.1234`, exponent or hexadecimal
forms, `NaN`, and infinities are not. A missing value (`q=` or a bare `q`) or
duplicate `q` parameter invalidates that entry. Malformed language tags,
wildcards, invalid weights, and `q=0` entries are skipped, not rejected as a
request error. Skipped entries do not veto the configured fallback language.

## Validating configuration

Construction callbacks run once, in order. After all callbacks finish, the
middleware validates the final catalog and any nonempty cookie name. A later
callback can replace an earlier invalid value before validation.

Catalog keys and a nonempty `Default` must have basic ASCII language-tag shape:
one to eight letters for the first component, then optional subtags of one to
eight letters or digits, separated by `-` or `_`. Surrounding ASCII whitespace
is ignored for matching. This is a shape check, not full BCP 47 validation or a
language-registry lookup. Invalid tags panic during construction, as do two keys
that normalize to the same spelling, such as `en-US` and `EN_us`. A nonempty
cookie name must pass `net/http.Cookie.Valid`; an invalid name also panics.

An empty catalog remains valid. An empty default selects `en`, and the default
need not name an entry in `Locales`. The catalog and each message map are copied;
finish loading translations before construction. Later map edits or changes to
a retained builder do not reconfigure published middleware.

## Reading the locale

In a handler, `locale.FromContext` returns the chosen language, for your own
text:

```go
lang := locale.FromContext(r.Context()) // "es"
```

Here `r` is the incoming `*http.Request`. Without middleware, `FromContext`
returns an empty string; handle that case if the handler also runs independently.

A match preserves the catalog key's original spelling for translation lookup.
For example, the key `ES_mx` is returned unchanged for a client asking for
`es-MX`. The fallback also uses the original key when the default has an exact
normalized key match; otherwise it preserves `Catalog.Default`, or uses `en`
when that is empty.

## Response headers

The middleware sets `Content-Language`, trimming surrounding whitespace and
replacing underscores with hyphens without changing case: the context value
`ES_mx` produces `Content-Language: ES-mx`. It adds `Accept-Language` to `Vary`.
When cookie detection is enabled, it also adds `Cookie` to `Vary`. This tells
caches which request headers can change the language. Error responses carry
the headers as well.

For every option, see the [`locale` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/locale).
