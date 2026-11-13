---
title: Capability Access
description: Filter tool availability by caller capability while enforcing resource authorization in shared application operations.
order: 50
summary:
  - Offer tools by a shared capability rule without granting access to every resource.
  - Authorize selected records inside shared application service methods.
  - Add availability to tool copies and understand per-request checks and private failures.
---

# Capability Access

A plan may unlock shipment tracking without granting access to every shipment.
**Availability** decides whether a caller is offered an operation.
**Resource authorization** decides whether that caller may access the selected
record. Use a tool availability policy for the first decision and a shared
application service for the second.

Start with [typed authentication](securing-mcp.md#typed-authentication) already
storing your application user in context. Admission to `/mcp` is not permission
to use every tool or read every record. Reuse your existing
[Gate rules](../authorization/overview.md), not a separate MCP permission model.

The examples import standard `context`, `arkoren.dev/core/mcp`,
`arkoren.dev/core/auth`, and `arkoren.dev/core/auth/gate`.

## Capability Rules

Assume these are your application's identity and tracking request/result types:

```go
type User struct {
	ID    string
	Paid  bool
	Admin bool
}

type ReadShipment struct {
	ID string `json:"id"`
}

type Shipment struct {
	ID    string `json:"id"`
	State string `json:"state"`
}
```

An application-wide rule can permit tracking for paid accounts and administrators:

```go
func canTrack(ctx context.Context, user User) (bool, error) {
	return user.Paid || user.Admin, nil
}
```

Adapt that shared rule to the context-only callback MCP needs with `gate.Bind`:

```go
trackingAvailable := gate.Bind(canTrack)
```

`gate.Bind` looks up the `User` that admission attached to the identity and runs
the rules in order. Missing identity, or a user of another type, denies
availability without an error. The lookup must use the same concrete `User`
type as admission; `User` and `*User` are different types.
If a real plan lookup fails, return its error rather than turning an outage
into an ordinary denial. Policies must be observational and safe for concurrent
use: listing tools must not change business data. Pass context into backend
lookups.

## Resource Checks

The policy above receives no arguments, so it cannot authorize `input.ID`.
Place that check in the shared operation before loading the shipment. This
sample service accepts your existing loader and resource-rule factory:

```go
type ShipmentService struct {
	Load    func(context.Context, ReadShipment) (Shipment, error)
	CanRead func(string) func(context.Context, User) (bool, error)
}
```

Construct it with non-nil application collaborators. `CanRead(id)` must enforce
your tenant, ownership, or other resource rules; an ID alone is not evidence of
ownership. Its shared method authorizes before loading:

```go
func (service *ShipmentService) Read(ctx context.Context, input ReadShipment) (Shipment, error) {
	if err := gate.Authorize(ctx, service.CanRead(input.ID)); err != nil {
		return Shipment{}, err
	}
	return service.Load(ctx, input)
}
```

Gate rejects missing identity, denies a false rule, and propagates a rule error
before `Load` runs. Use this same method from HTTP and MCP; a check only in an
MCP wrapper leaves other callers able to bypass it. If plan entitlement is a
business requirement on every transport, enforce the shared capability rule
in that service path too. Discovery filtering is not a substitute.

With a configured `shipments *ShipmentService`, publish the operation:

```go
read := mcp.NewTool("shipments.read", shipments.Read, func(action *mcp.ToolBuilder[ReadShipment, Shipment]) {
	action.Description("Read the state of a shipment the caller may access.")
	action.ReadOnly(true)
	action.Available(trackingAvailable)
})
```

Availability controls discovery and execution; `shipments.Read` still owns the
selected-record decision. Add ID validation using the
[input-schema pattern](tools.md#input-rules). `ReadOnly` remains a hint, not a guard.

Omitting `Available` permits every admitted caller. Explicit `Available(nil)`
instead records a sticky declaration error, even if a later callback provides
a valid policy.

## Guarded Copies

Package factories such as `MCPTools(prefix)` can return definitions with their
own policies. Select only the tools your endpoint needs, then add application
availability without mutating the owner's definition. With `tool` as one
selected `mcp.Tool`:

```go
guarded := tool.WithAvailability(trackingAvailable)
```

The copy requires the original policy and then the added policy. Denial
short-circuits; neither policy runs during construction. The original tool
stays unchanged, and nil policy is invalid. Captured state remains shared.

This is still a capability gate, not a resource policy. For reusable libraries
and [arks](../core-concepts/arks.md), keep application user types, plan names,
and permission rules application-owned. Pass a shared authorized operation or
a domain authorization callback into your own integration rather than teaching
the library your authentication model.

## Request Evaluation

The adapter evaluates availability for every managed tool during `tools/list`.
It evaluates availability again on every call, before argument decoding and
validation. Guessing a hidden name cannot bypass this check, and a prior list
does not grant a durable permission. Register one static catalog; do not cache
per-user servers to implement discovery filtering.

Discovery returns the complete filtered catalog without pagination or a next
cursor. A nonempty cursor is rejected. Native SDK tools remain visible without
managed availability; see [Native Extensions](tools.md#native-extensions).

`Tool.Available(ctx)` can check policy directly without decoding or invoking
the handler. It validates the declaration and cancellation first, checks
cancellation again after successful policy evaluation, and caches nothing.
Core also checks cancellation after input parsing, before handler execution.

## Evaluation Failures

A `false, nil` policy result is an ordinary denial. Direct `Tool.Call` returns
`mcp.ErrUnavailable`; the endpoint returns a failed tool result with message
`tool unavailable`. A policy error preserves its cause under
`mcp.ErrAvailability` in core, but becomes `tool failed` on the wire, even if it
wraps `mcp.ToolError` or `mcp.ToolFailure`.

A policy error or panic during listing fails the entire request with
`tool listing failed`, not a partial catalog. Core propagates policy and handler
panics; the adapter recovers them. Original errors and panic values stay in
protected local logs. [Safe Failures](tools.md#safe-failures) explains the other
redaction boundaries.

Test that denied callers never reach decoding or the handler, and that a
permitted capability still cannot read another tenant's resource. Direct core
tests cover policy/service behavior; SDK HTTP tests cover discovery, identity
propagation, and safe responses.

## Shared LLM Policy

If your application also runs an LLM agent, reuse the typed handler, input
schema, and context policy in a separate `llm.NewTool` declaration. These are
shared application inputs, not converters between tool types. MCP lists
availability per request; LLM discovery is fixed once per run and execution
has its own checks. See [Offering Capabilities](../llm/defining-tools.md#offering-capabilities)
for that boundary.

For exact declaration and policy contracts, see the
[core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp) and
[MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver).
