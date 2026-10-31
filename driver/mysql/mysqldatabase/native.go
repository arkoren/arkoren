package mysqldatabase

import "database/sql"

// SQL returns the underlying database/sql pool, accepted by any Go SQL
// library. It is always the pool, also when called on the driver of a
// transaction, so queries made through it run outside any transaction.
// Native calls also bypass callback lifetime guards and facade telemetry; keep
// protected work on the callback driver or its facade context instead.
//
// Example:
//
//	stats := driver.SQL().Stats()
//	openConnections := stats.OpenConnections
func (driver *Driver) SQL() *sql.DB {
	return driver.pool
}
