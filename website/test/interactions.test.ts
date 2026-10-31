import assert from "node:assert/strict";
import test, { type TestContext } from "node:test";
import { Window } from "happy-dom";
import { browserWindow, required } from "./dom.ts";
import { markdownToHtml } from "satteri";
import { tablesPlugin } from "../src/plugins/tables.ts";
import { initPageContents } from "../src/scripts/contents.ts";
import { initCopyControls } from "../src/scripts/copy.ts";

test("Markdown tables retain semantic cells inside keyboard-accessible scroll containers", (t) => {
  const { html } = markdownToHtml("| Key | Value |\n| --- | --- |\n| `identifier` | Sample |", {
    hastPlugins: [tablesPlugin],
  });
  const { document } = fixture(t, html);
  const container = document.querySelector<HTMLElement>(".table-scroll");
  assert.ok(container);
  assert.equal(container.tabIndex, 0);
  assert.equal(container.getAttribute("role"), "region");
  assert.ok(container.getAttribute("aria-label"));
  assert.equal(container.children.length, 1);
  assert.equal(required(container.firstElementChild).tagName, "TABLE");
  assert.equal(container.querySelectorAll("th").length, 2);
  assert.equal(required(container.querySelector("td code")).textContent, "identifier");
});

/** Status-only columns become accessible controls without changing prose tables. */
test("status matrices retain readable names and row and column associations", (t) => {
  const { html } = markdownToHtml(
    "| Backend | First | Second | Third | Fourth |\n| --- | --- | --- | --- | --- |\n| Fixture | Yes | No | Unknown | Partial — Requires fixture persistence. |",
    { hastPlugins: [tablesPlugin] },
  );
  const { document } = fixture(t, html);
  assert.deepEqual(
    [...document.querySelectorAll(".status")].map((icon) => icon.getAttribute("aria-label")),
    ["Yes", "No", "Unknown", "Partial"],
  );
  assert.equal(document.querySelectorAll("svg[aria-hidden='true']").length, 4);
  assert.equal(document.querySelectorAll("span[role='img']").length, 3);
  assert.equal(document.querySelectorAll("button").length, 1);
  assert.equal(
    required(document.querySelector("button")).getAttribute("aria-description"),
    "Requires fixture persistence.",
  );
  assert.equal(required(document.querySelector("tbody th[scope='row']")).textContent, "Fixture");
  assert.equal(document.querySelectorAll("thead th[scope='col']").length, 5);
});

/** Ordinary prose using a status word must not become an interactive matrix. */
test("mixed prose tables keep their original text cells", (t) => {
  const { html } = markdownToHtml(
    "| Key | Value |\n| --- | --- |\n| First | Yes |\n| Second | Details |",
    {
      hastPlugins: [tablesPlugin],
    },
  );
  const { document } = fixture(t, html);
  assert.equal(document.querySelectorAll("button").length, 0);
  assert.equal(required(document.querySelector("td:last-child")).textContent, "Yes");
});

/**
 * Install a fresh document with deterministic animation frames. Geometry is
 * supplied by individual tests because a DOM implementation has no CSS layout.
 * Restore globals and close the window after each test to avoid shared state.
 */
function fixture(t: TestContext, html: string) {
  const window = new Window({ url: "https://arkoren.dev/fixture" });
  const document = browserWindow(window).document;
  /** Animation callbacks run only when the test explicitly flushes this queue. */
  const frames: FrameRequestCallback[] = [];
  /** Happy DOM omits FontFaceSet; expose its completion as a controlled promise. */
  const fontsReady = Promise.withResolvers<void>();
  Object.defineProperty(window.document, "fonts", { value: { ready: fontsReady.promise } });
  const globals = {
    document: window.document,
    navigator: window.navigator,
    getComputedStyle: window.getComputedStyle.bind(window),
    addEventListener: window.addEventListener.bind(window),
    requestAnimationFrame: (callback: FrameRequestCallback) => frames.push(callback),
  };
  const previous = new Map(
    Object.keys(globals).map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]),
  );
  for (const [key, value] of Object.entries(globals)) {
    Object.defineProperty(globalThis, key, { value, configurable: true, writable: true });
  }
  window.document.body.innerHTML = html;
  t.after(() => {
    window.close();
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else Reflect.deleteProperty(globalThis, key);
    }
  });
  return { window: browserWindow(window), document, frames, fontsReady };
}

/**
 * Build contents with arbitrary labels and an encoded fragment to test lookup.
 * The optional marker attribute enables geometry checks on the same markup.
 */
function contentsMarkup(marker = "") {
  return `<nav data-page-contents ${marker}><ul>
    <li><a href="#top">First</a></li>
    <li><a href="#section%20two">Second</a></li>
    <li><a href="#missing">Missing</a></li>
  </ul></nav>`;
}

