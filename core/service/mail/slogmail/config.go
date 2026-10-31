package slogmail

import (
	"cmp"
	"log/slog"
)

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Detail(slogmail.DetailBody)
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	mailer := &Builder{}

	for _, configure := range configure {
		configure(mailer)
	}

	mailer.settings.Detail = cmp.Or(mailer.settings.Detail, DetailSummary)

	return mailer.settings
}

// Logger selects the shared destination for one record per accepted message.
// Nil resolves slog.Default when Send runs, including later default changes.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Logger(nil)
//	})
func (mailer *Builder) Logger(logger *slog.Logger) *Builder {
	mailer.settings.Logger = logger

	return mailer
}

// Level sets the severity of each message record. Zero is slog.LevelInfo;
// logger filtering still controls whether the record is written.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Level(slog.LevelDebug)
//	})
func (mailer *Builder) Level(level slog.Level) *Builder {
	mailer.settings.Level = level

	return mailer
}

// Detail selects summary-only logging or DetailBody, which additionally logs
// text and HTML bodies. The default is DetailSummary. Attachment contents are
// never logged; enable body logging only when its disclosure is acceptable.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Detail(slogmail.DetailBody)
//	})
func (mailer *Builder) Detail(detail Detail) *Builder {
	mailer.settings.Detail = detail

	return mailer
}

// BuilderFunc edits New's fresh construction draft synchronously, once, in
// callback order. Nil entries panic; expanded nil or empty slices omit edits.
// Completed settings are copied while the selected logger remains shared.
//
// Example:
//
//	policy := slogmail.BuilderFunc(func(mailer *slogmail.Builder) {
//		mailer.Detail(slogmail.DetailSummary)
//	})
//	driver := slogmail.New(policy)
//	var backend mail.Driver = driver
//	_ = backend
type BuilderFunc = func(*Builder)
