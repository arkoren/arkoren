import assert from "node:assert/strict";
import type { Window as HappyWindow } from "happy-dom";

/**
 * Adapt Happy DOM at the test boundary to the browser declarations used by
 * production controls. Happy DOM's declarations omit unrelated browser APIs
 * and legacy event fields. This test-only bridge keeps the real document and
 * event implementations together in their original realm without mutation.
 *
 * @example
 * import { Window } from 'happy-dom';
 * const window = browserWindow(new Window());
 * window.document.body.dispatchEvent(new window.MouseEvent('click'));
 */
export function browserWindow(window: HappyWindow): Window & typeof globalThis {
  return window as unknown as Window & typeof globalThis;
}

/**
 * Require fixture markup or a generated match before inspecting its contents.
 * Missing values fail the test immediately instead of hiding a broken fixture
 * behind optional chaining or a non-null assertion.
 *
 * @example
 * const link = required(document.querySelector('a'));
 * assert.equal(link.pathname, '/docs');
 */
export function required<T>(value: T | null | undefined): T {
  assert.ok(value !== null && value !== undefined, "required fixture value is missing");
  return value;
}
