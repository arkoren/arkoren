package pagination

// Option changes one [Config] setting.
// Options run synchronously in order on a fresh private configuration value.
// Validation and zero-default completion happen when
// PageParams.Normalize consumes the result. Nil entries panic when invoked.
//
// Example:
//
//	options := []pagination.Option{pagination.WithDefaultPerPage(10), pagination.WithMaxPerPage(50)}
//	config := pagination.NewConfig(options...)
//	_ = config.MaxPerPage
type Option func(*Config)

// NewConfig returns the default configuration with opts applied in order.
// It does not validate the completed value or refill zeros introduced by an
// option; PageParams.Normalize performs that work. Nil option entries panic.
//
// Example:
//
//	config := pagination.NewConfig(pagination.WithDefaultPerPage(10))
//	_ = config.DefaultPerPage // 10.
func NewConfig(opts ...Option) Config {
	config := defaultConfig()

	for _, opt := range opts {
		opt(&config)
	}

	return config
}

// WithConfig replaces the whole configuration. Later options still apply.
// The struct is captured by value. Zero fields are completed only by Normalize,
// not by NewConfig; negative maxima are likewise rejected at normalization.
//
// Example:
//
//	config := pagination.NewConfig(pagination.WithConfig(pagination.Config{MaxPerPage: 50}))
//	_ = config.MaxPerPage // 50.
func WithConfig(config Config) Option {
	return func(target *Config) {
		*target = config
	}
}

// WithDefaultPage sets [Config.DefaultPage].
// Normalize uses this fallback for an omitted/zero page, then raises values
// below one to one and enforces MaxPage and representable offset bounds.
//
// Example:
//
//	config := pagination.NewConfig(pagination.WithDefaultPage(2))
//	_ = config.DefaultPage // 2.
func WithDefaultPage(defaultPage int) Option {
	return func(config *Config) {
		config.DefaultPage = defaultPage
	}
}

// WithDefaultPerPage sets [Config.DefaultPerPage].
// Normalize uses this fallback for an omitted/zero size, raises it to one when
// negative, and caps it at MaxPerPage. Zero selects the built-in fallback.
//
// Example:
//
//	config := pagination.NewConfig(pagination.WithDefaultPerPage(10))
//	_ = config.DefaultPerPage // 10.
func WithDefaultPerPage(defaultPerPage int) Option {
	return func(config *Config) {
		config.DefaultPerPage = defaultPerPage
	}
}

// WithMaxPerPage sets [Config.MaxPerPage].
// Normalize caps requested sizes at this maximum; zero uses the built-in 100
// and a negative maximum yields ErrInvalidConfig instead of disabling the cap.
//
// Example:
//
//	config := pagination.NewConfig(pagination.WithMaxPerPage(50))
//	_ = config.MaxPerPage // 50.
func WithMaxPerPage(maxPerPage int) Option {
	return func(config *Config) {
		config.MaxPerPage = maxPerPage
	}
}

// WithMaxPage sets [Config.MaxPage].
// Normalize rejects larger pages with ErrPageOverflow. Zero selects 10000;
// a negative maximum yields ErrInvalidConfig rather than disabling the cap.
//
// Example:
//
//	config := pagination.NewConfig(pagination.WithMaxPage(500))
//	_ = config.MaxPage // 500.
func WithMaxPage(maxPage int) Option {
	return func(config *Config) {
		config.MaxPage = maxPage
	}
}

// CursorOption changes one [CursorConfig] setting.
// Options run synchronously in order on a fresh value. CursorParams.Normalize
// completes zero fields and validates maxima; a nil option entry panics.
//
// Example:
//
//	options := []pagination.CursorOption{pagination.WithCursorMaxPerPage(50)}
//	config := pagination.NewCursorConfig(options...)
//	_ = config.MaxPerPage
type CursorOption func(*CursorConfig)

// NewCursorConfig returns the default configuration with opts applied in order.
// It does not normalize or validate the result; CursorParams.Normalize owns
// those checks. Nil entries panic; expanded nil/empty slices make no changes.
//
// Example:
//
//	config := pagination.NewCursorConfig(pagination.WithCursorDefaultPerPage(10))
//	_ = config.DefaultPerPage // 10.
func NewCursorConfig(opts ...CursorOption) CursorConfig {
	config := defaultCursorConfig()

	for _, opt := range opts {
		opt(&config)
	}

	return config
}

// WithCursorConfig replaces the whole configuration. Later options still apply.
// The struct is captured by value. Zero fields are completed by Normalize,
// which also rejects negative maxima; construction itself does not validate.
//
// Example:
//
//	config := pagination.NewCursorConfig(pagination.WithCursorConfig(pagination.CursorConfig{MaxPerPage: 50}))
//	_ = config.MaxPerPage // 50.
func WithCursorConfig(config CursorConfig) CursorOption {
	return func(target *CursorConfig) {
		*target = config
	}
}

// WithCursorDefaultPerPage sets [CursorConfig.DefaultPerPage].
// Normalize uses this fallback for an omitted/zero size, then raises it to one
// if negative and caps it at MaxPerPage. Zero uses the built-in default of 25.
//
// Example:
//
//	config := pagination.NewCursorConfig(pagination.WithCursorDefaultPerPage(10))
//	_ = config.DefaultPerPage // 10.
func WithCursorDefaultPerPage(defaultPerPage int) CursorOption {
	return func(config *CursorConfig) {
		config.DefaultPerPage = defaultPerPage
	}
}

// WithCursorMaxPerPage sets [CursorConfig.MaxPerPage].
// Normalize caps requested sizes at this maximum. Zero uses 100; a negative
// maximum yields ErrInvalidConfig rather than removing the cap.
//
// Example:
//
//	config := pagination.NewCursorConfig(pagination.WithCursorMaxPerPage(50))
//	_ = config.MaxPerPage // 50.
func WithCursorMaxPerPage(maxPerPage int) CursorOption {
	return func(config *CursorConfig) {
		config.MaxPerPage = maxPerPage
	}
}
