import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { Window } from "happy-dom";
import { browserWindow, required } from "./dom.ts";

/** Resolve source and output independently of the invoking directory. */
const website = fileURLToPath(new URL("../", import.meta.url));
const dist = resolve(website, "dist");
const references = resolve(website, "../cmd/arkoren/skills/arkoren/references");

/** Load output for exhaustive link validation, not editorial assertions. */
function builtPages() {
  assert.ok(existsSync(dist), "run npm run build before testing docs");
  return new Map(
    readdirSync(dist, { recursive: true, encoding: "utf8" })
      .filter((name) => name.endsWith(".html"))
      .map((name) => [resolve(dist, name), readFileSync(resolve(dist, name), "utf8")]),
  );
}

/**
 * Collect one quoted attribute from generated HTML in document order. Decode
 * ampersands so URL comparisons preserve query separators; absent attributes
 * produce an empty list rather than a partial match.
 */
function attributes(html: string, name: string) {
  return [...html.matchAll(new RegExp(`\\b${name}="([^"]*)"`, "g"))].map((match) =>
    match[1].replaceAll("&amp;", "&"),
  );
}

test("generated social cards have valid PNG dimensions", () => {
  const cards = readdirSync(resolve(dist, "og"), { recursive: true, encoding: "utf8" }).filter(
    (file) => /-[0-9a-f]{10}\.png$/.test(file) && !/-(16x9|4x3|1x1)-/.test(file),
  );
  assert.ok(cards.length > 0);
  for (const file of cards) {
    const png = readFileSync(resolve(dist, "og", file));
    assert.equal(png.subarray(0, 8).toString("hex"), "89504e470d0a1a0a", file);
    assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [1200, 630], file);
  }
});

test("linked browser icons exist in the build", () => {
  const window = new Window();
  try {
    window.document.write(readFileSync(resolve(dist, "index.html"), "utf8"));
    const icons = window.document.querySelectorAll(
      'link[rel="icon"], link[rel="apple-touch-icon"]',
    );
    assert.ok(icons.length > 0);
    for (const icon of icons) assert.ok(existsSync(resolve(dist, `.${icon.getAttribute("href")}`)));
  } finally {
    window.close();
  }
});

test("documentation pages have one title and matching mobile and desktop contents", () => {
  for (const family of ["guides"]) {
    const sources = readdirSync(resolve(references, family), {
      recursive: true,
      encoding: "utf8",
    }).filter((name) => name.endsWith(".md") && !name.endsWith("/index.md"));
    assert.ok(sources.length > 0);
    for (const source of sources) {
      const page = resolve(dist, "docs", family, source.replace(/\.md$/, ".html"));
      const html = readFileSync(page, "utf8");
      assert.equal([...html.matchAll(/<h1\b/g)].length, 1, page);
      const mobile = html.match(/<details\b[^>]*data-toc-mobile[^>]*>([\s\S]*?)<\/details>/)?.[1];
      const rail = html.match(/<aside\b[^>]*data-docs-rail[^>]*>([\s\S]*?)<\/aside>/)?.[1];
      assert.ok(mobile, page);
      assert.ok(rail, page);
      assert.deepEqual(attributes(mobile, "href"), attributes(rail, "href"), page);
    }
  }
});

