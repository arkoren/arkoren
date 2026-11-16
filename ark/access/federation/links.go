package federation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/access/internal/storage"
)

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
//   - An unknown identity first goes to [Builder.TakeOver], when set, which may
//     hand it an existing user whose email was never verified (the identity's
//     verified email proves the mailbox). Otherwise it creates a user through
//     [Builder.Create] when [Builder.SignUp] is enabled, and is [ErrNoAccount]
//     when not. The package never links by email by itself.
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
	signUp   bool
	create   func(context.Context, auth.External) (string, error)
	fallback func(context.Context, string) (bool, error)
	verified func(context.Context, string) (bool, error)
	takeOver func(context.Context, auth.External) (string, error)
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
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	links := &Links{
		provider: provider,
		db:       db,
		instance: settings.instance,
		table:    storage.Table(settings.instance, "federation_links"),
		events:   settings.events,
		signUp:   settings.signUp,
		create:   settings.create,
		fallback: settings.fallback,
		verified: settings.verified,
		takeOver: settings.takeOver,
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
		if err := links.checkUnlink(ctx, subject, id); err != nil {
			return err
		}
		_, err := links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
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

// checkUnlink locks the connections of subject and checks that id is one of
// them and not the user's last way in. It runs in the caller's transaction.
func (links *Links) checkUnlink(ctx context.Context, subject, id string) error {
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
	if !slices.ContainsFunc(owned, func(link Link) bool { return link.ID == id }) {
		return ErrNotFound
	}
	if len(owned) > 1 {
		return nil
	}
	other, err := links.hasFallback(ctx, subject)
	if err != nil {
		return err
	}
	if !other {
		return ErrLastLink
	}

	return nil
}

// hasFallback reports whether subject has another way in than connections,
// through [Builder.Fallback]; without it there is none.
func (links *Links) hasFallback(ctx context.Context, subject string) (bool, error) {
	if links.fallback == nil {
		return false, nil
	}

	return links.fallback(ctx, subject)
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
