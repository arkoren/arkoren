# Google Cloud drivers

Install with `go get arkoren.dev/driver/gcp` to use
[Secret Manager](gcpsecret/README.md) or
[reCAPTCHA Enterprise](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpcaptcha)
with Arkoren. Configure the selected service's credentials and permissions;
CAPTCHA assessment results still require application-owned acceptance policy.

## Contributor tests

From the repository root, run `make check MODULE=driver/gcp`.
Tests use local fakes and require no Google Cloud account or credentials.
