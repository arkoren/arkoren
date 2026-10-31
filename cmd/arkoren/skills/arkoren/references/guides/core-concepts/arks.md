---
title: Arks
description: What an ark is, how it differs from core packages and drivers, and the availability of built-in arks.
order: 25
---

# Arks

## Introduction

A signup feature needs more than a database connection and a way to send mail.
It also needs to define a user, decide when registration succeeds, and choose
what a welcome message says. An **ark** is an optional, reusable domain feature
that combines Arkoren packages with those application-level concepts and rules.

An ark is an ordinary Go module, not an automatically discovered plugin. You
choose whether to add it and explicitly connect it to your application. Arkoren
has no built-in arks yet. The signup example below
illustrates the concept; it is not a module you can install today.

## Distinguishing arks from core and drivers

Core packages provide mechanisms without deciding your application's domain.
A database facade executes queries, and a mail facade sends messages. Neither
decides what a user is or when that user should receive a welcome email. A
[driver](facades-and-drivers.md) implements a service contract for a backend,
such as PostgreSQL or an email provider.

An ark supplies a domain feature built from those mechanisms. It can define
domain entities and rules, coordinate a workflow, and provide its own tables,
routes, jobs, or message content when the feature needs them.

| Part | Responsibility | Example |
| --- | --- | --- |
| Core | Reusable mechanisms and service contracts | Database access, mail delivery, HTTP routing. |
| Driver | Backend implementation of a contract, or a provider-specific adapter | PostgreSQL database access or provider webhook verification. |
| Ark | Reusable domain concepts, rules, and flows | A hypothetical signup feature that creates users and sends welcome mail. |
| Application | Composition, configuration, and application-specific policy | Choosing backends and deciding who can invoke signup. |

Domain assumptions are the distinction, not package size or the number of
dependencies. A core service can combine other services without becoming an ark.
It becomes domain-specific when it defines concepts such as users, teams, plans,
or an application's signup and checkout rules.

## Following a feature through the layers

Consider a hypothetical signup ark. Your application supplies database and mail
facades. The ark uses them to implement registration; their drivers provide the
backend operations:

```text
Application: chooses backends, wires dependencies, and authorizes requests
  -> Signup ark: applies registration rules and prepares welcome mail
       -> Database facade -> Database driver
       -> Mail facade     -> Mail driver
```

The ark owns the meaning of registration. The drivers know how to store data
or submit mail, not why a user is registering. Keeping those responsibilities
separate lets the domain feature use the selected backends through their
[contracts](contracts.md). Provider-specific capabilities and limits still need
explicit handling; a domain feature does not remove them.
