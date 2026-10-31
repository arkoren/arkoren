/**
 * Show status qualifications outside scroll containers so table edges cannot clip
 * them. Hover, keyboard focus, and tap share the same dismissible tooltip.
 */
export function initStatusTooltips(root: Document): void {
  const buttons = [...root.querySelectorAll<HTMLButtonElement>("button[data-status-detail]")];
  /** The shared component owns markup and scoped presentation. */
  const surface = root.querySelector<HTMLElement>("#doc-status-tooltip");
  if (buttons.length === 0 || surface === null) return;
  /** Retain the non-null surface for deferred interaction callbacks. */
  const tooltip = surface;
  let active: HTMLButtonElement | undefined;
  let pending: ReturnType<typeof setTimeout> | undefined;
  let reveal: ReturnType<typeof setTimeout> | undefined;

  /** Keep the label within the viewport and close to its current trigger. */
  function position(): void {
    if (active === undefined) return;
    const bounds = active.getBoundingClientRect();
    const width = tooltip.offsetWidth;
    const left = Math.max(
      8,
      Math.min(bounds.left + (bounds.width - width) / 2, innerWidth - width - 8),
    );
    tooltip.style.left = `${left}px`;
    tooltip.style.top = `${
      bounds.top > tooltip.offsetHeight + 18
        ? bounds.top - tooltip.offsetHeight - 10
        : bounds.bottom + 10
    }px`;
  }

  /** Cancel delayed dismissal while the pointer crosses into the tooltip. */
  function retain(): void {
    clearTimeout(pending);
  }

  /** Remove the transient description without moving the reader's focus. */
  function hide(): void {
    retain();
    clearTimeout(reveal);
    active?.removeAttribute("aria-describedby");
    active = undefined;
    tooltip.hidden = true;
  }

  /** Delay pointer dismissal so the tooltip itself remains hoverable. */
  function scheduleHide(): void {
    retain();
    pending = setTimeout(hide, 120);
  }

  /** Describe the selected status without duplicating tooltips in the DOM. */
  function show(button: HTMLButtonElement): void {
    hide();
    active = button;
    tooltip.textContent = button.dataset.statusDetail ?? "";
    button.setAttribute("aria-describedby", tooltip.id);
    tooltip.hidden = false;
    position();
  }

  /** Ignore quick pointer crossings; only sustained hover or focus opens a hint. */
  function scheduleShow(button: HTMLButtonElement): void {
    hide();
    reveal = setTimeout(() => show(button), 300);
  }

  for (const button of buttons) {
    button.addEventListener("pointerenter", (event) => {
      if (event.pointerType !== "touch") scheduleShow(button);
    });
    button.addEventListener("pointerleave", () => {
      if (root.activeElement !== button) {
        clearTimeout(reveal);
        scheduleHide();
      }
    });
    button.addEventListener("focus", () => scheduleShow(button));
    button.addEventListener("blur", hide);
    button.addEventListener("click", () => show(button));
  }
  tooltip.addEventListener("pointerenter", retain);
  tooltip.addEventListener("pointerleave", () => {
    if (root.activeElement !== active) scheduleHide();
  });
  root.addEventListener("keydown", (event) => {
    if (event.key === "Escape") hide();
  });
  root.addEventListener("pointerdown", (event) => {
    if (event.target !== tooltip && !active?.contains(event.target as Node)) hide();
  });
  root.addEventListener("scroll", position, { capture: true, passive: true });
  root.defaultView?.addEventListener("resize", position);
}
