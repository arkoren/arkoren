import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import test, { type TestContext } from "node:test";
import { Window } from "happy-dom";
import { browserWindow, required } from "./dom.ts";
import { initDocsSidebar, revealCurrentDoc } from "../src/scripts/sidebar.ts";
import { buildGuideTree } from "../src/lib/docs/tree.ts";
import { apiModules } from "../src/lib/docs/api.ts";
import { legacyApiRedirects } from "../src/lib/docs/redirects.ts";

test("navigation uses fixture folder metadata and page order without changing source ids", () => {
  const pages = [
    { id: "second/late", data: { title: "Later", order: 20 } },
    { id: "first/index", data: { title: "First fixture", order: 10 } },
    { id: "empty/index", data: { title: "Empty fixture", order: 15 } },
    { id: "second/index", data: { title: "Second fixture", order: 20 } },
    { id: "second/early", data: { title: "Earlier", order: 10 } },
    { id: "first/start", data: { title: "Start fixture", order: 10 } },
  ];
  const tree = buildGuideTree(pages);
  assert.deepEqual(
    tree.map((group) => group.meta.data.title),
    ["First fixture", "Second fixture"],
  );
  assert.deepEqual(
    tree.map((group) => group.pages.map((page) => page.id)),
    [["first/start"], ["second/early", "second/late"]],
  );
  assert.equal(tree[1].pages[0], pages[4]);
});

test("the reference catalog contains library module roots, never packages or tooling", () => {
  const groups = apiModules();
  assert.deepEqual(
    groups.map((group) => group.id),
    ["core", "foundation", "drivers"],
  );
  assert.deepEqual(
    groups[0].modules.map((module) => module.path),
    ["arkoren.dev/core"],
  );
  assert.deepEqual(
    groups[1].modules.map((module) => module.path),
    [
      "arkoren.dev/foundation/certificate",
      "arkoren.dev/foundation/collection",
      "arkoren.dev/foundation/key",
      "arkoren.dev/foundation/problem",
      "arkoren.dev/foundation/query",
      "arkoren.dev/foundation/validate",
    ],
  );
  assert.ok(groups[2].modules.length > 0);
  assert.equal(groups.flatMap((group) => group.modules).length, 44);
  for (const module of groups[2].modules) {
    assert.match(module.path, /^arkoren\.dev\/driver\/[^/]+$/);
  }
  for (const module of groups.flatMap((group) => group.modules)) {
    assert.equal(module.url, `https://pkg.go.dev/${module.path}`);
    const source = new URL(
      `../../${module.path.slice("arkoren.dev/".length)}/go.mod`,
      import.meta.url,
    );
    assert.equal(required(readFileSync(source, "utf8").match(/^module\s+(\S+)/m))[1], module.path);
  }
});

test("legacy aliases preserve actual package identities and send concepts to guides", () => {
  const redirects = legacyApiRedirects();
  assert.equal(
    redirects["/docs/api/core/cache"],
    "https://pkg.go.dev/arkoren.dev/core/service/cache",
  );
  assert.equal(
    redirects["/docs/api/foundation/schema.md"],
    "https://pkg.go.dev/arkoren.dev/foundation/query/schema",
  );
  assert.equal(redirects["/docs/api/core/testing"], "/docs/guides/testing/overview");
  assert.equal(
    redirects["/docs/api/drivers/redis/rediscache"],
    "https://pkg.go.dev/arkoren.dev/driver/redis/rediscache",
  );
  assert.equal(
    redirects["/docs/api/memory/slogmail"],
    "https://pkg.go.dev/arkoren.dev/core/service/mail/slogmail",
  );
});

test("every historical package redirect names a package in the checkout", () => {
  const redirects = legacyApiRedirects();
  for (const [route, target] of Object.entries(redirects)) {
    if (!target.startsWith("https://pkg.go.dev/")) continue;
    const directory = target.slice("https://pkg.go.dev/arkoren.dev/".length);
    const files = readdirSync(fileURLToPath(new URL(`../../${directory}/`, import.meta.url)));
    assert.ok(
      files.some((file) => file.endsWith(".go") && !file.endsWith("_test.go")),
      target,
    );
    assert.equal(redirects[route.replace(/\.md$/, "")], target);
  }
});

/**
 * Create a scrollable sidebar with explicit link and viewport measurements.
 * Pixel coordinates replace layout that Happy DOM cannot calculate; callers
 * vary geometry and overflow without coupling checks to production CSS.
 */
function fixture(t: TestContext, { top = 500, bottom = 530, overflow = "auto" } = {}) {
  const window = new Window();
  t.after(() => window.close());
  const document = browserWindow(window).document;
  document.body.innerHTML = `<aside data-docs-sidebar style="overflow-y: ${overflow}">
    <nav><a href="/fixture" aria-current="page">Fixture</a></nav></aside>`;
  const sidebar = required(document.querySelector<HTMLElement>("[data-docs-sidebar]"));
  const link = required(document.querySelector("a"));
  sidebar.getBoundingClientRect = () => new window.DOMRect(0, 72, 0, 400);
  link.getBoundingClientRect = () => new window.DOMRect(0, top, 0, bottom - top);
  sidebar.scrollTop = 0;
  return { window, document, sidebar };
}

