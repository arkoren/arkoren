# Temporal drivers

Use this module to run Arkoren schedules and workflows on Temporal. Install it
with `go get arkoren.dev/driver/temporal`.

- [Shared host API](https://pkg.go.dev/arkoren.dev/driver/temporal)
- [Schedule usage](temporalschedule/README.md)
- [Workflow usage](temporalworkflow/README.md)

Register drivers before starting the shared host, and stop the host during
application cleanup. NewFrom borrows its native client: the caller remains
responsible for closing it. Worker callbacks must cooperate with cancellation;
a cleanup deadline bounds the caller's wait, not arbitrary application code.

## Contributor tests

From the repository root, run `make check MODULE=driver/temporal` and
`make integration MODULE=driver/temporal`. Integration starts a disposable local
development server and removes only its Compose project's containers and
volumes. Port 17233 must be free. The development server is not a production
deployment configuration.
