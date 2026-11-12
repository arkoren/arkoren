package twofactor_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/ark/twofactor"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
)

// TestPingBeforeMigratingNamesMigrations checks readiness before table creation:
// the error must wrap ErrNotFound, name both tables and name Migrations().
func TestPingBeforeMigratingNamesMigrations(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		factors := newUnmigratedFactors(t, env)
		err := factors.Ping(t.Context())

		if !errors.Is(err, twofactor.ErrNotFound) || !strings.Contains(err.Error(), "Migrations()") ||
			!strings.Contains(err.Error(), "twofactor_secrets") || !strings.Contains(err.Error(), "twofactor_recovery_codes") {
			t.Fatalf("Ping = %v, want ErrNotFound naming Migrations() and both tables", err)
		}
	})
}

// TestMigrationsRoundTrip runs both migration directions twice. A second cycle
// catches objects a down step left behind.
func TestMigrationsRoundTrip(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		factors := newUnmigratedFactors(t, env)
		migrator := newMigrator(t, env, factors)

		for range 2 {
			if err := migrator.Up(t.Context()); err != nil {
				t.Fatalf("Up: %v", err)
			}

			if err := factors.Ping(t.Context()); err != nil {
				t.Fatalf("Ping after Up: %v", err)
			}

			if err := migrator.Rollback(t.Context()); err != nil {
				t.Fatalf("Rollback: %v", err)
			}

			if err := factors.Ping(t.Context()); !errors.Is(err, twofactor.ErrNotFound) {
				t.Fatalf("Ping after Rollback = %v, want ErrNotFound", err)
			}
		}
	})
}

// TestPingNamesOnlyTheMissingTable simulates a half-applied deployment.
func TestPingNamesOnlyTheMissingTable(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		instance := uniqueInstance()
		factors := newFactors(t, env, withInstance(instance))

		if _, err := env.db.Exec(t.Context(), query.Raw("DROP TABLE "+instance+"_twofactor_recovery_codes")); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			// The migrator's rollback cleanup drops the table again; recreate it.
			_, _ = env.db.Exec(context.Background(), query.Raw("CREATE TABLE "+instance+"_twofactor_recovery_codes (user_id TEXT)"))
		})

		err := factors.Ping(t.Context())

		if !errors.Is(err, twofactor.ErrNotFound) || !strings.Contains(err.Error(), "recovery_codes") || strings.Contains(err.Error(), "twofactor_secrets") {
			t.Fatalf("Ping = %v, want ErrNotFound naming only the recovery codes table", err)
		}
	})
}

// TestInstancesKeepSeparateData adds two instances to one migrator and enrolls a
// user in one of them only.
func TestInstancesKeepSeparateData(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		first := newUnmigratedFactors(t, env)
		second := newUnmigratedFactors(t, env)
		migrator := newMigrator(t, env, first, second)

		if err := migrator.Up(t.Context()); err != nil {
			t.Fatalf("Up: %v", err)
		}

		t.Cleanup(func() {
			if err := migrator.Rollback(context.Background()); err != nil {
				t.Errorf("Rollback: %v", err)
			}
		})

		enrolled(t, first, account{ID: 1})

		if enabled, err := first.Enrolled(t.Context(), "1"); err != nil || !enabled {
			t.Errorf("first Enrolled = %v, %v; want true", enabled, err)
		}

		if enabled, err := second.Enrolled(t.Context(), "1"); err != nil || enabled {
			t.Errorf("second Enrolled = %v, %v; want false", enabled, err)
		}
	})
}

// TestMigrationGroupsIdentifyModulePackageAndInstance checks the shipped group
// identities and that two default instances collide at Add.
func TestMigrationGroupsIdentifyModulePackageAndInstance(t *testing.T) {
	t.Parallel()

	env := newEnvironment(t, "sqlite")
	plain, err := twofactor.New(env.identity, env.db, env.cipher, env.hasher, func(ark *twofactor.Builder) { ark.Issuer("Example") })

	if err != nil {
		t.Fatal(err)
	}

	named := newUnmigratedFactors(t, env, withInstance("admin"))

	for factors, group := range map[*twofactor.TwoFactor]string{
		plain: "arkoren.dev:ark/twofactor:twofactor",
		named: "arkoren.dev:ark/twofactor:twofactor:admin",
	} {
		migrations := factors.Migrations()

		if len(migrations) != 2 {
			t.Fatalf("migrations = %d, want 2", len(migrations))
		}

		for _, definition := range migrations {
			if definition.Group() != group || definition.Err() != nil {
				t.Errorf("group = %q, err = %v; want %q", definition.Group(), definition.Err(), group)
			}
		}
	}

	again, err := twofactor.New(env.identity, env.db, env.cipher, env.hasher, func(ark *twofactor.Builder) { ark.Issuer("Example") })

	if err != nil {
		t.Fatal(err)
	}

	migrator := migrate.New(env.db)

	if err := migrator.Add(plain.Migrations()...); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Add(again.Migrations()...); !errors.Is(err, migrate.ErrDuplicateID) {
		t.Fatalf("Add of a second default instance = %v, want ErrDuplicateID", err)
	}
}
