package layout_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/layout"
)

// TestAddOAuthWritesTheClientProvider creates the provider and the key it needs,
// the redirect URL default, and points at add ark iam when there is no identity
// module to serve the routes.
func TestAddOAuthWritesTheClientProvider(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddOAuth([]string{"google"}, templates)
	apply(t, project, plan, err)

	if !slices.Equal(created(plan), []string{"internal/bootstrap/google.go", "internal/bootstrap/secrets.go", "internal/bootstrap/oauth_key.go"}) {
		t.Fatalf("plan creates %v", created(plan))
	}

	if google := read(t, root, "internal/bootstrap/google.go"); !strings.Contains(google, "compose.Startup(client.Start)") ||
		!strings.Contains(google, "var Google = application.Provide(") {
		t.Fatalf("google.go:\n%s", google)
	}

	if !slices.Equal(plan.Requires, []string{"arkoren.dev/ark/oauth@v0.9.0", "arkoren.dev/driver/aes@v0.2.0", "arkoren.dev/foundation/key@v0.2.0"}) {
		t.Fatalf("requires = %v", plan.Requires)
	}

	if inserted := edited(plan, ".env"); !slices.Equal(inserted, []string{"GOOGLE_REDIRECT_URL=http://127.0.0.1:8080/auth/google/callback", "OAUTH_KEY=file:.secrets/oauth-key"}) {
		t.Fatalf(".env lines = %v", inserted)
	}

	notes := stepTexts(plan.Steps)

	for _, want := range []string{
		"http://127.0.0.1:8080/auth/google/callback", "GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET", "OAUTH_KEY=file:.secrets/oauth-key",
		"arkoren key generate shared --out .secrets/oauth-key", "arkoren add ark iam",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes miss %q:\n%s", want, notes)
		}
	}

	if strings.Contains(notes, "GOOGLE_REDIRECT_URL,") {
		t.Fatalf("notes ask for the redirect URL the command wrote:\n%s", notes)
	}
}

// TestAddOAuthWritesEveryProvider checks each provider's file, environment and
// route name, and that the generated project passes the layout rules.
func TestAddOAuthWritesEveryProvider(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddOAuth(layout.OAuthProviders(), templates)
	apply(t, project, plan, err)

	for provider, wants := range map[string][]string{
		"google":    {"oauthauth.Google()", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_REDIRECT_URL"},
		"github":    {"oauthauth.GitHub()", "GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GITHUB_REDIRECT_URL"},
		"microsoft": {"oauthauth.Microsoft(tenant)", `cmp.Or(os.Getenv("MICROSOFT_TENANT"), "common")`, "MICROSOFT_CLIENT_SECRET", "MICROSOFT_REDIRECT_URL"},
		"apple": {
			"oauthauth.Apple(os.Getenv(\"APPLE_TEAM_ID\"), os.Getenv(\"APPLE_KEY_ID\"), signing)", "secret.NewBinding[key.Signing](\"APPLE_KEY\")",
			"APPLE_CLIENT_ID", "APPLE_REDIRECT_URL",
		},
		"oidc": {"oauthauth.OIDC(os.Getenv(\"OIDC_ISSUER_URL\"))", `client.Name("oidc")`, "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URL"},
	} {
		source := read(t, root, "internal/bootstrap/"+provider+".go")

		for _, want := range wants {
			if !strings.Contains(source, want) {
				t.Fatalf("%s.go misses %q:\n%s", provider, want, source)
			}
		}
	}

	if secret := read(t, root, "internal/bootstrap/apple.go"); strings.Contains(secret, "APPLE_CLIENT_SECRET") {
		t.Fatalf("apple.go reads a client secret, which Apple signs from its key:\n%s", secret)
	}

	if signUp := read(t, root, "internal/bootstrap/github_sign_up.go"); !strings.Contains(signUp, "var GitHubSignUp") {
		t.Fatalf("github_sign_up.go:\n%s", signUp)
	}

	env := read(t, root, ".env")

	for _, want := range []string{
		"APPLE_REDIRECT_URL=http://127.0.0.1:8080/auth/apple/callback\n", "OIDC_REDIRECT_URL=http://127.0.0.1:8080/auth/oidc/callback\n",
		"MICROSOFT_REDIRECT_URL=http://127.0.0.1:8080/auth/microsoft/callback\n",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf(".env misses %q:\n%s", want, env)
		}
	}

	for _, secret := range []string{"CLIENT_SECRET", "KEY_FILE", "CLIENT_ID"} {
		if strings.Contains(env, secret) {
			t.Fatalf(".env holds %s, which only the reader can supply:\n%s", secret, env)
		}
	}

	notes := stepTexts(plan.Steps)

	if !strings.Contains(notes, "arkoren key check signing .secrets/apple-key") || !strings.Contains(notes, "MICROSOFT_TENANT") {
		t.Fatalf("notes:\n%s", notes)
	}

	if found := load(t, root).Check(); len(found) != 0 {
		t.Fatalf("generated project violates the layout rules: %+v", found)
	}
}

