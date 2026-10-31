/**
 * Confirm ordinary website navigation to root and scoped guide archives.
 * Direct URLs, modified clicks, and links without JavaScript remain native.
 * Native dialog behavior supplies modal focus containment and Escape dismissal.
 * @example
 * initArchiveWarning(document);
 */
export function initArchiveWarning(document: Document): void {
  const dialog = document.querySelector<HTMLDialogElement>("[data-archive-warning]");
  const open = dialog?.querySelector<HTMLAnchorElement>("[data-archive-open]");
  const index = dialog?.querySelector<HTMLAnchorElement>("[data-archive-index]");
  const path = dialog?.querySelector<HTMLElement>("[data-archive-path]");
  const cancel = dialog?.querySelector<HTMLButtonElement>("[data-archive-cancel]");
  if (dialog == null || open == null || index == null || path == null || cancel == null) return;
  if (typeof dialog.showModal !== "function") return;

  /** Return keyboard focus to the initiating link after any dismissal. */
  let trigger: HTMLAnchorElement | undefined;
  dialog.addEventListener("close", () => trigger?.focus());
  cancel.addEventListener("click", () => dialog.close());
  open.addEventListener("click", () => dialog.close());
  index.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) dialog.close();
  });

  for (const link of document.querySelectorAll<HTMLAnchorElement>("a[href]")) {
    if (link === open) continue;
    const url = new URL(link.href, document.baseURI);
    const isLocal = url.origin === new URL(document.baseURI).origin;
    const isPublished = url.origin === "https://arkoren.dev";
    if (!isLocal && !isPublished) continue;
    if (!/^\/(?:docs\/guides\/)?llms-full\.txt$/.test(url.pathname)) continue;
    link.setAttribute("aria-haspopup", "dialog");
    link.addEventListener("click", (event) => {
      if (event.defaultPrevented || event.button !== 0) return;
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      trigger = link;
      open.href = link.href;
      open.target = link.target;
      open.rel = link.rel;
      index.href = url.pathname.replace("llms-full.txt", "llms.txt");
      path.textContent = url.pathname;
      dialog.showModal();
      cancel.focus();
    });
  }
}
