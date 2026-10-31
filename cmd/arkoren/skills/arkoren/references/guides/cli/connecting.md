---
title: Connecting the CLI
nav: Connecting
description: Select a trusted MCP endpoint, supply an existing credential, and verify discovery.
order: 20
---

# Connecting the CLI

## Introduction

Start with the [installed CLI](overview.md#installing-the-cli), an existing MCP
endpoint, and a credential accepted by that endpoint. The URL identifies the
MCP mount itself, such as `https://admin.example.com/mcp`, not a tool URL.
If you own the application but have no endpoint yet, [expose selected
diagnostics](../observability/exposing-diagnostics.md) first.

## Supplying an existing credential

Obtain a bearer API key, JWT, or OAuth access token through your application's
credential workflow or authorization server. This is not a separate inspection
secret. The CLI sends `Authorization: Bearer <token>`; it does not log in,
refresh tokens, manage cookies, or automatically configure mutual TLS.

Keep credentials out of version control. Prefer environment or trusted files
over `-token` when shell history or process arguments could reveal it. Use HTTPS
beyond loopback or a protected tunnel. A client token is not server
authentication configuration; the server independently verifies it and authorizes
tools. See [Securing MCP](../mcp/securing-mcp.md).

With your issued token already in `ACCESS_TOKEN`, explicitly select its intended
destination and verify the connection:

```sh
export ARKOREN_MCP_URL=https://admin.example.com/mcp
export ARKOREN_MCP_TOKEN="$ACCESS_TOKEN"
arkoren tools
```

`tools` initializes MCP and reads `tools/list` without invoking any tool,
including identity or health tools. Check that `inspector.state` is `ok`:
discovery can report a missing or unreachable endpoint without a nonzero exit.
Successful discovery includes the negotiated `protocol` and, when supplied,
the server implementation version in `arkoren`. It does not include tool-derived
application identity or identity-tool version overrides.

Inspect `command`, `description`, `kind`, and `follow` in the JSON `tools` array
to confirm the expected tools are available. These are advertised capabilities,
not authorization or read-only guarantees. An empty catalog can mean the caller
has no visible tools, not that the server has none.

## Requesting an application status report

Use `status` when you intend the endpoint's identity and health handlers to run,
not merely to verify discovery. It explicitly invokes the uniquely advertised
`identity` role, then the uniquely advertised `health` role, with `{}` inputs.
If discovery does not expose exactly one tool for a role, `status` skips that
role. Neither role guarantees read-only behavior; review the endpoint's tool
contracts before running the command:

```sh
arkoren status
```

The report includes the negotiated protocol and configuration sources without
printing the token. Identity can add application fields, process identity, and
an implementation-version override. An identity failure stops before health or
report output. A health failure appears in the report without by itself failing
the command, so `status` is not a readiness gate. A stdout write failure still
takes precedence and exits 1.

## Choosing configuration sources

URL and token each use the first nonempty value in this order:

| Setting | Flag | Environment | Current-directory file |
| --- | --- | --- | --- |
| Endpoint | `-url` | `ARKOREN_MCP_URL` | `ARKOREN_MCP_URL` in `./.env` |
| Credential | `-token` | `ARKOREN_MCP_TOKEN` | `ARKOREN_MCP_TOKEN` in `./.env` |

There is no default URL. Calling a tool or requesting `status` requires a URL;
`tools` instead reports `inspector.state: "not configured"` when none is selected.
URL userinfo (`user:password@host`), query strings, and fragments are rejected
before any request. Supply credentials through the token setting, never the URL.
Invalid-URL diagnostics omit the supplied value, including with `--verbose`.
Literal trailing slashes are normalized without decoding escaped path separators:
`/admin%2Fmcp` stays distinct from `/admin/mcp`.
Empty values do not clear lower-precedence values. `ARKOREN_INSPECT_TOKEN` is not
an alias.

The CLI reads only these two file keys, only in the current directory, never a
parent. The file may be absent. Its minimal syntax supports `KEY=VALUE`, an
optional `export ` prefix, quotes, and `#` comments. It performs no interpolation:
`$HOME` stays literal. Double quotes support `\n`, `\"`, and `\\` escapes.

Use `-env-file path` to replace `.env`; the named file must exist. Relative paths
are relative to the current directory, with no fallback to `.env`.
`-env-file ''` disables file loading, not environment or flag configuration.

## Trusting a file's destination

A project can control its `.env` URL. If that implicitly loaded URL would receive
a token selected from your environment or `-token`, the CLI refuses the command
with exit code 2 before any network request. It neither drops your token nor
substitutes a lower-precedence file token.

Pin the trusted destination with `-url` or `ARKOREN_MCP_URL`. Alternatively,
after reviewing the file, explicitly approve it for this run:

```sh
arkoren -env-file ./trusted-admin.env tools
```

This approves that file's URL for environment and flag tokens; precedence does
not change. Explicit `-env-file .env` is approval too. Do not add it blindly to
bypass a refusal in an untrusted checkout. Rotate a token if an older client
already sent it to an unintended destination.

A URL and token both selected from the same file work without extra approval.
An implicit file URL with no token also works. Keep paired local configuration
in a trusted, untracked file if that matches your workflow.

## Resolving connection failures

An actual HTTP 401 during connection or discovery exits 4: obtain or replace the
endpoint's credential. Missing tools or admission denial require checking server
policy, not guessing command names. Network, TLS, and protocol failures are
distinct from authentication;
[CLI reference](reference.md#exit-codes) describes their exit statuses.

All connecting commands, including remote help, enforce the trust check.
Bare `arkoren` and `arkoren --help` print offline root help and exit 0 without
reading configuration. `arkoren --version` prints only the installed CLI version,
also offline. `new` and `new --help` are offline too. Use `tools` for a
discovery-only connection check; remote help also discovers tools without
invoking them. Local help and version output do not establish a connection.

To reach a loopback admin listener on another machine, forward its port instead
of publishing it publicly:

```sh
ssh -L 9090:127.0.0.1:9090 user@host
```

While the tunnel is open, explicitly select `http://127.0.0.1:9090/mcp` and
supply the endpoint's existing credential. The tunnel does not replace endpoint
authentication.

Once discovery succeeds, [call a tool](calling-tools.md). For server-side
transport behavior, see the [MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver).