// TestAddOAuthWiresIdentityAfterIAM inserts one identity.Provider call per
// client into the callback of iam.New, in order, with GitHub's sign-up fragment.
func TestAddOAuthWiresIdentityAfterIAM(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddIAM(layout.IAMOptions{With: []string{"account"}}, templates)
	apply(t, project, plan, err)

	project = load(t, root)
	plan, err = project.AddOAuth([]string{"google", "github", "oidc"}, templates)
	apply(t, project, plan, err)

	wants := []string{
		"identity.Provider(compose.Use(Google))", "identity.Provider(compose.Use(GitHub), compose.Use(GitHubSignUp))",
		"identity.Provider(compose.Use(OIDC))",
	}

	if inserted := edited(plan, "internal/bootstrap/iam.go"); !slices.Equal(inserted, wants) {
		t.Fatalf("inserted = %v, want %v", inserted, wants)
	}

	identity := read(t, root, "internal/bootstrap/iam.go")

	if !strings.Contains(identity, "identity.Password(compose.Use(Hasher))\n\t\tidentity.Account(compose.Use(AccountView))\n\t\t"+strings.Join(wants, "\n\t\t")+"\n\t}))") {
		t.Fatalf("iam.go:\n%s", identity)
	}

	if last := plan.Steps[len(plan.Steps)-1]; last.Command != "make run" {
		t.Fatalf("last step = %+v, want make run", last)
	}

	if found := load(t, root).Check(); len(found) != 0 {
		t.Fatalf("generated project violates the layout rules: %+v", found)
	}
}

// TestAddOAuthDoesNotWireTwice skips a client the IAM provider already passes.
func TestAddOAuthDoesNotWireTwice(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddOAuth([]string{"google"}, templates)
	apply(t, project, plan, err)

	project = load(t, root)
	plan, err = project.AddIAM(layout.IAMOptions{}, templates)
	apply(t, project, plan, err)

	plan, err = load(t, root).AddOAuth([]string{"oidc"}, templates)
	if err != nil {
		t.Fatal(err)
	}

	if inserted := edited(plan, "internal/bootstrap/iam.go"); !slices.Equal(inserted, []string{"identity.Provider(compose.Use(OIDC))"}) {
		t.Fatalf("inserted = %v, want only the new client", inserted)
	}
}

// TestAddOAuthLeavesAStepWhenIAMCannotTakeTheLine names the lines to write for
// an IAM provider without an iam.New callback to extend.
func TestAddOAuthLeavesAStepWhenIAMCannotTakeTheLine(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/iam.go", "package bootstrap\n\nvar IAM = 1\n")

	plan, err := load(t, root).AddOAuth([]string{"google", "github"}, templates)
	if err != nil {
		t.Fatal(err)
	}

	if edited(plan, "internal/bootstrap/iam.go") != nil || !strings.Contains(stepTexts(plan.Steps), "identity.Provider(compose.Use(Google)) and identity.Provider(compose.Use(GitHub))") {
		t.Fatalf("plan = %+v", plan)
	}
}

// TestAddOAuthKeepsDotenvVariables never overwrites a variable .env sets and
// derives the callback from its HTTP_ADDR.
func TestAddOAuthKeepsDotenvVariables(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, ".env", "HTTP_ADDR=:9090\nGOOGLE_REDIRECT_URL=https://app.example.com/auth/google/callback")

	project := load(t, root)
	plan, err := project.AddOAuth([]string{"google", "github"}, templates)
	apply(t, project, plan, err)

	want := "HTTP_ADDR=:9090\nGOOGLE_REDIRECT_URL=https://app.example.com/auth/google/callback\nGITHUB_REDIRECT_URL=http://localhost:9090/auth/github/callback\nOAUTH_KEY=file:.secrets/oauth-key\n"

	if env := read(t, root, ".env"); env != want {
		t.Fatalf(".env = %q, want %q", env, want)
	}
}

