package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/access/internal/storage"
)

const (
	// DefaultPrefix starts every token text ("ark_<id>_<secret>"); change it with
	// [Builder.Prefix] so secret scanners can recognize your tokens.
	DefaultPrefix = "ark"

	// touchInterval is the shortest time between two writes of a token's
	// last-used time.
	touchInterval = 5 * time.Minute

	// maxNameLength bounds a token's name, in bytes.
	maxNameLength = 255

	// idLength and secretLength are the lengths of the public id and the secret
	// of a token text, in base32 characters (60 and 130 random bits).
	idLength     = 12
	secretLength = 26
)

// prefixPattern accepts the token prefixes: 2 to 16 lowercase letters or digits.
var prefixPattern = regexp.MustCompile(`^[a-z0-9]{2,16}$`)

// Kind says how a token was issued.
//
// Example:
//
//	if token.Kind == apitoken.KindDevice {
//		// a first-party app's token
//	}
type Kind string

const (
	// KindPersonal marks a named, scoped token a user created for themselves.
	KindPersonal Kind = "personal"

	// KindDevice marks a token issued to a first-party app when a login was
	// exchanged; it carries every scope of the catalog.
	KindDevice Kind = "device"
)

// Token describes an issued token. The secret is never part of it: it is shown
// once, when the token is issued.
//
// Example:
//
//	tokens, err := issuer.Tokens(ctx, subject)
//	for _, token := range tokens {
//		fmt.Println(token.Name, token.Scopes, token.LastUsedAt)
//	}
type Token struct {
	// ID identifies the token for revoking; it is not secret.
	ID string `json:"id"`

	// Kind is how the token was issued.
	Kind Kind `json:"kind"`

	// Name is the label the user gave the token, or the device name.
	Name string `json:"name"`

	// Scopes are the scopes the token grants.
	Scopes []string `json:"scopes"`

	// CreatedAt is when the token was issued.
	CreatedAt time.Time `json:"created_at"`

	// LastUsedAt is when the token last authenticated a request, at a precision
	// of about five minutes; zero if never.
	LastUsedAt time.Time `json:"last_used_at,omitzero"`

	// ExpiresAt is when the token stops working; zero means it never does.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Request is what a user asks for when creating a personal token.
//
// Example:
//
//	token, secret, err := issuer.Issue(ctx, subject, apitoken.Request{
//		Name:   "CI deploy",
//		Scopes: []string{"posts:write"},
//	})
type Request struct {
	// Name labels the token in the user's list; it is required, at most 255 bytes.
	Name string `json:"name"`

	// Scopes are the scopes to grant, a subset of the catalog from
	// [Builder.Scopes]. Empty grants none.
	Scopes []string `json:"scopes"`

	// ExpiresAt optionally ends the token; it must be in the future and within
	// [Builder.MaxTTL] when one is set. Zero means no expiry, or MaxTTL when set.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Builder configures [NewIssuer] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db, func(tokens *apitoken.Builder) {
//		tokens.Scopes("posts:read", "posts:write").MaxTTL(90 * 24 * time.Hour)
//	})
type Builder struct {
	instance string
	events   *event.Bus
	scopes   []string
	prefix   string
	maxTTL   time.Duration
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure apitoken.BuilderFunc = func(tokens *apitoken.Builder) {
//		tokens.Scopes("posts:read")
//	}
//	issuer, err := apitoken.NewIssuer(provider, db, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: the table is prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	tokens.Instance("tenant_a")
func (tokens *Builder) Instance(name string) *Builder {
	tokens.instance = name

	return tokens
}

// Events publishes [Issued] and [Revoked] on bus. A nil bus publishes nothing.
//
// Example:
//
//	tokens.Events(events)
func (tokens *Builder) Events(bus *event.Bus) *Builder {
	tokens.events = bus

	return tokens
}

// Scopes declares the catalog of scopes the application knows. Personal tokens
// may carry any subset; device tokens carry all of them. Without a catalog
// tokens carry no scopes. The names are copied.
//
// Example:
//
//	tokens.Scopes("posts:read", "posts:write")
func (tokens *Builder) Scopes(scopes ...string) *Builder {
	tokens.scopes = slices.Clone(scopes)

	return tokens
}

// Prefix replaces the token prefix, "ark" by default: 2 to 16 lowercase letters
// or digits. Changing it invalidates no stored token, but tokens issued under
// the old prefix are no longer recognized.
//
// Example:
//
//	tokens.Prefix("acme")
func (tokens *Builder) Prefix(prefix string) *Builder {
	tokens.prefix = prefix

	return tokens
}

// MaxTTL caps the lifetime of personal tokens: a longer expiry is refused and a
// token without one expires after ttl. Zero (the default) means no cap. Device
// tokens never expire by themselves; revoke them at logout.
//
// Example:
//
//	tokens.MaxTTL(90 * 24 * time.Hour)
func (tokens *Builder) MaxTTL(ttl time.Duration) *Builder {
	tokens.maxTTL = ttl

	return tokens
}

// Issuer issues, authenticates and revokes API tokens. It is safe for
// concurrent use. Create it with [NewIssuer].
//
// Tokens are opaque: "<prefix>_<id>_<secret>", where the id is a public lookup
// key and the secret carries 130 random bits and is stored only as SHA-256 hex.
// Each token is bound to the user's [auth.UserRecord.Version] when it is issued,
// so anything that changes the version (a password reset, or the application's
// own rotation) ends it. A token never carries factors and is never a browser
// session, so it can neither pass [auth.RequireLevel] nor change a credential.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db)
//	if err != nil {
//		return err
//	}
//	api := routes.With(auth.Middleware(issuer.Authenticator()), auth.Require())
type Issuer struct {
	provider *auth.UserProvider
	db       *database.Database
	instance string
	table    string
	events   *event.Bus
	catalog  []string
	prefix   string
	maxTTL   time.Duration
}

// NewIssuer returns a token issuer. provider finds users (to check that they
// exist and to read the version) and db holds the table. It does no I/O: the
// table comes from [Issuer.Migrations]. Errors wrap [ErrInvalidConfig].
//
// It registers a forget hook on provider that deletes the user's tokens in the
// caller's transaction.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db, func(tokens *apitoken.Builder) {
//		tokens.Scopes("posts:read")
//	})
func NewIssuer(provider *auth.UserProvider, db *database.Database, configure ...BuilderFunc) (*Issuer, error) {
	if provider == nil || db == nil {
		return nil, fmt.Errorf("%w: provider and database are required", ErrInvalidConfig)
	}
	settings := Builder{prefix: DefaultPrefix}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := storage.ValidInstance(settings.instance); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if !prefixPattern.MatchString(settings.prefix) {
		return nil, fmt.Errorf("%w: prefix %q must be 2 to 16 lowercase letters or digits", ErrInvalidConfig, settings.prefix)
	}
	if settings.maxTTL < 0 {
		return nil, fmt.Errorf("%w: negative maximum lifetime", ErrInvalidConfig)
	}
	catalog := slices.Clone(settings.scopes)
	slices.Sort(catalog)
	if slices.Contains(catalog, "") || len(slices.Compact(slices.Clone(catalog))) != len(catalog) {
		return nil, fmt.Errorf("%w: scopes must be unique and non-empty", ErrInvalidConfig)
	}
	issuer := &Issuer{
		provider: provider,
		db:       db,
		instance: settings.instance,
		table:    storage.Table(settings.instance, "api_tokens"),
		events:   settings.events,
		catalog:  catalog,
		prefix:   settings.prefix,
		maxTTL:   settings.maxTTL,
	}
	provider.OnForget(issuer.RevokeAll)

	return issuer, nil
}

// Migrations returns the migration that creates the "api_tokens" table
// (prefixed "<instance>_" for a named instance) in the group
// "arkoren.dev:ark/access:apitoken", plus ":<instance>". It creates the table
// strictly and its down step drops it. The application adds it to its migrator;
// constructing or reading it never runs DDL.
//
// Example:
//
//	if err := migrator.Add(issuer.Migrations()...); err != nil {
//		return err
//	}
func (issuer *Issuer) Migrations() []migrate.Definition {
	return storage.Group("apitoken", issuer.instance).Migrations(
		migrate.NewMigration("api-tokens-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(issuer.table, func(table *schema.Table) {
				table.String("id", 32).Bytewise().Primary()
				table.String("subject", 255).Bytewise()
				table.String("kind", 16)
				table.String("name", maxNameLength)
				table.String("secret_hash", 64).Bytewise()
				table.String("user_version", 64).Bytewise()
				table.Text("scopes")
				table.BigInteger("last_used_at").Default(0)
				table.BigInteger("expires_at").Default(0)
				table.BigInteger("created_at")
				table.Index("subject", "created_at")
			})))
			migration.Down(migrate.Schema(schema.Drop(issuer.table)))
		}),
	)
}

