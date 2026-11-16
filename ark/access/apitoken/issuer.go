package apitoken

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/access/internal/storage"
)

const (
	// DefaultPrefix starts every token text ("ark_<id>_<secret>"); change it with
	// [Builder.Prefix] so secret scanners can recognize your tokens.
	//
	// Example:
	//
	//	tokens.Prefix(apitoken.DefaultPrefix + "x") // "arkx_<id>_<secret>"
	DefaultPrefix = "ark"

	// touchInterval is the shortest time between two writes of a token's
	// last-used time. It is far below any sensible idle lifetime, so the stored
	// time lags the real use by at most an hour.
	touchInterval = time.Hour

	// DefaultDeviceIdleTTL is how long a device token may go unused before it
	// stops working, unless [Builder.DeviceIdleTTL] says otherwise.
	//
	// Example:
	//
	//	fmt.Println(apitoken.DefaultDeviceIdleTTL) // 2160h0m0s
	DefaultDeviceIdleTTL = 90 * 24 * time.Hour

	// maxNameLength bounds a token's name, in bytes.
	maxNameLength = 255

	// idLength and secretLength are the lengths of the public id and the secret
	// of a token text, in base32 characters (60 and 130 random bits).
	idLength     = 12
	secretLength = 26
)

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
	//
	// Example:
	//
	//	token, _, err := issuer.Issue(ctx, subject, apitoken.Request{Name: "CI"})
	//	fmt.Println(token.Kind == apitoken.KindPersonal) // true
	KindPersonal Kind = "personal"

	// KindDevice marks a token issued to a first-party app when a login was
	// exchanged; it carries every catalog scope the user may hold when it is
	// issued ([Builder.Grants]).
	//
	// Example:
	//
	//	token, _, err := issuer.IssueDevice(ctx, subject, "Pixel 9")
	//	fmt.Println(token.Kind == apitoken.KindDevice) // true
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
	// of about an hour; zero if never.
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
	// [Builder.Grants] that the user may hold. A scope outside the catalog is
	// [ErrInvalidRequest]; one the user may not hold is [ErrScopeNotGranted].
	// Empty grants none.
	Scopes []string `json:"scopes"`

	// ExpiresAt optionally ends the token; it must be in the future and within
	// [Builder.MaxTTL] when one is set. Zero means no expiry, or MaxTTL when set.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
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
// Its scopes never exceed what [Builder.Grants] lets the user hold; see
// [Builder.GrantTTL] for when that is re-checked.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db, store)
//	if err != nil {
//		return err
//	}
//	api := routes.With(auth.Middleware(issuer.Authenticator()), auth.Require())
type Issuer struct {
	provider *auth.UserProvider
	db       *database.Database
	instance string
	table    string
	store    *cache.Cache
	events   *event.Bus
	prefix   string
	maxTTL   time.Duration

	// catalog is the sorted scope catalog and grantable the user's ceiling in
	// it; grantable is nil, and grants unused, without a catalog. grantTTL is
	// [Builder.GrantTTL] and grants the namespace of the cached answers.
	catalog   []string
	grantable func(context.Context, string) ([]string, error)
	grantTTL  time.Duration
	grants    cache.Namespace[[]string]

	// personal and device are the enabled token kinds.
	personal bool
	device   bool

	// deviceIdle is how long a device token may stay unused; zero disables the
	// limit.
	deviceIdle time.Duration
	verified   func(context.Context, string) (bool, error)
}

