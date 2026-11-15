package federation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/access/internal/storage"
)

// ConnectIntentTTL is how long a connect intent stays valid: the time the user
// has to finish signing in at the provider after starting a connection (ten
// minutes).
//
// Example:
//
//	deadline := time.Now().Add(federation.ConnectIntentTTL)
const ConnectIntentTTL = 10 * time.Minute

// connectField is the session field that holds the connect intent.
var connectField = session.NewField[connectIntent]("arkoren:federation:connect")

// connectIntent records that a logged-in user asked to connect a provider: the
// next callback of that session may link its identity to this subject.
type connectIntent struct {
	// Subject is the user who started the connection.
	Subject string `json:"subject"`

	// ExpiresAt is when the intent stops being honored, in Unix seconds.
	ExpiresAt int64 `json:"expires_at"`
}

// validFor reports whether the intent belongs to subject and has not expired at
// now. An intent with another subject belongs to a user who has since logged
// out and been replaced in the session; it never links anything.
func (intent connectIntent) validFor(subject string, now time.Time) bool {
	return intent.Subject == subject && now.Unix() < intent.ExpiresAt
}

// errLostRace aborts a creating transaction whose link insert lost to a
// concurrent first login of the same external identity.
var errLostRace = errors.New("federation: lost link race")

// Link describes one connection of a user to an external identity, as shown in
// their settings. It holds no email or profile data: those belong to the
// provider, not to this package.
//
// Example:
//
//	links, err := store.List(ctx, subject)
//	for _, link := range links {
//		fmt.Println(link.ID, link.Issuer, link.LastUsedAt)
//	}
type Link struct {
	// ID identifies the connection for removing it; it is not secret.
	ID string `json:"id"`

	// Issuer names the external system, for example the OIDC issuer URL.
	Issuer string `json:"issuer"`

	// CreatedAt is when the connection was made.
	CreatedAt time.Time `json:"created_at"`

	// LastUsedAt is when the connection last logged the user in; zero if never.
	LastUsedAt time.Time `json:"last_used_at,omitzero"`
}