// Ping verifies, read-only, that the table exists. A missing table returns an
// error wrapping [ErrNotMigrated]. Use it as a readiness check.
//
// Example:
//
//	if err := issuer.Ping(ctx); err != nil {
//		return err
//	}
func (issuer *Issuer) Ping(ctx context.Context) error {
	return storage.Ping(ctx, issuer.db, issuer.table)
}

// row is one row of the tokens table.
type row struct {
	ID          string `db:"id"`
	Subject     string `db:"subject"`
	Kind        string `db:"kind"`
	Name        string `db:"name"`
	SecretHash  string `db:"secret_hash"`
	UserVersion string `db:"user_version"`
	Scopes      string `db:"scopes"`
	LastUsedAt  int64  `db:"last_used_at"`
	ExpiresAt   int64  `db:"expires_at"`
	CreatedAt   int64  `db:"created_at"`
}

// token converts a row to its public form.
func (r row) token() Token {
	var scopes []string
	// The column is written by this package as a JSON array; anything else
	// reads as no scopes, which is the safe direction.
	_ = json.Unmarshal([]byte(r.Scopes), &scopes)

	return Token{
		ID:         r.ID,
		Kind:       Kind(r.Kind),
		Name:       r.Name,
		Scopes:     scopes,
		CreatedAt:  storage.Time(r.CreatedAt),
		LastUsedAt: storage.Time(r.LastUsedAt),
		ExpiresAt:  storage.Time(r.ExpiresAt),
	}
}

