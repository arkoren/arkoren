import { createWebGL } from "./webgl";

/** Controls direct camera gestures; the static SVG is the no-WebGL path. */
export function mountCamera(root: HTMLElement, wake: () => void) {
  const candidateStage = root.querySelector<HTMLElement>("[data-bench-stage]");
  if (candidateStage === null) return null;
  const stage = candidateStage;
  const candidate = createWebGL(root);
  if (candidate === null) {
    stage.setAttribute(
      "aria-label",
      "Static instrument. Read the four cards to explore its parts.",
    );
    return null;
  }
  const graphics = candidate;
  const content = root.querySelector<HTMLElement>("[data-bench-content]");
  const still = matchMedia("(prefers-reduced-motion: reduce)");
  const coarse = matchMedia("(pointer: coarse)");
  const home = { azimuth: 70, elevation: 12, zoom: 1, x: 0, y: 0, z: 164 };
  const view = { ...home },
    target = { ...home };
  let needle = 0;
  let dirty = true;
  let drag: { x: number; y: number; azimuth: number; elevation: number; moved: boolean } | null =
    null;

  /** Camera and material fades advance only while the figure is visible. */
  function update(dt: number) {
    let moving = false;
    for (const key of Object.keys(view) as (keyof typeof view)[]) {
      const delta = target[key] - view[key];
      if (still.matches || Math.abs(delta) < 0.002) view[key] = target[key];
      else {
        view[key] += delta * (1 - Math.exp(-dt / 0.18));
        moving = true;
      }
    }
    if (moving || dirty || still.matches) {
      moving = graphics.draw(view, needle, dt, still.matches) || moving;
      dirty = moving;
    }
    root.dataset.cameraAzimuth = view.azimuth.toFixed(1);
    root.dataset.cameraZoom = view.zoom.toFixed(3);
    return moving;
  }
  /** Reduced motion applies gestures synchronously instead of easing them. */
  function redraw() {
    dirty = true;
    if (still.matches) update(0);
    wake();
  }
  function reset() {
    Object.assign(target, home);
    redraw();
  }
  function focus(id: string) {
    const point = graphics.center(id);
    target.azimuth = home.azimuth;
    target.elevation = home.elevation;
    target.x = point.x * 0.35;
    target.y = point.y * 0.35;
    target.z = 164 + (point.z - 164) * 0.3;
    target.zoom = 1.12;
    redraw();
  }
  /** All zoom inputs share finite bounds; the render loop supplies the easing. */
  function zoom(amount: number) {
    target.zoom = Math.max(0.85, Math.min(1.8, target.zoom * Math.exp(amount)));
    redraw();
  }
  /** Use the actual layout so stacked drawings never intercept page scrolling. */
  function canWheelZoom() {
    const bounds = stage.getBoundingClientRect();
    return (
      content !== null &&
      content.getBoundingClientRect().left >= bounds.right - 1 &&
      bounds.height < innerHeight
    );
  }
  stage.addEventListener(
    "wheel",
    (event) => {
      if (!canWheelZoom() || event.ctrlKey || event.deltaY === 0) return;
      event.preventDefault();
      const unit =
        event.deltaMode === WheelEvent.DOM_DELTA_LINE
          ? 16
          : event.deltaMode === WheelEvent.DOM_DELTA_PAGE
            ? stage.clientHeight
            : 1;
      zoom(-Math.max(-160, Math.min(160, event.deltaY * unit)) * 0.002);
    },
    { passive: false },
  );
  stage.addEventListener("keydown", (event) => {
    if (["+", "=", "-", "_"].includes(event.key)) {
      event.preventDefault();
      zoom(event.key === "-" || event.key === "_" ? -0.15 : 0.15);
      return;
    }
    if (event.key === "Home") {
      event.preventDefault();
      reset();
      return;
    }
    if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) return;
    event.preventDefault();
    target.azimuth += event.key === "ArrowRight" ? 6 : event.key === "ArrowLeft" ? -6 : 0;
    target.elevation = Math.max(
      5,
      Math.min(
        45,
        target.elevation + (event.key === "ArrowDown" ? 3 : event.key === "ArrowUp" ? -3 : 0),
      ),
    );
    redraw();
  });
  stage.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    drag = {
      x: event.clientX,
      y: event.clientY,
      azimuth: target.azimuth,
      elevation: target.elevation,
      moved: false,
    };
    stage.setPointerCapture(event.pointerId);
    stage.dataset.dragging = "";
  });
  /** Picking always uses the actual rendered surfaces, never screen-space proxies. */
  function inspect(event: PointerEvent, reframe: boolean) {
    const piece = graphics.pick(event);
    const isPart = piece !== undefined && piece !== "stand";
    if (reframe && !isPart) return;
    root.dispatchEvent(
      new CustomEvent("bench-inspect", {
        detail: { piece: isPart ? piece : "", reframe },
      }),
    );
  }
  stage.addEventListener("pointermove", (event) => {
    if (drag === null) {
      if (!coarse.matches) inspect(event, false);
      return;
    }
    const dx = event.clientX - drag.x,
      dy = event.clientY - drag.y;
    drag.moved ||= Math.hypot(dx, dy) > 5;
    if (!drag.moved) return;
    target.azimuth = drag.azimuth - dx * 0.18;
    target.elevation = Math.max(5, Math.min(45, drag.elevation + dy * 0.12));
    redraw();
  });
  stage.addEventListener("pointerup", (event) => {
    const moved = drag?.moved ?? false;
    drag = null;
    stage.removeAttribute("data-dragging");
    if (!moved) inspect(event, true);
  });
  stage.addEventListener("pointercancel", () => {
    drag = null;
    stage.removeAttribute("data-dragging");
  });
  stage.addEventListener("lostpointercapture", () => {
    drag = null;
    stage.removeAttribute("data-dragging");
  });
  /** Refit on each size change without carrying intrinsic canvas dimensions. */
  function refit() {
    stage.setAttribute(
      "aria-label",
      canWheelZoom()
        ? "Workbench camera. Drag or use arrow keys to orbit; scroll or use plus and minus to zoom; Home resets the view."
        : "Workbench camera. Drag or use arrow keys to orbit; use plus and minus to zoom; Home resets the view. Scrolling moves the page.",
    );
    redraw();
  }
  const resize = new ResizeObserver(refit);
  resize.observe(stage);
  root.addEventListener("bench-redraw", redraw);
  window.addEventListener(
    "pagehide",
    (event) => {
      if (!event.persisted) {
        resize.disconnect();
        graphics.dispose();
      }
    },
    { once: true },
  );
  graphics.draw(view, 0, 0, true);
  return {
    update,
    focus,
    reset,
    highlight(id: string) {
      graphics.select(id);
      redraw();
    },
    setNeedle(value: number) {
      if (needle !== value) {
        needle = value;
        dirty = true;
      }
    },
    isDragging: () => drag !== null,
  };
}
