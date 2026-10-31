// Package statement compiles PostgreSQL search SQL without executing it.
// Callers supply validated table identities and retain all transaction,
// readiness, migration, and result-ownership decisions.
package statement
