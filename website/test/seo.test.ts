import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";
import sharp from "sharp";
import { breadcrumbData, canonicalURL, serializeStructuredData } from "../src/lib/site/seo.ts";
import { required } from "./dom.ts";
import { checkoutModules } from "./modules.ts";

/**
 * JSON-LD variants emitted by the controlled local build. This is a test
 * projection, not a general Schema.org validator: assertions below compare
 * each consumed field with visible markup and fixture metadata. The tags let
 * find() retain each node's concrete shape without unchecked property casts.
 */
type GraphNode =
  | { "@type": "WebPage"; url: string; name: string; description: string }
  | { "@type": "WebSite"; url: string }
  | { "@type": "SoftwareSourceCode"; url: string; codeRepository: string; license: string }
  | { "@type": "BreadcrumbList"; itemListElement: { name: string; item: string }[] }
  | {
      "@type": "BlogPosting";
      mainEntityOfPage: string;
      headline: string;
      description: string;
      author: { name: string; url: string };
      publisher: { name: string };
      datePublished: string;
      dateModified?: string;
      image: string[];
    };

/** Entity spellings that the markup assertions decode before comparing text. */
const entities: Record<string, string> = {
  "&amp;": "&",
  "&lt;": "<",
  "&gt;": ">",
  "&quot;": '"',
  "&apos;": "'",
  "&#39;": "'",
  "&#x27;": "'",
};

/** Resolve generated files independently of the invoking shell's directory. */
const dist = fileURLToPath(new URL("../dist/", import.meta.url));

/**
 * Decode the entity spellings emitted in metadata and navigation text. This
 * keeps assertions independent of HTML escaping without prescribing wording.
 */
