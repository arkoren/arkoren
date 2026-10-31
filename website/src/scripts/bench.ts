import { parts } from "../lib/figure/model";
import { mountCamera } from "./camera";

/** Links the four persistent cards to direct surface inspection and the camera. */
export function initBench(root: HTMLElement) {
  const camera = mountCamera(root, run);
  const buttons = [...root.querySelectorAll<HTMLButtonElement>("[data-bench-part]")];
  const cards = [...root.querySelectorAll<HTMLElement>("[data-bench-card]")];
  const assemblies = [...root.querySelectorAll<SVGGElement>("[data-assembly]")];
  const still = matchMedia("(prefers-reduced-motion: reduce)");
  let selected = -1;
  let angle = 0;
  let target = 0;
  let frame = 0;
  let last = 0;
  let isVisible = false;

  /** Hover and card focus change only highlighting; surface clicks can reframe. */
  function select(index: number, reframe = false) {
    const part = parts[index];
    selected = index;
    camera?.highlight(part?.id ?? "");
    if (reframe && part !== undefined) {
      camera?.focus(part.id);
      target = index * (300 / (parts.length - 1));
    }
    for (const button of buttons)
      button.setAttribute("aria-pressed", String(button.dataset.benchPart === part?.id));
    for (const card of cards)
      card.toggleAttribute("data-active", card.dataset.benchCard === part?.id);
    for (const assembly of assemblies) {
      assembly.toggleAttribute("data-active", assembly.dataset.assembly === part?.id);
      assembly.toggleAttribute(
        "data-linked",
        part?.linked.some((id) => id === assembly.dataset.assembly) ?? false,
      );
    }
    if (still.matches) paintNeedle(target);
    run();
  }

  /** Restore a keyboard-focused card when the pointer leaves an inspection area. */
  function restoreFocus() {
    const index = cards.findIndex((card) => card.contains(document.activeElement));
    select(index);
  }

  /** The instrument dial follows selection, sharing the camera's frame loop. */
  function paintNeedle(value: number) {
    angle = value;
    camera?.setNeedle(value);
    if (still.matches) camera?.update(0);
  }

  /** Stops scheduling once the camera and dial have settled. */
  function tick(now: number) {
    frame = 0;
    const dt = last === 0 ? 0 : Math.min((now - last) / 1000, 1 / 30);
    last = now;
    paintNeedle(angle + (target - angle) * (1 - Math.exp(-dt / 0.2)));
    if (Math.abs(target - angle) < 0.01) paintNeedle(target);
    const isCameraMoving = camera?.update(dt) ?? false;
    if (angle !== target || isCameraMoving) run();
    else last = 0;
  }

  /** Schedules at most one frame while visible and motion is allowed. */
  function run() {
    if (frame === 0 && isVisible && !document.hidden && !still.matches)
      frame = requestAnimationFrame(tick);
  }

  for (const [index, button] of buttons.entries()) {
    button.addEventListener("click", () => select(index));
    button.addEventListener("keydown", (event) => {
      const next = {
        ArrowRight: (index + 1) % parts.length,
        ArrowLeft: (index + parts.length - 1) % parts.length,
        Home: 0,
        End: parts.length - 1,
      }[event.key];
      if (next === undefined) return;
      event.preventDefault();
      select(next);
      buttons[next]?.focus();
    });
  }
  for (const [index, card] of cards.entries()) {
    card.addEventListener("pointerenter", () => select(index));
    card.addEventListener("pointerleave", restoreFocus);
    card.addEventListener("focusin", () => select(index));
    card.addEventListener("focusout", (event) => {
      if (!(event.relatedTarget instanceof Node) || !card.contains(event.relatedTarget)) select(-1);
    });
  }

  /** SVG fallback picking shares the same selection behavior as WebGL. */
  function inspect(event: Event) {
    if (camera?.isDragging() || !(event.target instanceof Element)) return;
    const id = event.target.closest<SVGElement>("[data-assembly]")?.dataset.assembly;
    const index = parts.findIndex((part) => part.id === id);
    if (index < 0 || (index === selected && event.type === "pointerover")) return;
    select(index, event.type !== "pointerover");
  }
  root.querySelector("[data-bench-stage]")?.addEventListener("pointerover", inspect);
  root.querySelector("[data-bench-stage]")?.addEventListener("click", inspect);
  root.querySelector("[data-bench-stage]")?.addEventListener("pointerleave", restoreFocus);
  root.addEventListener("bench-inspect", (event) => {
    const { piece, reframe } = (event as CustomEvent<{ piece: string; reframe: boolean }>).detail;
    const index = parts.findIndex((part) => part.id === piece);
    if (index === selected && !reframe) return;
    select(index, reframe);
  });
  still.addEventListener("change", () => select(selected, false));
  new IntersectionObserver((entries) => {
    isVisible = entries.at(-1)?.isIntersecting ?? false;
    if (!isVisible) {
      cancelAnimationFrame(frame);
      frame = 0;
    }
    last = 0;
    run();
  }).observe(root);
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      cancelAnimationFrame(frame);
      frame = 0;
    }
    last = 0;
    run();
  });
  select(-1);
}
