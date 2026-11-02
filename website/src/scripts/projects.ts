/** Scramble glyphs remain visual; accessible link labels retain complete copy. */
const glyphs = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789<>/{}";

/** Initialize each announcement independently, including repeated components. */
for (const carousel of document.querySelectorAll<HTMLElement>("[data-projects]")) {
  initProjects(carousel);
}

/** Rotate only idle, visible cards; settle immediately when a reader interacts. */
function initProjects(carousel: HTMLElement) {
  const slides = [...carousel.querySelectorAll<HTMLElement>("[data-project]")];
  const names = slides.map((slide) => slide.querySelector<HTMLElement>("[data-project-name]"));
  const titles = names.map((name) => name?.textContent ?? "");
  const descriptions = slides.map((slide) =>
    slide.querySelector<HTMLElement>("[data-project-description]"),
  );
  const subtitles = descriptions.map(
    (description) => description?.dataset.projectDescription ?? "",
  );
  const words = descriptions.map((description) => [
    ...(description?.querySelectorAll<HTMLElement>("[data-project-word]") ?? []),
  ]);
  const motion = matchMedia("(prefers-reduced-motion: reduce)");
  const style = getComputedStyle(carousel);
  const duration = parseFloat(style.getPropertyValue("--duration-decode"));
  const hold = parseFloat(style.getPropertyValue("--duration-project-hold"));
  let active = 0;
  let frame = 0;

  /** Restore readable copy even when motion preferences change mid-decode. */
  function settle() {
    cancelAnimationFrame(frame);
    const name = names[active];
    if (name !== null && name !== undefined) name.textContent = titles[active];
    renderSubtitle(subtitles[active]);
  }

  /** Overlay each word inside its reserved bounds so wide glyphs cannot reflow. */
  function renderSubtitle(text: string) {
    const scrambledWords = text.split(" ");
    for (const [index, word] of words[active].entries()) {
      word.textContent = scrambledWords[index];
    }
  }

  /** Resolve left to right, leaving shared letters and spaces undisturbed. */
  function rotate() {
    if (
      motion.matches ||
      document.hidden ||
      carousel.getClientRects().length === 0 ||
      carousel.matches(":hover, :focus-within") ||
      slides.length < 2
    )
      return;

    settle();
    const previous = titles[active];
    const previousSubtitle = subtitles[active];
    active = (active + 1) % slides.length;
    const title = titles[active];
    const subtitle = subtitles[active];
    for (const [index, slide] of slides.entries()) {
      const isInactive = index !== active;
      slide.classList.toggle("invisible", isInactive);
      slide.inert = isInactive;
      slide.setAttribute("aria-hidden", String(isInactive));
    }

    const name = names[active];
    if (name === null || name === undefined) return;
    const startedAt = performance.now();

    /** Use elapsed time rather than frame counts so decoding survives slow frames. */
    function decode(now: number) {
      if (name === null || name === undefined) return;
      const progress = Math.min((now - startedAt) / duration, 1);
      name.textContent = scramble({ text: title, previous, progress });
      renderSubtitle(
        scramble({
          text: subtitle,
          previous: previousSubtitle,
          progress,
        }),
      );
      if (progress < 1) frame = requestAnimationFrame(decode);
      else settle();
    }

    decode(startedAt);
  }

  carousel.addEventListener("pointerenter", settle);
  carousel.addEventListener("focusin", settle);
  motion.addEventListener("change", settle);
  document.addEventListener("visibilitychange", settle);
  window.setInterval(rotate, hold);
}

/** Decode each line at the same pace, retaining shared letters and whitespace. */
function scramble({
  text,
  previous,
  progress,
}: {
  /** The complete text restored at the end of the transition. */
  text: string;
  /** The previous announcement's text, used to preserve shared characters. */
  previous: string;
  /** Elapsed transition time normalized to the range zero through one. */
  progress: number;
}) {
  const resolved = Math.floor(progress * text.length);
  return [...text]
    .map((letter, index) => {
      if (index < resolved || /\s/.test(letter) || letter === previous[index]) return letter;
      return glyphs[Math.floor(Math.random() * glyphs.length)];
    })
    .join("");
}