/** A missing or malformed comparison must not publish as an unrecognized prose table. */
test("every service drivers page publishes an accessible compatibility matrix", (t) => {
  const window = new Window();
  t.after(() => window.close());
  const guides = resolve(references, "guides");
  const sources = readdirSync(guides, { recursive: true, encoding: "utf8" }).filter((name) => {
    if (!name.endsWith(".md") || name === "core-concepts/facades-and-drivers.md") return false;
    return (
      /(?:^|\/)(?:[\w-]+-)?drivers\.md$/.test(name) ||
      /^## Drivers\s*$/m.test(readFileSync(resolve(guides, name), "utf8"))
    );
  });
  assert.ok(sources.length > 0);
  for (const source of sources) {
    window.document.body.innerHTML = readFileSync(
      resolve(dist, "docs/guides", source.replace(/\.md$/, ".html")),
      "utf8",
    );
    const matrices = [...window.document.querySelectorAll("main table")].filter((table) =>
      table.querySelector('tbody th[scope="row"]'),
    );
    assert.ok(matrices.length > 0, `${source}: missing compatibility matrix`);
    for (const table of matrices) {
      const headers = [...table.querySelectorAll('thead th[scope="col"]')];
      assert.ok(headers.length > 1, `${source}: name each capability column`);
      assert.ok(
        headers.every((header) => header.textContent.trim()),
        `${source}: unnamed capability column`,
      );
      for (const row of table.querySelectorAll("tbody tr")) {
        assert.ok(
          row.querySelector('th[scope="row"] a[href^="#"]')?.textContent.trim(),
          `${source}: link each driver row to its setup`,
        );
        const cells = [...row.querySelectorAll("td")];
        assert.equal(cells.length, headers.length - 1, `${source}: incomplete capability row`);
        for (const cell of cells) {
          const icon = cell.querySelector('[role="img"], button');
          assert.ok(icon, `${source}: missing status icon`);
          assert.ok(
            icon?.querySelector('svg[aria-hidden="true"]'),
            `${source}: missing status icon`,
          );
          assert.match(
            required(icon.getAttribute("aria-label")),
            /^(Yes|No|Unknown|Partial)$/,
            `${source}: missing accessible status`,
          );
          if (icon.tagName === "BUTTON") {
            const detail = icon.getAttribute("data-status-detail");
            assert.ok(detail !== null && detail.trim(), `${source}: tooltip needs a qualification`);
            assert.doesNotMatch(
              detail,
              /^(Yes|No|Unknown|Partial)[.!]?$/i,
              `${source}: tooltip must add information`,
            );
            assert.equal(icon.getAttribute("aria-description"), detail);
          } else {
            assert.equal(
              icon.getAttribute("tabindex"),
              null,
              `${source}: plain statuses are not controls`,
            );
            assert.equal(icon.getAttribute("data-status-detail"), null);
          }
        }
      }
    }
  }
});