// Issue creates a personal token for subject and returns it with its secret
// text, which is shown once and never stored. An invalid request is
// [ErrInvalidRequest]; an unknown subject returns an error wrapping
// [auth.ErrUserNotFound]. It also removes the user's tokens that an earlier
// version change already ended. It publishes [Issued].
//
// Example:
//
//	token, secret, err := issuer.Issue(ctx, subject, apitoken.Request{
//		Name:   "CI deploy",
//		Scopes: []string{"posts:write"},
//	})
//	if err != nil {
//		return err
//	}
//	fmt.Println(token.ID, secret) // show secret to the user once
func (issuer *Issuer) Issue(ctx context.Context, subject string, request Request) (Token, string, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > maxNameLength {
		return Token{}, "", ErrInvalidRequest
	}
	scopes := slices.Clone(request.Scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	for _, scope := range scopes {
		if _, known := slices.BinarySearch(issuer.catalog, scope); !known {
			return Token{}, "", ErrInvalidRequest
		}
	}
	now := time.Now()
	expiresAt := request.ExpiresAt
	if issuer.maxTTL > 0 {
		if expiresAt.IsZero() {
			expiresAt = now.Add(issuer.maxTTL)
		}
		if expiresAt.After(now.Add(issuer.maxTTL)) {
			return Token{}, "", ErrInvalidRequest
		}
	}
	if !expiresAt.IsZero() && !expiresAt.After(now) {
		return Token{}, "", ErrInvalidRequest
	}

	return issuer.issue(ctx, subject, KindPersonal, name, scopes, expiresAt)
}

// IssueDevice creates a device token for subject: it carries every scope of the
// catalog and does not expire, and its holder is expected to revoke it at
// logout ([Issuer.DeleteCurrentHandler]). device names the device in the user's
// list (for example "Pixel 9"). It returns the token with its secret text,
// shown once. Call it after a fresh login only; [Issuer.ExchangeHandler] does.
// It publishes [Issued].
//
// Example:
//
//	token, secret, err := issuer.IssueDevice(ctx, subject, "Pixel 9")
func (issuer *Issuer) IssueDevice(ctx context.Context, subject, device string) (Token, string, error) {
	name := strings.TrimSpace(device)
	if name == "" || len(name) > maxNameLength {
		return Token{}, "", ErrInvalidRequest
	}

	return issuer.issue(ctx, subject, KindDevice, name, slices.Clone(issuer.catalog), time.Time{})
}

// issue writes the token row and returns the token with its text.
func (issuer *Issuer) issue(ctx context.Context, subject string, kind Kind, name string, scopes []string, expiresAt time.Time) (Token, string, error) {
	record, err := issuer.provider.Find(ctx, subject)
	if err != nil {
		return Token{}, "", err
	}
	id := rand.Text()[:idLength]
	secret := rand.Text()[:secretLength]
	encoded, err := json.Marshal(scopes)
	if err != nil {
		return Token{}, "", fmt.Errorf("apitoken: encode scopes: %w", err)
	}
	created := row{
		ID:          id,
		Subject:     subject,
		Kind:        string(kind),
		Name:        name,
		SecretHash:  storage.Hash(secret),
		UserVersion: storage.Hash(record.Version),
		Scopes:      string(encoded),
		CreatedAt:   time.Now().Unix(),
	}
	if !expiresAt.IsZero() {
		created.ExpiresAt = expiresAt.Unix()
	}
	err = issuer.db.WithTransaction(ctx, func(ctx context.Context) error {
		if err := issuer.prune(ctx, subject, created.UserVersion); err != nil {
			return err
		}
		_, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
			statement.Insert(query.Values(created))
		}))

		return err
	})
	if err != nil {
		return Token{}, "", fmt.Errorf("apitoken: store token: %w", err)
	}
	issuer.emit(ctx, Issued, subject)
	if scopes == nil {
		scopes = []string{}
	}
	token := created.token()
	token.Scopes = scopes

	return token, issuer.prefix + "_" + id + "_" + secret, nil
}

