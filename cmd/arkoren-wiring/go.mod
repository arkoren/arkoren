module arkoren.dev/cmd/arkoren-wiring

go 1.27

require (
	arkoren.dev/ark/access v0.0.0
	arkoren.dev/ark/oauth v0.3.0
	arkoren.dev/core v0.8.1-0.20261115223453-71b8e64b3621
	arkoren.dev/driver/aes v0.1.0
	arkoren.dev/driver/bcrypt v0.1.0
	arkoren.dev/driver/sqlite v0.1.0
	arkoren.dev/foundation/key v0.1.0
	arkoren.dev/foundation/totp v0.1.0
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-jose/go-jose/v4 v4.1.6
)

require (
	arkoren.dev/foundation/collection v0.1.0 // indirect
	arkoren.dev/foundation/problem v0.1.0 // indirect
	arkoren.dev/foundation/query v0.2.0 // indirect
	arkoren.dev/foundation/validate v0.1.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	modernc.org/libc v1.79.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.61.0 // indirect
)

// ark/access has no release until its owner bootstrap tags v0.1.0, so the
// development-only wiring module builds against the checkout. Replace this line
// with a real requirement on v0.1.0 once the tag exists.
replace arkoren.dev/ark/access => ../../ark/access
