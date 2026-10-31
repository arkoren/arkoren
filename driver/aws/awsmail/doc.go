// Package awsmail provides an Amazon SES v2 [mail.Driver].
// New loads AWS configuration after invoking ordered Builder callbacks once.
// Nil callbacks panic; expanded nil/empty slices omit edits. NewFrom wraps an
// already configured native client without settings or lifecycle ownership.
//
// Example:
//
//	driver, err := awsmail.New(ctx, func(mailer *awsmail.Builder) {
//		mailer.Region("eu-west-1")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
package awsmail
