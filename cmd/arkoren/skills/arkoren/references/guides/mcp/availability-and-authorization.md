---
title: MCP Availability and Authorization
nav: Availability & Authorization
description: Offer capabilities by role or plan while keeping resource authorization in shared application services.
order: 50
---

# MCP Availability and Authorization

## Introduction

A paid plan may unlock invoice tools without granting access to every invoice.
**Availability** decides whether the caller is offered an operation;
**resource authorization** decides whether that caller can access the selected
record. Use `ToolBuilder.Available` for the first decision and your shared
application service for the second.

This guide assumes [authenticated HTTP admission](securing-mcp.md#require-your-typed-application-user)
already places your application `User` in request context. Endpoint admission
does not grant access to each tool or record. Reuse existing
[Gate rules](../authorization/overview.md), rather than introducing an MCP-only
permission model. `ReadOnly` and other annotations are hints, not guards.

Import `mcp` from `arkoren.dev/core/mcp`, `auth` from
`arkoren.dev/core/http/auth`, `gate` from `arkoren.dev/core/http/auth/gate`, and
`context` from the standard library. The sample `User` below stands for the
same concrete application identity type your HTTP admission already stores.

## Offer tools by capability

Use your application's identity and invoice types:

```go
type User struct {
	ID    string
	Paid  bool
	Admin bool
}

type ReadInvoice struct {
	ID string `json:"id"`
}

type Invoice struct {
	ID string `json:"id"`
}
```

Define a shared plan-or-admin rule:

```go
func canUseInvoices(ctx context.Context, user User) (bool, error) {
	return user.Paid || user.Admin, nil
}
```

Adapt the rule to a context-only availability policy:

```go
func invoicesAvailable(ctx context.Context) (bool, error) {
	user, ok := auth.FromContext[User](ctx)
	if !ok {
		return false, nil
	}
	return canUseInvoices(ctx, user)
}
```

Apply it to `read`, your shared invoice operation with signature
`func(context.Context, ReadInvoice) (Invoice, error)`:

```go
readTool := mcp.NewTool("invoices.read", read, func(definition *mcp.ToolBuilder[ReadInvoice, Invoice]) {
	definition.ReadOnly(true)
	definition.Available(invoicesAvailable)
})
```

Use the same application `User` type at admission and policy lookup. Missing
identity or a type mismatch (including `User` versus `*User`) denies availability
here. A plan lookup backed by a service can return
an error instead; propagate it, since an outage is not an ordinary denial.
The callback receives context but no arguments, so it cannot authorize an invoice
ID. Pass an already-authorized shared service operation as `read`.

Omitting `Available` offers a tool to everyone admitted to the endpoint.
Explicitly setting `Available(nil)` is different: it records a sticky declaration
error, even if another callback later supplies a valid policy.

When consuming a package's `MCPTools(prefix)` result, select only the tools this
endpoint needs. Add application capability policy without rebuilding or mutating
the owner's definition. Here `tool` is one selected `mcp.Tool` from that result:

```go
guarded := tool.WithAvailability(invoicesAvailable)
```

The returned copy requires both the original and added policies, leaving the
owner's definition unchanged. Neither policy runs during construction; nil is
invalid. This guards discovery and calls, but the shared typed application
method still needs its resource Gate checks.

## Authorize resources in the shared service

For the types above, an application-owned service function can check a
resource-specific Gate rule before calling the invoice client. Add this code to
the same package:

```go
func ReadAuthorized(
	ctx context.Context,
	input ReadInvoice,
	read func(context.Context, ReadInvoice) (Invoice, error),
	canRead func(string) func(context.Context, User) (bool, error),
) (Invoice, error) {
	if err := gate.Authorize(ctx, canRead(input.ID)); err != nil {
		return Invoice{}, err
	}
	return read(ctx, input)
}
```

Here `canRead(id)` is the application's existing Gate rule factory. It checks
tenant membership, ownership, or your other resource rules; the example does not
assume that invoice IDs identify their owners. The injected `read` operation can
be a client method such as `client.Read`. Gate rejects a missing authenticated
user, denies a false rule, and propagates a rule error before the client runs.

Place this authorization in the shared service path when both HTTP and MCP
expose the operation. Protecting only a tool wrapper leaves HTTP, jobs, or other
callers free to bypass it. Keep plan/role rules shared too when the same
capability must be required on every transport; discovery filtering alone is not
a substitute for those service requirements.

## Understand evaluation and failure

The adapter evaluates availability before returning managed entries from
`tools/list`, and `Tool.Call` evaluates it again before argument decoding or
validation on every call. Guessing a hidden tool name cannot bypass the check.
Keep names static and construct one endpoint for its registered catalog; do not
create a per-user server cache to filter discovery.

Discovery returns the complete filtered tool catalog without pagination or a
next cursor. Nonempty tool-list cursors are rejected. A policy error or panic
fails the entire discovery request with `tool listing failed`, rather than
returning a partial catalog. Native SDK tools have no managed availability
policy; see the [native boundary](tools.md#extend-through-the-native-sdk-deliberately).

For direct policy checks, `Tool.Available(ctx)` checks the declaration and
cancellation before evaluating the policy. After a successful policy evaluation,
it checks cancellation again. It neither parses input nor invokes the handler,
and it does not cache the result.

A `false, nil` result is a denial; `Tool.Call` returns `mcp.ErrUnavailable` for
that decision. A policy error instead preserves its original cause and wraps
`mcp.ErrAvailability`. Invalid declarations and already-canceled contexts fail
before policy execution. Calls also check cancellation after input parsing,
before the handler runs.

Policies must be observational and safe for concurrent use: listing a tool must
not mutate business data. Pass their context into backend lookups so
cancellation can stop work. Core propagates policy and handler panics; the
adapter recovers them.

On calls, denial becomes a failed tool result with `tool unavailable`; policy
errors and panics produce `tool failed`. Even a policy error wrapping
`mcp.ToolError` remains private. Original diagnostics stay in protected local
logs; see [safe errors](tools.md#set-hints-and-return-safe-failures).

## Reuse policy across MCP and LLM tools

If your application also runs its own LLM agent, reuse the typed handler, input
`validate` schema, and `invoicesAvailable` function in a separate `llm.NewTool`
declaration. These are shared application inputs, not tool converters; LLM core
does not import MCP. LLM discovery is fixed once per run, unlike MCP's per-list
evaluation. See [LLM capability availability](../llm/agents-and-tools.md#offer-tools-by-capability)
for execution checks and revocation behavior.

## Keep ark policy application-owned

If you are writing a reusable service or [ark](../core-concepts/arks.md), its client should not assume the
application's user type, permission names, paid-plan model, or administrator role.
Most applications only need the shared service wrapper above. A library that
also offers a tool factory can accept a domain authorization callback and typed
definition configuration. This optional factory is sample application code,
not a shipped Arkoren or ark API:

```go
func InvoiceTool(
	read func(context.Context, ReadInvoice) (Invoice, error),
	authorize func(context.Context, string) error,
	configure ...mcp.ToolBuilderFunc[ReadInvoice, Invoice],
) mcp.Tool {
	handle := func(ctx context.Context, input ReadInvoice) (Invoice, error) {
		if err := authorize(ctx, input.ID); err != nil {
			return Invoice{}, err
		}
		return read(ctx, input)
	}
	return mcp.NewTool("invoices.read", handle, configure...)
}
```

The application supplies `authorize` using its domain rules and supplies
`Available` through `configure`. The factory owns tool construction, not the
application's permission model. These callbacks remain shared and must be safe
for concurrent use. This alternative replaces `readTool`; do not register both
under `invoices.read` on the same endpoint. If the underlying shared service
already authorizes, prefer passing that service operation directly instead of
adding a second guard.

Use [direct tests](tools.md#test-behavior-before-transport) to prove that denied
callers never reach the handler, and adapter tests to check discovery and redaction.

For the exact declaration and call contracts, see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp)
and [MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver).
