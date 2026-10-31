package cloudflarequeue

import (
	"log/slog"
	"time"
)

// Builder holds optional Cloudflare polling settings during construction.
// Callbacks share one fresh draft; completed settings are copied. Native clients
// remain caller-owned and NewFrom ignores Endpoint.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.BatchSize(10)
//	}
//	driver, err := New("api-token", "account-id", map[string]string{"emails": "queue-id"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config holds draft values, not a live driver.
	config settings
}

// BuilderFunc edits one fresh polling draft synchronously, once in order, before
// validation. Nil entries panic; expanded nil or empty slices omit edits. Routing
// is captured before callbacks, and retained drafts cannot change the driver.
// Native clients and loggers remain shared, not recursively copied or sanitized.
//
// Example:
//
//	configure := func(consumer *cloudflarequeue.Builder) {
//		consumer.BatchSize(5)
//	}
//	policies := []cloudflarequeue.BuilderFunc{configure}
//	driver, err := cloudflarequeue.New(token, accountID, routes, policies...)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// Endpoint replaces New's API base URL. Empty uses Cloudflare's default;
// NewFrom ignores it. It returns the same draft without making requests.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.Endpoint("http://localhost:8080/")
//	}
//	driver, err := New("api-token", "account-id", map[string]string{"emails": "queue-id"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) Endpoint(endpoint string) *Builder {
	consumer.config.Endpoint = endpoint

	return consumer
}

// BatchSize replaces the sequential pull batch size. Zero selects one;
// construction rejects values outside 1–100. It returns the same draft.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.BatchSize(10)
//	}
//	driver, err := New("api-token", "account-id", map[string]string{"emails": "queue-id"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) BatchSize(count int) *Builder {
	consumer.config.BatchSize = count

	return consumer
}

// VisibilityTimeout replaces the nonrenewable lease. Zero selects 30 seconds;
// construction accepts 1ms through 12 hours. It returns the same draft.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.VisibilityTimeout(time.Minute)
//	}
//	driver, err := New("api-token", "account-id", map[string]string{"emails": "queue-id"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) VisibilityTimeout(timeout time.Duration) *Builder {
	consumer.config.VisibilityTimeout = timeout

	return consumer
}

// PollInterval replaces the initial empty-poll delay. Zero selects one second;
// negative values fail construction. It returns the same draft.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.PollInterval(time.Second)
//	}
//	driver, err := New("api-token", "account-id", map[string]string{"emails": "queue-id"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) PollInterval(interval time.Duration) *Builder {
	consumer.config.PollInterval = interval

	return consumer
}

// MaxPollInterval replaces the backoff cap. Zero selects ten seconds; a positive
// cap below PollInterval is raised to it. It returns the same draft.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.MaxPollInterval(5 * time.Second)
//	}
//	driver, err := New("api-token", "account-id", map[string]string{"emails": "queue-id"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) MaxPollInterval(interval time.Duration) *Builder {
	consumer.config.MaxPollInterval = interval

	return consumer
}

// Logger replaces the shared delivery logger. Nil selects slog.Default at
// construction. It returns the same draft without mutating the logger.
// Handler-returned errors (including settlement failures), Fail causes and
// confirmed-settlement warnings are logged as supplied; contents are not recursively
// sanitized. Keep credentials and payloads out of application errors and apply
// redaction in the application's logger if its handlers require it.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.Logger(slog.New(slog.DiscardHandler))
//	}
//	driver, err := New("api-token", "account-id", map[string]string{"emails": "queue-id"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) Logger(logger *slog.Logger) *Builder {
	consumer.config.Logger = logger

	return consumer
}

// configured invokes policies directly once in order on a fresh draft. Nil
// entries panic; empty or nil slice expansion supplies no policies.
func configured(configure ...BuilderFunc) settings {
	consumer := &Builder{}

	for _, configure := range configure {
		configure(consumer)
	}

	return consumer.config
}
