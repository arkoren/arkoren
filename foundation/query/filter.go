package query

// PredicateBuilderFunc edits only predicates in a group or scope. Group
// callbacks share one restricted draft; each Scope callback has its own.
// The alias preserves identity with func(*PredicateBuilder), including slices.
//
// Example:
//
//	policies := []query.PredicateBuilderFunc{
//		func(predicate *query.PredicateBuilder) {
//			predicate.Where("active", true)
//		},
//	}
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Scope(policies...)
//	})
//	_ = users
type PredicateBuilderFunc = func(*PredicateBuilder)

// PredicateBuilder declares only filters inside scopes and predicate groups.
// It has no source, selection, mutation, ordering, or compilation methods.
// Methods return this draft; publication detaches retained nested declarations.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.Where("active", true)
//			predicate.OrWhere("role", "admin")
//		})
//	})
//	_ = users
type PredicateBuilder struct {
	// draft stores predicate state without exposing root-only methods.
	draft Builder
}

// WhereRaw adds the predicate described by [Builder.WhereRaw].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereRaw("lower(email) = ?", "ada@example.test")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereRaw(expression string, args ...any) *PredicateBuilder {
	predicate.draft.addRaw(booleanAnd, expression, args)

	return predicate
}

// OrWhereRaw adds the predicate described by [Builder.OrWhereRaw].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereRaw("lower(email) = ?", "ada@example.test")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereRaw(expression string, args ...any) *PredicateBuilder {
	predicate.draft.addRaw(booleanOr, expression, args)

	return predicate
}

// Where adds the predicate described by [Builder.Where].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.Where("active", true)
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) Where(column string, args ...any) *PredicateBuilder {
	predicate.draft.addComparison(booleanAnd, column, args...)

	return predicate
}

// OrWhere adds the predicate described by [Builder.OrWhere].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhere("role", "admin")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhere(column string, args ...any) *PredicateBuilder {
	predicate.draft.addComparison(booleanOr, column, args...)

	return predicate
}

// WhereNull adds the predicate described by [Builder.WhereNull].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereNull("deleted_at")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereNull(column string) *PredicateBuilder {
	predicate.draft.addNull(booleanAnd, column, false)

	return predicate
}

// OrWhereNull adds the predicate described by [Builder.OrWhereNull].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereNull("deleted_at")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereNull(column string) *PredicateBuilder {
	predicate.draft.addNull(booleanOr, column, false)

	return predicate
}

// WhereNotNull adds the predicate described by [Builder.WhereNotNull].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereNotNull("email")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereNotNull(column string) *PredicateBuilder {
	predicate.draft.addNull(booleanAnd, column, true)

	return predicate
}

// OrWhereNotNull adds the predicate described by [Builder.OrWhereNotNull].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereNotNull("email")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereNotNull(column string) *PredicateBuilder {
	predicate.draft.addNull(booleanOr, column, true)

	return predicate
}

// WhereIn adds the predicate described by [Builder.WhereIn].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereIn("role", "admin", "editor")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereIn[T any](column string, values ...T) *PredicateBuilder {
	predicate.draft.addIn(booleanAnd, column, anyValues(values), false)

	return predicate
}

// OrWhereIn adds the predicate described by [Builder.OrWhereIn].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereIn("role", "admin", "editor")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereIn[T any](column string, values ...T) *PredicateBuilder {
	predicate.draft.addIn(booleanOr, column, anyValues(values), false)

	return predicate
}

// WhereNotIn adds the predicate described by [Builder.WhereNotIn].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereNotIn("role", "guest")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereNotIn[T any](column string, values ...T) *PredicateBuilder {
	predicate.draft.addIn(booleanAnd, column, anyValues(values), true)

	return predicate
}

// OrWhereNotIn adds the predicate described by [Builder.OrWhereNotIn].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereNotIn("role", "guest")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereNotIn[T any](column string, values ...T) *PredicateBuilder {
	predicate.draft.addIn(booleanOr, column, anyValues(values), true)

	return predicate
}

// WhereBetween adds the predicate described by [Builder.WhereBetween].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereBetween("score", 1, 10)
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereBetween(column string, minimum, maximum any) *PredicateBuilder {
	predicate.draft.addBetween(booleanAnd, column, minimum, maximum, false)

	return predicate
}

