---
title: Cache Keys
description: Declare typed cache identities, reuse expiry policies, and preserve names safely across deployments.
order: 30
summary:
  - Declare exact typed identities and independent write-lifetime variants.
  - Reuse namespace prefixes and policies without implying storage isolation.
  - Preserve deployed names, protect sensitive identities, and reject invalid definitions.
---

# Cache Keys

Readers and writers must agree on which entry holds a product card and how long
a new copy should live. A **key** is an immutable definition of that entry's
exact name, Go value type, and write lifetime. A **namespace** supplies a reusable
literal prefix and policy for keys of one type. Neither definition allocates
storage, contacts a driver, or belongs to a particular cache facade.

Use `cache` from `arkoren.dev/core/service/cache` and standard-library `time` in
these examples. [Cache Overview](overview.md) owns executing operations with
the resulting keys.

## Typed Definitions

Suppose a catalog caches the public fields needed to render a product listing.
Declare the value shape separately from its storage identity:

```go
type ProductCard struct {
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
}
```

Choose a feature-owned name with a version when incompatible meanings need
separate entries:

```go
card := cache.NewKey[ProductCard]("catalog.v1:sku-482")
```

The resulting `cache.Key[ProductCard]` addresses exactly `catalog.v1:sku-482`.
`NewKey` does not split, escape, trim, normalize, or add separators. Even an
explicit empty name is valid. A zero-value key is not valid.

The type supplies compile-time guidance for facade calls, not a backend schema.
Two keys with different Go types or TTLs but the same completed name address the
same stored entry. If a schema change still decodes successfully, JSON decoding
cannot detect its changed meaning; version or invalidate that identity explicitly.

## Expiry Policies

Without configuration, a key has TTL zero: writes have no expiry. To expire a
product card five minutes after a write, declare that policy at construction:

```go
card := cache.NewKey[ProductCard]("catalog.v1:sku-482", func(entry *cache.KeyBuilder) {
	entry.TTL(5 * time.Minute)
})
```

This is an alternative to the preceding declaration. The TTL applies to `Put`,
successful `Add` and `CompareAndPut`, and a computing `Remember`. Reading through
a key never changes an entry's expiry. `card.TTL()` reports the policy, whereas
`store.TTL(ctx, card)` reports a stored entry's remaining lifetime.

No expiry is not a retention guarantee: eviction, deletion, or backend loss can
still remove the entry. [Counters](counters.md) have separate expiry rules, and
[leases](locks.md) take their own explicit duration.

If one write needs a shorter lifetime at the same physical name, derive another
definition:

```go
brief := card.Derive(func(entry *cache.KeyBuilder) {
	entry.TTL(30 * time.Second)
})
```

`brief` keeps `card`'s exact name; `card` retains its five-minute policy.
Derivation changes no stored data. The new policy matters only on a later write.
Deriving with `TTL(0)` similarly declares a no-expiry variant without modifying
the original definition.

## Shared Namespaces

When all catalog cards share a prefix and lifetime, declare the policy once:

```go
cards := cache.NewNamespace[ProductCard]("catalog.v1", func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(5 * time.Minute)
})
```

Without configuration, namespaces use separator `":"` and TTL zero. Obtain a
typed key by supplying a nonempty child name:

```go
card := cards.Key("sku-482")
```

`card.Name()` is exactly `catalog.v1:sku-482`; the dot in the prefix is untouched.
A callback on `Key` can override the inherited TTL for that definition. Child
names are strings: convert numeric IDs explicitly with standard-library `strconv`,
for example `cards.Key(strconv.Itoa(id))`.

Namespaces only produce ordinary keys. They do not partition storage, authorize
access, create a bulk-clear boundary, or isolate two entries with identical
completed names. `Name()`, `Separator()`, and `TTL()` reveal the declared prefix,
future join string, and write policy—not current backend state.

## Nested Policies

If archived product cards use a longer lifetime, create a subnamespace:

