# Cloudflare drivers

Install with `go get arkoren.dev/driver/cloudflare` to use
[Queues](cloudflarequeue/README.md) or
[Turnstile verification](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarecaptcha)
with Arkoren. Provision queues and HTTP pull consumers before consuming jobs.
Turnstile verification fails closed; applications own token extraction and
hostname and action policy.

## Contributor tests

From the repository root, run `make check MODULE=driver/cloudflare`.
Tests use local HTTP fakes and require no Cloudflare account or credentials.
