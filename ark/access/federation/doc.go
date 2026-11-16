// Package federation connects external identities, such as a Google or GitHub
// account verified by ark/oauth, to local users. It owns one table of links and
// the rules that map a verified identity to a subject; it never imports the
// protocol module, because its input is the plain [auth.External] that module
// hands to an [auth.Resolver].
//
// # Rules
//
//   - A first login with an unknown identity creates a user only when
//     [Builder.SignUp] is on (it is off by default) and then through
//     [Builder.Create]; otherwise it fails with [ErrNoAccount] (403) and
//     creates nothing. Logging in with a connected identity and connecting a
//     provider while logged in never depend on it.
//   - It never links by email. An address that belongs to an existing user makes
//     [Builder.Create] fail (a 409 with the directory); the user logs in and
//     connects the provider from their settings. The one exception is
//     [Builder.TakeOver], which the facade sets: a first login whose email the
//     provider verified may be handed an existing user whose email never was, so
//     whoever registered an address first cannot keep its owner out.
//   - A user holds one connection per issuer, and an external identity belongs to
//     at most one user. Concurrent first logins of one identity create one user:
//     the loser rolls back and returns the winner's subject.
//   - Connecting is deliberate. [Links.ConnectHandler] (a POST, fresh login
//     required) stores a connect intent in the session for ten minutes and
//     redirects to the provider; only a callback in that session links to the
//     logged-in user, and the intent is spent either way.
//   - Disconnecting the last connection is refused unless [Builder.Fallback] says
//     the user can log in another way, such as a password.
//   - A silent re-login through a provider adds a federated factor that is not a
//     step-up method, so it never makes a user who has a password or an
//     authenticator fresh; a federated-only user is fresh right after logging in.
//
// # Storage
//
// The package never runs DDL. [Links.Migrations] describes the table
// "federation_links" (prefixed "<instance>_" for a named [Builder.Instance]) in
// the group "arkoren.dev:ark/access:federation", and the application's migrator
// executes it. Issuer, external identifier and subject compare byte for byte.
// No email or profile data is stored. Every package of the module assumes the same
// [database.Database]; the creating transaction of a first login joins the
// transaction carried by the context.
//
// Example:
//
//	links, err := federation.NewLinks(provider, db, func(links *federation.Builder) {
//		links.SignUp(true).Create(createUser).Fallback(passwords.Has)
//	})
//	if err != nil {
//		return err
//	}
//	web.Get("/auth/google/callback", google.CallbackHandler(guard, links.Resolve))
//	web.Post("/connections/{provider}", links.ConnectHandler(guard, map[string]string{"google": "/auth/google"}))
package federation