function decode(text: string) {
  return text.replace(/&(?:amp|lt|gt|quot|apos|#39|#x27);/g, (entity: string) =>
    required(entities[entity]),
  );
}

/**
 * Read one double-quoted attribute and decode its text. Missing attributes
 * return an empty string; missing markup fails through required() because it
 * indicates an invalid fixture or an absent generated metadata element.
 */
function attribute(tag: string | undefined, name: string) {
  return decode(required(tag).match(new RegExp(`\\b${name}="([^"]*)"`))?.[1] ?? "");
}

/** Collect content pages, excluding the error page and meta-refresh redirects. */
function contentPages() {
  assert.ok(existsSync(dist), "run npm run build before testing SEO");
  return readdirSync(dist, { recursive: true, encoding: "utf8" })
    .filter((file) => file.endsWith(".html") && file !== "404.html")
    .map((file) => ({ file, html: readFileSync(resolve(dist, file), "utf8") }))
    .filter(({ html }) => !html.includes('http-equiv="refresh"'));
}

/**
 * Read the known graph emitted by the shared layout. Duplicate scripts fail
 * before JSON parsing, and malformed JSON propagates its parse error. The
 * consumed node fields are checked against markup by the individual tests.
 */
function graph(html: string): GraphNode[] {
  const scripts = [
    ...html.matchAll(/<script\b[^>]*type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/g),
  ];
  assert.equal(scripts.length, 1);
  const data: { "@context": string; "@graph": GraphNode[] } = JSON.parse(scripts[0][1]);
  assert.equal(data["@context"], "https://schema.org");
  return data["@graph"];
}

/**
 * Map a public absolute URL to Astro's file-format HTML output. Only its path
 * selects the file; the homepage uses index.html and fragments do not affect
 * lookup. The caller subsequently checks that the output actually exists.
 */
function htmlFile(url: string | URL) {
  const path = new URL(url).pathname;
  return resolve(dist, path === "/" ? "index.html" : `${path.slice(1)}.html`);
}

/**
 * Collect sitemap locations in document order without depending on the
 * integration's whitespace. Entity decoding preserves query separators.
 */
function locations(xml: string) {
  return [...xml.matchAll(/<loc>([^<]+)<\/loc>/g)].map((match) => decode(match[1]));
}

test("canonical URLs remove file suffixes, filters, fragments, and trailing slashes", () => {
  assert.equal(
    canonicalURL("/docs/example.html?q=filter#part", "https://example.test").href,
    "https://example.test/docs/example",
  );
  assert.equal(canonicalURL("/docs/", "https://example.test").href, "https://example.test/docs");
  assert.equal(canonicalURL("/index.html", "https://example.test").href, "https://example.test/");
});

test("an explicit canonical URL can point to a different preferred page", () => {
  assert.equal(
    canonicalURL("https://preferred.test/guide?utm_source=test", "https://example.test").href,
    "https://preferred.test/guide",
  );
});

test("JSON-LD serialization cannot terminate its containing script element", () => {
  const fixture = { headline: '</script><script>alert("test")</script>', name: "A & B" };
  const serialized = serializeStructuredData(fixture);
  assert.ok(!serialized.includes("<"));
  assert.deepEqual(JSON.parse(serialized), fixture);
});

test("breadcrumb schema preserves labels, section fragments, and order", () => {
  assert.deepEqual(
    breadcrumbData(
      [
        { name: "Index", href: "/docs#group" },
        { name: "A & B", href: "/docs/example" },
      ],
      "https://example.test",
    ),
    {
      "@type": "BreadcrumbList",
      itemListElement: [
        {
          "@type": "ListItem",
          position: 1,
          name: "Index",
          item: "https://example.test/docs#group",
        },
        {
          "@type": "ListItem",
          position: 2,
          name: "A & B",
          item: "https://example.test/docs/example",
        },
      ],
    },
  );
});

test("every content page has consistent metadata, a valid graph, and discovery links", () => {
  const pages = contentPages();
  assert.ok(pages.length > 0);
  for (const { file, html } of pages) {
    const links = html.match(/<link\b[^>]*>/g) ?? [];
    const canonical = links.filter((link) => attribute(link, "rel") === "canonical");
    assert.equal(canonical.length, 1, file);
    const url = attribute(canonical[0], "href");
    assert.equal(new URL(url).origin, "https://arkoren.dev", file);
    assert.equal(htmlFile(url), resolve(dist, file), file);
    const data = graph(html);
    const page = required(data.find((node) => node["@type"] === "WebPage"));
    assert.equal(page.url, url, file);
    assert.equal(page.name, decode(required(html.match(/<title>([^<]+)<\/title>/))[1]), file);
    const meta = html.match(/<meta\b[^>]*>/g) ?? [];
    const description = meta.find((tag) => attribute(tag, "name") === "description");
    assert.ok(attribute(description, "content"), file);
    assert.equal(page.description, attribute(description, "content"), file);
    const social = (key: string) =>
      meta
        .filter((tag) => attribute(tag, "property") === key || attribute(tag, "name") === key)
        .map((tag) => attribute(tag, "content"));
    const title = social("og:title");
    assert.equal(title.length, 1, file);
    assert.ok(title[0], file);
    assert.deepEqual(social("twitter:title"), title, file);
    assert.deepEqual(social("og:description"), [page.description], file);
    assert.deepEqual(social("og:url"), [url], file);
    const images = social("og:image");
    assert.equal(images.length, 1, file);
    assert.ok(existsSync(resolve(dist, new URL(images[0]).pathname.slice(1))), file);
    assert.deepEqual(social("twitter:image"), images, file);
    assert.ok(social("og:image:alt")[0], file);
    assert.ok(
      !meta.some(
        (tag) =>
          ["robots", "googlebot", "bingbot"].includes(attribute(tag, "name")) &&
          /noindex|nosnippet|none/i.test(attribute(tag, "content")),
      ),
      file,
    );
    assert.ok(
      links.some(
        (link) =>
          attribute(link, "rel") === "sitemap" && attribute(link, "href") === "/sitemap-index.xml",
      ),
      file,
    );
    assert.ok(
      links.some(
        (link) =>
          attribute(link, "rel") === "describedby" && attribute(link, "href") === "/llms.txt",
      ),
      file,
    );
  }
});

test("the sitemap contains exactly canonical content pages and no redirects or import stubs", () => {
  const sitemapURLs = locations(readFileSync(resolve(dist, "sitemap-index.xml"), "utf8"));
  assert.ok(sitemapURLs.length > 0);
  const urls = sitemapURLs.flatMap((url) => {
    assert.equal(new URL(url).origin, "https://arkoren.dev");
    return locations(readFileSync(resolve(dist, new URL(url).pathname.slice(1)), "utf8"));
  });
  assert.equal(new Set(urls).size, urls.length);
  const expected = contentPages()
    .filter(
      ({ file }) =>
        file === "index.html" ||
        file === "docs.html" ||
        file === "blog.html" ||
        file.startsWith("docs/") ||
        file.startsWith("blog/"),
    )
    .map(({ file }) => canonicalURL(`/${file}`, "https://arkoren.dev").href);
  assert.deepEqual([...urls].sort(), expected.sort());
  for (const url of urls) assert.ok(existsSync(htmlFile(url)), url);
});

test("the homepage connects the website identity to its visible source repository", () => {
  const html = readFileSync(resolve(dist, "index.html"), "utf8");
  const data = graph(html);
  const website = required(data.find((node) => node["@type"] === "WebSite"));
  const software = data.find((node) => node["@type"] === "SoftwareSourceCode");
  assert.ok(software);
  assert.equal(software.url, website.url);
  assert.equal(new URL(software.codeRepository).protocol, "https:");
  assert.ok(html.includes(`href="${software.codeRepository}"`));
  assert.ok(html.includes(`href="${software.license}"`));
});

test("robots permits public crawlers and advertises the generated sitemap", () => {
  const robots = readFileSync(resolve(dist, "robots.txt"), "utf8");
  assert.match(robots, /User-agent: \*\nAllow: \/\n/);
  assert.ok(!robots.includes("Disallow:"));
  const sitemap = required(robots.match(/^Sitemap: (.+)$/m))[1];
  assert.equal(new URL(sitemap).origin, "https://arkoren.dev");
  assert.ok(existsSync(resolve(dist, new URL(sitemap).pathname.slice(1))));
});

test("documentation breadcrumbs match the visible navigation and resolve to real anchors", () => {
  for (const { file, html } of contentPages().filter(
    ({ file }) => file === "docs.html" || file.startsWith("docs/"),
  )) {
    const items = required(
      graph(html).find((node) => node["@type"] === "BreadcrumbList"),
    ).itemListElement;
    const nav = required(html.match(/<nav\b[^>]*aria-label="Breadcrumb"[\s\S]*?<\/nav>/))[0];
    const labels = [...nav.matchAll(/<li\b[^>]*>([\s\S]*?)<\/li>/g)].map((match) =>
      decode(match[1].replace(/<[^>]+>/g, "")),
    );
    assert.deepEqual(
      labels,
      items.map((item) => item.name),
      file,
    );
    assert.equal([...nav.matchAll(/aria-current="page"/g)].length, 1, file);
    for (const item of items) {
      const url = new URL(item.item);
      const target = readFileSync(htmlFile(url), "utf8");
      if (url.hash !== "") assert.ok(target.includes(`id="${url.hash.slice(1)}"`), item.item);
    }
  }
});

test("Markdown alternates exist and link back to the canonical HTML page", () => {
  for (const { file, html } of contentPages()) {
    const alternate = (html.match(/<link\b[^>]*>/g) ?? []).find(
      (link) => attribute(link, "type") === "text/markdown",
    );
    if (alternate === undefined) continue;
    const raw = new URL(attribute(alternate, "href"), "https://arkoren.dev");
    const markdown = readFileSync(resolve(dist, raw.pathname.slice(1)), "utf8");
    const canonical = canonicalURL(`/${file}`, "https://arkoren.dev").href;
    assert.ok(markdown.includes(`[HTML documentation](${canonical})`), file);
  }
});

test("the API catalog is indexed without local declarations or Markdown alternates", () => {
  const pages = contentPages().filter(({ file }) => file.startsWith("docs/api"));
  assert.deepEqual(
    pages.map(({ file }) => file),
    ["docs/api.html"],
  );
  const html = pages[0].html;
  assert.doesNotMatch(html, /type="text\/markdown"/);
  assert.ok(
    graph(html).some(
      (node) => node["@type"] === "WebPage" && node.url === "https://arkoren.dev/docs/api",
    ),
  );
  const index = readFileSync(resolve(dist, "docs/api/llms.txt"), "utf8");
  const root = readFileSync(resolve(dist, "llms.txt"), "utf8");
  const urls = [...index.matchAll(/\]\((https:\/\/pkg\.go\.dev\/[^)]+)\)/g)].map(
    (match) => match[1],
  );
  assert.deepEqual(
    urls.toSorted(),
    checkoutModules().map((path) => `https://pkg.go.dev/${path}`),
  );
  for (const url of urls) {
    assert.match(
      url,
      /^https:\/\/pkg\.go\.dev\/arkoren\.dev\/(?:core|(?:foundation|driver)\/[^/]+)$/,
    );
    assert.ok(root.includes(url), url);
  }
  assert.doesNotMatch(index, /\/docs\/api\/.*\.md|\/docs\/api\/llms-full\.txt/);
  assert.equal(existsSync(resolve(dist, "docs/api/llms-full.txt")), false);
});

