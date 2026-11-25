package layout

import (
	"cmp"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/files"
)

// dotenvFile is the project's local environment file, which the generated
// executables load with godotenv/autoload.
const dotenvFile = ".env"

// dotenvExampleFile is the committed copy of the project's environment file,
// which lists every variable without local values.
const dotenvExampleFile = ".env.example"

// envVariable is one NAME=value line that `add ark` defaults in .env.
type envVariable struct {
	// name is the variable name, such as DATABASE_URL.
	name string
	// value is the default written when the variable is absent. It never holds a
	// secret: a key binding holds only the reference of the secret, such as
	// file:.secrets/link-key.
	value string
	// example says the variable is also written to .env.example. Only the key
	// bindings are: the example documents what a deployment must configure, and
	// the other defaults are local conveniences.
	example bool
}

// planEnv plans the lines of the variables wanted returns that .env does not
// set yet, and returns the server's local base URL as .env gives it. .env is
// created when missing. The variables marked [envVariable.example] are also
// planned for .env.example, which is only edited, and only when the project has
// one. See [Project.planEnvFile] for the rules of one file.
func (project *Project) planEnv(plan *Plan, wanted func(baseURL string) []envVariable) (string, error) {
	baseURL, err := project.planEnvFile(plan, dotenvFile, true, wanted)

	if err != nil {
		return "", err
	}

	_, err = project.planEnvFile(plan, dotenvExampleFile, false, func(baseURL string) []envVariable {
		return slices.DeleteFunc(wanted(baseURL), func(variable envVariable) bool {
			return !variable.example
		})
	})

	return baseURL, err
}

// planEnvFile plans the lines of the variables wanted returns that the file
// name does not set yet, and returns the server's local base URL. wanted
// receives that base URL, such as http://127.0.0.1:8080, derived from HTTP_ADDR
// in the file (":8080" when unset; an unspecified host becomes localhost), so
// defaults that name the server's own address agree with the address it listens
// on.
//
// A variable already present in the file, even with an empty value, is never
// changed. Lines are appended, each reported in [Edit.Inserted]; a missing file
// is created with them when create is true and left missing otherwise. Nothing
// is planned when no variable is missing.
func (project *Project) planEnvFile(plan *Plan, name string, create bool, wanted func(baseURL string) []envVariable) (string, error) {
	original, err := os.ReadFile(filepath.Join(project.Root, name))
	missing := errors.Is(err, fs.ErrNotExist)

	if err != nil && !missing {
		return "", err
	}

	values := parseDotenv(original)
	baseURL := localURL(values["HTTP_ADDR"])

	var inserted []string

	for _, variable := range wanted(baseURL) {
		if _, set := values[variable.name]; !set {
			inserted = append(inserted, variable.name+"="+variable.value)
		}
	}

	if len(inserted) == 0 || (missing && !create) {
		return baseURL, nil
	}

	content := string(original)

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	content += strings.Join(inserted, "\n") + "\n"

	if missing {
		plan.Create = append(plan.Create, files.File{Path: name, Content: []byte(content)})

		return baseURL, nil
	}

	plan.Edit = append(plan.Edit, Edit{Path: name, Inserted: inserted, original: original, content: []byte(content)})

	return baseURL, nil
}

// parseDotenv returns the variables a .env file sets, by name. It reads
// NAME=value lines, an optional leading "export " and quoted values, and skips
// blank lines and comments.
func parseDotenv(content []byte) map[string]string {
	values := map[string]string{}

	for line := range strings.Lines(string(content)) {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		name, value, found := strings.Cut(line, "=")

		if found && !strings.HasPrefix(name, "#") {
			values[strings.TrimSpace(name)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}

	return values
}

// localURL returns the http URL a browser on this machine uses to reach a
// server listening on addr: the host:port of addr with an empty or unspecified
// host replaced by localhost. An empty or malformed addr means the default
// listen address, :8080.
func localURL(addr string) string {
	host, port, err := net.SplitHostPort(cmp.Or(addr, ":8080"))

	if err != nil {
		host = ""
		port = "8080"
	}

	if address := net.ParseIP(host); host == "" || (address != nil && address.IsUnspecified()) {
		host = "localhost"
	}

	return "http://" + net.JoinHostPort(host, port)
}
