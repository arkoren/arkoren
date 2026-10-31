/**
 * Reveal the current documentation link inside the desktop sidebar only.
 * Adjusting the tree viewport avoids scrolling its selector, article, or page,
 * which scrollIntoView can do when it also moves ancestor scroll containers.
 * Already visible links and the non-scrollable mobile menu stay untouched.
 * @param root Document containing the rendered documentation navigation.
 */
export function revealCurrentDoc(root: Document): void {
  /** Each full-page navigation renders a fresh sidebar and current link. */
  const sidebar = root.querySelector<HTMLElement>("[data-docs-sidebar]");
  const current = sidebar?.querySelector<HTMLElement>('a[aria-current="page"]');
  if (sidebar === null || current === undefined || current === null) return;
  if (current.closest("[hidden]") !== null) return;
  /** The area selector stays above the independently scrolling navigation tree. */
  const menu = sidebar.querySelector<HTMLElement>("[data-menu]") ?? sidebar;

  /** Mobile navigation has visible overflow and must not move the document. */
  const overflow = root.defaultView?.getComputedStyle(menu).overflowY;
  if (overflow !== "auto" && overflow !== "scroll") return;

  /** Keep a small reading gutter rather than placing the row at the edge. */
  const inset = 96;
  /** Move only far enough to bring the selected row and its gutter into view. */
  const viewport = menu.getBoundingClientRect();
  const item = current.getBoundingClientRect();
  if (item.top < viewport.top + inset) {
    menu.scrollTop += item.top - viewport.top - inset;
  } else if (item.bottom > viewport.bottom - inset) {
    menu.scrollTop += item.bottom - viewport.bottom + inset;
  }
}

/**
 * Switch the sidebar's navigation tree without touching the article or URL.
 * Each area retains its own tree scroll position. Native buttons keep mouse,
 * Enter, and Space activation; the shared selector follows their pressed state.
 * @param root Document containing the documentation sidebar.
 */
export function initDocsSidebar(root: Document): void {
  const sidebar = root.querySelector<HTMLElement>("[data-docs-sidebar]");
  if (sidebar === null || sidebar.dataset.areasInitialized !== undefined) return;
  const controls = [...sidebar.querySelectorAll<HTMLButtonElement>("[data-doc-area]")];
  const panels = [...sidebar.querySelectorAll<HTMLElement>("[data-doc-panel]")];
  if (controls.length === 0 || panels.length === 0) return;
  sidebar.dataset.areasInitialized = "";

  /** Only the navigation viewport scrolls; its area selector stays in place. */
  const menu = sidebar.querySelector<HTMLElement>("[data-menu]");
  const positions = new Map<string, number>();
  let selected = controls.find((control) => control.getAttribute("aria-pressed") === "true")
    ?.dataset.docArea;

  /** Retain both trees so expanded groups and reading position survive a switch. */
  function select(control: HTMLButtonElement): void {
    const area = control.dataset.docArea;
    if (area === undefined || area === selected) return;
    if (!panels.some((panel) => panel.dataset.docPanel === area)) return;
    if (selected !== undefined && menu !== null) positions.set(selected, menu.scrollTop);
    for (const button of controls) button.setAttribute("aria-pressed", String(button === control));
    for (const panel of panels) panel.hidden = panel.dataset.docPanel !== area;
    selected = area;
    if (menu !== null) menu.scrollTop = positions.get(area) ?? 0;
  }

  for (const control of controls) control.addEventListener("click", () => select(control));
}
