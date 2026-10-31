package awsmail

// Builder is the mutable SES construction draft. New invokes callbacks once,
// in order, before loading AWS configuration; retaining a draft does not retain
// the resulting client. Native clients supplied to NewFrom need no settings.
//
// Example:
//
//	configure := func(mailer *Builder) {
//		mailer.Region("eu-west-1")
//	}
//	driver, err := New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config holds settings until AWS client construction.
	config settings
}

// BuilderFunc edits the SES construction draft once, before AWS configuration
// loading. Nil entries panic; native conversion through NewFrom needs no draft.
//
// Example:
//
//	var configure awsmail.BuilderFunc = func(mailer *awsmail.Builder) {
//		mailer.Region("eu-west-1")
//	}
//	driver, err := awsmail.New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// Region selects an AWS region, or the default chain when empty. It replaces
// the previous setting and returns the same construction draft.
//
// Example:
//
//	configure := func(mailer *Builder) {
//		mailer.Region("eu-west-1")
//	}
//	driver, err := New(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (mailer *Builder) Region(region string) *Builder {
	mailer.config.Region = region

	return mailer
}

// configured applies construction policies directly on a fresh draft. Nil
// entries panic; nil or empty expanded slices make no changes.
func configured(configure ...BuilderFunc) settings {
	mail := &Builder{}

	for _, configure := range configure {
		configure(mail)
	}

	return mail.config
}