```go
archive := cards.Subnamespace("archive", func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(time.Hour)
})
archived := archive.Key("sku-482")
```

`archived.Name()` is `catalog.v1:archive:sku-482`. The child inherits its parent's
type, separator, and TTL before applying edits. Here the child uses one hour,
while `cards` keeps five minutes.

`Subnamespace` joins its own name with the parent's separator. A child separator
edit affects only later joins, not the already completed prefix. To keep the same
prefix but change future policy, derive a namespace instead:

```go
briefCards := cards.Derive(func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(30 * time.Second)
})
```

Future keys from `briefCards` inherit thirty seconds; existing keys and entries
remain unchanged. Changing a derived namespace's separator similarly affects
future joins, never existing prefix bytes. Published definitions are detached
from their construction drafts, so retaining a builder cannot mutate them.

## Identity Safety

Literal joining can collide. Prefix `a:b` with child `c` and prefix `a` with
child `b:c` both produce `a:b:c`. An explicit empty separator means concatenation,
not the default. Validate untrusted components against an allowed alphabet or
encode each component with an unambiguous application-owned scheme before
joining. A namespace is not an authorization boundary.

Reserve physical entry names starting with `lock:` for the facade, and logical
lease names starting with `remember:` for `Remember`. Its coordination entry is
`lock:remember:<completed-key>`. These are conventions in one backend key space;
an ordinary write to a completed lock name can overwrite a lease. Check the
completed identity after all namespace joins.

### Preserving Names

If a deployed namespace uses the former dot separator, preserve it explicitly:

```go
legacy := cache.NewNamespace[ProductCard]("catalog.v1", func(namespace *cache.NamespaceBuilder) {
	namespace.Separator(".")
})
```

`legacy.Key("sku-482")` addresses `catalog.v1.sku-482`, not the current default
`catalog.v1:sku-482`. Existing entries are not renamed or read through aliases.
Standalone `NewKey` names remain unchanged.

During a rollout, keep readers, writers, deletions, and `Remember` on the same
completed identity. `Remember` also derives its lease name from that identity.
For security-sensitive entries such as webhook receipts, changing identities
can admit replays: preserve old names for their required retention period or
plan an explicit migration. Built-in session records and rate-limit counters
already select `":"` explicitly and need no migration for this default change.

### Inspecting Identities

Owned `Key` and `Namespace` definitions redact themselves in ordinary formatting,
Go-syntax formatting, and structured logging. Unsupported or mismatched `fmt`
verbs such as `%d` also format a redacted marker rather than reflecting private
names; quoting, hexadecimal formatting, width, and precision never expose the
identity.

`Name()` deliberately returns the literal identity. That string, raw driver
arguments, and raw `Match` or `Entry` descriptors are not redacted. Do not log
them indiscriminately: identities can contain personal identifiers or secrets.
[Monitoring](monitoring.md#privacy-boundaries) explains the separate facade
telemetry boundary.

## Definition Errors

If policy comes from configuration, check the published definition before
serving requests:

```go
if err := cards.Err(); err != nil {
	return err
}
```

Keys expose the same `Err()` method. Zero-value keys and namespaces, empty
namespace names, and empty `Key` or `Subnamespace` child names wrap
`cache.ErrInvalidKey`. This does not prohibit the explicit empty standalone
identity accepted by `NewKey`. Negative TTLs wrap `cache.ErrInvalidTTL`.

The first declaration error survives derivation and child creation, even if a
later callback supplies a valid setting. Construct a fresh valid definition
instead of trying to repair descendants. Construction callbacks run once,
synchronously in order, even for invalid declarations; callback panics propagate
and a supplied nil callback panics. An expanded nil or empty callback slice
supplies no edits.

Facade operations reject invalid definitions before encoding, decoding, computing,
or driver I/O—even on a cache hit. An already-canceled operation context takes
precedence over definition errors. Raw driver access bypasses these definition
checks.

For every definition and option, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
