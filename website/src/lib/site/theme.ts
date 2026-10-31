import type { ShikiConfig } from "astro";

/** Red accents distinguish control flow from otherwise neutral syntax. */
interface CodePalette {
  /** The site's existing neutral code-pane surface. */
  background: string;
  /** Ordinary identifiers and function calls remain the reading anchor. */
  foreground: string;
  /** Comments recede without falling below normal-text contrast. */
  comment: string;
  /** Control flow uses a restrained crimson accent in each scheme. */
  keyword: string;
  /** Strings use warm gray so literal-heavy examples do not become pink. */
  string: string;
  /** Types and literal values stay close to the neutral foreground. */
  type: string;
}

/**
 * Build a TextMate theme shared by Markdown, MDX, and Astro code panes.
 * Leave punctuation and calls neutral so long examples stay quiet.
 *
 * @param scheme - The operating-system color scheme.
 * @param palette - Readable red and neutral syntax roles.
 * @returns A build-time Shiki theme with no client-side highlighter.
 */
function createCodeTheme(scheme: "light" | "dark", palette: CodePalette) {
  return {
    name: `kiln-red-${scheme}`,
    type: scheme,
    colors: {
      "editor.background": palette.background,
      "editor.foreground": palette.foreground,
    },
    tokenColors: [
      {
        scope: ["comment", "punctuation.definition.comment"],
        settings: { foreground: palette.comment },
      },
      { scope: ["keyword", "storage"], settings: { foreground: palette.keyword } },
      {
        scope: ["string", "punctuation.definition.string", "markup.inline.raw"],
        settings: { foreground: palette.string },
      },
      {
        scope: ["entity.name.type", "support.type", "storage.type", "constant", "support.constant"],
        settings: { foreground: palette.type },
      },
      {
        scope: ["entity.name.tag", "entity.other.attribute-name", "markup.heading"],
        settings: { foreground: palette.keyword },
      },
      { scope: "invalid", settings: { foreground: palette.keyword, fontStyle: "underline" } },
    ],
  };
}

/**
 * Kiln uses the site's existing neutral surfaces and text, reserving red
 * for control flow. Surfaces match the surface token in styles/global.css.
 */
export const codeThemes = {
  light: createCodeTheme("light", {
    background: "#fdfdfc",
    foreground: "#262626",
    comment: "#707070",
    keyword: "#b42332",
    string: "#6d5554",
    type: "#494340",
  }),
  dark: createCodeTheme("dark", {
    background: "#191817",
    foreground: "#f0e3de",
    comment: "#9a9390",
    keyword: "#eb626b",
    string: "#c0aaa5",
    type: "#d2c8c3",
  }),
} satisfies NonNullable<ShikiConfig["themes"]>;
