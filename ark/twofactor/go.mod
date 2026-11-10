module arkoren.dev/ark/twofactor

go 1.27

// arkoren.dev/foundation/totp has no release yet, so it is deliberately absent
// from this list; add `require arkoren.dev/foundation/totp` at its first tag.
// Until then the module builds only inside the go.work workspace.
require (
	arkoren.dev/core v0.1.0
	arkoren.dev/driver/aes v0.1.0
	arkoren.dev/driver/bcrypt v0.1.0
	arkoren.dev/driver/mysql v0.1.0
	arkoren.dev/driver/postgres v0.1.0
	arkoren.dev/driver/sqlite v0.1.0
	arkoren.dev/foundation/problem v0.1.0
	arkoren.dev/foundation/query v0.1.0
)
