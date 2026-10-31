// Package gate authorizes the authenticated user with plain Go rules.
//
// A rule is a func(ctx, U) (bool, error): true allows, false yields
// [ErrForbidden], an error is returned as is. [Authorize] runs rules in
// order and stops at the first denial; [Require] does so as a middleware.
// There are no combinators and no registry: compose rules with ordinary Go
// if and ||.
//
// The free functions publish nothing. To observe denials, build a [Gate]
// with [New] and [Builder.Events] and call its [Gate.Authorize] and
// [Gate.Require] methods, which behave the same.
//
// # Wiring
//
//	r := router.New()
//	r.Use(session.Middleware(store))
//	r.Use(csrf.Middleware())
//	identity := auth.NewSessionIdentity(loadUser)
//	r.Use(auth.Middleware(identity.Authenticator()))
//
//	r.Post("/login", login) // see package auth
//	r.With(auth.Require[User]()).Get("/me", me)
//
//	policy := &Policy{db: db}
//	r.With(gate.Require(policy.Admin)).Get("/admin", dashboard)
//	r.Put("/posts/{id}", update) // checks policy.CanEdit in the handler
//
// # Rules
//
// Rules are plain functions, or methods on a struct that holds the database;
// avoid globals. A rule about one resource closes over its identifier, which
// is only known inside the handler, so check it there with [Authorize]:
//
//	type Policy struct{ db *database.Database }
//
//	func (policy *Policy) Admin(_ context.Context, user User) (bool, error) {
//		return user.Role == "admin", nil
//	}
//	func (policy *Policy) CanEdit(postID int) func(context.Context, User) (bool, error) {
//		return func(ctx context.Context, user User) (bool, error) {
//			if user.Role == "admin" {
//				return true, nil
//			}
//			_, err := policy.db.Find[Post](ctx, query.Table("posts", func(statement *query.Builder) {
//				statement.Where("id", postID)
//				statement.Where("author_id", user.ID)
//			}))
//			if errors.Is(err, database.ErrNoRows) {
//				return false, nil
//			}
//			return err == nil, err
//		}
//	}
//	func update(w http.ResponseWriter, r *http.Request) error {
//		id, err := request.Param[int](r, "id")
//		if err != nil {
//			return err
//		}
//		if err := gate.Authorize(r.Context(), policy.CanEdit(id)); err != nil {
//			return err
//		}
//		// ...
//	}
//
// # Resource access belongs in queries
//
// Abilities say what a user may do; which rows they may see is a query
// concern. Put it in a scope so a handler cannot forget it:
//
//	func VisibleTo(user User) query.Scope {
//		return func(predicate *query.PredicateBuilder) {
//			predicate.Where("author_id", user.ID)
//		}
//	}
//	post, err := db.Find[Post](ctx, query.Table("posts", func(statement *query.Builder) {
//		statement.Where("id", id)
//		statement.Scope(VisibleTo(user))
//	}))
//
// # Invariants need conditional writes
//
// A check followed by a write races with other requests. Make the write
// conditional, and lock the parent row when the invariant spans rows
// (lock the group row FOR UPDATE inside a transaction):
//
//	n, err := db.Exec(ctx, query.Raw(
//		`DELETE FROM members WHERE group_id = ? AND user_id = ?
//	 AND EXISTS (SELECT 1 FROM members WHERE group_id = ? AND user_id <> ?)`,
//		groupID, userID, groupID, userID))
//	if err == nil && n == 0 {
//		return problem.Conflict() // last member
//	}
//
// # Events
//
// With [Builder.Events], a [Gate] publishes [Denied] ("http.gate.denied") when a
// rule returns false, with the request method (empty for [Gate.Authorize]) and
// the zero-based index of the denying rule. Paths, users, and causes are omitted;
// event contexts still retain request context values. Clients control the rate
// of denials, so listeners must be cheap and a rate limiter should sit in front
// of the gate.
//
// # Which status
//
//   - 401: no user ([auth.ErrUnauthenticated]).
//   - 403: the user is known and the action is not allowed, and the
//     resource's existence is not secret.
//   - 404: the user must not learn the resource exists; a scoped query that
//     finds no row returns this.
//   - 409: the request is allowed but conflicts with current state, such as
//     a conditional write affecting 0 rows.
package gate
