package twofactor_test

import (
	"time"

	"arkoren.dev/ark/twofactor"
	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/core/service/schedule"
)

// Example wires the ark into an application: construct it from the session
// identity, run its migrations, mount the handlers and schedule the sweep.
func Example() {
	var (
		identity  auth.SessionIdentity[int64, account]
		db        *database.Database
		cipher    *crypto.Encrypter
		hasher    *hash.Hasher
		migrator  *migrate.Migrator
		scheduler *schedule.Scheduler
		web       *router.Router
	)

	factors, err := twofactor.New(identity, db, cipher, hasher, func(ark *twofactor.Builder) {
		ark.Issuer("Example")
	})
	if err != nil {
		return
	}

	_ = migrator.Add(factors.Migrations()...)
	_ = scheduler.Add(factors.Sweep(schedule.UTC().Hour(3)))

	web.Post("/two-factor/verify", factors.Verify(identity))
	web.Post("/two-factor/recover", factors.Recover(identity))
	web.With(identity.RequireFresh(10*time.Minute)).Post("/two-factor/enroll", factors.Enroll())
	web.With(identity.RequireFresh(10*time.Minute)).Delete("/two-factor", factors.Disable())
	web.Post("/two-factor/confirm", factors.Confirm())
}
