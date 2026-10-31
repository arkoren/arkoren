# Arkoren Session

`arkoren.dev/core/http/session` is a package in the `arkoren.dev/core` module. It owns
session state and the HTTP middleware (`session.Middleware`,
`session.FromContext`). Handlers read the session with `request.Session` and
`request.MustSession` from `arkoren.dev/core/http/request`.

Sessions are stored as JSON records in a `*cache.Cache`, so every cache
driver works as a session store.

Given an existing `store` (`*cache.Cache` from `arkoren.dev/core/service/cache`)
and `r` router, keep sessions for one hour. Import `time` for the duration:

```go
r.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.TTL(time.Hour)
	cookies.SameSite("strict")
}))
```

The default middleware needs no context-selection settings. For a custom context
slot, allocate one `session.NewHandle()` and share it through
`session.Builder.Handle`, `auth.SessionBuilder.Handle`, and
`request.WithSessionHandle`. Copies select the same slot; separate constructor
calls select different slots. The zero handle selects the default slot. Handles
do not isolate cookies, cache records, or authorization; independent sessions also
need appropriate cookie and storage settings. See the
[`auth` reference](https://pkg.go.dev/arkoren.dev/core/http/auth)
for coordinated middleware, identity, and handler wiring.

The default cookie is `__Host-arkoren.session` (plain `arkoren.session` with `Builder.AllowInsecure`, a
domain, or a non-`/` path); upgrading from the old default logs existing sessions out once.

Use a shared `auth.NewSessionIdentity(loadUser)` for authentication: its checked
`Login` and `Logout` methods rotate automatically while retaining unrelated data.
Call `Session.Regenerate` for other privilege changes to prevent session
fixation. Call `Session.Destroy` to remove all data and retire the old ID. Persistence
occurs before response headers, not when these methods return; monitor
`Builder.ErrorHandler` or persistence events for storage failures.
Loaded-session persistence uses the cache driver's atomic `CompareAndPut`, so a
pending save cannot overwrite successful retirement, even after its lease expires
or when it proposes a regenerated ID. Upgrade all writers and drain older
requests before relying on this guarantee; stored keys and JSON do not change.

`New` copies only the outer input map; values passed to `New` or `Put` remain
borrowed. A runtime `jsontext.Value` must match its field's Go type and cannot
impersonate persisted JSON. After a real save and reload, typed decoding uses
the existing JSON representation, with no new field tags or storage wrappers.
Existing nil interface values round-trip; they are not missing fields.

`MaxLifetime` caps initial expiry, renewal, and application `Extend` calls at the
original creation deadline. Reaching that deadline discards a later save and
cookie. A negative maximum still disables the cap. Best-effort lease release
has a separate ten-second context deadline; backends must honor that context.
Zero selects default durations. Negative idle TTL or `PersistenceTimeout` fails
middleware construction; negative maximum lifetime and renewal windows remain
valid disabled settings.

Session events report registered route patterns, not expanded URL paths; `Path`
is empty without a matched pattern. Keep route literals free of secrets. The
configured error handler still receives the original storage error for explicit
application handling; events do not carry that error or its text.

The cache driver serializes session data but does not encrypt
it; do not store secrets unless the values or backend are protected.

Cookie-authenticated state-changing routes also need
[CSRF protection](../middleware/csrf/README.md).

Use the [memory cache driver](../../service/cache/memorycache/README.md) in tests.