// prune deletes the tokens of subject that can never authenticate again: those
// bound to an older user version and those that expired.
func (issuer *Issuer) prune(ctx context.Context, subject, version string) error {
	for _, condition := range []func(*query.Builder){
		func(statement *query.Builder) { statement.Where("user_version", "<>", version) },
		func(statement *query.Builder) {
			statement.Where("expires_at", ">", 0).Where("expires_at", "<=", time.Now().Unix())
		},
	} {
		_, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
			statement.Where("subject", subject).Delete()
			condition(statement)
		}))
		if err != nil {
			return err
		}
	}

	return nil
}

// Tokens lists the tokens of subject, newest first, including expired ones
// until the next issue removes them. The secrets are not recoverable.
//
// Example:
//
//	tokens, err := issuer.Tokens(ctx, subject)
func (issuer *Issuer) Tokens(ctx context.Context, subject string) ([]Token, error) {
	rows, err := issuer.db.Select[row](ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("subject", subject).OrderByDesc("created_at").OrderByDesc("id")
	}))
	if err != nil {
		return nil, fmt.Errorf("apitoken: list tokens: %w", err)
	}
	tokens := make([]Token, 0, len(rows))
	for _, stored := range rows {
		tokens = append(tokens, stored.token())
	}

	return tokens, nil
}

// Revoke deletes the listed tokens of subject; ids that are unknown or belong
// to another user are ignored. A revoked token fails to authenticate at once.
// It publishes [Revoked] when at least one token was deleted.
//
// Example:
//
//	err := issuer.Revoke(ctx, subject, token.ID)
func (issuer *Issuer) Revoke(ctx context.Context, subject string, ids ...string) error {
	_, err := issuer.revoke(ctx, subject, ids)

	return err
}

// revoke deletes the tokens and reports how many were deleted.
func (issuer *Issuer) revoke(ctx context.Context, subject string, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	affected, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("subject", subject)
		statement.WhereIn("id", ids...)
		statement.Delete()
	}))
	if err != nil {
		return 0, fmt.Errorf("apitoken: revoke: %w", err)
	}
	if affected > 0 {
		issuer.emit(ctx, Revoked, subject)
	}

	return affected, nil
}

// RevokeAll deletes every token of subject ("sign out everywhere"). It is also
// the forget hook of the user provider. It publishes [Revoked] when at least one
// token was deleted.
//
// Example:
//
//	err := issuer.RevokeAll(ctx, subject)
func (issuer *Issuer) RevokeAll(ctx context.Context, subject string) error {
	affected, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))
	if err != nil {
		return fmt.Errorf("apitoken: revoke all: %w", err)
	}
	if affected > 0 {
		issuer.emit(ctx, Revoked, subject)
	}

	return nil
}

