# Disposable S3 integration fixture, not a production MinIO distribution.
# Build from driver/aws with: docker compose build minio-storage
# Pin the official Alpine multi-platform index so base contents cannot drift.
FROM alpine:3.22.7@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f

# BuildKit supplies the target architecture; only upstream binaries with reviewed
# SHA-256 digests are accepted. See README.md for release and digest provenance.
ARG TARGETARCH
RUN set -eu; \
    case "${TARGETARCH}" in \
        amd64) checksum=7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f ;; \
        arm64) checksum=5c83cd2cf151717ba0243f73e1c7802ff36e272b67144bdd7f1f7d684fd6f03d ;; \
        *) printf 'Unsupported MinIO fixture architecture: %s\n' "${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    release=RELEASE.2025-09-07T16-13-09Z; \
    wget -q -O /usr/local/bin/minio \
        "https://github.com/minio/minio/releases/download/${release}/minio.linux-${TARGETARCH}.${release}"; \
    printf '%s  /usr/local/bin/minio\n' "${checksum}" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/minio

# Compose supplies `server /data`; retaining the binary entrypoint also permits
# `docker compose run --rm --no-deps minio-storage --version` for version checks.
ENTRYPOINT ["minio"]
