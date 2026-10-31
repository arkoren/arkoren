package postgressearch

import "arkoren.dev/core/service/database"

// Database returns the database the driver reads and writes through, the one
// passed to [NewFrom]. It remains application-owned; Stop never closes it. Calls
// through this facade retain its normal context routing and telemetry policies.
//
// Example:
//
//	driver, err := postgressearch.NewFrom(db)
//	if err != nil {
//		return err
//	}
//	_ = driver.Database().Dialect()
func (driver *Driver) Database() *database.Database {
	return driver.db
}
