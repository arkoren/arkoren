// Package filelock implements only the platform file-lock primitive.
// The parent driver resolves paths, coordinates process-wide lock identities,
// and retains ownership across session and transaction callback lifetimes.
package filelock