test("desktop and mobile contents track scroll and history with an optional marker", async (t) => {
  const { window, document, frames, fontsReady } = fixture(
    t,
    '<main id="top"><h2 id="section two">Fixture</h2></main>' +
      contentsMarkup("data-contents-marker") +
      contentsMarkup(),
  );
  let sectionTop = 200;
  document.documentElement.style.scrollPaddingTop = "88px";
  required(document.getElementById("top")).getBoundingClientRect = () => new window.DOMRect();
  required(document.getElementById("section two")).getBoundingClientRect = () =>
    new window.DOMRect(0, sectionTop);
  const navs = [...document.querySelectorAll("nav")];
  const secondItem = navs[0].querySelectorAll("li")[1];
  Object.defineProperties(secondItem, { offsetTop: { value: 30 }, offsetHeight: { value: 20 } });
  navs.forEach(initPageContents);
  assert.equal(
    required(navs[0].querySelector<HTMLAnchorElement>('[aria-current="location"]')).hash,
    "#top",
  );
  sectionTop = 95.5;
  window.dispatchEvent(new window.Event("scroll"));
  window.dispatchEvent(new window.Event("scrollend"));
  assert.equal(frames.length, 2, "one update per contents instance");
  frames.splice(0).forEach((frame) => frame(0));
  for (const nav of navs) {
    assert.equal(nav.querySelectorAll('[aria-current="location"]').length, 1);
    assert.equal(
      required(nav.querySelector<HTMLAnchorElement>('[aria-current="location"]')).hash,
      "#section%20two",
    );
  }
  assert.equal(
    required(navs[0].querySelector("ul")).style.getPropertyValue("--marker-top"),
    "30px",
  );
  assert.equal(
    required(navs[0].querySelector("ul")).style.getPropertyValue("--marker-height"),
    "20px",
  );
  assert.equal(required(navs[1].querySelector("ul")).hasAttribute("data-marker"), false);
  sectionTop = 300;
  window.dispatchEvent(new window.Event("hashchange"));
  frames.splice(0).forEach((frame) => frame(0));
  assert.equal(
    required(navs[1].querySelector<HTMLAnchorElement>('[aria-current="location"]')).hash,
    "#top",
  );
  sectionTop = 20;
  fontsReady.resolve();
  await Promise.resolve();
  frames.splice(0).forEach((frame) => frame(0));
  assert.equal(
    required(navs[0].querySelector<HTMLAnchorElement>('[aria-current="location"]')).hash,
    "#section%20two",
  );
});

test("contents handles an empty or missing-target list without scheduling work", (t) => {
  const { document, window, frames } = fixture(
    t,
    '<nav><a href="#absent">Absent</a></nav><nav></nav>',
  );
  document.querySelectorAll("nav").forEach(initPageContents);
  window.dispatchEvent(new window.Event("resize"));
  assert.equal(frames.length, 0);
});

test("copy controls copy command and pre text without labels, then restore feedback", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { document, window } = fixture(
    t,
    '<button data-copy="exact command">copy</button><div class="prose"><pre><code>line one\nline two\n</code></pre></div>',
  );
  const copied: string[] = [];
  t.mock.method(window.navigator.clipboard, "writeText", async (text: string) => {
    copied.push(text);
  });
  initCopyControls(document);
  const command = required(document.querySelector<HTMLButtonElement>("[data-copy]"));
  const prose = required(document.querySelector<HTMLButtonElement>(".copyable button"));
  command.click();
  await Promise.resolve();
  assert.equal(command.textContent, "copied");
  prose.click();
  await Promise.resolve();
  assert.deepEqual(copied, ["exact command", "line one\nline two\n"]);
  assert.equal(prose.textContent, "copied");
  assert.equal(prose.getAttribute("aria-live"), "polite");
  t.mock.timers.tick(1500);
  assert.equal(command.textContent, "copy");
  assert.equal(prose.textContent, "copy");
});

test("clipboard rejection reports failure and permits retry without an unhandled promise", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { document, window } = fixture(t, '<button data-copy="retry text">copy</button>');
  t.mock.method(window.navigator.clipboard, "writeText", async () => {
    throw new Error("denied");
  });
  initCopyControls(document);
  const button = required(document.querySelector("button"));
  button.click();
  assert.equal(button.disabled, true);
  await Promise.resolve();
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, "copy failed");
  t.mock.timers.tick(1500);
  assert.equal(button.textContent, "copy");
  t.mock.method(window.navigator.clipboard, "writeText", async () => {});
  button.click();
  await Promise.resolve();
  assert.equal(button.textContent, "copied");
});

test("copy failure also handles an unavailable clipboard API", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { document, window } = fixture(t, '<button data-copy="plain text">copy</button>');
  Object.defineProperty(window.navigator, "clipboard", { value: undefined });
  initCopyControls(document);
  const button = required(document.querySelector("button"));
  button.click();
  await Promise.resolve();
  assert.equal(button.textContent, "copy failed");
  assert.equal(button.disabled, false);
  t.mock.timers.tick(1500);
  assert.equal(button.textContent, "copy");
});
