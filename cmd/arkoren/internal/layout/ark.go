package layout

import (
	"cmp"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// modulePins is the version of each opt-in Arkoren module that the templates of
// `arkoren add ark` import. The CLI fetches exactly these versions with go get
// instead of letting go mod tidy resolve the latest release, so a generated
// provider always builds against the modules it was written for. Bump an entry
// in the same change as any template or API change that needs a newer module,
// using the version the release plan proposes. Core is not listed: the project's
// own core requirement, set by `arkoren new`, already provides it.
var modulePins = map[string]string{
	"arkoren.dev/ark/iam":         "v0.5.0",
	"arkoren.dev/ark/oauth":       "v0.7.0",
	"arkoren.dev/driver/aes":      "v0.1.0",
	"arkoren.dev/driver/bcrypt":   "v0.1.0",
	"arkoren.dev/driver/sqlite":   "v0.1.0",
	"arkoren.dev/foundation/key":  "v0.1.1",
	"arkoren.dev/foundation/totp": "v0.2.0",
}

// IAMFeatures lists the values `arkoren add ark iam --with` accepts, in the order
// the generated code presents them.
var IAMFeatures = []string{"reset", "verification", "two-factor", "tokens", "permissions", "account", "google", "github"}

// iamFeatureKeys maps each --with value to the template field that turns its
// part of the generated code on.
var iamFeatureKeys = map[string]string{
	"reset": "Reset", "verification": "Verification", "two-factor": "TwoFactor", "tokens": "Tokens",
	"permissions": "Permissions", "account": "Account", "google": "Google", "github": "GitHub",
}

// iamFragment is a provider file of `add ark iam` that exists only when its
// feature is selected: the feature value, the template below add/ark, the file
// it writes in internal/bootstrap (without .go) and the provider it declares.
type iamFragment struct {
	feature  string
	template string
	file     string
	provider string
}

// iamFragments are the provider files `add ark iam` writes beside iam.go, in
// presentation order.
var iamFragments = []iamFragment{
	{"reset", "iam/password_reset", "password_reset", "PasswordReset"},
	{"verification", "iam/email_verification", "email_verification", "EmailVerification"},
	{"tokens", "iam/token_scopes", "token_scopes", "TokenScopes"},
	{"permissions", "iam/roles", "roles", "Roles"},
	{"account", "iam/account_view", "account_view", "AccountView"},
	{"google", "oauth/google", "google", "Google"},
	{"github", "oauth/github", "github", "GitHub"},
	{"github", "iam/github_sign_up", "github_sign_up", "GitHubSignUp"},
}

// arkPrerequisite is a shared provider that generated files use through
// compose.Use and that `add ark` writes when internal/bootstrap does not declare
// it yet: the provider name and its file, which is also its template below
// add/ark/provider.
type arkPrerequisite struct {
	provider string
	file     string
}

// IAMOptions selects what [Project.AddIAM] creates.
type IAMOptions struct {
	// With lists the optional features to enable, a subset of [IAMFeatures]:
	// reset (password reset and registration), verification (email
	// verification), two-factor, tokens, permissions, account (GET /account),
	// google and github (sign-in clients). Password login is always on.
	With []string
	// Process names the process root internal/bootstrap/<process> that runs the
	// identity jobs, which send the module's emails. Empty selects "server". It
	// is unused when neither reset nor verification is selected. The identity
	// routes always go into the server process.
	Process string
}

// iamSelection is the validated feature set of one [Project.AddIAM] call.
type iamSelection struct {
	with []string
}

// has reports whether feature was selected.
func (selection iamSelection) has(feature string) bool {
	return slices.Contains(selection.with, feature)
}

// jobs reports whether the selection sends email, which needs the queue, the
// mailer and the IAMJobs provider.
func (selection iamSelection) jobs() bool {
	return selection.has("reset") || selection.has("verification")
}

// oauth reports whether the selection includes a sign-in client.
func (selection iamSelection) oauth() bool {
	return selection.has("google") || selection.has("github")
}

// data returns the template fields of the generated files: one "true" per
// selected feature, Jobs when email is sent, and the issuer name authenticator
// apps show.
func (selection iamSelection) data(issuer string) map[string]string {
	data := map[string]string{"Issuer": issuer}

	for _, feature := range selection.with {
		data[iamFeatureKeys[feature]] = "true"
	}

	if selection.jobs() {
		data["Jobs"] = "true"
	}

	return data
}

// fragments returns the fragment provider files of the selected features.
func (selection iamSelection) fragments() []iamFragment {
	return slices.DeleteFunc(slices.Clone(iamFragments), func(fragment iamFragment) bool {
		return !selection.has(fragment.feature)
	})
}

// providers returns the providers the generated files declare, which must not
// exist yet.
func (selection iamSelection) providers() []string {
	providers := []string{"IAM", "IAMRouter"}

	if selection.jobs() {
		providers = append(providers, "IAMJobs")
	}

	for _, fragment := range selection.fragments() {
		providers = append(providers, fragment.provider)
	}

	return providers
}

// prerequisites returns the shared providers the generated code uses, in
// dependency order.
func (selection iamSelection) prerequisites() []arkPrerequisite {
	required := []arkPrerequisite{{"Database", "database"}, {"Cache", "cache"}, {"Hasher", "hasher"}}

	if selection.jobs() {
		required = append(required,
			arkPrerequisite{"LinkSigner", "link_signer"}, arkPrerequisite{"Mailer", "mailer"},
			arkPrerequisite{"Queue", "queue"}, arkPrerequisite{"Dispatcher", "dispatcher"},
			arkPrerequisite{"Consumer", "consumer"}, arkPrerequisite{"Work", "work"})
	}

	if selection.has("two-factor") {
		required = append(required, arkPrerequisite{"TwoFactorKey", "two_factor_key"})
	}

	if selection.oauth() {
		required = append(required, arkPrerequisite{"OAuthKey", "oauth_key"})
	}

	return required
}

// steps lists what `add ark iam` cannot do itself, as next steps without commands.
func (selection iamSelection) steps() []console.Step {
	steps := []console.Step{
		{Text: "Add compose.Require(migrator.Add(compose.Use(IAM).Migrations()...)) to the Migrator provider; the migrate process builds IAM, " +
			"so it needs the same environment as the server (database, keys, URLs) before it runs the migrations"},
		{Text: "Set DATABASE_URL, for example file:app.db for SQLite"},
	}

	if selection.has("reset") {
		steps = append(steps, console.Step{Text: "Set PASSWORD_RESET_URL to the absolute address of the page that reads the reset link from the URL fragment"})
	}

	if selection.has("verification") {
		steps = append(steps, console.Step{Text: "Set EMAIL_VERIFY_URL to the absolute address of the page that reads the verification link from the URL fragment"})
	}

	if selection.jobs() {
		steps = append(steps, keyStep("LinkSigner", "LINK_KEY_FILE"),
			console.Step{Text: "IAMJobs sends the emails in the process whose Compose lists it; with the memory queue that is the process that serves HTTP, " +
				"so move IAMJobs and Work to a worker when you swap the Queue driver, and deploy workers before enabling a new email feature"},
			console.Step{Text: "Replace the logging Mailer with a delivering driver before production"})
	}

	if selection.has("two-factor") {
		steps = append(steps, keyStep("TwoFactorKey", "TWO_FACTOR_KEY_FILE"))
	}

	if selection.oauth() {
		steps = append(steps, keyStep("OAuthKey", "OAUTH_KEY_FILE"))
	}

	for _, provider := range []string{"google", "github"} {
		if selection.has(provider) {
			steps = append(steps, oauthEnvironmentStep(provider))
		}
	}

	return steps
}

// keyStep tells how to provision the key file one provider loads. It has no
// command because the file name is the reader's choice.
func keyStep(provider, variable string) console.Step {
	return console.Step{Text: "Create the " + provider + " key file with `arkoren key generate shared --out FILE` and set " + variable +
		" to it, the same file for every process"}
}

// oauthEnvironmentStep names the environment variables of one sign-in client.
func oauthEnvironmentStep(provider string) console.Step {
	upper := strings.ToUpper(provider)

	return console.Step{Text: "Set " + upper + "_CLIENT_ID, " + upper + "_CLIENT_SECRET and " + upper + "_REDIRECT_URL (the absolute address of the " +
		provider + " callback route) in the environment of every process that serves HTTP"}
}

// AddIAM plans the identity module of ark/iam: internal/bootstrap/iam.go with
// the IAM, IAMRouter and, when reset or verification is selected, IAMJobs
// providers; one fragment provider file per selected feature (PasswordReset,
// EmailVerification, TokenScopes, Roles, AccountView, and for the sign-in
// clients Google, GitHub and GitHubSignUp), each returning the construction
// callback the facade passes to its package; and every shared provider the
// generated code uses that internal/bootstrap does not declare yet (Database,
// Cache, Hasher, and as selected LinkSigner, Mailer, Queue, Dispatcher,
// Consumer, Work, TwoFactorKey, OAuthKey), written with local defaults. A
// provider that already exists, found by name, is left alone and used.
//
// It inserts compose.Use(bootstrap.IAMRouter) into the Compose of the server
// process and, for the jobs, compose.Use(bootstrap.IAMJobs) and
// compose.Use(bootstrap.Work) into the Compose of options.Process, skipping
// providers the Compose already uses. The plan lists the modules it needs with
// their pinned versions ([Plan.Requires]) and the steps it cannot do itself
// ([Plan.Steps]): adding the migrations to the Migrator, and the environment and
// key files.
//
// It returns a usage error for an unknown feature, for a provider the module
// declares that internal/bootstrap already declares, for an existing target
// file, or when a process has no Compose to edit. It never changes the project.
//
// Example:
//
//	plan, err := project.AddIAM(layout.IAMOptions{With: []string{"reset", "tokens"}}, templates)
func (project *Project) AddIAM(options IAMOptions, templates fs.FS) (*Plan, error) {
	selection, err := newIAMSelection(options.With)

	if err != nil {
		return nil, err
	}

	routes, worker, err := project.iamProcesses(selection, cmp.Or(options.Process, "server"))

	if err != nil {
		return nil, err
	}

	plan := &Plan{}

	if err := project.renderIAM(plan, templates, selection); err != nil {
		return nil, err
	}

	if err := project.planIAMUses(plan, selection, routes, worker); err != nil {
		return nil, err
	}

	plan.Steps = selection.steps()

	return plan, project.planRequires(plan)
}

// newIAMSelection validates the --with values and returns the selection. An
// unknown value is a usage error that lists the accepted ones.
func newIAMSelection(with []string) (iamSelection, error) {
	for _, feature := range with {
		if !slices.Contains(IAMFeatures, feature) {
			return iamSelection{}, console.UsageError("unknown feature %q for add ark iam; --with accepts %s", feature, strings.Join(IAMFeatures, ", "))
		}
	}

	return iamSelection{with: with}, nil
}

// iamProcesses checks that no provider the selection declares exists yet and
// returns the process files whose Compose receives the new providers: the
// server's, for the routes, and the one of process, for the jobs. It returns a
// usage error when a needed process has no Compose.
func (project *Project) iamProcesses(selection iamSelection, process string) (routes, worker string, err error) {
	for _, provider := range selection.providers() {
		if project.declares("internal/bootstrap", provider) {
			return "", "", console.UsageError("internal/bootstrap already declares %s", provider)
		}
	}

	routes = project.composeFile("server")
	worker = project.composeFile(process)

	if routes == "" || (selection.jobs() && worker == "") {
		return "", "", console.Fail(console.TypeUsage,
			[]console.Step{{Text: "Create the process with arkoren add process NAME"}},
			"no Compose to edit in internal/bootstrap/server or internal/bootstrap/%s", process)
	}

	return routes, worker, nil
}

// planIAMUses plans the Compose insertions of the selection: IAMRouter into the
// server's file routes and, for the jobs, IAMJobs and Work into the file worker.
// When both are one file, one edit inserts all three, because two edits planned
// from the same original content cannot both be applied.
func (project *Project) planIAMUses(plan *Plan, selection iamSelection, routes, worker string) error {
	providers := []string{"IAMRouter"}

	if selection.jobs() && worker == routes {
		providers = append(providers, "IAMJobs", "Work")
	}

	if err := project.planUses(plan, routes, providers); err != nil || !selection.jobs() || worker == routes {
		return err
	}

	return project.planUses(plan, worker, []string{"IAMJobs", "Work"})
}

// renderIAM appends to plan iam.go, the fragment files of the selection and the
// shared providers it needs that internal/bootstrap lacks.
func (project *Project) renderIAM(plan *Plan, templates fs.FS, selection iamSelection) error {
	data := selection.data(project.name())

	if err := project.render(plan, templates, "add/ark/iam/iam.go", "internal/bootstrap/iam.go", data); err != nil {
		return err
	}

	for _, fragment := range selection.fragments() {
		if err := project.render(plan, templates, "add/ark/"+fragment.template+".go", "internal/bootstrap/"+fragment.file+".go", data); err != nil {
			return err
		}
	}

	return project.renderPrerequisites(plan, templates, selection.prerequisites())
}

// AddOAuth plans one OAuth sign-in client of ark/oauth: internal/bootstrap/<provider>.go,
// a provider named for it (Google, GitHub) that builds the client from its
// environment, registers its startup and returns it, plus the OAuthKey provider
// when internal/bootstrap does not declare one. It does not change the IAM
// provider: the plan's notes say to pass the client to identity.Provider.
//
// It returns a usage error for an unknown provider (google and github are
// supported) or when the provider already exists. It never changes the project.
//
// Example:
//
//	plan, err := project.AddOAuth("google", templates)
func (project *Project) AddOAuth(provider string, templates fs.FS) (*Plan, error) {
	identifier := map[string]string{"google": "Google", "github": "GitHub"}[provider]

	if identifier == "" {
		return nil, console.UsageError("unknown OAuth provider %q; add ark oauth supports google and github", provider)
	}

	if project.declares("internal/bootstrap", identifier) {
		return nil, console.UsageError("internal/bootstrap already declares %s", identifier)
	}

	plan := &Plan{}

	if err := project.render(plan, templates, "add/ark/oauth/"+provider+".go", "internal/bootstrap/"+provider+".go", nil); err != nil {
		return nil, err
	}

	if err := project.renderPrerequisites(plan, templates, []arkPrerequisite{{"OAuthKey", "oauth_key"}}); err != nil {
		return nil, err
	}

	plan.Steps = []console.Step{
		oauthEnvironmentStep(provider),
		keyStep("OAuthKey", "OAUTH_KEY_FILE"),
		{Text: "Pass the client to the identity module in internal/bootstrap/iam.go with identity.Provider(compose.Use(" + identifier + "))"},
	}

	return plan, project.planRequires(plan)
}

// renderPrerequisites appends to plan the file of every shared provider in
// required that internal/bootstrap does not declare yet.
func (project *Project) renderPrerequisites(plan *Plan, templates fs.FS, required []arkPrerequisite) error {
	for _, prerequisite := range required {
		if project.declares("internal/bootstrap", prerequisite.provider) {
			continue
		}

		if err := project.render(plan, templates, "add/ark/provider/"+prerequisite.file+".go", "internal/bootstrap/"+prerequisite.file+".go", nil); err != nil {
			return err
		}
	}

	return nil
}

// planUses plans the insertion of the bootstrap providers into the Compose of
// the process file target, in the order given. Providers the Compose already
// uses are skipped, and when none is left nothing is planned.
func (project *Project) planUses(plan *Plan, target string, providers []string) error {
	return project.planEdit(plan, target, func(content []byte) ([]byte, []string, error) {
		return addUses(target, content, project.Module, providers)
	})
}

// name returns the last element of the module path without a trailing
// major-version element such as v2: the project's name.
func (project *Project) name() string {
	base := path.Base(project.Module)

	if majorVersion.MatchString(base) && path.Dir(project.Module) != "." {
		base = path.Base(path.Dir(project.Module))
	}

	return base
}

// planRequires records on plan the pinned modules that the files it creates
// import and go.mod does not require yet, as module@version, sorted. An import
// of a module without a pin is left to go mod tidy.
func (project *Project) planRequires(plan *Plan) error {
	required, err := project.requirements()

	if err != nil {
		return err
	}

	imported, err := pinnedImports(plan.Create)

	if err != nil {
		return err
	}

	for _, module := range imported {
		if !slices.Contains(required, module) {
			plan.Requires = append(plan.Requires, module+"@"+modulePins[module])
		}
	}

	return nil
}

// pinnedImports returns, sorted and without repeats, the pinned modules that the
// Go files in created import.
func pinnedImports(created []files.File) ([]string, error) {
	var modules []string

	for _, file := range created {
		if !strings.HasSuffix(file.Path, ".go") {
			continue
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Content, parser.ImportsOnly)

		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.ToSlash(file.Path), err)
		}

		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)

			if module := pinnedModule(importPath); err == nil && module != "" {
				modules = append(modules, module)
			}
		}
	}

	slices.Sort(modules)

	return slices.Compact(modules), nil
}

// pinnedModule returns the module of modulePins that provides importPath, or ""
// when no pinned module does.
func pinnedModule(importPath string) string {
	for module := range modulePins {
		if importPath == module || strings.HasPrefix(importPath, module+"/") {
			return module
		}
	}

	return ""
}
