## Development

When starting the dev server, use background mode:

```
astro dev --background
```

Manage the background server with `astro dev stop`, `astro dev status`, and `astro dev logs`.

## Testing

Before adding a test, identify the user-visible failure it prevents. Protect
navigation, search, filtering, sorting, shareable URLs, copying, accessibility,
publication, and human/agent discovery. Keep cheap focused unit tests for real
logic; validate generated links, assets, metadata, redirects, and draft exclusion
against built output. Consolidate overlapping checks instead of adding a new
assertion for every page or past design request.

Use controlled fixtures for text-dependent behavior, not published copy.
Metadata tests check presence and propagation, not prescribed wording. Copy,
artwork, section order, and example selection must remain editable without test
changes. Do not assert marketing claims, exact SVG paths, example counts,
decorative animation choreography, exact palette values, decorative spacing, or
component proportions. Browser checks for readability, contrast, and usable hit
targets remain appropriate because they protect access, not a particular design.

Test browser outcomes in `test/browser/`: actual navigation, keyboard/focus,
client-script wiring, clipboard, overflow, and reduced motion. A CSS declaration
or source-code regex is not evidence of correct rendering. Do not replace these
checks with pixel snapshots or class-name assertions. Keep browser coverage to
representative journeys; add a case for a distinct failure, not each cosmetic
variation. Fixture builds must isolate output and caches from the working site.

Run `npm run fmt`, `npm run check`, `npm run build`, and `npm test` before delivery. Browser
setup and focused commands are in README.md. Report checks not run as unverified;
do not weaken an assertion merely to make a broken user journey pass.

From the repository root, `make docs` runs the website checks. After shared
guide changes, also run `make generate` (requires website dependencies). The build
checks guide-index freshness without writing. Review offline source links and
editorial conventions explicitly; there is no custom raw-Markdown policy gate.
The single authored guide tree is `cmd/arkoren/skills/arkoren/references/guides/`.
`/docs/api` lists library modules from local go.mod metadata and links to
pkg.go.dev; do not restore local declarations or probe the network during
builds. Keep legacy redirects separate from the module-only catalog.

## Styles and formatting

Styling is Tailwind CSS v4 through `@tailwindcss/vite`; there is no
`tailwind.config.js`. Markup uses utility classes; `oxfmt` sorts them using the
theme in `src/styles/global.css`.

`src/styles/global.css` is the single design system. Every design value (color,
type step, spacing unit, radius, breakpoint, container, duration, easing, shadow)
is a token in its `@theme` block, and tokens exist nowhere else. The file is
ordered theme, variants, fonts, `@layer base`, `@layer components`, utilities,
view-transition at-rules. It resets Tailwind's defaults, so only our scale
exists; read the theme for the values rather than guessing a default.

- **Token**: add it to `@theme`. A `--text-*` step carries its own
  `--line-height` and `--letter-spacing`. Colors are semantic and follow the OS
  scheme through `light-dark()`; use `/opacity` instead of new alpha colors.
- **Utility**: a layout or motion rule that one declaration list expresses
  (`page-width`, `cols-docs`, `duration-*`) is an `@utility`. Use it
  instead of an arbitrary value.
- **Component class**: add to `@layer components` only when a pattern repeats
  (`.btn`, `.field`, `.selector`) or must style markup that scripts or authors
  generate (search hits, blog MDX). Otherwise compose utilities in the markup.
  If a class string passes about ten utilities, extract an Astro component.
- **No arbitrary values** (`p-[22px]`, `text-[13px]`, `[&>li]:`). Add a token or
  an `@utility`. Do not add `<style>` blocks, CSS Modules or co-located `.css`
  files. A literal color, size or duration belongs in `@theme`; SVG artwork
  geometry and one-off keyframe timing in `motion.css` are the exceptions.

Tailwind scans only the `@source` entries in `global.css` (components, layouts,
pages, scripts, plugins, blog MDX), never guide Markdown, tests or docs. Put a class
that only a script or plugin emits in one of those folders as a literal string.

Three plain stylesheets are imported from `global.css`. They hold only what
utilities cannot express and read theme variables:

- `prose.css`: Markdown, MDX and `set:html` output. The classes that
  `src/plugins` emit (`callout`, `table-scroll`, `status-matrix`, `doc-anchor`)
  are a contract with this file; change both together.
- `motion.css`: `@keyframes` and SVG choreography for the artwork and `[data-slide]`.
- `season.css`: the Halloween palette swap and decorations. The palette only
  reassigns semantic color tokens, so no component needs seasonal classes.

Tailwind's own layer order (`theme`, `base`, `components`, `utilities`) applies;
the plain files put their rules in `components`, so a utility on an element
always wins. `!important` is only for reduced-motion resets and Shiki's palette.

Structure:

- `src/components/<area>/`: `ui` (shared primitives), `site` (chrome), `docs`,
  `blog`, `home`, `season`. PascalCase, one word where possible, two at most.
- `src/layouts/`: `Main` (document), `Site` (chrome around pages), `Docs`.
- `src/scripts/`: client-only code imported from `<script>` tags. It may touch
  `document`.
- `src/lib/<area>/` (`docs`, `blog`, `og`, `site`): code that runs at build time
  or is shared with tests; it never touches the DOM. Filenames are one word.
- `src/plugins/`: Markdown plugins.

Scripts, tests and plugins find elements through `data-*` attributes, roles or
ARIA state (`data-header`, `[aria-current]`), never through styling classes.
Style states with variants over the same attributes (`current:`, `data-hero-visible:`).
Prefer semantic elements (`header`, `nav`, `main`, `article`, `section`, `aside`,
`time`, `figure`, `dl`, `ol`) and `button` for actions, `a` for navigation.

Run `npm run fmt` (Oxfmt, configured in `.oxfmtrc.json`) before delivery.
`npm run fmt:check` is part of `npm test`. Markdown and MDX are not formatted, so
prose is never rewritten.

## Documentation

Full documentation: https://docs.astro.build

Consult these guides before working on related tasks:

- [Adding pages, dynamic routes, or middleware](https://docs.astro.build/en/guides/routing/)
- [Working with Astro components](https://docs.astro.build/en/basics/astro-components/)
- [Using React, Vue, Svelte, or other framework components](https://docs.astro.build/en/guides/framework-components/)
- [Adding or managing content](https://docs.astro.build/en/guides/content-collections/)
- [Adding styles or using Tailwind](https://docs.astro.build/en/guides/styling/)
- [Supporting multiple languages](https://docs.astro.build/en/guides/internationalization/)