// OrWhereBetween adds the predicate described by [Builder.OrWhereBetween].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereBetween("score", 1, 10)
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereBetween(column string, minimum, maximum any) *PredicateBuilder {
	predicate.draft.addBetween(booleanOr, column, minimum, maximum, false)

	return predicate
}

// WhereNotBetween adds the predicate described by [Builder.WhereNotBetween].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereNotBetween("score", 1, 10)
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereNotBetween(column string, minimum, maximum any) *PredicateBuilder {
	predicate.draft.addBetween(booleanAnd, column, minimum, maximum, true)

	return predicate
}

// OrWhereNotBetween adds the predicate described by [Builder.OrWhereNotBetween].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereNotBetween("score", 1, 10)
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereNotBetween(column string, minimum, maximum any) *PredicateBuilder {
	predicate.draft.addBetween(booleanOr, column, minimum, maximum, true)

	return predicate
}

// WhereColumn adds the predicate described by [Builder.WhereColumn].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereColumn("created_at", "<", "updated_at")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereColumn(first string, args ...string) *PredicateBuilder {
	predicate.draft.addColumn(booleanAnd, first, args...)

	return predicate
}

// OrWhereColumn adds the predicate described by [Builder.OrWhereColumn].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereColumn("created_at", "<", "updated_at")
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereColumn(first string, args ...string) *PredicateBuilder {
	predicate.draft.addColumn(booleanOr, first, args...)

	return predicate
}

// WhereGroup adds the predicate described by [Builder.WhereGroup].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereGroup(func(predicate *query.PredicateBuilder) {
//				predicate.Where("active", true)
//			})
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereGroup(configure ...PredicateBuilderFunc) *PredicateBuilder {
	predicate.draft.addGroup(booleanAnd, false, configure...)

	return predicate
}

// OrWhereGroup adds the predicate described by [Builder.OrWhereGroup].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereGroup(func(predicate *query.PredicateBuilder) {
//				predicate.Where("active", true)
//			})
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereGroup(configure ...PredicateBuilderFunc) *PredicateBuilder {
	predicate.draft.addGroup(booleanOr, false, configure...)

	return predicate
}

// WhereNotGroup adds the predicate described by [Builder.WhereNotGroup].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereNotGroup(func(predicate *query.PredicateBuilder) {
//				predicate.Where("active", true)
//			})
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereNotGroup(configure ...PredicateBuilderFunc) *PredicateBuilder {
	predicate.draft.addGroup(booleanAnd, true, configure...)

	return predicate
}

// OrWhereNotGroup adds the predicate described by [Builder.OrWhereNotGroup].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereNotGroup(func(predicate *query.PredicateBuilder) {
//				predicate.Where("active", true)
//			})
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereNotGroup(configure ...PredicateBuilderFunc) *PredicateBuilder {
	predicate.draft.addGroup(booleanOr, true, configure...)

	return predicate
}

// WhereExists adds the predicate described by [Builder.WhereExists].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereExists(query.Table("members"))
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereExists(sub Query) *PredicateBuilder {
	predicate.draft.addExists(booleanAnd, sub, false)

	return predicate
}

// WhereNotExists adds the predicate described by [Builder.WhereNotExists].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.WhereNotExists(query.Table("bans"))
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) WhereNotExists(sub Query) *PredicateBuilder {
	predicate.draft.addExists(booleanAnd, sub, true)

	return predicate
}

// OrWhereExists adds the predicate described by [Builder.OrWhereExists].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereExists(query.Table("members"))
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereExists(sub Query) *PredicateBuilder {
	predicate.draft.addExists(booleanOr, sub, false)

	return predicate
}

// OrWhereNotExists adds the predicate described by [Builder.OrWhereNotExists].
// It changes this restricted draft and returns the same receiver; validation
// errors remain sticky until the containing query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.OrWhereNotExists(query.Table("bans"))
//		})
//	})
//	_ = users
func (predicate *PredicateBuilder) OrWhereNotExists(sub Query) *PredicateBuilder {
	predicate.draft.addExists(booleanOr, sub, true)

	return predicate
}
