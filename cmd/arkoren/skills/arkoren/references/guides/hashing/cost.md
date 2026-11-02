---
title: Choosing Cost
nav: Cost
description: Measure a known hash policy, tune its work settings, and bound the resources available to password requests.
order: 30
summary:
  - Measure a fixed hash policy with three sequential fixture checks on representative hardware.
  - Tune resource settings against a deployment budget rather than a universal timing target.
  - Limit concurrent work, request rates, and input sizes while keeping stored records trusted.
---

# Choosing Cost

Password hashing makes guesses expensive, including legitimate login attempts.
Choose a hash policy that fits your latency, CPU, and memory budgets at expected
concurrency. A quick check on an idle laptop does not establish that a busy
production instance can sustain the same policy.

Start with the [driver defaults](drivers.md): Argon2id uses 64 MiB, three
iterations, and four lanes; bcrypt uses cost 12. These are starting policies,
not throughput or security guarantees for your deployment. This page assumes
you have selected a driver and can run a controlled test on representative
hardware under the same container resource limits as the service.

## Set Budgets

Decide how much of a login's latency budget can go to password verification,
and how much CPU and memory one instance can reserve for password work. Account
for normal traffic, bursts, account-hiding dummy checks, and verification-time
rehashes, which add a new hash after a successful check.

An attacker's offline guessing hardware can differ substantially from yours.
There is no elapsed-time result that proves a password is unguessable. Prefer a
measured policy your service can sustain over an arbitrarily high setting that
makes login unavailable.

## Measure Checks

Use a fixed, locally generated fixture, not a real password or imported record.
Add this bounded measurement test to your application's `_test.go` file. Import `testing`
and `time`, `hash` from `arkoren.dev/core/service/hash`, and `argon2hash` from
`arkoren.dev/driver/argon2/argon2hash`.

```go
func TestHashCost(t *testing.T) {
	if testing.Short() {
		t.Skip("measures password hashing cost")
	}
	hasher := hash.New(argon2hash.New())
	const password = "local measurement fixture"
	stored, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if err := t.Context().Err(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		matched, err := hasher.Check(password, stored)
		if err != nil || !matched {
			t.Fatalf("fixture verification failed: matched=%t, error=%v", matched, err)
		}
		t.Logf("check duration: %s", time.Since(start))
	}
}
```

This performs one setup hash and three sequential checks with known default
settings. The reported durations cover each facade check, not fixture creation
or account lookup. No password or encoded record is logged.

Run one uncached measurement explicitly:

```sh
go test -run '^TestHashCost$' -count=1 -parallel=1 -timeout=30s -v
```

The test timeout stops the test process; it is not a per-hash cancellation API.
Do not replace the fixed loop with an automatic search that repeatedly raises
cost, or measure arbitrary stored records supplied by users. Inspect variation
across the samples, then repeat a bounded run under representative service load.
Record the policy, hardware, limits, and observed durations so later comparisons
have meaning. Measure hash creation separately if registration or upgrades are a
significant part of your load.

## Tune Settings

For Argon2id, increasing `MemoryCost` reserves more memory per operation;
increasing `TimeCost` performs more iterations. Parallelism changes CPU scheduling
and does not multiply the configured memory budget by the lane count. Change
one setting at a time and repeat the bounded measurement before deployment.

For example, to test a measured candidate with four iterations and otherwise
default settings, replace the test's construction with:

```go
hasher := hash.New(argon2hash.New(func(policy *argon2hash.Builder) {
	policy.TimeCost(4)
}))
```

This is a candidate to measure, not a universal recommendation. For bcrypt,
use `bcrypthash` from `arkoren.dev/driver/bcrypt/bcrypthash` instead:

```go
hasher := hash.New(bcrypthash.New(func(policy *bcrypthash.Builder) {
	policy.Cost(13)
}))
```

A bcrypt cost increase of one approximately doubles computation; it is not
comparable numerically to an Argon2 iteration count. Keep settings controlled by
deployment configuration, not request fields. Constructor normalization does not
make extreme costs safe to run.

Changing the policy affects new hashes only. Existing checks use their stored
parameters; [upgrade them after verification](upgrading.md). `NeedsRehash`
reports any policy difference, including a lower target cost, rather than only
stronger settings. Do not automatically weaken stored hashes to handle a burst.
Use deliberately low work settings only for isolated behavior tests, with a
separate production construction path.

## Budget Concurrent Work

Argon2's memory cost applies to each in-flight operation. Four default-policy
operations require roughly 256 MiB of Argon2 working memory, before runtime and
application overhead. Reserve headroom rather than treating that arithmetic as
a complete process-memory estimate. bcrypt needs a CPU budget even though its
working-memory demand is smaller.

Use a shared admission limit across checks, registration hashes, password changes,
dummy checks, and rehashes on each instance. For a simple no-wait policy,
initialize a channel once at service construction:

```go
slots := make(chan struct{}, 4)
```

The capacity of four is illustrative; derive yours from the deployment budget.
Every password operation must use the same instance-wide gate. In the operation
handler, acquire a slot before doing the expensive work; `errors` is the
standard-library package:

```go
select {
case slots <- struct{}{}:
	defer func() {
		<-slots
	}()
default:
	return errors.New("password processing capacity exhausted")
}
```

The slot stays held until that handler returns. Map the capacity error to your
application's overload response, not a wrong-password result. Unlike a waiting
queue, this rejects excess work immediately and does not accumulate waiting
requests. A nil channel admits no work in this form; initialize it explicitly.

Neither hashing driver's operations accept a context. Once computation begins,
request cancellation cannot interrupt it. Check cancellation before admission
where useful, but do not start a goroutine per request and assume abandoning its
result stops the computation. A per-instance gate also does not coordinate
capacity across replicas.

## Bound Request Work

Apply [rate limits](../abuse-prevention/rate-limiting.md) before password work on
login, registration, password changes, and recovery endpoints that create hashes.
Concurrency limits cap in-flight work; rate limits control how frequently clients
can claim that capacity. Neither replaces request-body or password-length limits.

Bound request bodies before decoding, then enforce an application-chosen maximum
password byte length before hashing. bcrypt requires at most 72 bytes on both
creation and verification; never silently truncate. Argon2 still needs an input
limit even without that scheme-specific bound. Keep stored hashes in trusted
application storage: a valid format can encode work far above your current
policy, so format validation alone is not a resource limit.

For the settings and operations, see the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash),
[`argon2hash` API reference](https://pkg.go.dev/arkoren.dev/driver/argon2/argon2hash),
and [`bcrypthash` API reference](https://pkg.go.dev/arkoren.dev/driver/bcrypt/bcrypthash).
