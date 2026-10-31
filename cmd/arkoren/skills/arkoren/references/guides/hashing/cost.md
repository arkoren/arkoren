---
title: Choosing the Cost
nav: Cost
description: Pick password-hashing settings by measuring them on production hardware, bound memory use, and protect login routes from floods.
order: 20
---

# Choosing the Cost

## Introduction

Making a password guess expensive also makes a legitimate login expensive.
The **cost** settings control that work: CPU time for bcrypt, and time and
memory for Argon2id. Choose the highest cost your servers can sustain at the
expected concurrency, not just the highest cost one login can tolerate.

There is no single right number, because it depends on your hardware and your
traffic. This page shows how to pick one: set a target, measure it where the
service runs, account for concurrent logins, and keep clients from turning
the cost against you.

## A target to aim for

A good target is a check that takes around a quarter of a second on your
production hardware. This adds a modest delay to login while making each
offline password guess more expensive; an attacker's hardware can differ
substantially from yours. The defaults of both drivers, Argon2id with 64 MiB and three
iterations, or bcrypt with cost 12, are safe starting points that follow
OWASP's guidance; measure them on your servers before raising them.

Raise the cost when your hardware allows it, never below the defaults outside
tests. Lowering it later is possible, but every stored hash made with the
higher cost then [needs rehashing](upgrading.md), because `NeedsRehash`
reports any cost difference from the current settings, not only an increase.

## Measuring on your hardware

Import `hash` from `arkoren.dev/core/service/hash`, `argon2hash` from
`arkoren.dev/driver/argon2/argon2hash`, and standard-library `testing` and `time`
in your application's `_test.go` file. The concurrency wrapper below additionally
uses standard-library `context`.

Measure on the machines the service runs on, not on a laptop, whose processor
and memory differ. This test times one check with the default Argon2id policy.
If you plan to change it, add only those
[driver settings](drivers.md#configuration) to `argon2hash.New`:

```go
func TestHashCost(t *testing.T) {
	if testing.Short() {
		t.Skip("measures real hashing cost")
	}

	hasher := hash.New(argon2hash.New())
	stored, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	ok, err := hasher.Check("correct horse battery staple", stored)
	if err != nil || !ok {
		t.Fatalf("verify benchmark password: %v, %v", ok, err)
	}
	t.Logf("one check took %s", time.Since(start))
}
```

Run it with `go test -run TestHashCost -v` in the same container image and on
the same instance type as production. Run it a few times and read the slowest
result, since the first run also pays for warming up.

## Memory and concurrent logins

Argon2id's memory cost is per hash. With the default settings, each hash uses
64 MiB while it runs, so ten concurrent logins need 640 MiB. That is what makes
the function expensive to attack on graphics cards, and also what an attacker
can exploit to exhaust your memory with a flood of login attempts.

Decide how many hashes may run at once on one instance, and enforce it. A
buffered channel is enough:

```go
type Passwords struct {
	hasher *hash.Hasher
	slots  chan struct{}
}

func (passwords *Passwords) Check(ctx context.Context, attempt string, stored []byte) (bool, error) {
	select {
	case passwords.slots <- struct{}{}:
		defer func() {
			<-passwords.slots
		}()
	case <-ctx.Done():
		return false, ctx.Err()
	}

	return passwords.hasher.Check(attempt, stored)
}
```

Initialize `slots` with `make(chan struct{}, 8)` when you construct `Passwords`;
a nil channel never admits a check. With the default settings, eight checks
need about 512 MiB for Argon2's working memory, in addition to application
overhead. This bound covers only calls through this wrapper; use the same
budget for sign-up hashes and rehashes too. Additional requests wait for a slot
and give up when their context ends. This bounds concurrent
hash work, not the number of waiting requests; keep the route-level rate limits
below as well. bcrypt uses little working memory by comparison, so use a
concurrency limit when its CPU time is the concern.

Once admitted, neither driver's hashing operation accepts a context, so
cancellation cannot interrupt a hash already in progress. A waiting request's
context only bounds its wait for a slot.

## Protecting login routes

Every request to a route that hashes costs you the full price of a hash. Always
put a [rate limit](../abuse-prevention/rate-limiting.md) on login, sign-up, and
password-change routes, so that no client can make the service hash without
pause. A stricter limit on those routes than on the rest of the service is
usual, as [Rate Limiting](../abuse-prevention/rate-limiting.md)
shows.

> [!WARNING]
> Limit the length of passwords you accept, for example to 256 characters with
> [validation](../validation/overview.md). Hashing a multi-megabyte "password" is
> an easy way to burn CPU on your servers. For bcrypt the limit is lower, as
> described in [Hashing Drivers](drivers.md#bcrypt).

For the settings of each algorithm, see [Hashing Drivers](drivers.md), and for
the facade, see the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
