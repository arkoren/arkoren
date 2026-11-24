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
	"arkoren.dev/ark/iam":         "v0.6.1",
	"arkoren.dev/ark/oauth":       "v0.8.0",
	"arkoren.dev/driver/aes":      "v0.1.0",
	"arkoren.dev/driver/bcrypt":   "v0.1.0",
	"arkoren.dev/driver/sqlite":   "v0.1.0",
	"arkoren.dev/foundation/key":  "v0.1.1",
	"arkoren.dev/foundation/totp": "v0.2.0",
}

// IAMFeatures lists the values `arkoren add ark iam --with` accepts, in the order
// the generated code presents them.
var IAMFeatures = []string{"reset", "verification", "two-factor", "tokens", "permissions", "account"}

// iamFeatureKeys maps each --with value to the template field that turns its
// part of the generated code on.
var iamFeatureKeys = map[string]string{
	"reset": "Reset", "verification": "Verification", "two-factor": "TwoFactor", "tokens": "Tokens",
	"permissions": "Permissions", "account": "Account",
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
	// verification), two-factor, tokens, permissions and account (GET
	// /auth/account). Password login is always on. Sign-in clients are not
	// features: add them with [Project.AddOAuth] before or after the module.
	With []string
	// Process names the process root internal/bootstrap/<process> that runs the
	// identity jobs, which send the module's emails. Empty selects "server". It
	// is unused when neither reset nor verification is selected. The identity
	// routes always go into the server process.
	Process string
}

