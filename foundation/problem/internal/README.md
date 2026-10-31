# problem/internal

Internal support code for the `arkoren.dev/foundation/problem` package.

## Scope

This package currently provides Accept-header parsing and quality negotiation
utilities used by the public `problem` package.

## Stability

This is toolkit-contributor documentation. Go's `internal` import restriction
prevents applications outside the problem module from importing these helpers,
even when a helper's name is exported. They may change without notice; consumers
use the public `arkoren.dev/foundation/problem` API instead.