// Builder configures [NewLinks] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	links, err := federation.NewLinks(provider, db, func(links *federation.Builder) {
//		links.Create(createUser).Fallback(hasPassword)
//	})
type Builder struct {
	instance string
	events   *event.Bus
	create   func(context.Context, auth.External) (string, error)
	fallback func(context.Context, string) (bool, error)
	verified func(context.Context, string) (bool, error)
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure federation.BuilderFunc = func(links *federation.Builder) {
//		links.Instance("tenant_a")
//	}
//	links, err := federation.NewLinks(provider, db, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: the table is prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	links.Instance("tenant_a")
func (links *Builder) Instance(name string) *Builder {
	links.instance = name

	return links
}

// Events publishes [LinkCreated] and [LinkDeleted] on bus. A nil bus publishes
// nothing.
//
// Example:
//
//	links.Events(events)
func (links *Builder) Events(bus *event.Bus) *Builder {
	links.events = bus

	return links
}

// Create sets the function that makes a local user for an external identity
// that is not connected to anyone, during a logged-out login. It returns the new
// user's subject; its errors pass through to the client (a taken email is
// a 409 with the directory). Use [auth.External.EmailVerified] to decide
// whether the new user's email counts as verified. Without it, an unknown
// identity gets [ErrNotLinked]. The package never links by email: Create must
// not return an existing user.
//
// Example:
//
//	links.Create(func(ctx context.Context, external auth.External) (string, error) {
//		if external.EmailVerified {
//			return directory.CreateVerified(ctx, external.Email)
//		}
//		return directory.Create(ctx, external.Email)
//	})
func (links *Builder) Create(create func(ctx context.Context, external auth.External) (string, error)) *Builder {
	links.create = create

	return links
}

// Fallback sets the check that tells whether a user can still log in without
// their connections, for example whether they have a password. Without it the
// last connection of a user can never be removed.
//
// Example:
//
//	links.Fallback(passwords.Has)
func (links *Builder) Fallback(check func(ctx context.Context, subject string) (bool, error)) *Builder {
	links.fallback = check

	return links
}

// Verified sets the check that tells whether a user's email is verified.
// [Links.ConnectHandler] then answers 403 ([account.ErrEmailUnverified]'s
// problem) to an unverified user: whoever registered an address first must not
// attach a provider before its owner proves the address. Without it the handler
// admits everyone, which is only right when the application enforces the rule
// itself; the facade always sets it with the built-in users.
//
// Example:
//
//	links.Verified(directory.Verified)
func (links *Builder) Verified(check func(ctx context.Context, subject string) (bool, error)) *Builder {
	links.verified = check

	return links
}

// Links owns the table that connects external identities (issuer and subject)
// to local users. It is safe for concurrent use. Create it with [NewLinks].
//
// An external identity is whatever a protocol module such as ark/oauth verified;
// this package sees only the [auth.External] it hands over, so it never imports
// that module. [Links.Resolve] is the [auth.Resolver] that maps a verified
// identity to a local subject, with these rules:
//
//   - A known identity logs its user in (a deleted or disabled user is
//     [auth.ErrUnauthenticated]).
//   - An unknown identity creates a user through [Builder.Create], or is
//     [ErrNotLinked] without it. The package never links by email.
//   - A logged-in user who started a connection ([Links.ConnectHandler]) links
//     the identity to their own account instead. The intent lives in their
//     session for ten minutes, is single use, and exists so that a cross-site
//     request to the provider's login route can never link an identity to a
//     victim.
//
// Example:
//
//	links, err := federation.NewLinks(provider, db)
//	if err != nil {
//		return err
//	}
//	web.Get("/auth/google/callback", google.CallbackHandler(guard, links.Resolve))
type Links struct {
	provider *auth.UserProvider
	db       *database.Database
	instance string
	table    string
	events   *event.Bus
	create   func(context.Context, auth.External) (string, error)
	fallback func(context.Context, string) (bool, error)
	verified func(context.Context, string) (bool, error)
}

// NewLinks returns the connection store. provider finds users and db holds the
// table. It does no I/O: the table comes from [Links.Migrations]. Errors wrap
// [ErrInvalidConfig].
//
// It registers a forget hook on provider that deletes the user's connections in
// the caller's transaction.
//
// Example:
//
//	links, err := federation.NewLinks(provider, db, func(links *federation.Builder) {
//		links.Fallback(passwords.Has)
//	})
func NewLinks(provider *auth.UserProvider, db *database.Database, configure ...BuilderFunc) (*Links, error) {
	if provider == nil || db == nil {
		return nil, fmt.Errorf("%w: provider and database are required", ErrInvalidConfig)
	}
	settings := Builder{}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := storage.ValidInstance(settings.instance); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	links := &Links{
		provider: provider,
		db:       db,
		instance: settings.instance,
		table:    storage.Table(settings.instance, "federation_links"),
		events:   settings.events,
		create:   settings.create,
		fallback: settings.fallback,
		verified: settings.verified,
	}
	provider.OnForget(links.forget)

	return links, nil
}

// Migrations returns the migration that creates the "federation_links" table
// (prefixed "<instance>_" for a named instance) in the group
// "arkoren.dev:ark/access:federation", plus ":<instance>". The table has two
// unique keys, (issuer, external) and (subject, issuer): one user per external
// identity and one connection per issuer and user. The migration creates the
// table strictly and its down step drops it. The application adds it to its
// migrator; constructing or reading it never runs DDL.
//
// Example:
//
//	if err := migrator.Add(links.Migrations()...); err != nil {
//		return err
//	}
func (links *Links) Migrations() []migrate.Definition {
	return storage.Group("federation", links.instance).Migrations(
		migrate.NewMigration("links-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(links.table, func(table *schema.Table) {
				table.String("id", 32).Bytewise().Primary()
				table.String("subject", 255).Bytewise()
				table.String("issuer", 255).Bytewise()
				table.String("external", 255).Bytewise()
				table.BigInteger("created_at")
				table.BigInteger("last_used_at").Default(0)
				table.Unique("issuer", "external")
				table.Unique("subject", "issuer")
			})))
			migration.Down(migrate.Schema(schema.Drop(links.table)))
		}),
	)
}

// Ping verifies, read-only, that the table exists. A missing table returns an
// error wrapping [ErrNotMigrated]. Use it as a readiness check.
//
// Example:
//
//	if err := links.Ping(ctx); err != nil {
//		return err
//	}
func (links *Links) Ping(ctx context.Context) error {
	return storage.Ping(ctx, links.db, links.table)
}

// forget deletes the connections of subject in the caller's transaction.
func (links *Links) forget(ctx context.Context, subject string) error {
	_, err := links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))
	if err != nil {
		return fmt.Errorf("federation: forget links: %w", err)
	}

	return nil
}

// row is one row of the links table.
type row struct {
	ID         string `db:"id"`
	Subject    string `db:"subject"`
	Issuer     string `db:"issuer"`
	External   string `db:"external"`
	CreatedAt  int64  `db:"created_at"`
	LastUsedAt int64  `db:"last_used_at"`
}

// find returns the connection of an external identity, or false when none.
func (links *Links) find(ctx context.Context, issuer, external string) (row, bool, error) {
	found, err := links.db.Find[row](ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Where("issuer", issuer).Where("external", external).Limit(1)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, fmt.Errorf("federation: find link: %w", err)
	}

	return found, true, nil
}