test("article schema propagates visible title, description, author, and publication dates", () => {
  for (const { file, html } of contentPages().filter(({ file }) => file.startsWith("blog/"))) {
    const article = required(graph(html).find((node) => node["@type"] === "BlogPosting"));
    const page = required(graph(html).find((node) => node["@type"] === "WebPage"));
    assert.equal(article.mainEntityOfPage, page.url, file);
    assert.equal(
      article.headline,
      decode(required(html.match(/<h1\b[^>]*>([^<]+)<\/h1>/))[1]),
      file,
    );
    assert.equal(article.description, page.description, file);
    assert.ok(html.includes(article.author.name.replaceAll("&", "&amp;")), file);
    const meta = html.match(/<meta\b[^>]*>/g) ?? [];
    assert.equal(
      article.datePublished,
      attribute(
        meta.find((tag) => attribute(tag, "property") === "article:published_time"),
        "content",
      ),
      file,
    );
    const modified = meta.find((tag) => attribute(tag, "property") === "article:modified_time");
    assert.equal(
      article.dateModified,
      modified === undefined ? undefined : attribute(modified, "content"),
      file,
    );
    assert.ok(
      meta.some(
        (tag) =>
          attribute(tag, "property") === "og:type" && attribute(tag, "content") === "article",
      ),
      file,
    );
  }
});

