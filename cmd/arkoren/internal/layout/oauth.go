package layout

import (
	"errors"
	"io/fs"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// oauthClient is one sign-in provider of `arkoren add ark oauth`.
type oauthClient struct {
	// name is the command value, the client's login method name and its route
	// below the identity mount: /auth/<name>/callback.
	name string
	// identifier is the bootstrap provider the generated file declares, and the
	// template field that tells iam.go to pass it to identity.Provider.
	identifier string
	// credentials lists the suffixes of the NAME_<suffix> environment variables
	// only the reader can supply, such as CLIENT_ID.
	credentials []string
	// signUp names the settings fragment provider that lets a first login of
	// this provider create a user, or is empty. Only GitHub has one, as before
	// the providers moved to add ark oauth: sign-up creates accounts, so it
	// stays the application's decision for every other provider, whose email
	// claims are opt-in (Microsoft, OIDC) or may be a private relay address
	// (Apple). Turn it on by writing a fragment like GitHubSignUp.
	signUp string
}

// appleKeyBinding is the variable whose value references the ECDSA P-256 key
// Apple issued, which only the Apple client resolves.
const appleKeyBinding = "APPLE_KEY"

// oauthClients lists the providers `add ark oauth` supports, in the order the
// usage message presents them.
var oauthClients = []oauthClient{
	{name: "google", identifier: "Google", credentials: []string{"CLIENT_ID", "CLIENT_SECRET"}},
	{name: "github", identifier: "GitHub", credentials: []string{"CLIENT_ID", "CLIENT_SECRET"}, signUp: "GitHubSignUp"},
	{name: "microsoft", identifier: "Microsoft", credentials: []string{"CLIENT_ID", "CLIENT_SECRET"}},
	{name: "apple", identifier: "Apple", credentials: []string{"CLIENT_ID", "TEAM_ID", "KEY_ID"}},
	{name: "oidc", identifier: "OIDC", credentials: []string{"ISSUER_URL", "CLIENT_ID", "CLIENT_SECRET"}},
}

// OAuthProviders returns the provider names `arkoren add ark oauth` accepts, in
// the order its usage presents them. The slice is new on every call.
//
// Example:
//
//	usage := "PROVIDER: " + strings.Join(layout.OAuthProviders(), ", ")
func OAuthProviders() []string {
	names := make([]string, len(oauthClients))

	for index, client := range oauthClients {
		names[index] = client.name
	}

	return names
}

// findOAuthClient returns the client called name.
func findOAuthClient(name string) (oauthClient, bool) {
	index := slices.IndexFunc(oauthClients, func(client oauthClient) bool { return client.name == name })

	if index < 0 {
		return oauthClient{}, false
	}

	return oauthClients[index], true
}

// variable returns the name of the client's environment variable with suffix,
// such as GOOGLE_CLIENT_ID.
func (client oauthClient) variable(suffix string) string {
	return strings.ToUpper(client.name) + "_" + suffix
}

// redirect returns the default of the client's redirect URL variable: its
// callback route below the /auth mount on the server at baseURL.
func (client oauthClient) redirect(baseURL string) envVariable {
	return envVariable{name: client.variable("REDIRECT_URL"), value: baseURL + "/auth/" + client.name + "/callback"}
}

// providers returns the bootstrap providers one identity.Provider call passes
// for the client: the client, then its settings fragment.
func (client oauthClient) providers() []string {
	if client.signUp == "" {
		return []string{client.identifier}
	}

	return []string{client.identifier, client.signUp}
}

// steps lists what only the reader can do for the client: register the callback
// URL with the provider and supply its credentials, and for Apple provision the
// signing key.
func (client oauthClient) steps(baseURL string) []console.Step {
	variables := make([]string, len(client.credentials))

	for index, suffix := range client.credentials {
		variables[index] = client.variable(suffix)
	}

	steps := []console.Step{{Text: "Register " + client.redirect(baseURL).value + " as the redirect URL of your " + client.identifier +
		" OAuth client, then set " + strings.Join(variables, ", ") + " in .env"}}

	switch client.name {
	case "apple":
		file := bindingFile(appleKeyBinding)

		steps = append(steps, console.Step{Text: "Save the .p8 signing key Apple issued for your Services ID as " + file + ", which " + appleKeyBinding + "=file:" + file +
			" names in .env, and check it with `arkoren key check signing " + file + "`; `arkoren key generate signing --out " + file + "` creates a throwaway key for local tests"})
	case "microsoft":
		steps = append(steps, console.Step{Text: "Set MICROSOFT_TENANT to a tenant ID, organizations or consumers to restrict who may sign in; it defaults to common"})
	}

	return steps
}

// AddOAuth plans the OAuth sign-in clients of ark/oauth for providers, each one
// of google, github, microsoft, apple or oidc: internal/bootstrap/<provider>.go
// with a provider named for it (Google, GitHub, Microsoft, Apple, OIDC) that
// builds the client from its environment, registers its startup and returns it;
// the OAuthKey provider once, unless internal/bootstrap declares one; and, for
// GitHub, the GitHubSignUp settings fragment that lets a first GitHub login
// create a user. The other providers log in existing users only.
//
// When internal/bootstrap declares the IAM provider, the plan inserts
// identity.Provider(compose.Use(<Client>)[, compose.Use(<Client>SignUp)]) into
// the callback IAM passes to iam.New, and leaves a step only when that is
// impossible. Without IAM, only the client is added and a step says to run
// `arkoren add ark iam`, which wires every client declared by then. It appends
// to .env each client's <PROVIDER>_REDIRECT_URL, the callback below the /auth
// mount on the address of HTTP_ADDR, when .env lacks it; secrets, key paths and
// client IDs are left to the reader and listed in the steps.
//
// It returns a usage error for no provider, an unknown or repeated one, or a
// client internal/bootstrap already declares. It never changes the project.
//
// Example:
//
//	plan, err := project.AddOAuth([]string{"google", "github"}, templates)
func (project *Project) AddOAuth(providers []string, templates fs.FS) (*Plan, error) {
	clients, err := project.oauthSelection(providers)

	if err != nil {
		return nil, err
	}

	plan := &Plan{}

	for _, client := range clients {
		if err := project.renderOAuth(plan, templates, client); err != nil {
			return nil, err
		}
	}

	written, err := project.renderPrerequisites(plan, templates, []arkPrerequisite{secretsPrerequisite, {"OAuthKey", "oauth_key", "OAUTH_KEY"}})

	if err != nil {
		return nil, err
	}

	closing, err := project.planProviders(plan, clients)

	if err != nil {
		return nil, err
	}

	baseURL, err := project.planEnv(plan, func(baseURL string) []envVariable {
		var variables []envVariable

		for _, client := range clients {
			variables = append(variables, client.redirect(baseURL))

			if client.name == "apple" {
				variables = append(variables, bindingVariable(appleKeyBinding))
			}
		}

		return append(variables, bindings(written)...)
	})

	if err != nil {
		return nil, err
	}

	for _, client := range clients {
		plan.Steps = append(plan.Steps, client.steps(baseURL)...)
	}

	plan.Steps = append(plan.Steps, keySteps(written)...)
	plan.Steps = append(plan.Steps, closing)

	return plan, project.planRequires(plan)
}

// oauthSelection resolves the provider names to clients, in order. It returns
// a usage error for no provider, an unknown or repeated name, or a client that
// internal/bootstrap already declares.
func (project *Project) oauthSelection(names []string) ([]oauthClient, error) {
	if len(names) == 0 {
		return nil, console.UsageError("add ark oauth needs at least one provider")
	}

	var clients []oauthClient

	for _, name := range names {
		client, found := findOAuthClient(name)

		if !found {
			return nil, console.UsageError("unknown OAuth provider %q; add ark oauth supports %s", name, strings.Join(OAuthProviders(), ", "))
		}

		if slices.ContainsFunc(clients, func(chosen oauthClient) bool { return chosen.name == name }) {
			return nil, console.UsageError("OAuth provider %q is listed twice", name)
		}

		if project.declares("internal/bootstrap", client.identifier) {
			return nil, console.UsageError("internal/bootstrap already declares %s", client.identifier)
		}

		clients = append(clients, client)
	}

	return clients, nil
}

// renderOAuth appends the client's provider file to plan, and its settings
// fragment when it has one.
func (project *Project) renderOAuth(plan *Plan, templates fs.FS, client oauthClient) error {
	if err := project.render(plan, templates, "add/ark/oauth/"+client.name+".go", "internal/bootstrap/"+client.name+".go", nil); err != nil {
		return err
	}

	if client.signUp == "" {
		return nil
	}

	return project.render(plan, templates, "add/ark/iam/github_sign_up.go", "internal/bootstrap/github_sign_up.go", nil)
}

// planProviders plans the insertion of the clients into the IAM provider and
// returns the step that closes the plan: starting the server when the clients
// are wired, running `add ark iam` when IAM does not exist yet, or the line to
// write when IAM exists but cannot take the insertion.
func (project *Project) planProviders(plan *Plan, clients []oauthClient) (console.Step, error) {
	existing := project.declaringSource("internal/bootstrap", "IAM")

	if existing == nil {
		return console.Step{Text: "Serve the sign-in routes with the identity module, which wires every client declared by then", Command: "arkoren add ark iam"}, nil
	}

	groups := make([][]string, len(clients))
	identifiers := make([]string, len(clients))

	for index, client := range clients {
		groups[index] = client.providers()
		identifiers[index] = "identity.Provider(compose.Use(" + client.identifier + "))"
	}

	err := project.planEdit(plan, existing.rel, func(content []byte) ([]byte, []string, error) {
		return addProviders(existing.rel, content, groups)
	})

	if errors.Is(err, errNoInsertionPoint) {
		return console.Step{Text: "Pass the clients to the identity module in " + existing.rel + " with " + strings.Join(identifiers, " and ")}, nil
	}

	return console.Step{Text: "Start the server: the sign-in routes are served under /auth", Command: "make run"}, err
}
