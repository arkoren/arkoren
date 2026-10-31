package database

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/telemetry"
)

// settings configures a [Database].
type settings struct {
	// Telemetry receives the spans and metrics of every operation. Nil uses
	// [telemetry.Default], resolved each time an operation runs, so calling
	// [telemetry.SetDefault] after [New] still takes effect.
	Telemetry *telemetry.Telemetry

	// System is the db.system.name attribute, such as "postgresql", "mysql",
	// or "sqlite". Empty derives it from the dialect the driver reports
	// through [Driver.Dialect], and falls back to "other_sql".
	System string

	// Namespace is the db.namespace attribute, usually the database name.
	// Empty omits it.
	Namespace string

	// QueryText records the compiled SQL verbatim as db.query.text. Bound
	// arguments are omitted, but literal values and comments are not redacted.
	// New leaves it disabled to avoid automatically exporting sensitive SQL.
	QueryText bool

	// CursorCodec signs and verifies the cursors of [Database.CursorPaginate].
	// Nil leaves them unsigned plain base64 JSON, which a client can forge
	// (the position only; the query's own filters still apply).
	CursorCodec *pagination.CursorCodec

	// Events receives [TransactionCommitted] and [TransactionRolledBack].
	// Nil, the default, publishes nothing.
	Events *event.Bus
}

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	database.New(driver, func(connection *database.Builder) {
//		connection.Namespace("billing")
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	connection := &Builder{}

	for _, configure := range configure {
		configure(connection)
	}

	return connection.settings
}

// Telemetry sets the telemetry that records operations. Without it the
// database follows [telemetry.Default].
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	database.New(driver, func(connection *database.Builder) {
//		connection.Telemetry(nil)
//	})
func (connection *Builder) Telemetry(t *telemetry.Telemetry) *Builder {
	connection.settings.Telemetry = t

	return connection
}

// System sets the db.system.name attribute, overriding the one derived
// from the driver's dialect.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	database.New(driver, func(connection *database.Builder) {
//		connection.System("custom")
//	})
func (connection *Builder) System(system string) *Builder {
	connection.settings.System = system

	return connection
}

// Namespace sets the db.namespace attribute.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	database.New(driver, func(connection *database.Builder) {
//		connection.Namespace("billing")
//	})
func (connection *Builder) Namespace(namespace string) *Builder {
	connection.settings.Namespace = namespace

	return connection
}

// QueryText turns the db.query.text attribute on or off.
// It is disabled by default. Enabling it records compiled SQL verbatim: bound
// argument values remain separate, but SQL literals and comments can contain
// secrets or personal data. Enable it only for statements safe to export.
// This setting does not change query execution, operation labels, or metrics.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	database.New(driver, func(connection *database.Builder) {
//		connection.QueryText(true) // Only for statements audited as safe to export.
//	})
func (connection *Builder) QueryText(enabled bool) *Builder {
	connection.settings.QueryText = enabled

	return connection
}

// CursorCodec signs the cursors of [Database.CursorPaginate] with codec
// and rejects cursors it did not sign with [pagination.ErrCursorDecode].
// Switching an existing deployment to a codec invalidates cursors clients
// already hold.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	database.New(driver, func(connection *database.Builder) {
//		connection.CursorCodec(nil)
//	})
func (connection *Builder) CursorCodec(codec *pagination.CursorCodec) *Builder {
	connection.settings.CursorCodec = codec

	return connection
}

// Events sets the bus that receives [TransactionCommitted] and
// [TransactionRolledBack].
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	database.New(driver, func(connection *database.Builder) {
//		connection.Events(nil)
//	})
func (connection *Builder) Events(bus *event.Bus) *Builder {
	connection.settings.Events = bus

	return connection
}

// BuilderFunc configures a Builder.
type BuilderFunc = func(*Builder)
