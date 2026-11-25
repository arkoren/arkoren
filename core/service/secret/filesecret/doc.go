// Package filesecret provides a read-only [secret.Driver] over files, for the
// "file:" scheme of a secretmux driver. It suits Docker and Kubernetes secrets,
// systemd credentials and any deployment that mounts a secret as a file.
//
// The reference name is a path, absolute or relative to the working directory.
// Symbolic links are followed and the file is read again on every Get, so a
// rotated mount is picked up on the next read. A missing file is
// [secret.ErrNotFound], a file over [MaxSize] (1 MiB) is an error, and writes
// are [secret.ErrUnsupported]. Contents are returned unmodified; decode into a
// string through [secret.Store.Get] to drop the single trailing newline that
// editors and "echo" append, or into []byte to keep every byte.
//
// There is no directory prefix or path policy. A reference selects any file the
// process can read, so references must come from trusted configuration, never
// from request data. Restrict the files the process can read with the file
// system, not with this driver.
//
// Example:
//
//	sources, err := secretmux.New(func(sources *secretmux.Builder) {
//		sources.Source("file", filesecret.New())
//	})
//	if err != nil {
//		return err
//	}
//	secrets := secret.New(sources)
//	password, err := secrets.Get(ctx, secret.NewSecret[string]("file:/run/secrets/db-password"))
//	if err != nil {
//		return err
//	}
//	useCredential(password)
package filesecret
