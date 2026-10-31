import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test, { type TestContext } from "node:test";
import { Window } from "happy-dom";
import { browserWindow, required } from "./dom.ts";
import { renderArchive } from "../src/lib/docs/archive.ts";
import { initArchiveWarning } from "../src/scripts/archive.ts";

/**
 * Create archive links and their shared warning dialog in an isolated realm.
 * Labels are controlled fixture copy. Cleanup belongs to the test context,
 * including when assertions fail before a dialog is dismissed.
 */
function fixture(t: TestContext, links: string) {
  const window = new Window({ url: "https://arkoren.dev/fixture" });
  const document = browserWindow(window).document;
  document.body.innerHTML = `${links}
    <dialog data-archive-warning aria-labelledby="fixture-title" aria-describedby="fixture-description">
      <h2 id="fixture-title">Fixture title</h2><p id="fixture-description">Fixture description</p>
      <code data-archive-path></code>
      <a data-archive-index href="/llms.txt">Fixture index</a>
      <button data-archive-cancel>Fixture cancel</button>
      <a data-archive-open href="/llms-full.txt">Fixture confirm</a>
    </dialog>`;
  t.after(() => window.close());
  initArchiveWarning(document);
  return {
    window: browserWindow(window),
    document,
    dialog: required(document.querySelector("dialog")),
  };
}

test("archive notices derive sizes from fixture pages and preserve their order verbatim", () => {
  const pages = ["# Fixture one\n\né", "# Fixture two\n\n```go\nfixture()\n```"];
  const body = pages.join("\n\n---\n\n") + "\n";
  const archive = renderArchive({
    heading: "Fixture",
    indexURL: "https://example.test/index",
    pages,
  });
  assert.ok(archive.includes("https://example.test/index"));
  assert.ok(archive.includes(`${Buffer.byteLength(body)} UTF-8 bytes`));
  assert.ok(archive.includes(`approximately ${Math.ceil(body.length / 4)} tokens`));
  assert.ok(archive.includes("2 pages"));
  assert.ok(archive.endsWith(body));
});

test("an empty archive still has generated metadata and its supplied index", () => {
  const archive = renderArchive({
    heading: "Empty fixture",
    indexURL: "/fixture-index",
    pages: [],
  });
  assert.ok(archive.includes("0 pages"));
  assert.ok(archive.includes("/fixture-index"));
});

test("root archive navigation waits for confirmation and focuses cancel", (t) => {
  const { window, document, dialog } = fixture(
    t,
    '<a id="trigger" href="/llms-full.txt">Fixture archive</a>',
  );
  const trigger = required(document.querySelector<HTMLAnchorElement>("#trigger"));
  const event = new window.MouseEvent("click", { bubbles: true, cancelable: true });
  trigger.dispatchEvent(event);
  assert.equal(event.defaultPrevented, true);
  assert.equal(dialog.open, true);
  assert.equal(document.activeElement, document.querySelector("[data-archive-cancel]"));
  assert.equal(
    required(document.querySelector("[data-archive-open]")).getAttribute("href"),
    trigger.href,
  );
  assert.equal(
    required(document.querySelector("[data-archive-index]")).getAttribute("href"),
    "/llms.txt",
  );
  required(document.querySelector<HTMLButtonElement>("[data-archive-cancel]")).click();
  assert.equal(dialog.open, false);
  assert.equal(document.activeElement, trigger);
});

test("retired API archives do not prompt readers to load a bulk export", (t) => {
  const { window, document, dialog } = fixture(
    t,
    '<a id="trigger" href="/docs/api/llms-full.txt">Fixture archive</a>',
  );
  required(document.getElementById("trigger")).dispatchEvent(
    new window.MouseEvent("click", { cancelable: true }),
  );
  assert.equal(dialog.open, false);
  assert.equal(required(document.getElementById("trigger")).hasAttribute("aria-haspopup"), false);
});

test("guide archive confirmation closes the modal without intercepting navigation again", (t) => {
  const { window, document, dialog } = fixture(
    t,
    '<a id="trigger" href="/docs/guides/llms-full.txt">Fixture archive</a>',
  );
  required(document.getElementById("trigger")).dispatchEvent(
    new window.MouseEvent("click", { cancelable: true }),
  );
  assert.equal(
    required(document.querySelector<HTMLAnchorElement>("[data-archive-index]")).pathname,
    "/docs/guides/llms.txt",
  );
  const confirm = new window.MouseEvent("click", { bubbles: true, cancelable: true });
  required(document.querySelector("[data-archive-open]")).dispatchEvent(confirm);
  assert.equal(confirm.defaultPrevented, false);
  assert.equal(dialog.open, false);
});

test("index navigation closes the warning without blocking the index", (t) => {
  const { window, document, dialog } = fixture(
    t,
    '<a id="trigger" href="/llms-full.txt">Fixture archive</a>',
  );
  required(document.getElementById("trigger")).dispatchEvent(
    new window.MouseEvent("click", { cancelable: true }),
  );
  const event = new window.MouseEvent("click", { bubbles: true, cancelable: true });
  required(document.querySelector("[data-archive-index]")).dispatchEvent(event);
  assert.equal(event.defaultPrevented, false);
  assert.equal(dialog.open, false);
});

