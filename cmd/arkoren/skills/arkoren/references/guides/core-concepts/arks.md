---
title: Arks
description: Distinguish reusable domain features from service mechanisms, backend drivers, and application policy.
order: 25
summary:
  - Understand arks as optional modules that ship behavior, and see which are available.
  - Separate reusable domain rules from service operations and backend communication.
  - Keep deployment choices and explicit feature integration under application ownership.
---

# Arks

Sending an email is a service operation. Deciding that a completed report needs
an email, who receives it, and what the message means is domain behavior. An
**ark** is an optional module that ships behavior: a reusable feature that
combines Arkoren services with those concepts and rules, or a protocol client or
server built on core. A **driver**, by contrast, only implements a core service
contract for one backend.

## Current Availability

Three arks exist. Each has its own group in the Arks section of these guides:

| Ark | Ships |
| --- | --- |
| `arkoren.dev/ark/iam` | Registration, login, two-factor, API tokens, social connections, permissions and roles, and their routes, with their tables. See [IAM](../iam/overview.md). |
| `arkoren.dev/ark/oauth` | An OAuth 2 and OpenID Connect client for browser and native login and for verifying bearer tokens. See [OAuth](../oauth/overview.md). |
| `arkoren.dev/ark/mcp` | An MCP server over HTTP for the tools you define with core. See [MCP Server](../mcp-server/first-server.md). |

The report feature below illustrates the boundary; it is not an installable
integration or a promised API.

An ark is an ordinary Go module that an application explicitly chooses and
connects. It is not an automatically discovered plugin. An ark that owns tables
ships migrations that your application runs, and keeps user data by subject so
that forgetting a user removes it. You can use Arkoren's services directly in
application-owned features without introducing an ark.

## Domain Boundaries

A [facade](facades-and-drivers.md) exposes a service operation, such as storing
a file or submitting mail. Its driver performs the backend work. Neither needs
to decide whether a report is ready for delivery or which recipients are
entitled to receive it.

An ark adds that domain meaning. A reusable report-delivery feature might
define a delivery request, track its status, and prepare notification content.
It could use storage, queues, and mail without making those services aware of
reports.

| Responsibility | Owner | Report Example |
| --- | --- | --- |
| Portable service behavior | Facade and contract | Submit a message through the mail service. |
| Backend communication | Driver | Submit that message to a particular mail provider. |
| Reusable domain rules | Ark | Notify recipients when a report becomes ready. |
| Deployment and application policy | Application | Choose the provider and authorize delivery requests. |

The distinction is the meaning of the behavior, not its size. A component does
not become an ark merely because it uses several services. Conversely, a small
feature can be domain-specific if it defines what a report, membership, or
subscription means.

## Feature Composition

In the hypothetical report feature, your application supplies configured
services and connects the feature to its entry points:

```text
Application: authorizes delivery and supplies configured services
  -> Report feature: tracks readiness and prepares notification content
       -> Storage facade -> Selected storage driver
       -> Mail facade    -> Selected mail driver
```

The feature owns its domain rules. The application still chooses backends,
credentials, exposure, and resource ownership. When a feature needs tables,
routes, or background work, those are explicit integration responsibilities,
not a reason to hide construction or startup inside ordinary service calls.

Using portable [contracts](contracts.md) lets a domain feature work through
selected service implementations. It does not remove provider constraints:
an in-memory backend still loses state on restart, and unsupported operations
still need an explicit decision.

For your own features, use typed dependencies and explicit registration as
described in [Application Composition](../application/composition.md). That
composition pattern applies whether the feature lives in your application or
is eventually shared as a separate module.
