package postgressearch

// settings holds the optional instance identity. Empty uses the default tables
// and migration group; nonempty identities are validated before publication.
type settings struct {
	// instance prefixes table names and qualifies the literal migration group.
	instance string
}

// Builder chooses the PostgreSQL search instance during NewFrom. The application
// owns the database and executes Migrations; construction never runs DDL.
// Callbacks run directly once in order, even on invalid inputs. Nil entries
// panic; expanded nil or empty slices omit edits. Retained drafts are detached.
//
// Example:
//
//	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
//		engine.Instance("catalog")
//	})
//	if err != nil {
//		return err
//	}
//	return migrator.Add(driver.Migrations()...)
type Builder struct {
	// config is the draft's private instance policy.
	config settings
}

// Instance replaces the instance identity and returns this draft. Empty selects
// default tables; otherwise NewFrom requires at most 40 bytes of [a-z0-9_].
// Renaming an existing instance is unsupported because it changes table names.
// Leading digits are valid: generated SQL identifiers are quoted, while physical
// names, migration groups, and advisory-lock identities retain their literal bytes.
//
// Example:
//
//	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
//		engine.Instance("catalog")
//	})
//	if err != nil {
//		return err
//	}
//	return migrator.Add(driver.Migrations()...)
func (engine *Builder) Instance(value string) *Builder {
	engine.config.instance = value

	return engine
}

// BuilderFunc edits one NewFrom construction draft. Policies run synchronously
// once in order, including on invalid inputs; nil entries panic. The published
// instance is detached from retained drafts and the database remains borrowed.
//
// Example:
//
//	var configure postgressearch.BuilderFunc = func(engine *postgressearch.Builder) {
//		engine.Instance("2026_catalog")
//	}
//	driver, err := postgressearch.NewFrom(db, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver.Migrations()
type BuilderFunc = func(*Builder)