// emit publishes topic with the subject once the surrounding transaction, if
// any, has committed.
func (issuer *Issuer) emit(ctx context.Context, topic event.Topic[UserEvent], subject string) {
	// A failing hook only loses a notification, never the committed change.
	_ = issuer.db.AfterCommit(ctx, func(ctx context.Context) error {
		issuer.events.Emit(ctx, topic, UserEvent{Subject: subject})

		return nil
	})
}

// parse splits a token text into its id and secret. ok is false for text that
// does not have the shape of a token of this issuer; recognized is false when
// the prefix is not ours, so another authenticator may try the text.
func (issuer *Issuer) parse(text string) (id, secret string, recognized, ok bool) {
	rest, found := strings.CutPrefix(text, issuer.prefix+"_")
	if !found {
		return "", "", false, false
	}
	id, secret, found = strings.Cut(rest, "_")

	return id, secret, true, found && len(id) == idLength && len(secret) == secretLength
}

// Authenticator returns the authenticator for bearer tokens: install it with
// [auth.Middleware], next to or instead of the guard's. A request without a
// bearer token, or with one that does not start with the prefix, passes through
// anonymously (so another authenticator, such as a JWT one, can try); a
// malformed, unknown, expired, revoked or outdated token is
// [auth.ErrUnauthenticated] with a Bearer challenge. The identity carries the
// token's scopes and the user's record, and no factors: it is never a browser
// session, [auth.RequireLevel] rejects it, and the credential handlers of this
// module refuse it.
//
// Example:
//
//	api := routes.With(auth.Middleware(issuer.Authenticator()), auth.Require())
//	api.With(gate.Require(gate.HasScopes("posts:write"))).Post("/api/posts", createPost)
func (issuer *Issuer) Authenticator() auth.Authenticator {
	return auth.Bearer(func(ctx context.Context, text string) (auth.Identity, error) {
		id, secret, recognized, ok := issuer.parse(text)
		if !recognized {
			return auth.Identity{}, auth.ErrNoCredentials
		}
		if !ok {
			return auth.Identity{}, auth.ErrUnauthenticated
		}
		stored, err := issuer.db.Find[row](ctx, query.Table(issuer.table, func(statement *query.Builder) {
			statement.Where("id", id).Limit(1)
		}))
		if errors.Is(err, database.ErrNoRows) {
			return auth.Identity{}, auth.ErrUnauthenticated
		}
		if err != nil {
			return auth.Identity{}, fmt.Errorf("apitoken: load token: %w", err)
		}
		now := time.Now().Unix()
		if subtle.ConstantTimeCompare([]byte(storage.Hash(secret)), []byte(stored.SecretHash)) != 1 ||
			(stored.ExpiresAt != 0 && stored.ExpiresAt <= now) {
			return auth.Identity{}, auth.ErrUnauthenticated
		}
		record, err := issuer.provider.Find(ctx, stored.Subject)
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
			return auth.Identity{}, auth.ErrUnauthenticated
		}
		if err != nil {
			return auth.Identity{}, err
		}
		if subtle.ConstantTimeCompare([]byte(storage.Hash(record.Version)), []byte(stored.UserVersion)) != 1 {
			return auth.Identity{}, auth.ErrUnauthenticated
		}
		if stored.LastUsedAt < now-int64(touchInterval/time.Second) {
			// The timestamp is informational: a failed write must not fail the
			// request, and the next use retries it.
			_, _ = issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
				statement.Where("id", id).Update(map[string]any{"last_used_at": now})
			}))
		}

		return auth.NewIdentity(stored.Subject, func(principal *auth.IdentityBuilder) {
			principal.Scopes(stored.token().Scopes...).Record(record)
		}), nil
	})
}

// presentedID returns the id of the bearer token r presented, or false when the
// request carries none of this issuer's tokens.
func (issuer *Issuer) presentedID(r *http.Request) (string, bool) {
	scheme, text, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	id, _, recognized, ok := issuer.parse(text)

	return id, recognized && ok
}
