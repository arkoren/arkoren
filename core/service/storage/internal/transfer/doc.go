// Package transfer handles HTTP presentation and upload sniffing for storage.
// Bodies and response writers are borrowed. The parent owns acquisition,
// not-found problems, key validation, and cleanup/error precedence.
package transfer