test("a selected item below the sidebar viewport is revealed without scrolling the page", (t) => {
  const { document, sidebar } = fixture(t);
  revealCurrentDoc(document);
  assert.equal(sidebar.scrollTop, 154);
  assert.equal(document.documentElement.scrollTop, 0);
  assert.equal(document.body.scrollTop, 0);
});

test("revealing a selected page scrolls the tree without moving the area selector", (t) => {
  const { window, document, sidebar } = fixture(t);
  const menu = document.createElement("details");
  menu.dataset.menu = "";
  menu.style.overflowY = "auto";
  menu.append(required(sidebar.firstElementChild));
  sidebar.append(menu);
  sidebar.style.overflowY = "hidden";
  menu.getBoundingClientRect = () => new window.DOMRect(0, 120, 0, 352);
  revealCurrentDoc(document);
  assert.equal(menu.scrollTop, 154);
  assert.equal(sidebar.scrollTop, 0);
  assert.equal(document.documentElement.scrollTop, 0);
});

test("switching sidebar areas preserves the article and each tree's scroll position", (t) => {
  const window = new Window();
  t.after(() => window.close());
  const document = browserWindow(window).document;
  document.body.innerHTML = `<aside data-docs-sidebar>
    <button data-doc-area="guides" aria-pressed="true">Guide fixture</button>
    <button data-doc-area="api" aria-pressed="false">Reference fixture</button>
    <details data-menu open>
      <nav data-doc-panel="guides">Guide links</nav>
      <nav data-doc-panel="api" hidden>Reference links</nav>
    </details>
  </aside><main><input value="Reader's unfinished note"></main>`;
  const article = required(document.querySelector("main"));
  const input = required(article.querySelector("input"));
  const menu = required(document.querySelector<HTMLElement>("[data-menu]"));
  const guides = required(document.querySelector<HTMLButtonElement>('[data-doc-area="guides"]'));
  const api = required(document.querySelector<HTMLButtonElement>('[data-doc-area="api"]'));
  const guidePanel = required(document.querySelector<HTMLElement>('[data-doc-panel="guides"]'));
  const apiPanel = required(document.querySelector<HTMLElement>('[data-doc-panel="api"]'));
  const url = window.location.href;
  initDocsSidebar(document);
  menu.scrollTop = 154;
  api.click();
  assert.equal(guidePanel.hidden, true);
  assert.equal(apiPanel.hidden, false);
  assert.equal(api.getAttribute("aria-pressed"), "true");
  assert.equal(guides.getAttribute("aria-pressed"), "false");
  assert.equal(menu.scrollTop, 0);
  menu.scrollTop = 88;
  guides.click();
  assert.equal(guidePanel.hidden, false);
  assert.equal(apiPanel.hidden, true);
  assert.equal(menu.scrollTop, 154);
  api.click();
  assert.equal(menu.scrollTop, 88);
  assert.equal(window.location.href, url);
  assert.equal(document.querySelector("main"), article);
  assert.equal(input.value, "Reader's unfinished note");
});

test("a current page in the hidden area does not move the visible tree", (t) => {
  const { document, sidebar } = fixture(t);
  required(document.querySelector("nav")).hidden = true;
  revealCurrentDoc(document);
  assert.equal(sidebar.scrollTop, 0);
});

test("a selected item above the sidebar viewport is revealed", (t) => {
  const { document, sidebar } = fixture(t, { top: 40, bottom: 70 });
  sidebar.scrollTop = 200;
  revealCurrentDoc(document);
  assert.equal(sidebar.scrollTop, 72);
});

test("an already visible selected item keeps the sidebar position", (t) => {
  const { document, sidebar } = fixture(t, { top: 200, bottom: 230 });
  sidebar.scrollTop = 100;
  revealCurrentDoc(document);
  assert.equal(sidebar.scrollTop, 100);
});

test("mobile navigation does not scroll the document or the non-scrollable sidebar", (t) => {
  const { document, sidebar } = fixture(t, { overflow: "visible" });
  revealCurrentDoc(document);
  assert.equal(sidebar.scrollTop, 0);
  assert.equal(document.documentElement.scrollTop, 0);
});

test("pages without a selected documentation item leave the sidebar untouched", (t) => {
  const { document, sidebar } = fixture(t);
  required(document.querySelector("a")).removeAttribute("aria-current");
  revealCurrentDoc(document);
  assert.equal(sidebar.scrollTop, 0);
});

test("pages without a sidebar require no work", (t) => {
  const { document, sidebar } = fixture(t);
  sidebar.remove();
  assert.doesNotThrow(() => revealCurrentDoc(document));
});
