package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Environment variable names, which are also the only keys read from a .env
// file.
const (
	EnvURL = "ARKOREN_MCP_URL"
	// EnvToken selects an existing bearer credential for the approved endpoint.
	// Flags take precedence; the same key in the env file is the last fallback.
	EnvToken = "ARKOREN_MCP_TOKEN"
)

// defaultEnvFile is the file read, relative to the working directory, when
// -env-file is not given.
const defaultEnvFile = ".env"

// Where a setting came from, as shown by status and version. A custom
// -env-file is reported by its path instead of [fromDotEnv].
const (
	fromFlag   = "flag"
	fromEnv    = "env"
	fromDotEnv = ".env"
)

// setting is one resolved configuration value and the place it came from.
type setting struct {
	// value is the setting's text; empty means unset.
	value string

	// from is [fromFlag], [fromEnv], [fromDotEnv], or the -env-file path.
	from string
}

// settings is the inspector URL and token the command line, the environment,
// and the env file resolved to.
type settings struct {
	// url is the selected inspection root and its approval-relevant source.
	url setting

	// token is the selected bearer credential, retained separately from output.
	token setting
}

// resolveSettings picks the URL and token. For each, the first non-empty value
// wins among the flag, the real environment, and the env file. The file is
// read relative to dir; envFile is nil for the default ./.env (whose absence
// is not an error), points at an explicit path (whose absence is), or is ""
// to skip the file entirely. Only [envURL] and [envToken] are ever taken from
// the file, and every other line is ignored without being stored. An implicit
// file URL cannot receive a token from a flag or the environment; a nonempty
// explicit envFile approves that file's URL for those tokens.
func resolveSettings(flagURL, flagToken string, envFile *string, env func(string) (string, bool), dir string) (settings, error) {
	var file map[string]string
	var fileName string

	switch {
	case envFile == nil:
		fileName = defaultEnvFile
		values, err := readEnvFile(dir, defaultEnvFile)

		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return settings{}, err
		}

		file = values
	case *envFile != "":
		fileName = *envFile
		values, err := readEnvFile(dir, *envFile)

		if err != nil {
			return settings{}, err
		}

		file = values
	}

	pick := func(flagValue, key string) setting {
		if flagValue != "" {
			return setting{
				value: flagValue,
				from:  fromFlag,
			}
		}

		if value, ok := env(key); ok && value != "" {
			return setting{
				value: value,
				from:  fromEnv,
			}
		}

		if value := file[key]; value != "" {
			return setting{
				value: value,
				from:  fileName,
			}
		}

		return setting{}
	}

	found := settings{
		url:   pick(flagURL, EnvURL),
		token: pick(flagToken, EnvToken),
	}

	if envFile == nil && found.url.from == fromDotEnv &&
		(found.token.from == fromFlag || found.token.from == fromEnv) {
		return settings{}, errors.New("inspector URL from implicit ./.env cannot use an environment or flag token; " +
			"pin the trusted URL with -url or ARKOREN_MCP_URL, or approve a trusted file with -env-file PATH")
	}

	return found, nil
}

// readEnvFile reads and parses one env file. A relative path is relative to
// dir. A missing file returns an error wrapping [fs.ErrNotExist].
func readEnvFile(dir, path string) (map[string]string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}

	data, err := os.ReadFile(path)

	if err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}

	return ParseEnv(string(data))
}

// ParseEnv extracts [envURL] and [envToken] from env file text. The format
// is minimal: KEY=VALUE lines with an optional "export " prefix, values that
// are bare, 'single quoted' (literal), or "double quoted" (with \n, \", and
// \\ escapes), # comments on their own line or after a bare value or a
// closing quote, and no interpolation. Lines whose key is not one of the two
// kept keys are skipped before their values are looked at, so an unrelated
// secret on a malformed line never causes an error or is held in memory. A
// kept key with an unterminated quote is an error naming the line.
func ParseEnv(text string) (map[string]string, error) {
	values := map[string]string{}

	for number, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, rest, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)

		if !ok || (key != EnvURL && key != EnvToken) {
			continue
		}

		value, err := parseEnvValue(strings.TrimSpace(rest))

		if err != nil {
			return nil, fmt.Errorf("env file line %d: %s: %w", number+1, key, err)
		}

		values[key] = value
	}

	return values, nil
}

// parseEnvValue reads the value part of an env line; see [parseEnv].
func parseEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	quote := raw[0]

	if quote != '"' && quote != '\'' {
		value, _, _ := strings.Cut(raw, " #")

		return strings.TrimSpace(value), nil
	}

	var value strings.Builder

	for index := 1; index < len(raw); index++ {
		character := raw[index]

		switch {
		case character == quote:
			return value.String(), nil
		case character == '\\' && quote == '"' && index+1 < len(raw):
			index++

			switch raw[index] {
			case 'n':
				value.WriteByte('\n')
			default:
				value.WriteByte(raw[index])
			}
		default:
			value.WriteByte(character)
		}
	}

	return "", errors.New("unterminated quote")
}

// ParseBaseURL validates the inspector URL: http or https, with a host, and
// no userinfo, query, or fragment. Bearer credentials must use the token setting;
// URL errors deliberately omit the supplied value, which can contain secrets.
// Literal trailing slashes are normalized on the escaped path so encoded
// separators still select the approved resource. Tool names are never appended.
func ParseBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(raw)

	if err != nil {
		return nil, errors.New("invalid inspector URL: could not parse endpoint")
	}

	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("invalid inspector URL: want http://host[:port][/path]")
	}

	if base.User != nil {
		return nil, errors.New("invalid inspector URL: userinfo is not allowed; use the token setting for credentials")
	}

	if strings.ContainsAny(raw, "?#") {
		return nil, errors.New("invalid inspector URL: it must be the inspection root, without a query or fragment")
	}

	base.RawPath = strings.TrimRight(base.EscapedPath(), "/")
	// EscapedPath returns valid escaping even when RawPath was absent.
	base.Path, _ = url.PathUnescape(base.RawPath)

	return base, nil
}