// iamSelection is the validated feature set of one [Project.AddIAM] call, and
// the sign-in clients that internal/bootstrap already declares, which the
// generated IAM provider passes to identity.Provider.
type iamSelection struct {
	with    []string
	clients []oauthClient
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

// data returns the template fields of the generated files: one "true" per
// selected feature, Jobs when email is sent, one per declared sign-in client
// (named for its provider, such as Google), and the issuer name authenticator
// apps show.
func (selection iamSelection) data(issuer string) map[string]string {
	data := map[string]string{"Issuer": issuer}

	for _, feature := range selection.with {
		data[iamFeatureKeys[feature]] = "true"
	}

	if selection.jobs() {
		data["Jobs"] = "true"
	}

	for _, client := range selection.clients {
		data[client.identifier] = "true"
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

	return required
}

// environment returns the .env variables the selection can default safely,
// given the local base URL of the server: DATABASE_URL when database says
// the command writes the Database provider, the reset and verification page
// addresses, and the redirect URL of each declared sign-in client. Secrets and
// key paths are never defaulted.
func (selection iamSelection) environment(database bool) func(baseURL string) []envVariable {
	return func(baseURL string) []envVariable {
		var variables []envVariable

		if database {
			variables = append(variables, envVariable{"DATABASE_URL", "file:app.db"})
		}

		if selection.has("reset") {
			variables = append(variables, envVariable{"PASSWORD_RESET_URL", baseURL + "/reset-password"})
		}

		if selection.has("verification") {
			variables = append(variables, envVariable{"EMAIL_VERIFY_URL", baseURL + "/verify-email"})
		}

		for _, client := range selection.clients {
			variables = append(variables, client.redirect(baseURL))
		}

		return variables
	}
}

// steps lists what only the reader can do before the server starts, then the
// command that starts it: the key files the selection loads, and notes on email
// delivery. The environment, migrations and routes need no step.
func (selection iamSelection) steps() []console.Step {
	var steps []console.Step

	if selection.jobs() {
		steps = append(steps, keyStep("LinkSigner", "LINK_KEY_FILE", "link.key"),
			console.Step{Text: "IAMJobs sends the emails in the process whose Compose lists it; with the memory queue that is the process that serves HTTP, " +
				"so move IAMJobs and Work to a worker when you swap the Queue driver, and deploy workers before enabling a new email feature"},
			console.Step{Text: "Replace the logging Mailer with a delivering driver before production"})
	}

	if selection.has("two-factor") {
		steps = append(steps, keyStep("TwoFactorKey", "TWO_FACTOR_KEY_FILE", "two-factor.key"))
	}

	return append(steps, console.Step{
		Text:    "Start the server: it applies the migrations first and serves the identity routes under /auth, such as POST /auth/login",
		Command: "make run",
	})
}

// keyStep tells how to provision the key file one provider loads: the command
// creates it, and the reader then points variable at it. The path is never
// written to .env, and the file stays out of version control.
func keyStep(provider, variable, file string) console.Step {
	return console.Step{
		Text:    "Create the " + provider + " key, then set " + variable + "=" + file + " in .env; every process loads the same file, which stays out of version control",
		Command: "arkoren key generate shared --out " + file,
	}
}

// AddIAM plans the identity module of ark/iam: internal/bootstrap/iam.go with
// the IAM, IAMRouter and, when reset or verification is selected, IAMJobs
// providers; one fragment provider file per selected feature (PasswordReset,
// EmailVerification, TokenScopes, Roles, AccountView), each returning the
// construction callback the facade passes to its package; and every shared
// provider the generated code uses that internal/bootstrap does not declare yet
// (Database, Cache, Hasher, and as selected LinkSigner, Mailer, Queue,
// Dispatcher, Consumer, Work, TwoFactorKey), written with local defaults. A
// provider that already exists, found by name, is left alone and used. A
// sign-in client that internal/bootstrap already declares (see [Project.AddOAuth])
// is passed to identity.Provider in iam.go.
//
// The module's migrations apply when the server starts. Without a Migrator
// provider, the plan writes one (migrate.New over Database with the
// application's migrations.All and IAM.Migrations, plus internal/migrations
// when it has no All) and a Migrate provider whose Startup callback runs
// Migrator.Up, which holds the migration lock, so replicas are safe. With a
// Migrator, the plan inserts IAM.Migrations into it, and only when that is
// impossible leaves a step.
//
// It inserts compose.Use(bootstrap.Migrate) when the plan writes it and
// compose.Use(bootstrap.IAMRouter) into the Compose of the server process, and,
// for the jobs, compose.Use(bootstrap.IAMJobs) and compose.Use(bootstrap.Work)
// into the Compose of options.Process, skipping providers the Compose already
// uses. The routes are mounted under /auth. It appends to .env the variables
// with safe local defaults it lacks ([Plan.Edit]): DATABASE_URL when it writes
// the Database provider, the reset and verification page addresses and the
// declared clients' redirect URLs, derived from HTTP_ADDR, never a secret or a
// key path. The plan lists the modules it needs with their pinned versions
// ([Plan.Requires]) and the steps only the reader can do ([Plan.Steps]): key
// files and starting the server.
//
// It returns a usage error for an unknown feature (a sign-in provider such as
// google points to `add ark oauth`), for a provider the module declares that
// internal/bootstrap already declares, for an existing target file, or when a
// process has no Compose to edit. It never changes the project.
//
// Example:
//
//	plan, err := project.AddIAM(layout.IAMOptions{With: []string{"reset", "tokens"}}, templates)
func (project *Project) AddIAM(options IAMOptions, templates fs.FS) (*Plan, error) {
	selection, err := project.newIAMSelection(options.With)

	if err != nil {
		return nil, err
	}

	routes, worker, err := project.iamProcesses(selection, cmp.Or(options.Process, "server"))

	if err != nil {
		return nil, err
	}

	plan := &Plan{}
	writesDatabase := !project.declares("internal/bootstrap", "Database")

	if err := project.renderIAM(plan, templates, selection); err != nil {
		return nil, err
	}

	writesMigrate, err := project.planMigrator(plan, templates)

	if err != nil {
		return nil, err
	}

	if err := project.planIAMUses(plan, selection, routes, worker, writesMigrate); err != nil {
		return nil, err
	}

	if _, err := project.planEnv(plan, selection.environment(writesDatabase)); err != nil {
		return nil, err
	}

	plan.Steps = append(plan.Steps, selection.steps()...)

	return plan, project.planRequires(plan)
}

// newIAMSelection validates the --with values and returns the selection with the
// sign-in clients internal/bootstrap declares. An unknown value is a usage
// error that lists the accepted ones; a sign-in provider, which moved to
// [Project.AddOAuth], also gets the command that adds it.
func (project *Project) newIAMSelection(with []string) (iamSelection, error) {
	for _, feature := range with {
		if client, found := findOAuthClient(feature); found {
			return iamSelection{}, console.Fail(console.TypeUsage,
				[]console.Step{{Text: "Add the " + client.name + " sign-in client; add ark iam wires every declared client", Command: "arkoren add ark oauth " + client.name}},
				"%q is not an add ark iam feature: sign-in providers are added with add ark oauth", feature)
		}

		if !slices.Contains(IAMFeatures, feature) {
			return iamSelection{}, console.UsageError("unknown feature %q for add ark iam; --with accepts %s", feature, strings.Join(IAMFeatures, ", "))
		}
	}

	selection := iamSelection{with: with}

	for _, client := range oauthClients {
		if project.declares("internal/bootstrap", client.identifier) && (client.signUp == "" || project.declares("internal/bootstrap", client.signUp)) {
			selection.clients = append(selection.clients, client)
		}
	}

	return selection, nil
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

// planIAMUses plans the Compose insertions of the selection: Migrate when the
// plan writes it, then IAMRouter, into the server's file routes and, for the
// jobs, IAMJobs and Work into the file worker. When both are one file, one edit
// inserts all of them, because two edits planned from the same original content
// cannot both be applied.
func (project *Project) planIAMUses(plan *Plan, selection iamSelection, routes, worker string, migrate bool) error {
	var providers []string

	if migrate {
		providers = append(providers, "Migrate")
	}

	providers = append(providers, "IAMRouter")

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
