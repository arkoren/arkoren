/**
 * Enhance documentation commands and prose code blocks with clipboard feedback.
 * Only the pre element supplies prose text, never its adjacent button label.
 * Clipboard rejection (including an unavailable API) remains local to the
 * button, and restores its original label after the same feedback interval.
 * @param root Documentation subtree; call once after rendering.
 * @example initCopyControls(document);
 */
export function initCopyControls(root: ParentNode): void {
  for (const pre of root.querySelectorAll<HTMLElement>(".prose pre")) {
    const button = Object.assign(document.createElement("button"), {
      type: "button",
      className: "copy",
      textContent: "copy",
    });
    const wrap = Object.assign(document.createElement("div"), { className: "copyable" });
    pre.replaceWith(wrap);
    wrap.append(pre, button);
    bindCopy(button, () => pre.textContent ?? "");
  }
  for (const button of root.querySelectorAll<HTMLButtonElement>("[data-copy]")) {
    bindCopy(button, () => button.dataset.copy ?? "");
  }
}

/**
 * Bind one text source. Disable only while the clipboard promise is pending;
 * repeated copies restart feedback rather than letting an older timer erase it.
 */
function bindCopy(button: HTMLButtonElement, text: () => string): void {
  const label = button.textContent;
  let timer: ReturnType<typeof setTimeout>;
  button.setAttribute("aria-live", "polite");
  button.addEventListener("click", async () => {
    clearTimeout(timer);
    button.disabled = true;
    try {
      await navigator.clipboard.writeText(text());
      button.textContent = "copied";
    } catch {
      button.textContent = "copy failed";
    } finally {
      button.disabled = false;
    }
    timer = setTimeout(() => {
      button.textContent = label;
    }, 1500);
  });
}
