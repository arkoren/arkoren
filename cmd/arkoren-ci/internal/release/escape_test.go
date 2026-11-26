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
		"indented":       {"    <name>", "    \\<name>"},
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

// escaped fails the test unless EscapeHTML maps input to want.
func escaped(t *testing.T, input, want string) {
	t.Helper()
	if got := EscapeHTML(input); got != want {
		t.Fatalf("EscapeHTML(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeHTMLCodeSpanContinuesOntoNextLine(t *testing.T) {
	t.Parallel()
	escaped(t, "use `foo\n<bar>` here", "use `foo\n<bar>` here")
}

func TestEscapeHTMLWrappedSpanDoesNotHideLaterHTML(t *testing.T) {
	t.Parallel()
	escaped(t, "- x `a\nb` <y> `z`", "- x `a\nb` \\<y> `z`")
}

func TestEscapeHTMLSpanNeverCrossesListItems(t *testing.T) {
	t.Parallel()
	escaped(t, "- a `b <x>\n- c`", "- a `b \\<x>\n- c`")
}

func TestEscapeHTMLSpanNeverCrossesSetextUnderline(t *testing.T) {
	t.Parallel()
	escaped(t, "a `b <x>\n===\nc`", "a `b \\<x>\n===\nc`")
}

func TestEscapeHTMLSpanNeverCrossesTableRows(t *testing.T) {
	t.Parallel()
	escaped(t, "| a `b |\nc` <x>", "| a `b |\nc` \\<x>")
}

func TestEscapeHTMLIndentedListParagraph(t *testing.T) {
	t.Parallel()
	escaped(t, "- a\n\n    <x>", "- a\n\n    \\<x>")
}

func TestEscapeHTMLFenceEndsWithItsListItem(t *testing.T) {
	t.Parallel()
	escaped(t, "- a\n  ```\n  <x>\n<y>", "- a\n  ```\n  <x>\n\\<y>")
}

func TestEscapeHTMLBacktickInfoIsNotFence(t *testing.T) {
	t.Parallel()
	escaped(t, "``` a`b\n<x>", "``` a`b\n\\<x>")
}

func TestEscapeHTMLUnsafeAutolink(t *testing.T) {
	t.Parallel()
	escaped(t, "see <javascript:alert(1)> and <HTTPS://x.dev>", "see \\<javascript:alert(1)> and <HTTPS://x.dev>")
}

func TestEscapeHTMLCodeSpanClosesOnExactRun(t *testing.T) {
	t.Parallel()
	escaped(t, "`a``<x>` <y>", "`a``<x>` \\<y>")
}
