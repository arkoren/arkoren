# MinIO integration fixture

Contributor-only: `driver/aws/compose.yaml` builds this disposable S3 fixture
because Docker Hub no longer serves the pinned `minio/minio` image. The fixture
keeps MinIO `RELEASE.2025-09-07T16-13-09Z`; it does not use a mirror or AIStor.
Do not use this image or its test credentials in production.

## Build provenance

The [Dockerfile](Dockerfile) pins the official Docker Library
[`alpine:3.22.7`](https://hub.docker.com/_/alpine) multi-platform index at
`sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f`.
Alpine supplies the CA bundle, `wget`, and `sha256sum`; no packages are installed.

MinIO binaries come only from the official
[GitHub release](https://github.com/minio/minio/releases/tag/RELEASE.2025-09-07T16-13-09Z).
The [release API](https://api.github.com/repos/minio/minio/releases/tags/RELEASE.2025-09-07T16-13-09Z)
reports the following SHA-256 digests for assets uploaded on April 24, 2026:

| Platform | Asset | SHA-256 |
| --- | --- | --- |
| `linux/amd64` | `minio.linux-amd64.RELEASE.2025-09-07T16-13-09Z` | `7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f` |
| `linux/arm64` | `minio.linux-arm64.RELEASE.2025-09-07T16-13-09Z` | `5c83cd2cf151717ba0243f73e1c7802ff36e272b67144bdd7f1f7d684fd6f03d` |

The release is not marked immutable. The build verifies the selected binary
against the digest in the Dockerfile before making it executable. An unsupported
target architecture or a digest mismatch fails the build; do not bypass that
check if upstream replaces an asset. No binary is stored in this checkout.

## Run the fixture

Use Docker Compose with BuildKit. From `driver/aws`, the normal
`docker compose up -d --wait` builds the image when it is missing. Compose keeps
the existing `server /data` command, credentials, and loopback port `9001`.
Its health check uses Alpine's `wget` against `/minio/health/live` on port `9000`
inside the container; the image does not require `curl`.

To rebuild only this fixture, run from `driver/aws`:

```sh
docker compose build --no-cache minio-storage
```

A successful build prints `/usr/local/bin/minio: OK`. To run the complete AWS
integration suite with automatic teardown, use `make integration MODULE=driver/aws`
from the repository root. The target creates a unique disposable Compose project
unless you set `COMPOSE_PROJECT_NAME`; do not reuse a project whose data you need.