// TestAddOAuthCreatesMissingDotenv writes the variables into a new .env when the
// project has none.
func TestAddOAuthCreatesMissingDotenv(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	if err := os.Remove(filepath.Join(root, ".env")); err != nil {
		t.Fatal(err)
	}

	project := load(t, root)
	plan, err := project.AddOAuth([]string{"google"}, templates)
	apply(t, project, plan, err)

	if !slices.Contains(created(plan), ".env") {
		t.Fatalf("plan creates %v, want .env", created(plan))
	}

	if env := read(t, root, ".env"); env != "GOOGLE_REDIRECT_URL=http://localhost:8080/auth/google/callback\nOAUTH_KEY=file:.secrets/oauth-key\n" {
		t.Fatalf(".env = %q", env)
	}
}

// TestAddOAuthRefusesBadProviders names the supported providers, and rejects
// none, repeated and existing ones.
func TestAddOAuthRefusesBadProviders(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)

	_, err := project.AddOAuth([]string{"myspace"}, templates)
	requireUsage(t, err, "supports google, github, microsoft, apple, oidc")

	_, err = project.AddOAuth(nil, templates)
	requireUsage(t, err, "at least one provider")

	_, err = project.AddOAuth([]string{"google", "google"}, templates)
	requireUsage(t, err, "listed twice")

	plan, err := project.AddOAuth([]string{"github"}, templates)
	apply(t, project, plan, err)

	_, err = load(t, root).AddOAuth([]string{"github"}, templates)
	requireUsage(t, err, "internal/bootstrap already declares GitHub")
}

// TestAddOAuthWritesKeyBindingsToBothEnvFiles points the key bindings at files
// below .secrets in .env and .env.example, and keeps the redirect default out of
// the example.
func TestAddOAuthWritesKeyBindingsToBothEnvFiles(t *testing.T) {
	t.Parallel()

	project := load(t, scaffoldProject(t))
	plan, err := project.AddOAuth([]string{"apple"}, templates)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"APPLE_REDIRECT_URL=http://127.0.0.1:8080/auth/apple/callback", "APPLE_KEY=file:.secrets/apple-key", "OAUTH_KEY=file:.secrets/oauth-key"}

	if inserted := edited(plan, ".env"); !slices.Equal(inserted, want) {
		t.Fatalf(".env lines = %v, want %v", inserted, want)
	}

	if inserted := edited(plan, ".env.example"); !slices.Equal(inserted, want[1:]) {
		t.Fatalf(".env.example lines = %v, want %v", inserted, want[1:])
	}
}

// TestAddOAuthResolvesAppleKeyThroughSecrets writes the Secrets provider the key
// providers use and no key file variable.
func TestAddOAuthResolvesAppleKeyThroughSecrets(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	project := load(t, root)
	plan, err := project.AddOAuth([]string{"apple"}, templates)
	apply(t, project, plan, err)

	secrets := read(t, root, "internal/bootstrap/secrets.go")

	for _, want := range []string{"var Secrets = application.Provide(", `sources.Source("file", filesecret.New())`, "secrets.Bindings(os.LookupEnv)"} {
		if !strings.Contains(secrets, want) {
			t.Fatalf("secrets.go misses %q:\n%s", want, secrets)
		}
	}

	for _, name := range []string{"apple", "oauth_key"} {
		if source := read(t, root, "internal/bootstrap/"+name+".go"); strings.Contains(source, "_KEY_FILE") || !strings.Contains(source, "compose.Use(Secrets)") {
			t.Fatalf("%s.go does not resolve through Secrets:\n%s", name, source)
		}
	}
}

// TestAddOAuthSkipsSecretsTheProjectDeclares leaves an existing Secrets
// provider alone.
func TestAddOAuthSkipsSecretsTheProjectDeclares(t *testing.T) {
	t.Parallel()

	root := scaffoldProject(t)
	writeFile(t, root, "internal/bootstrap/secrets.go", "package bootstrap\n\nvar Secrets = 1\n")

	plan, err := load(t, root).AddOAuth([]string{"google"}, templates)
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(created(plan), "internal/bootstrap/secrets.go") {
		t.Fatalf("plan creates %v, want the existing Secrets kept", created(plan))
	}
}
