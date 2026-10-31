# Restate drivers

Use this module to run Arkoren schedules and workflows on Restate. Install it
with `go get arkoren.dev/driver/restate`.

- [Shared host API](https://pkg.go.dev/arkoren.dev/driver/restate)
- [Schedule usage](restateschedule/README.md)
- [Workflow usage and limits](restateworkflow/README.md)

Bind drivers before starting the shared host. Start opens the endpoint;
Serve handles requests and optional deployment registration. Stop the host
during application cleanup. Configure server identity keys for production:
AllowInsecure is an explicit local-development opt-out.

## Contributor tests

From the repository root, run `make check MODULE=driver/restate` and
`make integration MODULE=driver/restate`. Integration starts a disposable local
server and removes only its Compose project's containers and volumes.
Ports 18080 and 19070, plus the endpoint ports selected by the test fixtures,
must be free. Leave experimental persistent features disabled unless testing
their explicitly selected profile on a fresh disposable project.