test("built pages have no duplicate IDs or broken local links and fragments", () => {
  const pages = builtPages();
  for (const [page, html] of pages) {
    const ids = attributes(html, "id");
    assert.equal(new Set(ids).size, ids.length, `${page}: duplicate ID`);
    for (const href of attributes(html, "href")) {
      const url = new URL(href, `https://arkoren.test/${relative(dist, page)}`);
      if (url.origin !== "https://arkoren.test") continue;
      const path = resolve(dist, decodeURIComponent(url.pathname).replace(/^\//, ""));
      const target = [path, `${path}.html`, resolve(path, "index.html")].find(
        (candidate) =>
          pages.has(candidate) || (existsSync(candidate) && statSync(candidate).isFile()),
      );
      assert.ok(target, `${page}: missing target ${href}`);
      if (url.hash && pages.has(target)) {
        assert.ok(
          attributes(required(pages.get(target)), "id").includes(
            decodeURIComponent(url.hash.slice(1)),
          ),
          `${page}: missing fragment ${href}`,
        );
      }
    }
  }
});

test("the sidebar and overview expose the same guide links", (t) => {
  const window = new Window();
  t.after(() => window.close());
  window.document.write(readFileSync(resolve(dist, "docs.html"), "utf8"));
  const links = [
    ...window.document.querySelectorAll('[data-doc-panel="guides"] a[href^="/docs/guides/"]'),
  ];
  assert.ok(links.length > 0);
  for (const link of links) {
    assert.ok(
      window.document.querySelector(`[data-guide-section] a[href="${link.getAttribute("href")}"]`),
    );
  }
});

test("the API catalog and sidebar link to every public library module exactly once", (t) => {
  const window = new Window();
  t.after(() => window.close());
  window.document.write(readFileSync(resolve(dist, "docs/api.html"), "utf8"));
  const overview = new Window();
  t.after(() => overview.close());
  overview.document.write(readFileSync(resolve(dist, "docs/api.html"), "utf8"));
  const repository = resolve(website, "..");
  const moduleFiles = [
    "core/go.mod",
    ...["foundation", "driver"].flatMap((area) =>
      readdirSync(resolve(repository, area))
        .filter((name) => existsSync(resolve(repository, area, name, "go.mod")))
        .map((name) => `${area}/${name}/go.mod`),
    ),
  ];
  const expected = moduleFiles
    .map(
      (file) =>
        "https://pkg.go.dev/" +
        required(readFileSync(resolve(repository, file), "utf8").match(/^module\s+(\S+)/m))[1],
    )
    .sort();
  const sidebar = required(window.document.querySelector('[data-doc-panel="api"]'));
  const links = [...sidebar.querySelectorAll('a[href^="https://pkg.go.dev/"]')];
  assert.deepEqual(links.map((link) => link.getAttribute("href")).sort(), expected);
  const overviewLinks = [
    ...overview.document.querySelectorAll('main a[href^="https://pkg.go.dev/"]'),
  ];
  assert.deepEqual(overviewLinks.map((link) => link.getAttribute("href")).sort(), expected);
  for (const link of links) {
    const family = required(
      required(link.closest("section")).querySelector("h3"),
    ).textContent.trim();
    const entry = required(
      overviewLinks.find(
        (candidate) => candidate.getAttribute("href") === link.getAttribute("href"),
      ),
    );
    assert.equal(
      required(required(entry.closest("section")).querySelector("h2")).textContent.trim(),
      family,
    );
  }
});

test("each documentation page reveals its active navigation group and publishes raw Markdown", async () => {
  for (const family of ["guides"]) {
    const sources = readdirSync(resolve(references, family), {
      recursive: true,
      encoding: "utf8",
    }).filter((name) => name.endsWith(".md") && !name.endsWith("/index.md"));
    for (const source of sources) {
      const route = `docs/${family}/${source.replace(/\.md$/, "")}`;
      const html = readFileSync(resolve(dist, `${route}.html`), "utf8");
      const panel = family === "guides" ? "guides" : "api";
      /** Parse only navigation: large API declaration trees are irrelevant here. */
      const navigation = html.match(
        new RegExp(`<nav\\b[^>]*data-doc-panel="${panel}"[^>]*>[\\s\\S]*?<\\/nav>`),
      )?.[0];
      assert.ok(navigation, route);
      const window = new Window();
      try {
        window.document.body.innerHTML = navigation;
        const active = required(
          browserWindow(window).document.querySelector('a[aria-current="page"]'),
        );
        assert.equal(active?.getAttribute("href"), `/${route}`);
        assert.ok(required(active.closest("details")).open, route);
        assert.ok(existsSync(resolve(dist, `${route}.md`)), route);
      } finally {
        await window.happyDOM.close();
      }
    }
  }
});

test("agent indexes include all group raw-page links", () => {
  const index = readFileSync(resolve(dist, "docs/guides/llms.txt"), "utf8");
  const root = readFileSync(resolve(dist, "llms.txt"), "utf8");
  for (const folder of readdirSync(resolve(references, "guides"))) {
    const group = readFileSync(resolve(dist, "docs/guides", folder, "llms.txt"), "utf8");
    const links = [...group.matchAll(/\]\((https:\/\/arkoren\.dev\/docs\/guides\/[^)]+\.md)\)/g)];
    assert.ok(links.length > 0, folder);
    for (const [, url] of links) {
      assert.ok(index.includes(url), url);
      assert.ok(root.includes(url), url);
      assert.ok(existsSync(resolve(dist, new URL(url).pathname.slice(1))), url);
    }
  }
});
