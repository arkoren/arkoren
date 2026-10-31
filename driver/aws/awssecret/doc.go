// Package awssecret provides an AWS Secrets Manager [secret.Driver]. Missing
// secrets return [secret.ErrNotFound].
// New loads AWS configuration after ordered Builder callbacks run directly once
// on a fresh draft. Nil entries panic; expanded nil/empty slices omit edits.
// Completed values are copied; native providers remain shared. NewFrom has no
// settings and leaves its native client's ownership with the caller.
//
// Example:
//
//	driver, err := awssecret.New(ctx, func(secrets *awssecret.Builder) {
//		secrets.Region("eu-west-1")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
package awssecret
