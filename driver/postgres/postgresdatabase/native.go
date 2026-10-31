package postgresdatabase

import "github.com/jackc/pgx/v5/pgxpool"

// DB returns the underlying pgx connection pool, for pgx features the
// database contract does not cover, such as COPY, LISTEN, and batches. It is
// always the root pool, also on callback drivers: it bypasses session lifetime
// guards, transaction routing, and facade telemetry. Do not use it for work that
// must remain protected by a retained session's lock after connection loss.
//
// Example:
//
//	config := driver.DB().Config()
//	maxConnections := config.MaxConns
func (driver *Driver) DB() *pgxpool.Pool {
	return driver.pool
}
