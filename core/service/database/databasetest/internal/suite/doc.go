// Package suite implements database driver checks and their private fixtures.
// The public databasetest package owns options and selects the timeout before
// entering this harness. Every check retains its original subtest identity.
package suite