// NewIssuer returns a token issuer. provider finds users (to check that they
// exist and to read the version), db holds the table and store caches the
// users' grants when [Builder.GrantTTL] is positive (it is required either way,
// so enabling the cache later changes no signature). It does no I/O: the table
// comes from [Issuer.Migrations]. Errors wrap [ErrInvalidConfig].
//
// It registers a forget hook on provider that deletes the user's tokens in the
// caller's transaction.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db, store, func(tokens *apitoken.Builder) {
//		tokens.Grants(apitoken.AnyScope, "posts:read")
//	})
func NewIssuer(provider *auth.UserProvider, db *database.Database, store *cache.Cache, configure ...BuilderFunc) (*Issuer, error) {
	if provider == nil || db == nil || store == nil {
		return nil, fmt.Errorf("%w: provider, database and store are required", ErrInvalidConfig)
	}
	settings := Builder{prefix: DefaultPrefix, personal: true, device: true}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	catalog := slices.Clone(settings.scopes)
	slices.Sort(catalog)
	issuer := &Issuer{
		provider: provider,
		db:       db,
		store:    store,
		instance: settings.instance,
		table:    storage.Table(settings.instance, "api_tokens"),
		events:   settings.events,
		catalog:  catalog,
		prefix:   settings.prefix,
		maxTTL:   settings.maxTTL,
		grantTTL: settings.grantTTL,
		grants:   grantsNamespace(settings.instance, settings.grantTTL),
		personal: settings.personal,
		device:   settings.device,

		deviceIdle: settings.deviceIdle(),
		verified:   settings.verified,
	}
	if len(catalog) > 0 {
		issuer.grantable = settings.grantable
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

// PersonalTokens reports whether personal tokens are enabled
// ([Builder.PersonalTokens]), which the facade reads to decide what to mount.
//
// Example:
//
//	if issuer.PersonalTokens() {
//		web.Post("/tokens", issuer.CreateHandler(guard))
//	}
func (issuer *Issuer) PersonalTokens() bool {
	return issuer.personal
}

// DeviceTokens reports whether device tokens are enabled
// ([Builder.DeviceTokens]), which the facade reads to decide what to mount.
//
// Example:
//
//	if issuer.DeviceTokens() {
//		web.Post("/device-tokens", issuer.ExchangeHandler(guard))
//	}
func (issuer *Issuer) DeviceTokens() bool {
	return issuer.device
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
func (row row) token() Token {
	var scopes []string
	// The column is written by this package as a JSON array; anything else
	// reads as no scopes, which is the safe direction.
	_ = json.Unmarshal([]byte(row.Scopes), &scopes)

	return Token{
		ID:         row.ID,
		Kind:       Kind(row.Kind),
		Name:       row.Name,
		Scopes:     scopes,
		CreatedAt:  storage.Time(row.CreatedAt),
		LastUsedAt: storage.Time(row.LastUsedAt),
		ExpiresAt:  storage.Time(row.ExpiresAt),
	}
}

// Issue creates a personal token for subject and returns it with its secret
// text, which is shown once and never stored. An invalid request, including a
// scope outside the catalog, is [ErrInvalidRequest]; a catalog scope the user
// may not hold ([Builder.Grants]) is [ErrScopeNotGranted]; an unknown subject
// returns an error wrapping
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
	name, err := tokenName(request.Name)
	if err != nil {
		return Token{}, "", err
	}
	scopes, err := issuer.scopes(request.Scopes)
	if err != nil {
		return Token{}, "", err
	}
	expiresAt, err := issuer.expiry(request.ExpiresAt, time.Now())
	if err != nil {
		return Token{}, "", err
	}
	if err := issuer.checkGranted(ctx, subject, scopes); err != nil {
		return Token{}, "", err
	}

	return issuer.issue(ctx, subject, KindPersonal, name, scopes, expiresAt)
}

// tokenName trims the name of a token, which must then be 1 to maxNameLength
// bytes.
func tokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLength {
		return "", ErrInvalidRequest
	}

	return name, nil
}

// scopes returns requested sorted and without duplicates, or
// [ErrInvalidRequest] when one is not in the catalog.
func (issuer *Issuer) scopes(requested []string) ([]string, error) {
	scopes := slices.Compact(slices.Sorted(slices.Values(requested)))
	for _, scope := range scopes {
		if _, known := slices.BinarySearch(issuer.catalog, scope); !known {
			return nil, ErrInvalidRequest
		}
	}

	return scopes, nil
}

// checkGranted returns [ErrScopeNotGranted] unless the user may hold every one
// of scopes now. It asks the grant function directly, whatever
// [Builder.GrantTTL] says: a token is rare and must not be minted from a stale
// answer. It calls nothing for no scopes.
func (issuer *Issuer) checkGranted(ctx context.Context, subject string, scopes []string) error {
	if len(scopes) == 0 {
		return nil
	}
	granted, err := issuer.granted(ctx, subject)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if !slices.Contains(granted, scope) {
			return ErrScopeNotGranted
		}
	}

	return nil
}

// expiry returns the expiry of a token requested to expire at requested (zero
// for never): with a maximum lifetime a zero request gets the maximum and a
// later one is refused, and an expiry not after now is refused.
func (issuer *Issuer) expiry(requested, now time.Time) (time.Time, error) {
	if issuer.maxTTL > 0 {
		limit := now.Add(issuer.maxTTL)
		if requested.IsZero() {
			requested = limit
		}
		if requested.After(limit) {
			return time.Time{}, ErrInvalidRequest
		}
	}
	if !requested.IsZero() && !requested.After(now) {
		return time.Time{}, ErrInvalidRequest
	}

	return requested, nil
}

// IssueDevice creates a device token for subject: it carries every catalog scope
// the user may hold now ([Builder.Grants]) and expires after [Builder.MaxTTL] when one is set, and after going
// unused for [Builder.DeviceIdleTTL] (90 days by default); its holder is also
// expected to revoke it at logout ([Issuer.DeleteCurrentHandler]). device names the device in the user's
// list (for example "Pixel 9"). It returns the token with its secret text,
// shown once. Call it after a fresh login only; [Issuer.ExchangeHandler] does.
// It publishes [Issued].
//
// Example:
//
//	token, secret, err := issuer.IssueDevice(ctx, subject, "Pixel 9")
func (issuer *Issuer) IssueDevice(ctx context.Context, subject, device string) (Token, string, error) {
	name, err := tokenName(device)
	if err != nil {
		return Token{}, "", err
	}

	expiresAt, err := issuer.expiry(time.Time{}, time.Now())
	if err != nil {
		return Token{}, "", err
	}
	scopes, err := issuer.granted(ctx, subject)
	if err != nil {
		return Token{}, "", err
	}

	return issuer.issue(ctx, subject, KindDevice, name, scopes, expiresAt)
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
