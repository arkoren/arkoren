package twofactor_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/ark/twofactor"
	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/aes/aescrypto"
	"arkoren.dev/driver/bcrypt/bcrypthash"
	mysql "arkoren.dev/driver/mysql/mysqldatabase"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/key"
	"arkoren.dev/foundation/query"
)

const (
	// postgresDSN is the PostgreSQL service of compose.yaml.
	postgresDSN = "postgres://postgres:arkoren-integration@127.0.0.1:15433/arkoren_integration?sslmode=disable"

	// mysqlDSN is the MySQL service of compose.yaml.
	mysqlDSN = "root:arkoren-integration@tcp(127.0.0.1:13306)/arkoren_integration?parseTime=true"
)

// account is the application's user type in tests.
type account struct {
	// ID is the login identifier reported by AuthID.
	ID int64
}

// AuthID reports the identifier accepted by directory.load.
func (user account) AuthID() int64 {
	return user.ID
}

// directory is a fake users table: an ID absent from it is deleted, and one
// marked suspended exists but cannot authenticate.
type directory struct {
	// mutex guards the maps.
	mutex sync.Mutex

	// present holds the existing users.
	present map[int64]bool

	// suspended holds the existing users whose loader reports ErrNoCredentials.
	suspended map[int64]bool
}

// add makes ids existing users.
func (users *directory) add(ids ...int64) {
	users.mutex.Lock()
	defer users.mutex.Unlock()

	for _, id := range ids {
		users.present[id] = true
	}
}

// remove deletes ids.
func (users *directory) remove(ids ...int64) {
	users.mutex.Lock()
	defer users.mutex.Unlock()

	for _, id := range ids {
		delete(users.present, id)
	}
}

// suspend marks id as existing but disabled.
func (users *directory) suspend(id int64) {
	users.mutex.Lock()
	defer users.mutex.Unlock()

	users.suspended[id] = true
}

// load is the identity's loader.
func (users *directory) load(_ context.Context, id int64) (account, error) {
	users.mutex.Lock()
	defer users.mutex.Unlock()

	switch {
	case !users.present[id]:
		return account{}, auth.ErrNotFound
	case users.suspended[id]:
		return account{}, auth.ErrNoCredentials
	}

	return account{ID: id}, nil
}

// environment is one database dialect with the services the ark borrows.
type environment struct {
	// db is the database under test.
	db *database.Database

	// cipher encrypts secrets.
	cipher *crypto.Encrypter

	// hasher hashes recovery codes.
	hasher *hash.Hasher

	// users is the fake users table behind identity.
	users *directory

	// identity is the application's session identity.
	identity auth.SessionIdentity[int64, account]
}

// forEachDialect runs test against SQLite and, outside short mode, against the
// PostgreSQL and MySQL services of compose.yaml.
func forEachDialect(t *testing.T, test func(t *testing.T, env environment)) {
	t.Helper()

	t.Parallel()

	for _, name := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if name != "sqlite" && testing.Short() {
				t.Skip("integration test")
			}

			test(t, newEnvironment(t, name))
		})
	}
}

// newEnvironment opens the database of dialect and builds the borrowed services.
func newEnvironment(t *testing.T, dialect string) environment {
	t.Helper()

	var (
		driver database.Driver
		stop   func(context.Context) error
		err    error
	)

	switch dialect {
	case "postgres":
		var native *postgres.Driver

		native, err = postgres.New(postgresDSN)
		driver, stop = native, native.Stop
	case "mysql":
		var native *mysql.Driver

		native, err = mysql.New(mysqlDSN)
		driver, stop = native, native.Stop
	default:
		var native *sqlite.Driver

		native, err = sqlite.New(":memory:")
		driver, stop = native, native.Stop
	}

	if err != nil {
		t.Fatalf("new %s driver: %v", dialect, err)
	}

	t.Cleanup(func() {
		if err := stop(context.Background()); err != nil {
			t.Errorf("stop %s driver: %v", dialect, err)
		}
	})

	material, err := key.GenerateShared()

	if err != nil {
		t.Fatal(err)
	}

	aes, err := aescrypto.New(material)

	if err != nil {
		t.Fatal(err)
	}

	cipher, err := crypto.New(aes)

	if err != nil {
		t.Fatal(err)
	}

	users := &directory{present: map[int64]bool{}, suspended: map[int64]bool{}}

	return environment{
		db:     database.New(driver),
		cipher: cipher,
		hasher: hash.New(bcrypthash.New(func(hasher *bcrypthash.Builder) {
			hasher.Cost(4)
		})),
		users:    users,
		identity: auth.NewSessionIdentity(users.load),
	}
}

// uniqueInstance returns a valid instance name unique to the call.
func uniqueInstance() string {
	return "t_" + strings.ToLower(rand.Text())[:12]
}

// withInstance selects the instance, replacing the unique one newFactors picks.
func withInstance(instance string) twofactor.BuilderFunc {
	return func(ark *twofactor.Builder) {
		ark.Instance(instance)
	}
}

// newFactors builds the ark for env with a unique instance and the issuer
// "Example", then runs its migrations and drops the tables when the test ends.
func newFactors(t *testing.T, env environment, configure ...twofactor.BuilderFunc) *twofactor.TwoFactor {
	t.Helper()

	factors := newUnmigratedFactors(t, env, configure...)
	migrator := newMigrator(t, env, factors)

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := migrator.Rollback(ctx); err != nil {
			t.Errorf("cleanup Rollback: %v", err)
		}
	})

	return factors
}

// newUnmigratedFactors builds the ark without creating its tables.
func newUnmigratedFactors(t *testing.T, env environment, configure ...twofactor.BuilderFunc) *twofactor.TwoFactor {
	t.Helper()

	configure = append([]twofactor.BuilderFunc{func(ark *twofactor.Builder) {
		ark.Issuer("Example")
		ark.Instance(uniqueInstance())
	}}, configure...)

	factors, err := twofactor.New(env.identity, env.db, env.cipher, env.hasher, configure...)

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return factors
}

// newMigrator returns a migrator over env's database, holding the migrations of
// factors, with its own bookkeeping table and lock; the table is dropped when
// the test ends.
func newMigrator(t *testing.T, env environment, factors ...*twofactor.TwoFactor) *migrate.Migrator {
	t.Helper()

	name := uniqueInstance()
	migrator := migrate.New(env.db, func(migrator *migrate.Builder) {
		migrator.Table(name + "_migrations")
		migrator.LockName(name)
	})

	for _, factor := range factors {
		if err := migrator.Add(factor.Migrations()...); err != nil {
			t.Fatalf("Add migrations: %v", err)
		}
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, err := env.db.Exec(ctx, query.Raw("DROP TABLE IF EXISTS "+name+"_migrations")); err != nil {
			t.Errorf("drop bookkeeping table: %v", err)
		}
	})

	return migrator
}

// loggedIn returns a request carrying user as the authenticated user and the
// JSON body.
func loggedIn(user account, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	return r.WithContext(auth.NewContext(r.Context(), user))
}

// challenged returns a request whose session holds a login waiting for user's
// second factor, and that session.
func challenged(t *testing.T, env environment, user account, body string) (*http.Request, *session.Session) {
	t.Helper()

	current := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := env.identity.Challenge(current, user, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	return r.WithContext(session.NewContext(r.Context(), current)), current
}

// codeBody returns the JSON body that submits code.
func codeBody(code string) string {
	return fmt.Sprintf(`{"code":%q}`, code)
}