// insert connects an external identity to subject and reports whether a row was
// written: false means one of the two unique keys already holds a row.
func (links *Links) insert(ctx context.Context, subject string, external auth.External) (bool, error) {
	id, err := storage.NewID()
	if err != nil {
		return false, err
	}
	affected, err := links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Insert(query.Values(row{
			ID:        id,
			Subject:   subject,
			Issuer:    external.Issuer,
			External:  external.Subject,
			CreatedAt: time.Now().Unix(),
		}))
		statement.OnConflict().DoNothing()
	}))
	if err != nil {
		return false, fmt.Errorf("federation: insert link: %w", err)
	}

	return affected > 0, nil
}

// emit publishes topic with the subject once the surrounding transaction, if
// any, has committed.
func (links *Links) emit(ctx context.Context, topic event.Topic[UserEvent], subject string) {
	// A failing hook only loses a notification, never the committed change.
	_ = links.db.AfterCommit(ctx, func(ctx context.Context) error {
		links.events.Emit(ctx, topic, UserEvent{Subject: subject})

		return nil
	})
}

// Lookup returns the subject connected to an external identity, or
// [ErrNotLinked]. It never creates anything and ignores connect intents.
//
// Example:
//
//	subject, err := links.Lookup(ctx, "https://accounts.google.com", "1089")
//	if errors.Is(err, federation.ErrNotLinked) {
//		// unknown identity
//	}
func (links *Links) Lookup(ctx context.Context, issuer, external string) (string, error) {
	found, ok, err := links.find(ctx, issuer, external)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotLinked
	}

	return found.Subject, nil
}

// Link connects an external identity to subject. Connecting an identity that is
// already connected to subject is a no-op; one connected to another user is
// [ErrLinkedElsewhere], and a second identity from an issuer the user already
// has is [ErrAlreadyLinked]. It publishes [LinkCreated].
//
// Example:
//
//	err := links.Link(ctx, subject, auth.External{Issuer: "https://accounts.google.com", Subject: "1089"})
func (links *Links) Link(ctx context.Context, subject string, external auth.External) error {
	written, err := links.insert(ctx, subject, external)
	if err != nil {
		return err
	}
	if written {
		links.emit(ctx, LinkCreated, subject)

		return nil
	}
	existing, ok, err := links.find(ctx, external.Issuer, external.Subject)
	if err != nil {
		return err
	}
	switch {
	case ok && existing.Subject == subject:
		return nil
	case ok:
		return ErrLinkedElsewhere
	default:
		return ErrAlreadyLinked
	}
}

// List returns the connections of subject, oldest first.
//
// Example:
//
//	links, err := store.List(ctx, subject)
func (links *Links) List(ctx context.Context, subject string) ([]Link, error) {
	rows, err := links.db.Select[row](ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Where("subject", subject).OrderBy("created_at").OrderBy("id")
	}))
	if err != nil {
		return nil, fmt.Errorf("federation: list links: %w", err)
	}
	listed := make([]Link, 0, len(rows))
	for _, stored := range rows {
		listed = append(listed, Link{
			ID:         stored.ID,
			Issuer:     stored.Issuer,
			CreatedAt:  storage.Time(stored.CreatedAt),
			LastUsedAt: storage.Time(stored.LastUsedAt),
		})
	}

	return listed, nil
}

// Unlink removes the connection id of subject. A connection that does not exist
// or belongs to someone else is [ErrNotFound]. Removing the user's only
// connection is [ErrLastLink] unless [Builder.Fallback] says they can still log
// in another way. It publishes [LinkDeleted].
//
// The user's connections are locked for the count, so two concurrent unlinks
// cannot both pass the last-way-in check: the second waits and then counts
// again. Under PostgreSQL REPEATABLE READ or SERIALIZABLE, which the
// application chooses for its connection, the second unlink instead fails with
// a serialization error that the caller should retry.
//
// Example:
//
//	if err := links.Unlink(ctx, subject, id); err != nil {
//		return err
//	}
func (links *Links) Unlink(ctx context.Context, subject, id string) error {
	err := links.db.WithTransaction(ctx, func(ctx context.Context) error {
		// Take a write lock on every connection of the user before counting
		// them (a no-op update works on all three databases; SQLite serializes
		// writers anyway). Without it two concurrent unlinks of different
		// connections each see the other still present and both succeed,
		// leaving the user no way in. The second waits here, then counts again.
		_, err := links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
			statement.Where("subject", subject).Update(map[string]any{"subject": subject})
		}))
		if err != nil {
			return err
		}
		owned, err := links.List(ctx, subject)
		if err != nil {
			return err
		}
		found := false
		for _, link := range owned {
			found = found || link.ID == id
		}
		if !found {
			return ErrNotFound
		}
		if len(owned) == 1 {
			other := false
			if links.fallback != nil {
				if other, err = links.fallback(ctx, subject); err != nil {
					return err
				}
			}
			if !other {
				return ErrLastLink
			}
		}
		_, err = links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
			statement.Where("subject", subject).Where("id", id).Delete()
		}))

		return err
	})
	if err != nil {
		return err
	}
	links.emit(ctx, LinkDeleted, subject)

	return nil
}

