# Arkoren Website

This README is for contributors building, maintaining, and publishing the website.
For using Arkoren in an application, start with the
[installation guide](../cmd/arkoren/skills/arkoren/references/guides/first-steps/installation.md).

The site is Astro with Tailwind CSS v4. `src/styles/global.css` is its only design
system (tokens, utilities, element defaults); `prose.css`, `motion.css` and `season.css`
cover Markdown output, keyframes and the Halloween theme. Components live in
`src/components/<area>/`, client code in `src/scripts/`, build-time code in
`src/lib/<area>/`. The styling rules are in `AGENTS.md`.

The Astro website renders guides from
`../cmd/arkoren/skills/arkoren/references/`. Edit that Markdown, not copies under `src/`.
The same sources also produce the raw Markdown routes and `llms.txt` files.

`/docs/api` is a categorized catalog of core, foundation, and vendor library
modules at `pkg.go.dev/<module>` URLs, the canonical Go
API reference. Module declarations in
the checkout determine its links; no generated catalog or network check is
required. Links do not establish release or indexing availability. Package,
memory-driver, conformance-suite, CLI, and private-tooling entries are excluded.
Former HTML and raw API URLs redirect to their actual package on pkg.go.dev;
the former testing overview redirects to the testing guide. The retired API
bulk archive redirects to the module index, not an export of declarations.

Collections defer Markdown rendering to Vite rather than persisting rendered
HTML in the content store. Keep this enabled: content digests alone do not
invalidate HTML when a Markdown plugin's implementation changes. The warm-cache
regression test checks renderer updates against unchanged guides and posts.

## Build and check

Use Node.js 26.13 or later, the current LTS release. `.node-version` pins it for
local version managers and for Cloudflare Workers Builds. From `website/`, install the
dependencies and build:

```sh
npm install
npm run fmt
npm run check
npm run build
npm run test:docs
```

