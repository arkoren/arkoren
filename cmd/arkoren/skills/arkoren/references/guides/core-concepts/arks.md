---
title: Arks
description: Distinguish reusable domain features from service mechanisms, backend drivers, and application policy.
order: 25
summary:
  - Understand arks as optional domain features, with no built-in arks currently available.
  - Separate reusable domain rules from service operations and backend communication.
  - Keep deployment choices and explicit feature integration under application ownership.
---

# Arks

Sending an email is a service operation. Deciding that a completed report needs
an email, who receives it, and what the message means is domain behavior. An
**ark** is an optional, reusable domain feature that combines Arkoren services
with those concepts and rules.

## Current Availability

Arkoren has no built-in arks yet. The feature below illustrates the boundary;
it is not an installable integration or a promised API.

An ark is an ordinary Go module that an application explicitly chooses and
connects. It is not an automatically discovered plugin. You can use Arkoren's
services directly in application-owned features without introducing an ark.

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