test("Cloudflare header rules canonicalize Markdown and keep agent indexes fetchable but unindexed", () => {
  const source = readFileSync(resolve(dist, "_headers"), "utf8");
  const rules = source
    .split(/\n\s*\n/)
    .map((block) => block.split("\n").filter((line) => !line.startsWith("#")))
    .filter((lines) => lines[0])
    .map(([pattern, ...lines]) => ({ pattern, lines }));
  assert.ok(rules.length <= 100);
  for (const rule of rules) assert.ok(rule.pattern.split("*").length <= 2, rule.pattern);
  for (const { file, html } of contentPages().filter(({ file }) => file.startsWith("docs/"))) {
    const path = `/${file.replace(/\.html$/, ".md")}`;
    if (!existsSync(resolve(dist, path.slice(1)))) continue;
    const rule = rules.find(
      ({ pattern }) => pattern.endsWith(".md") && path.startsWith(pattern.split("*")[0]),
    );
    assert.ok(rule, path);
    const [prefix, suffix] = rule.pattern.split("*");
    const splat = path.slice(prefix.length, -suffix.length);
    const link = required(rule.lines.find((line) => line.trim().startsWith("Link:"))).replace(
      ":splat",
      splat,
    );
    const canonical = (html.match(/<link\b[^>]*>/g) ?? []).find(
      (tag) => attribute(tag, "rel") === "canonical",
    );
    assert.ok(link.includes(`<${attribute(canonical, "href")}>; rel="canonical"`), file);
  }
  for (const file of readdirSync(dist, { recursive: true, encoding: "utf8" }).filter((file) =>
    /llms(?:-full)?\.txt$/.test(file),
  )) {
    const path = `/${file}`;
    const matching = rules.filter(({ pattern }) => {
      const [prefix, suffix] = pattern.split("*");
      return path.startsWith(prefix) && path.endsWith(suffix);
    });
    assert.ok(
      matching.some(({ lines }) => lines.some((line) => line.trim() === "X-Robots-Tag: noindex")),
      file,
    );
  }
});