`npm run fmt` formats the website with [Oxfmt](https://oxc.rs/docs/guide/usage/formatter),
configured in `.oxfmtrc.json`: 2-space indentation, double quotes, semicolons,
and a print width of 100. It covers TypeScript, JSON, CSS, and `.astro` files, and
sorts Tailwind classes using `src/styles/global.css`;
Markdown, MDX, and `public/` are never rewritten. `npm run fmt:check` reports
unformatted files without changing them, and `npm test` runs it first.

The build writes `dist/` and creates the Pagefind search index. The docs tests
check the built pages for missing pages or anchors, duplicate IDs, and shared
desktop and mobile section navigation. Run them after rebuilding; they test
`dist/`, not the live development server.

Run `npm test` after website functionality changes to include routing, SEO, and
blog fixture checks as well as documentation checks and Chromium reader journeys.
Install the test browser once with `npx playwright install chromium` (on Linux,
use `npx playwright install --with-deps chromium`). `npm run test:static` runs
the Node-based checks; `npm run test:browser` runs the browser journeys alone.
For an installed Google Chrome, use `PLAYWRIGHT_CHANNEL=chrome npm run test:browser`.
Browser tests create an isolated fixture site and real Pagefind index, start and
stop their own preview server, and leave the working build and caches alone.
Failure traces are saved in gitignored `test-results/`.

Tests protect reader behavior and publishing, not the current design or wording.
Use fixtures for text-dependent behavior. Check layout and motion in a browser,
not with CSS-source assertions. Copy-only documentation edits do not require
changes to functionality tests. The contributor rules are in `AGENTS.md`.

The CI `docs` job runs installation, type checks, a fresh build, and the full
`npm test` suite on every routine run. The build checks the consumer guide index for
freshness without writing; Astro validates guide metadata. Offline source-link
containment and editorial source conventions require review, not a separate
repository policy suite. To exercise the rendered job
locally from the repository root, run:

```sh
make generate
make docs
```

New or materially revised guides also require the editorial review in the
contributor `developer-documentation` skill. Automated checks protect structure
and rendering; they do not establish the accuracy or teaching quality of prose.

The routing tests start Astro's preview server and check the built 404 fallback
for missing pages, generated guides, API pages, Markdown, and Go import paths.

The SEO tests check canonical URLs, sitemaps, structured data, breadcrumbs,
Markdown alternates, and Cloudflare header rules. A separate fixture build
checks that drafts stay unpublished and article metadata is rendered safely.
`ARKOREN_BLOG` overrides the blog source directory for that fixture build;
leave it unset for production builds.

## Images and icons

Keep image sources in `src/assets/images/`. Import SVGs with `?url` when an
external image is needed, or as Astro components for inline artwork. Use
`Image` from `astro:assets` for raster images so Astro can resize and optimize
them. Imported assets get content-hashed URLs; unused logo exports remain
available in the source tree rather than being copied into every build.

The shared social card is processed by `src/lib/site/brand.ts` as a 1200 × 630 PNG.
It is the fallback image for pages without a page-specific card, including the
API catalog. Guides and posts use the cards described under [Publish a blog post](#publish-a-blog-post).

The primary rose is `#c70036`, shared by the homepage banner, brand SVGs,
browser theme color, and social-card renderer. After editing the SVG masters,
run `npm run assets:brand` to regenerate logo/banner PNGs, the multi-size ICO,
and the Apple touch icon. The renderer uses bundled fonts for reproducible
exports. Run `npm run build` afterward to regenerate the optimized assets and
all page-specific social cards; their content hashes invalidate the old cache.

Browser icons are the exception: `public/favicon.svg`, `public/favicon.ico`
(16, 32, and 48 px), and `public/apple-touch-icon.png` (180 px) retain stable
URLs. After editing the SVG, regenerate the raster icons from it. These use
a solid rose background and a white mark, with square corners.

## Typography

Monospace text uses self-hosted Maple Mono v7.9 WOFF2 files in
`public/fonts/maple-mono/`, downloaded from the
[official release](https://github.com/subframe7536/maple-font/releases/tag/v7.9).
Keep its SIL Open Font License (`LICENSE.txt`) with the font files.
`src/styles/global.css` defines the font faces and the type scale; code blocks use its
`text-sm` mono step.
Geist remains the sans-serif font, loaded through Google Fonts.

Documentation tables fill the content column. The Markdown table plugin adds
a keyboard-focusable horizontal scroll container at build time, so wide tables
scroll within the column even without JavaScript. Shared styles retain native
table layout and keep code identifiers on one line.

## Search and agent discovery

For coding tasks, the recommended agent path is an `llms.txt` index followed by
individual guide Markdown pages. The site-wide and guides `llms-full.txt` files
export all guides for offline indexing or deliberate large-context use; they do
not include API declarations. API indexes link only to module roots on
pkg.go.dev. Each export includes generated content sizes and a rough character-based
token estimate, not a model-specific tokenizer count. Ordinary website clicks
open a shared warning dialog; direct URLs and no-JavaScript links remain usable.

The official `@astrojs/sitemap` integration generates `sitemap-index.xml` and
its page sitemap. It includes canonical HTML content, not import-path landing
pages, redirects, raw Markdown, or `llms.txt` files. Modification dates are
omitted rather than guessed from build times. `robots.txt` permits public
crawlers and links to the sitemap. This policy also permits training crawlers;
change it separately if you want to restrict training access.

The shared layout emits canonical and social metadata, a site identity, and
page-specific JSON-LD. Documentation breadcrumbs and their structured data
use the same navigation items. Blog metadata comes from the post frontmatter.
For pages that need a different preferred URL, pass `canonical` to the main
layout; it also sets the Open Graph URL and WebPage identity.

To verify the domain with Google or Bing through HTML metadata, set
`PUBLIC_GOOGLE_SITE_VERIFICATION` or `PUBLIC_BING_SITE_VERIFICATION` in the
build environment, then rebuild. Both values are optional public tokens, not
API keys. Submit `https://arkoren.dev/sitemap-index.xml` in the relevant
webmaster tools after deployment.

The site deploys to Cloudflare Workers as static assets. `wrangler.jsonc` holds the
configuration: it uploads `dist/` with no Worker script and no SSR adapter, so
asset requests are free and do not count against the Workers daily request
limit. Adding a Worker script or `run_worker_first` would change that. Keep the
Worker name in `wrangler.jsonc` identical to the name in the Cloudflare dashboard.

Workers Builds deploys from GitHub with these settings: root directory `website`,
build command `npm run build`, deploy command `npx wrangler deploy`, and
production branch `main`. In **Settings > Builds > Previews Base**, turn off
**Builds for Preview branches** to avoid PR preview deployments and their
Cloudflare bot comments. Production deployments continue to serve `https://arkoren.dev`.
Deployments never create Git tags or releases. The build uses the Wrangler
version pinned in `package.json`. To check the configuration without uploading
anything, build the site and run
`npx wrangler deploy --dry-run`.

In **Settings > Builds > Production > Build watch paths**, set include paths to
`website/*`, `cmd/arkoren/skills/arkoren/*`, `*.go`, and `*go.mod`.
Set exclude paths to `*_test.go`. Cloudflare's
[build watch paths](https://developers.cloudflare.com/workers/ci-cd/builds/build-watch-paths/)
match repository-relative paths, not paths relative to the `website` build
directory. Its prefix and suffix wildcards also match nested paths, so
`website/*` includes descendants and `*.go` includes Go files across modules.
Exclusions take precedence. The Go inputs are needed because the
site discovers module declarations, package paths, and package comments for its
API catalog and vanity-import pages. Contributor-only skills and unrelated files
do not trigger a deployment. Cloudflare bypasses watch paths for empty pushes
or pushes with at least 3,000 changed files or 20 commits.
The required GitHub `website` check runs independently on every PR; it does not
deploy the site or post preview comments.

The build reads the Node version from `website/.node-version`. A `NODE_VERSION`
build variable set in the Cloudflare dashboard overrides that file; if you set
one, use `26.13.0` or newer. Cloudflare reads the
copied `public/_headers` rules to give Markdown
responses canonical HTTP links to HTML and mark agent indexes and bundles
`noindex`. The files remain fetchable by agents. These rules do not apply to
responses generated by Worker code. Other hosts must
configure equivalent HTTP headers; Astro's static build does not preserve
endpoint response headers by itself.

The build also writes a root `404.html` from `src/pages/404.astro`.
`wrangler.jsonc` sets `assets.not_found_handling` to `404-page`, so Cloudflare
serves it with status `404` for missing URLs at any depth, including generated
documentation pages, as described in
[Cloudflare's custom 404 setup](https://developers.cloudflare.com/workers/static-assets/routing/static-site-generation/#custom-404-pages).
On other hosts, configure the error document as `/404.html` and retain HTTP
status `404` and the requested URL. Do not redirect missing URLs to `/404` or
serve the home page with status `200`. The error page includes shared navigation
and search, but is excluded from search indexes and the sitemap.

After deployment, verify the live HTML, `/robots.txt`, `/sitemap-index.xml`,
`/llms.txt`, and a documentation `.md` URL. Check that the Markdown response
has its canonical `Link` header and `/llms-full.txt` has `X-Robots-Tag: noindex`.
Check CDN rules for redirects and browser challenges: website code cannot
override a domain-level redirect or guarantee crawler access through a WAF.

## Release notes

Published module history lives only in `src/content/releases/`, not in blog posts
or the CLI's embedded guides. The `releases` content collection loads these files
offline. The changelog reader and automated sync PRs are upcoming; this importer
does not publish modules, tags, GitHub releases, or website deployments.

To import confirmed GitHub publications, use Node.js 26.13+ and an authenticated
`gh` CLI with repository read access. From `website/`, run:

```sh
npm run releases:sync
npm run releases:check
```

Sync reads every page of GitHub releases and peels annotated tags through the
read-only GitHub Git API to full commit SHAs. It includes prereleases (including
plain `v0.1.0` tags marked prerelease on GitHub) and excludes drafts. Unknown
published modules or malformed tags stop the import: reconcile the publication
and committed module inventory before retrying. A branch-valued
`target_commitish` is not proof of a release commit; the peeled tag is authoritative.
A full-SHA `target_commitish` must agree with that commit.

One managed ordinary Markdown file represents each module/version, for example
`src/content/releases/driver/redis/v0.1.0.md`. Its frontmatter is JSON, a YAML
subset, with exactly these fields:

```yaml
---
{
  "module": "arkoren.dev/driver/redis",
  "version": "v0.1.0",
  "publishedAt": "2026-11-01T14:07:41Z",
  "commit": "56b2d33afea8ae0614e973e5efc2af2bd6eed39d",
  "prerelease": true
}
---
```

This example comes from the initial Redis publication; sync obtains metadata from GitHub.
`publishedAt` is the release's publication timestamp, not its tag or commit date.
Module names must match committed canonical `go.mod` declarations. Versions are
normal `v0.X.Y` values without suffixes or leading zeros. Filename and metadata
must agree; duplicate identities, extra metadata, invalid dates, and path traversal
are rejected. Tag, GitHub URL, and future `/changelog/<module-directory>/<version>`
URL are derived by `releaseLinks`, not stored as untrusted frontmatter.

The body is the published GitHub body verbatim after replacing CRLF and bare CR
with LF. No trimming, final-newline insertion, Unicode normalization, link rewriting,
or prose editing occurs. Null GitHub bodies become empty bodies. The deterministic
frontmatter is followed by one newline after its closing fence; everything after
that newline belongs to the body. Correct notes on GitHub, rerun sync, review
the resulting diff, and merge through the normal website checks. An unchanged
rerun changes no bytes; `releases:check` exits nonzero if confirmed records differ.
Neither command deletes older files missing from an API response.

All remote records and existing managed files are validated before writing.
API or validation failure leaves files unchanged. Writes use atomic per-file
renames; a disk failure during writing can leave a partial batch, so review the
diff and rerun after recovery. The importer rejects symlinks and writes only
managed release files. Future sync automation can use the same functions and
restrict its PR diff to `website/src/content/releases/`; preview artifacts and
proposed versions are never publication evidence.

Release bodies are untrusted input. Loading never compiles Markdown as MDX or
executes expressions, imports, shell text, or raw HTML. The shared
`renderReleaseBody` helper parses Markdown and rejects raw HTML and active or
ambiguous URL schemes rather than silently altering published text. If notes
fail that gate, correct the GitHub body and retry. Code fences containing HTML
remain inert code. The upcoming reader must use this helper, **not** Astro's
trusted-author `render()` pipeline or MDX. Relative Markdown links are preserved;
the reader must resolve them against GitHub source context, not website routes.
The initial imported bodies have no relative Markdown links. Safety and importer
fixtures run offline with `npm run test:releases`, also included in `npm test`.

## Publish a blog post

Add a Markdown file to `src/content/blog/`. Its filename, without the
extension, becomes the permanent URL: `introducing-arkoren.mdx` is published at
`/blog/introducing-arkoren`. Keep that filename stable after publication.

Use `.md` for prose and code. Use `.mdx` when a post renders site components,
such as `ContractGrid`, `ContractCard`, or `BadgeRow` with the lists in
`src/lib/docs/catalog.ts`; import them below the frontmatter. MDX reads `{` and `<`
outside code as expressions and tags, so keep them in code spans or blocks.

```yaml
---
title: "A short, descriptive title"
description: "A summary for the archive and social previews."
publishedAt: 2026-10-18
category: Announcements
author: Arkoren
draft: true
---
```

Write the body below the frontmatter. Start sections at `##`; the layout renders
the title and builds section navigation from those headings. Add `updatedAt`
when you revise a published post.

`author` defaults to `Arkoren`. Give a name alone (`author: Jane Doe`), or an
object when the byline needs more:

```yaml
author:
  name: Erik C. Forés
  title: Sr. Cloud Technical Lead       # muted role beneath the name
  url: https://erik.cat                 # structured data and article:author
  avatar: ../../assets/images/authors/erik-avatar.webp
  company:                              # appended after the role
    name: HP
    url: https://hp.com/
```

Posts can opt into a reader's margin with these frontmatter fields:

```yaml
authorDesk: "A short personal aside, signed with the post's author name."
behindPost:
  title: "How this article came together"
  text: "A detail about the examples, research, or editorial choices."
readingShelf:
  - title: The quickstart
    description: "A useful starting point for trying the ideas."
    href: /docs/guides/first-steps/quickstart
explore:
  - title: Follow the quickstart
    description: "Build a service, one step at a time."
    href: /docs/guides/first-steps/quickstart
```

All fields are optional. On wide screens, the author's desk, behind-the-post
note, and annotated reading shelf occupy the outer column, with the sticky
table of contents between the article and the margin. At 1100px and below,
the reader's margin is hidden. Keep its content supplementary; essential
information belongs in the article. Shelf links accept local paths or HTTP(S) URLs.
Docs and blog pages use `Contents.astro` for both desktop navigation
and the closed-by-default “On this page” disclosure at 1100px and below.
The desktop version shares the active-section marker and scroll tracking.
`explore` adds a reading list after the body;
links accept local paths or HTTP(S) URLs. Keep notes short and supplementary
so readers can follow the article without leaving the main column.

For an author object, only `name` is required. `avatar` is an image path relative to the post; the
collection's `image()` helper validates the file, and Astro generates
display-sized images. Posts without one use the site logo. To show
a banner above the “All posts” link, add `banner` with `src`, `alt`, `width`, and
`height`. Use the image's intrinsic dimensions so the layout reserves its space.
The announcement uses `../../assets/images/blog/introducing-arkoren.svg` (1200 × 400,
3:1).

The banner is not the social image. Every post and guide gets a
generated 1200 × 630 card with its title (see `src/lib/og/image.ts`). To share a
designed image instead, add `ogImage` with `src` (a 1200 × 630 PNG or JPEG
relative to the post) and `alt`.

Set `draft: false` (or remove it) when the post is ready, then run
`npm run build` followed by `npm test`. Drafts are excluded from post pages, search, and
category selectors, including during development. Dates describe publication;
they do not schedule it. Use `draft: true` to withhold a future post.

Categories and counts are built from all published posts. Use consistent category
spelling and case; adding a new category requires no configuration. The archive
searches titles, descriptions, authors, categories, and body text. Submit a search
with Enter or the arrow button. Choose a category from the visible list, or click
the order text to cycle through newest first, oldest first, and title A–Z.
Category and order changes apply immediately and preserve the other filters.
Search terms match without regard to case, and every word must be present.

Archive state uses `q`, `category`, and `sort` query parameters, for example
`/blog?q=cache&category=Announcements&sort=oldest`. Sort values are `newest`
(the default), `oldest`, and `title`. Default values are omitted from generated
URLs. Unknown categories and sort values fall back to all categories and newest
first. JavaScript enhances the static archive; without it, all posts remain
readable, newest first.

After changing documentation, also refresh the consumer guide index and run
the website checks from the repository root:

```sh
make generate
make docs
```

## Preview

From `website/`, start the development server in the background:

```sh
npm run astro -- dev --background
```

Use the URL it prints to view the site. Manage it with
`npm run astro -- dev status`, `npm run astro -- dev logs`, and
`npm run astro -- dev stop`. To preview the production build instead, run
`npm run preview`.

The page shell is in `src/layouts/Docs.astro`, the documentation navigation is
in `src/components/docs/Sidebar.astro`, and the Markdown transformations are in
`src/plugins/references.ts`. Keep guide prose, code, and links in the Markdown
sources so web readers and coding agents receive the same information.