// UnlinkAll removes every connection of subject without the last-way-in check
// of [Links.Unlink]. A password reset of a user whose email was never verified
// calls it, because whoever registered the address first may have connected
// their own provider account. It publishes [LinkDeleted] when something was
// removed and is idempotent.
//
// Example:
//
//	err := links.UnlinkAll(ctx, subject)
func (links *Links) UnlinkAll(ctx context.Context, subject string) error {
	affected, err := links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))
	if err != nil {
		return fmt.Errorf("federation: unlink all: %w", err)
	}
	if affected > 0 {
		links.emit(ctx, LinkDeleted, subject)
	}

	return nil
}

// Resolve is the [auth.Resolver] of the package: it maps a verified external
// identity to the local subject to log in, as the protocol module's callback
// expects. See [Links] for the rules. It reads the logged-in identity and the
// session from ctx, which protocol modules take from the request, so the
// callback route needs the session and auth middleware. Errors that are
// problems ([ErrNotLinked], [ErrLinkedElsewhere], the error of [Builder.Create])
// reach the client as problem responses.
//
// Example:
//
//	web.Get("/auth/google/callback", google.CallbackHandler(guard, links.Resolve))
func (links *Links) Resolve(ctx context.Context, external auth.External) (string, error) {
	if external.Issuer == "" || external.Subject == "" {
		return "", fmt.Errorf("federation: empty issuer or subject: %w", auth.ErrNoCredentials)
	}
	current := links.takeIntent(ctx)
	existing, linked, err := links.find(ctx, external.Issuer, external.Subject)
	if err != nil {
		return "", err
	}
	switch {
	case linked && current != "" && existing.Subject != current:
		return "", ErrLinkedElsewhere
	case linked:
		return links.login(ctx, existing)
	case current != "":
		if err := links.Link(ctx, current, external); err != nil {
			return "", err
		}

		return current, nil
	}

	return links.createUser(ctx, external)
}

// takeIntent returns the subject of the connect intent in the session of ctx,
// provided it is unexpired and belongs to the logged-in user, and removes it:
// an intent serves one callback whatever its outcome. It returns "" when there
// is none.
func (links *Links) takeIntent(ctx context.Context) string {
	current, ok := session.FromContext(ctx)
	if !ok {
		return ""
	}
	intent, err := current.Get(connectField)
	if err != nil {
		return ""
	}
	// Deleting can only fail for an invalid field; the intent is unusable then
	// anyway, and a stale one still expires by its own time.
	_ = current.Delete(connectField)
	identity, ok := auth.FromContext(ctx)
	if !ok || !identity.Session() || !intent.validFor(identity.Subject(), time.Now()) {
		return ""
	}

	return intent.Subject
}

// login returns the subject of a known connection after checking the user is
// still allowed in, and records the use.
func (links *Links) login(ctx context.Context, found row) (string, error) {
	if _, err := links.provider.Find(ctx, found.Subject); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
			return "", auth.ErrUnauthenticated
		}

		return "", err
	}
	// The time is informational: a failed write must not fail the login.
	_, _ = links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Where("id", found.ID).Update(map[string]any{"last_used_at": time.Now().Unix()})
	}))

	return found.Subject, nil
}

// createUser makes a user for an unknown identity and connects it, in one
// transaction. When a concurrent first login of the same identity wins the
// insert, this one rolls its user back and returns the winner's subject.
func (links *Links) createUser(ctx context.Context, external auth.External) (string, error) {
	if links.create == nil {
		return "", ErrNotLinked
	}
	var subject string
	err := links.db.WithTransaction(ctx, func(ctx context.Context) error {
		created, err := links.create(ctx, external)
		if err != nil {
			return err
		}
		written, err := links.insert(ctx, created, external)
		if err != nil {
			return err
		}
		if !written {
			return errLostRace
		}
		subject = created

		return nil
	})
	if errors.Is(err, errLostRace) {
		winner, ok, err := links.find(ctx, external.Issuer, external.Subject)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrNotLinked
		}

		return links.login(ctx, winner)
	}
	if err != nil {
		return "", err
	}
	links.emit(ctx, LinkCreated, subject)

	return subject, nil
}
