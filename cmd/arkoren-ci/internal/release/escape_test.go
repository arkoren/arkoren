package release

import "testing"

func TestEscapeHTML(t *testing.T) {
	t.Parallel()

	cases := map[string][2]string{
		"placeholder":    {`mount "/auth/<name>" now`, `mount "/auth/\<name>" now`},
		"suffix":         {`"<name>_ms" and "<name>_bytes"`, `"\<name>_ms" and "\<name>_bytes"`},
		"closing":        {`a </div> b`, `a \</div> b`},
		"comment":        {`<!-- x -->`, `\<!-- x -->`},
		"code span":      {"use `/<name>` here", "use `/<name>` here"},
		"double span":    {"use ``a ` <b>`` and <c>", "use ``a ` <b>`` and \\<c>"},
		"escaped":        {`already \<name>`, `already \<name>`},
		"autolink":       {`see <https://example.com/a>`, `see <https://example.com/a>`},
		"email":          {`mail <a@b.co>`, `mail <a@b.co>`},
		"comparison":     {`a < b and 1<2`, `a < b and 1<2`},
		"fence":          {"```\n<name>\n```\n<x>", "```\n<name>\n```\n\\<x>"},
		"tilde fence":    {"~~~go\n<name>\n~~~", "~~~go\n<name>\n~~~"},
		"indented":       {"    <name>", "    <name>"},
		"unclosed tick":  {"a ` <name>", "a ` \\<name>"},
		"plain":          {"nothing here", "nothing here"},
		"unclosed fence": {"```\n<name>", "```\n<name>"},
	}

	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := EscapeHTML(item[0]); got != item[1] {
				t.Fatalf("EscapeHTML(%q) = %q, want %q", item[0], got, item[1])
			}
		})
	}
}