test("backdrop dismissal restores focus without opening the archive", (t) => {
  const { window, document, dialog } = fixture(
    t,
    '<a id="trigger" href="/llms-full.txt">Fixture archive</a>',
  );
  const trigger = required(document.getElementById("trigger"));
  trigger.dispatchEvent(new window.MouseEvent("click", { cancelable: true }));
  dialog.dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  assert.equal(dialog.open, false);
  assert.equal(document.activeElement, trigger);
});

test("modified clicks retain native navigation", (t) => {
  const { window, document, dialog } = fixture(
    t,
    '<a id="trigger" href="/llms-full.txt">Fixture archive</a>',
  );
  const event = new window.MouseEvent("click", { cancelable: true, metaKey: true });
  required(document.getElementById("trigger")).dispatchEvent(event);
  assert.equal(event.defaultPrevented, false);
  assert.equal(dialog.open, false);
});

test("external archives and ordinary documentation links are not intercepted", (t) => {
  const { window, document, dialog } = fixture(
    t,
    '<a id="external" href="https://example.test/llms-full.txt">External fixture</a>' +
      '<a id="page" href="/docs/guides/fixture.md">Page fixture</a>',
  );
  const external = new window.MouseEvent("click", { cancelable: true });
  const page = new window.MouseEvent("click", { cancelable: true });
  required(document.getElementById("external")).dispatchEvent(external);
  required(document.getElementById("page")).dispatchEvent(page);
  assert.equal(external.defaultPrevented, false);
  assert.equal(page.defaultPrevented, false);
  assert.equal(dialog.open, false);
});

test("missing dialog markup leaves the original links usable", (t) => {
  const window = new Window({ url: "https://arkoren.dev/fixture" });
  t.after(() => window.close());
  window.document.body.innerHTML = '<a href="/llms-full.txt">Fixture archive</a>';
  initArchiveWarning(browserWindow(window).document);
  const event = new window.MouseEvent("click", { cancelable: true });
  required(window.document.querySelector("a")).dispatchEvent(event);
  assert.equal(event.defaultPrevented, false);
});

test("built documentation pages share a labelled dialog and retain raw archive hrefs", (t) => {
  const window = new Window();
  t.after(() => window.close());
  window.document.write(readFileSync(new URL("../dist/docs.html", import.meta.url), "utf8"));
  const dialog = browserWindow(window).document.querySelector<HTMLDialogElement>(
    "dialog[data-archive-warning]",
  );
  assert.ok(dialog);
  assert.ok(window.document.getElementById(required(dialog.getAttribute("aria-labelledby"))));
  for (const id of required(dialog.getAttribute("aria-describedby")).split(" ")) {
    assert.ok(window.document.getElementById(id));
  }
  assert.ok(window.document.querySelector('a[href="/llms-full.txt"]'));
  assert.equal(dialog.open, false);
});

test("published archive links also warn when browsing a local development origin", (t) => {
  const dom = new Window({ url: "http://localhost:4321/fixture" });
  t.after(() => dom.close());
  const window = browserWindow(dom);
  const document = window.document;
  document.body.innerHTML = `
    <a id="trigger" href="https://arkoren.dev/docs/guides/llms-full.txt">Fixture archive</a>
    <dialog data-archive-warning><code data-archive-path></code>
      <a data-archive-index></a><button data-archive-cancel></button><a data-archive-open></a>
    </dialog>`;
  initArchiveWarning(document);
  const event = new window.MouseEvent("click", { cancelable: true });
  required(document.getElementById("trigger")).dispatchEvent(event);
  assert.equal(event.defaultPrevented, true);
  assert.equal(required(document.querySelector("dialog")).open, true);
  assert.equal(
    required(document.querySelector<HTMLAnchorElement>("[data-archive-open]")).href,
    "https://arkoren.dev/docs/guides/llms-full.txt",
  );
  assert.equal(
    required(document.querySelector<HTMLAnchorElement>("[data-archive-index]")).href,
    "http://localhost:4321/docs/guides/llms.txt",
  );
});

test("unavailable native dialogs leave archive navigation untouched", (t) => {
  const dom = new Window({ url: "https://arkoren.dev/fixture" });
  t.after(() => dom.close());
  const window = browserWindow(dom);
  const document = window.document;
  document.body.innerHTML = `
    <a id="trigger" href="/llms-full.txt">Fixture archive</a>
    <dialog data-archive-warning><code data-archive-path></code>
      <a data-archive-index></a><button data-archive-cancel></button><a data-archive-open></a>
    </dialog>`;
  Object.defineProperty(required(document.querySelector("dialog")), "showModal", {
    value: undefined,
  });
  initArchiveWarning(document);
  const event = new window.MouseEvent("click", { cancelable: true });
  required(document.getElementById("trigger")).dispatchEvent(event);
  assert.equal(event.defaultPrevented, false);
});

test("the built root archive retains all guides without a local API export", () => {
  const root = readFileSync(new URL("../dist/llms-full.txt", import.meta.url), "utf8");
  const guides = readFileSync(
    new URL("../dist/docs/guides/llms-full.txt", import.meta.url),
    "utf8",
  );
  const separator = "\n\n---\n\n";
  const rootBody = root.slice(root.indexOf(separator) + separator.length);
  const guidesBody = guides.slice(guides.indexOf(separator) + separator.length);
  assert.equal(rootBody, guidesBody);
  assert.ok(root.slice(0, root.indexOf(separator)).includes("https://arkoren.dev/llms.txt"));
  assert.ok(
    guides.slice(0, guides.indexOf(separator)).includes("https://arkoren.dev/docs/guides/llms.txt"),
  );
});
