package query_test

import (
	"fmt"

	"arkoren.dev/foundation/query"
)

// ExamplePredicateBuilderFunc uses the restricted callback alias for a protected
// scope, leaving the whole user disjunction inside its own AND boundary.
func ExamplePredicateBuilderFunc() {
	policies := []query.PredicateBuilderFunc{
		func(predicate *query.PredicateBuilder) {
			predicate.Where("active", true)
		},
	}

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("role", "admin")
		statement.OrWhere("role", "owner")
		statement.Scope(policies...)
	}).BuildFor(query.Postgres)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())
	// Output:
	// SELECT * FROM "users" WHERE ("role" = $1 OR "role" = $2) AND ("active" = $3)
	// [admin owner true]
}
