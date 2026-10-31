/**
 * Track the last heading above the sticky-header reading line for one contents
 * list. Each copy (mobile or desktop) updates independently from the same page
 * headings. Does not intercept navigation or scroll; CSS owns reduced motion.
 * The optional data-contents-marker enables the docs rail's moving indicator.
 * @example document.querySelectorAll<HTMLElement>('[data-page-contents]').forEach(initPageContents);
 */
export function initPageContents(nav: HTMLElement): void {
  /** Resolve targets inside the owning document, ignoring missing fragments. */
  const doc = nav.ownerDocument;
  const targets = [...nav.querySelectorAll<HTMLAnchorElement>('a[href^="#"]')].flatMap((link) => {
    const heading = doc.getElementById(decodeURIComponent(link.hash.slice(1)));
    return heading ? [{ link, heading }] : [];
  });
  if (targets.length === 0) return;

  /** Use a per-list frame gate so several scroll events never repeat layout. */
  let isScheduled = false;

  /** Mark exactly one destination; Overview wins before the first section. */
  function mark() {
    const line = (parseFloat(getComputedStyle(doc.documentElement).scrollPaddingTop) || 88) + 8;
    const current =
      targets.findLast((target) => target.heading.getBoundingClientRect().top <= line) ??
      targets[0];
    for (const target of targets) {
      if (target === current) target.link.setAttribute("aria-current", "location");
      else target.link.removeAttribute("aria-current");
    }
    if (nav.hasAttribute("data-contents-marker")) moveMarker(current.link);
  }

  /** Coalesce scroll, history, and layout notifications into one measurement. */
  function schedule() {
    if (isScheduled) return;
    isScheduled = true;
    requestAnimationFrame(() => {
      isScheduled = false;
      mark();
    });
  }

  for (const event of ["scroll", "scrollend", "resize", "hashchange", "pageshow"]) {
    addEventListener(event, schedule, { passive: true });
  }
  doc.fonts.ready.then(schedule);
  mark();
}

/**
 * Position the optional docs marker using list-local geometry. Its transition
 * and reduced-motion override live in the .toc-list rule of styles/global.css, not JavaScript scrolling.
 */
function moveMarker(link: HTMLAnchorElement): void {
  const list = link.closest("ul");
  const item = link.closest("li");
  if (!list || !item) return;
  list.style.setProperty("--marker-top", `${item.offsetTop}px`);
  list.style.setProperty("--marker-height", `${item.offsetHeight}px`);
  list.dataset.marker = "";
}
