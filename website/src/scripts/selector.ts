/** ARIA state belongs to the caller; the shared selector only follows it. */
const selectedControl =
  ":scope > :is(a, button)" +
  ':is([aria-current]:not([aria-current="false"]), [aria-selected="true"], [aria-pressed="true"])';

/**
 * Move one background between selected controls without fading the selection.
 * Navigation links, URL-driven filters, and panel tabs retain their own input
 * handling. Focus and selection reveal only this horizontal scroll container.
 * @param root Selection group rendered by SegmentedSelector.
 */
export function initSegmentedSelector(root: HTMLElement): void {
  if (root.dataset.selectorInitialized !== undefined) return;
  root.dataset.selectorInitialized = "";
  const indicator = root.querySelector<HTMLElement>("[data-selector-indicator]");
  if (indicator === null) return;
  /** Keep the existing highlight node while updating only its geometry. */
  const appearance = indicator.style;

  /** Keep the full control visible without scrolling the article or document. */
  function reveal(control: HTMLElement): void {
    const viewport = root.getBoundingClientRect();
    const bounds = control.getBoundingClientRect();
    const inset = parseFloat(getComputedStyle(root).paddingLeft) + root.clientLeft;
    if (bounds.left < viewport.left + inset) {
      root.scrollLeft += bounds.left - viewport.left - inset;
    } else if (bounds.right > viewport.right - inset) {
      root.scrollLeft += bounds.right - viewport.right + inset;
    }
  }

  /** Measure in the group's scroll coordinates so an overflowing strip still slides. */
  function update(): void {
    const active = root.querySelector<HTMLElement>(selectedControl);
    if (active === null || root.clientWidth === 0) {
      delete root.dataset.selectorReady;
      return;
    }
    appearance.width = `${active.offsetWidth}px`;
    appearance.transform = `translateX(${active.offsetLeft}px)`;
    root.dataset.selectorReady = "";
    const focused = root.ownerDocument.activeElement;
    reveal(focused instanceof HTMLElement && focused.parentElement === root ? focused : active);
  }

  /** Follow completed ARIA updates, not clicks that might keep the old selection. */
  const observer = new MutationObserver(update);
  observer.observe(root, {
    subtree: true,
    childList: true,
    attributes: true,
    attributeFilter: ["aria-current", "aria-selected", "aria-pressed", "hidden"],
  });
  const resize = new ResizeObserver(update);
  resize.observe(root);
  for (const control of root.querySelectorAll<HTMLElement>(":scope > :is(a, button)")) {
    resize.observe(control);
  }
  root.addEventListener("focusin", (event) => {
    if (!(event.target instanceof Element)) return;
    const control = event.target.closest<HTMLElement>("a, button");
    if (control?.parentElement === root) reveal(control);
  });
  update();
  requestAnimationFrame(() => {
    root.dataset.selectorAnimate = "";
  });
  document.fonts.ready.then(update);
  window.addEventListener("pagereveal", update);
}
