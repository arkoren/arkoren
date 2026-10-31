import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { stripTypeScriptTypes } from "node:module";
import test, { type TestContext } from "node:test";
import { Window } from "happy-dom";

/**
 * Execute the production TypeScript after stripping its annotations, exactly
 * as the layout does. Dates, storage, and activation timers are controlled so
 * each test can inspect calendar and manual choices without waiting.
 */
function fixture(
  t: TestContext,
  {
    nowAt = new Date(2026, 9, 23),
    saved,
    blocked = false,
  }: { nowAt?: Date; saved?: string | null; blocked?: boolean } = {},
) {
  const window = new Window({ url: "https://arkoren.dev/fixture" });
  t.after(() => window.close());
  Object.defineProperty(window, "Date", {
    value: class extends Date {
      constructor() {
        super(nowAt.getTime());
      }
    },
  });
  /** Keep activation deterministic without starting real timers in Happy DOM. */
  let activate: (() => void) | undefined;
  Object.defineProperties(window, {
    setTimeout: {
      value: (callback: () => void) => {
        activate = callback;
        return 1;
      },
    },
    clearTimeout: {
      value: () => {
        activate = undefined;
      },
    },
    setInterval: { value: () => 1 },
  });
  if (saved != null) window.localStorage.setItem("arkoren-season", saved);
  if (blocked) {
    for (const key of ["localStorage", "sessionStorage"]) {
      Object.defineProperty(window, key, {
        get() {
          throw new Error("Storage blocked");
        },
      });
    }
  }
  window.document.body.innerHTML =
    "<button data-season-toggle hidden></button><button data-season-toggle hidden></button>";
  window.eval(
    stripTypeScriptTypes(
      readFileSync(new URL("../src/scripts/season.ts", import.meta.url), "utf8"),
    ),
  );
  window.document.dispatchEvent(new window.Event("DOMContentLoaded"));
  return {
    window,
    root: window.document.documentElement,
    buttons: [...window.document.querySelectorAll("button")],
    activate() {
      activate?.();
    },
  };
}

test("the calendar activates only within the seasonal window", (t) => {
  for (const [month, day, expected] of [
    [9, 23, "normal"],
    [9, 24, "halloween"],
    [10, 1, "halloween"],
    [10, 2, "normal"],
  ] as const) {
    const page = fixture(t, { nowAt: new Date(2026, month, day) });
    page.activate();
    assert.equal(page.root.dataset.season, expected);
  }
});

test("manual preferences synchronize controls and survive a page load", (t) => {
  const page = fixture(t);
  page.buttons[0].click();
  assert.ok(page.buttons.every((button) => button.getAttribute("aria-pressed") === "true"));
  const saved = page.window.localStorage.getItem("arkoren-season");
  assert.equal(fixture(t, { saved }).root.dataset.season, "halloween");
  page.buttons[1].click();
  assert.equal(page.root.dataset.season, "normal");
});

test("a saved opt-out wins over automatic activation", (t) => {
  const page = fixture(t, { nowAt: new Date(2026, 9, 31), saved: '{"year":2026,"enabled":false}' });
  page.activate();
  assert.equal(page.root.dataset.season, "normal");
});

test("a manual opt-out cancels pending automatic activation", (t) => {
  const page = fixture(t, { nowAt: new Date(2026, 9, 31) });
  page.buttons[0].click();
  page.buttons[0].click();
  page.activate();
  assert.equal(page.root.dataset.season, "normal");
});

test("invalid or expired preferences fall back to the calendar", (t) => {
  for (const saved of [
    "invalid",
    '{"year":2026,"enabled":"true"}',
    '{"year":2025,"enabled":true}',
  ]) {
    const page = fixture(t, { saved });
    page.activate();
    assert.equal(page.root.dataset.season, "normal");
  }
});

test("blocked storage does not prevent a manual choice", (t) => {
  const page = fixture(t, { blocked: true });
  page.buttons[0].click();
  assert.equal(page.root.dataset.season, "halloween");
});