test("a fixture build excludes drafts and propagates article metadata without unsafe script content", async () => {
  const website = fileURLToPath(new URL("../", import.meta.url));
  const sharedFiles = [
    ".astro/content-assets.mjs",
    ".astro/content-modules.mjs",
    "node_modules/.astro/data-store.json",
  ];
  const sharedBefore = sharedFiles.map((file) =>
    existsSync(resolve(website, file)) ? readFileSync(resolve(website, file)) : undefined,
  );
  const directory = realpathSync(mkdtempSync(resolve(tmpdir(), "arkoren-seo-")));
  try {
    const input = resolve(directory, "posts");
    mkdirSync(input);
    await sharp({ create: { width: 1600, height: 800, channels: 3, background: "#ff2056" } })
      .png()
      .toFile(resolve(input, "banner.png"));
    writeFileSync(
      resolve(input, "visible.md"),
      `---
title: 'Fixture </script> & title'
description: 'Fixture description'
publishedAt: 2026-01-02
updatedAt: 2026-02-03
author:
  name: 'Fixture author'
  url: 'https://example.com/fixture-author'
  avatar: './banner.png'
category: 'Fixture category'
authorDesk: 'Fixture desk & context'
behindPost:
  title: 'Fixture behind title'
  text: 'Fixture behind context'
readingShelf:
  - title: 'Fixture shelf link'
    description: 'Fixture shelf context'
    href: https://example.com/reading
explore:
  - title: 'Fixture next step'
    description: 'Fixture link context'
    href: /docs
banner:
  src: './banner.png'
  alt: 'Fixture banner'
  width: 1200
  height: 600
---

## Section

Fixture body.
`,
    );
    writeFileSync(
      resolve(input, "hidden.md"),
      `---
title: 'Hidden fixture'
description: 'Unpublished fixture'
publishedAt: 2026-01-01
category: 'Fixture category'
draft: true
---

Unpublished body.
`,
    );
    writeFileSync(
      resolve(input, "plain.md"),
      `---
title: 'Plain fixture'
description: 'No optional editorial content'
publishedAt: 2026-01-01
category: 'Fixture category'
---

## Plain section

Fixture body without a margin.
`,
    );
    const output = resolve(directory, "output");
    // Isolate generated .astro files and caches, not just the HTML output. A
    // fixture build must never replace the imports used by a running dev server.
    const root = resolve(directory, "site");
    mkdirSync(root);
    for (const entry of ["src", "public", "tsconfig.json"]) {
      cpSync(resolve(website, entry), resolve(root, entry), { recursive: true });
    }
    symlinkSync(resolve(website, "node_modules"), resolve(root, "node_modules"), "dir");
    const config = resolve(root, "astro.config.ts");
    writeFileSync(
      config,
      `import config from ${JSON.stringify(pathToFileURL(resolve(website, "astro.config.ts")).href)};
export default { ...config, root: ${JSON.stringify(root)}, cacheDir: ${JSON.stringify(resolve(root, ".cache"))} };
`,
    );
    execFileSync(
      "npm",
      [
        "exec",
        "--",
        "astro",
        "build",
        "--root",
        root,
        "--config",
        "astro.config.ts",
        "--force",
        "--outDir",
        output,
      ],
      {
        // Astro stages prerender output under cwd when outDir is outside it.
        // A fixture cwd keeps that staging area out of the working website.
        cwd: root,
        env: {
          ...process.env,
          ARKOREN_BLOG: input,
          ARKOREN_REFERENCES: resolve(website, "../cmd/arkoren/skills/arkoren/references"),
          ARKOREN_REPO: resolve(website, ".."),
          PUBLIC_GOOGLE_SITE_VERIFICATION: "fixture-google-token",
          PUBLIC_BING_SITE_VERIFICATION: "fixture-bing-token",
        },
        timeout: 60000,
        stdio: "pipe",
      },
    );
    for (const [index, file] of sharedFiles.entries()) {
      const after = existsSync(resolve(website, file))
        ? readFileSync(resolve(website, file))
        : undefined;
      assert.deepEqual(after, sharedBefore[index], `fixture build must not overwrite ${file}`);
    }
    assert.ok(!existsSync(resolve(output, "blog/hidden.html")));
    const archive = readFileSync(resolve(output, "blog.html"), "utf8");
    assert.ok(!archive.includes('href="/blog/hidden"'));
    const sitemap = readFileSync(resolve(output, "sitemap-0.xml"), "utf8");
    assert.ok(!sitemap.includes("/blog/hidden"));
    assert.ok(sitemap.includes("/blog/visible"));
    const plain = readFileSync(resolve(output, "blog/plain.html"), "utf8");
    assert.doesNotMatch(plain, /data-blog-margin|data-blog-explore/);
    assert.match(plain, /data-blog-reading/);
    assert.match(plain, /data-toc-mobile/);
    const html = readFileSync(resolve(output, "blog/visible.html"), "utf8");
    const banner = required(html.match(/<figure\b[^>]*data-banner[\s\S]*?<\/figure>/))[0];
    assert.match(banner, /\bloading="eager"/);
    assert.match(banner, /\bfetchpriority="high"/);
    const toc = required(html.match(/<aside\b[^>]*data-blog-aside[\s\S]*?<\/aside>/))[0];
    assert.match(toc, /aria-label="On this page"/);
    assert.doesNotMatch(toc, /aria-label="Author’s desk"|aria-label="Reading shelf"/);
    const margin = required(html.match(/<aside\b[^>]*data-blog-margin[\s\S]*?<\/aside>/))[0];
    assert.doesNotMatch(margin, /aria-label="On this page"/);
    assert.match(margin, /aria-label="Author’s desk"/);
    assert.ok(margin.includes("Fixture desk &amp; context"));
    assert.ok(margin.includes("Fixture author"));
    assert.match(margin, /aria-label="Behind the post"/);
    assert.ok(margin.includes("Fixture behind title"));
    assert.ok(margin.includes("Fixture behind context"));
    assert.match(margin, /aria-label="Reading shelf"/);
    assert.match(margin, /href="https:\/\/example.com\/reading"/);
    assert.ok(margin.includes("Fixture shelf link"));
    assert.ok(margin.includes("Fixture shelf context"));
    const explore = required(html.match(/<nav\b[^>]*data-blog-explore[\s\S]*?<\/nav>/))[0];
    assert.match(explore, /aria-label="Keep exploring"/);
    assert.match(explore, /href="\/docs"/);
    assert.ok(explore.includes("Fixture next step"));
    assert.ok(explore.includes("Fixture link context"));
    const candidates = attribute(required(banner.match(/<img\b[^>]*>/))[0], "srcset").split(", ");
    assert.ok(candidates.length > 0);
    for (const candidate of candidates) {
      assert.ok(existsSync(resolve(output, candidate.split(" ")[0].slice(1))));
    }
    const avatar = required(html.match(/<img\b[^>]*data-avatar[^>]*>/))[0];
    assert.ok(attribute(avatar, "srcset"));
    const article = required(graph(html).find((node) => node["@type"] === "BlogPosting"));
    assert.equal(article.headline, "Fixture </script> & title");
    assert.equal(article.author.name, "Fixture author");
    assert.equal(article.author.url, "https://example.com/fixture-author");
    assert.equal(article.datePublished, "2026-01-02T00:00:00.000Z");
    assert.equal(article.dateModified, "2026-02-03T00:00:00.000Z");
    assert.ok(article.publisher.name.length > 0);
    // Quoted values may contain ">", such as the fixture title's "</script>".
    const meta = html.match(/<meta\b(?:[^>"]|"[^"]*")*>/g) ?? [];
    const content = (key: string) =>
      meta
        .filter((tag) => attribute(tag, "property") === key || attribute(tag, "name") === key)
        .map((tag) => attribute(tag, "content"));
    assert.deepEqual(content("og:title"), ["Fixture </script> & title"]);
    assert.deepEqual(content("og:pubdate"), [article.datePublished]);
    assert.deepEqual(content("article:author"), [article.author.url]);
    assert.deepEqual(content("twitter:data1"), [article.author.name]);
    assert.equal(content("twitter:data2").length, 1);
    // The shared card and Google's three article images are generated for the post.
    const images = [...content("og:image"), ...article.image].map((url) => new URL(url).pathname);
    assert.equal(article.image.length, 3);
    for (const path of images) {
      assert.match(path, /^\/og\/blog\/visible-/);
      const png = readFileSync(resolve(output, path.slice(1)));
      assert.equal(png.subarray(0, 8).toString("hex"), "89504e470d0a1a0a", path);
    }
    const shared = readFileSync(resolve(output, images[0].slice(1)));
    assert.deepEqual([shared.readUInt32BE(16), shared.readUInt32BE(20)], [1200, 630]);
    assert.equal(
      attribute(
        meta.find((tag) => attribute(tag, "name") === "google-site-verification"),
        "content",
      ),
      "fixture-google-token",
    );
    assert.equal(
      attribute(
        meta.find((tag) => attribute(tag, "name") === "msvalidate.01"),
        "content",
      ),
      "fixture-bing-token",
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
