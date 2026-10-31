# AWS drivers

Install with `go get arkoren.dev/driver/aws` to use AWS services or compatible
S3 and SQS endpoints with Arkoren.

- [S3 storage](awsstorage/README.md)
- [SQS queues](awsqueue/README.md)
- [SNS and SQS broadcast](awsbroadcast/README.md)
- [SES mail](awsmail/README.md)
- [Secrets Manager](awssecret/README.md)

Configure credentials and permissions for the services you select. Each package
documents native-client ownership and provider limitations.

## Contributor tests

From the repository root, run `make check MODULE=driver/aws` and
`make integration MODULE=driver/aws`. Integration uses only disposable local
MiniStack and MinIO services and removes its own Compose project and volumes.
Ports 4567, 9001, and 14566–14569 must be free. Do not use the fixture credentials
in production. See [MinIO fixture provenance](fixtures/minio/README.md).
