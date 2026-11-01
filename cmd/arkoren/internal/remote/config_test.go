package remote

import (
	"strings"
	"testing"
)

// TestEnvFileOnlyKeepsTheTwoKeys checks the env parser's credential boundary.
// Unrelated keys are ignored even when their values contain invalid quoting.
func TestEnvFileOnlyKeepsTheTwoKeys(t *testing.T) {
	t.Parallel()

	values, err := ParseEnv("OTHER_SECRET=\"unterminated\nexport ARKOREN_MCP_URL=\"http://a/b\"\nPATH=/bin\n# ARKOREN_MCP_TOKEN=commented\nARKOREN_MCP_TOKEN=abc # note\n")

	if err != nil {
		t.Fatal(err)
	}

	if len(values) != 2 || values[EnvURL] != "http://a/b" || values[EnvToken] != "abc" {
		t.Fatalf("values = %v", values)
	}
}

// TestEnvValueForms checks quoting, comments, and literal env-file values.
// Shell expansion is deliberately absent, including for dollar-prefixed text.
func TestEnvValueForms(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		`ARKOREN_MCP_TOKEN=plain`:             "plain",
		`ARKOREN_MCP_TOKEN='a b#c'`:           "a b#c",
		`ARKOREN_MCP_TOKEN="a \"b\" \\ c"`:    `a "b" \ c`,
		`ARKOREN_MCP_TOKEN="x" # trailing`:    "x",
		`ARKOREN_MCP_TOKEN=a#b`:               "a#b",
		`  export   ARKOREN_MCP_TOKEN = v  `:  "v",
		`ARKOREN_MCP_TOKEN=$HOME`:             "$HOME",
		"ARKOREN_MCP_TOKEN=crlf\r":            "crlf",
		`ARKOREN_MCP_TOKEN=`:                  "",
		`ARKOREN_MCP_TOKEN='single \n stays'`: `single \n stays`,
	}

	for line, expected := range cases {
		values, err := ParseEnv(line)

		if err != nil || values[EnvToken] != expected {
			t.Fatalf("%q: got %q, %v; want %q", line, values[EnvToken], err, expected)
		}
	}
}

// TestEnvUnterminatedQuoteOnAKeptKeyIsAnError checks retained-key syntax errors.
// The diagnostic must identify the original file line.
func TestEnvUnterminatedQuoteOnAKeptKeyIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := ParseEnv("\n" + `ARKOREN_MCP_TOKEN="abc`); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
}
