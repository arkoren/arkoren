# Hatchet drivers

Use this module to run Arkoren schedules and workflows on Hatchet. Install it
with `go get arkoren.dev/driver/hatchet`.

- [Shared host API](https://pkg.go.dev/arkoren.dev/driver/hatchet)
- [Schedule API](https://pkg.go.dev/arkoren.dev/driver/hatchet/hatchetschedule)
- [Workflow usage and limits](hatchetworkflow/README.md)

Register drivers before starting the shared host, and stop the host during
application cleanup. Read the host API's lifecycle limits before deployment:
the pinned SDK does not guarantee caller-bounded startup or shutdown and has
a documented durable-wait shutdown race.

## Contributor tests

From the repository root, run `make check MODULE=driver/hatchet` and
`make integration MODULE=driver/hatchet`. Integration starts disposable local
services and removes only its Compose project's containers and volumes.
Ports 17077 and 18888 must be free. The seeded credentials in `internal/itest`
are for this disposable fixture only; do not use them in production.
