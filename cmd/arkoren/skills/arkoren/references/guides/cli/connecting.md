---
title: Connecting
description: Approve an MCP destination, supply an existing bearer credential, and verify discovery.
order: 20
summary:
  - Pin a trusted MCP URL and supply an existing bearer credential before discovery.
  - Resolve flags, environment variables, and files without leaking inherited tokens to a checkout's URL.
  - Distinguish discovery from status calls and resolve authentication, TLS, and tunnel failures.
---

# Connecting

Before calling a tool, establish which server will receive your credential.
Start with the [installed CLI](overview.md#installing-arkoren), an existing MCP
endpoint, and a token that endpoint accepts. If you own the application but have
not exposed diagnostics, follow [Capture Setup](../diagnostics/capture-setup.md)
first.

## Select Destination

Use the MCP mount URL, such as `https://admin.example.com/mcp`, not a tool URL.
There is no default destination. The CLI accepts HTTP or HTTPS URLs with a host,
but rejects userinfo, query strings, and fragments before requesting anything.
Credentials belong in the token setting, never in the URL. Invalid-URL errors
omit the supplied value, including in verbose mode.

Keep private endpoints on loopback or a private network. Use HTTPS beyond
loopback or a protected tunnel. Server-side authentication, TLS, allowed Host
values, and Origin validation remain the endpoint owner's responsibility;
[Securing MCP](../mcp/securing-mcp.md) explains those boundaries. A terminal
client does not make browser-facing protections unnecessary.

Select the trusted destination explicitly before supplying a credential:

```sh
export ARKOREN_MCP_URL=https://admin.example.com/mcp
```

The CLI normalizes literal trailing slashes but preserves escaped path
separators: `/admin%2Fmcp` does not become `/admin/mcp`. It does not append tool
names or follow HTTP redirects.

## Verify Discovery

Obtain a bearer API key, JWT, or OAuth access token through your application's
existing credential workflow. With that value already in `ACCESS_TOKEN`, supply
it without putting the literal secret in a command argument:

```sh
export ARKOREN_MCP_TOKEN="$ACCESS_TOKEN"
```

The CLI sends `Authorization: Bearer <token>`. It does not log in, refresh tokens,
manage cookies, or automatically configure mutual TLS. Keep tokens out of
version control and shared logs; environment variables and trusted files still
need access protection. `--token` can expose a value in history or process
arguments.

Discover tools without invoking their handlers:

```sh
arkoren tools
```

Check that the JSON report has `inspector.state: "ok"`. A missing or unreachable
endpoint can produce a report with exit 0, so the exit code alone does not prove
discovery succeeded. Successful discovery includes the negotiated `protocol`
and any server-supplied implementation version in `arkoren`, not tool-derived
application identity.

The `tools` array lists commands, descriptions, `kind`, and `follow` support.
These are advertised hints, not authorization or read-only guarantees. An empty
catalog can mean this caller sees no tools. Remote tool help also performs only
discovery; it does not invoke identity, health, or other tools.

## Configuration Sources

URL and token independently select their first nonempty value:

| Setting | First | Second | Third |
| --- | --- | --- | --- |
| Endpoint | `--url` | `ARKOREN_MCP_URL` environment variable | `ARKOREN_MCP_URL` in `./.env` |
| Credential | `--token` | `ARKOREN_MCP_TOKEN` environment variable | `ARKOREN_MCP_TOKEN` in `./.env` |

Empty values do not clear lower-precedence values. `ARKOREN_INSPECT_TOKEN` is
not an alias. Without a URL, tool calls and `status` fail with exit 2; `tools`
instead reports `inspector.state: "not configured"`.

Only those two keys are read from the current directory's `.env`; parent
directories are not searched. The file may be absent. Its syntax supports
`KEY=VALUE`, an optional `export ` prefix, quoted values, and `#` comments.
There is no interpolation: `$HOME` stays literal. Double quotes support `\n`,
`\"`, and `\\` escapes.

`--env-file PATH` replaces `.env`, and the named file must exist. Relative paths
resolve from the working directory, without fallback to `.env`.
`--env-file ''` disables file loading, not environment variables or flags.

## Approve File URLs

A checkout can supply its own `.env`. If that implicitly loaded file's URL would
receive a token selected from your environment or `--token`, the CLI exits 2
before any network request. It does not discard the inherited token or fall
back to a file token.

Prefer pinning a trusted URL with `--url` or `ARKOREN_MCP_URL`. If you instead
want the file to choose the destination, review it before explicitly approving
it for this invocation:

```sh
arkoren --env-file ./trusted-admin.env tools
```

This permits the file's URL to receive an environment or flag token; it does not
change precedence. Explicit `--env-file .env` is approval too. Never add it
blindly to bypass a refusal in an untrusted checkout. If a previous client sent
a token to an unintended destination, rotate that token.

A URL and token both selected from the same file work without extra approval.
An implicit file URL without any token also works. Keep paired configuration in
a trusted, untracked file when using that arrangement.

## Request Status

Use `status` only when you intend application handlers to run. It invokes the
uniquely advertised `identity` role, then the uniquely advertised `health` role,
once each with `{}`. Missing or ambiguous roles are skipped. Review their
contracts first: a role does not guarantee read-only behavior.

```sh
arkoren status
```

The report includes configuration sources without printing the token. Identity
can add application and process details and override the reported server version.
An identity failure stops before health or report output. A health failure is
included in the report without itself failing the command; `status` is not a
readiness gate. Use a [condition wait](waiting-and-following.md#gate-readiness)
for that purpose.

## Resolve Failures

An actual HTTP 401 exits 4: obtain or replace the accepted credential. An empty
catalog or admission denial requires checking server policy, not guessing tool
names. TLS verification, networking, and protocol failures are separate from
authentication; consult [exit codes](reference.md#exit-codes).

For private HTTPS, configure the intended
[client trust](../http-networking/certificates.md#choose-client-trust). Do not
disable certificate verification to hide a trust mismatch. CLI help and version
output are offline and cannot verify TLS or endpoint availability.

If an admin listener is loopback-only on another machine, forward its port
instead of publishing it publicly:

```sh
ssh -L 9090:127.0.0.1:9090 user@host
```

While the tunnel is open, explicitly select `http://127.0.0.1:9090/mcp` and
supply the endpoint's existing credential. The tunnel does not replace endpoint
authentication. Once discovery succeeds, continue with [Calling Tools](calling-tools.md).
