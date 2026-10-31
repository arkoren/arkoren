/**
 * Restore manual preferences before paint; delay the automatic seasonal palette.
 * The layout transpiles this source before inlining it in the document head.
 */
(() => {
  /** The document root owns the palette and decorative animation state. */
  const root = document.documentElement;
  /**
   * A manual choice belongs only to the current calendar year's season.
   * Undefined delegates to the calendar after automatic activation is ready.
   */
  let preference: boolean | undefined;
  /** Memory-only choices must survive refreshes when storage is blocked. */
  let preferenceYear = new Date().getFullYear();
  /** The first automatic activation waits two seconds after the document is ready. */
  let automaticReady = false;
  /**
   * The scheduled entrance is cancelled after a manual choice. Undefined
   * means no entrance has been scheduled in this document yet.
   */
  let activationTimer: number | undefined;
  /**
   * One animation clock per tab, shared by full-page navigations. Milliseconds
   * since the epoch preserve the phase; undefined means decorations are off.
   */
  let animationStartedAt: number | undefined;
  let animationOffset = 0;
  /**
   * A manual wiggle has its own epoch-millisecond clock and must not restart
   * the drop or ghosts. Undefined leaves the normal swing timeline intact.
   */
  let spiderWiggleStartedAt: number | undefined;
  /** Clears data-season-fading once the slow color fade (--duration-slow, 500ms) has finished. */
  let fadeTimer: number | undefined;

  /**
   * Mark the document as fading while the palette changes, so styles/season.css transitions every
   * color slowly. The first paint is exempt: it is not ready yet, so nothing animates on load.
   */
  function fadePalette() {
    if (root.dataset.seasonReady === undefined) return;
    root.dataset.seasonFading = "";
    window.clearTimeout(fadeTimer);
    fadeTimer = window.setTimeout(() => delete root.dataset.seasonFading, 600);
  }

  /**
   * Replay only a completed CSS swing after the spider receives a click.
   * Running entrances reject extra clicks so their drop and ghost timelines
   * remain uninterrupted. Events outside the seasonal palette are ignored.
   */
  function wiggleSpider(event: Event) {
    if (root.dataset.season !== "halloween") return;
    if (!(event.currentTarget instanceof Element)) return;
    const animations = event.currentTarget
      .getAnimations({ subtree: true })
      .filter(
        (animation): animation is CSSAnimation =>
          animation instanceof CSSAnimation && animation.animationName.startsWith("spider-"),
      );
    if (!animations.length || animations.some((animation) => animation.playState !== "finished"))
      return;
    const swing = animations.find((animation) => animation.animationName === "spider-swing");
    if (!(swing?.effect instanceof KeyframeEffect)) return;
    spiderWiggleStartedAt = new Date().getTime();
    swing.effect.setKeyframes([
      { transform: "rotate(0deg)" },
      { transform: "rotate(-22deg)" },
      { transform: "rotate(25deg)" },
      { transform: "rotate(-18deg)" },
      { transform: "rotate(0deg)" },
    ]);
    swing.effect.updateTiming({ delay: 0, duration: 450, iterations: 3 });
    swing.currentTime = 0;
    swing.play();
  }

  /**
   * Connect or clear the tab's animation phase before paint. Enabling retains
   * a valid clock across documents; disabling clears it while preserving the
   * rendered offset during the fade. Blocked storage leaves a page-local clock.
   */
  function connectAnimationClock(nowAt: Date, isEnabled: boolean) {
    if (!isEnabled) {
      animationStartedAt = undefined;
      spiderWiggleStartedAt = undefined;
      // Keep the rendered flight phase stable while the ghosts fade out.
      try {
        sessionStorage.removeItem("arkoren-season-animation");
      } catch {
        /* Optional storage. */
      }
      return;
    }
    const now = nowAt.getTime();
    animationStartedAt = readAnimationClock(nowAt) ?? animationStartedAt;
    animationStartedAt ??= now;
    animationOffset = Math.max(0, now - animationStartedAt);
    root.style.setProperty("--season-elapsed", `${animationOffset}ms`);
    try {
      sessionStorage.setItem(
        "arkoren-season-animation",
        JSON.stringify({
          year: nowAt.getFullYear(),
          startedAt: animationStartedAt,
        }),
      );
    } catch {
      /* Decorations still work without storage. */
    }
  }

  /**
   * Return a finite epoch-millisecond clock from this year's current tab.
   * Future, non-positive, malformed, and expired clocks are ignored. A valid
   * clock also records that the automatic entrance already ran in this tab.
   */
  function readAnimationClock(nowAt: Date): number | undefined {
    const saved = readStoredObject("sessionStorage", "arkoren-season-animation");
    if (saved?.year !== nowAt.getFullYear()) return undefined;
    const startedAt = saved.startedAt;
    if (typeof startedAt !== "number" || !Number.isFinite(startedAt)) return undefined;
    if (startedAt <= 0 || startedAt > nowAt.getTime()) return undefined;
    return startedAt;
  }

  /**
   * Decode optional browser storage at its untrusted boundary. Both storage
   * access and JSON parsing can fail; either failure means no saved choice.
   */
  function readStoredObject(
    storage: "localStorage" | "sessionStorage",
    key: string,
  ): Record<string, unknown> | undefined {
    try {
      const saved: unknown = JSON.parse(window[storage].getItem(key) ?? "null");
      if (typeof saved !== "object" || saved === null) return undefined;
      return Object.fromEntries(Object.entries(saved));
    } catch {
      return undefined;
    }
  }

  /** Cached documents retain animations; seek them rather than replaying them. */
  function restoreAnimations() {
    refreshSeason();
    if (animationStartedAt === undefined) return;
    const elapsed = Math.max(0, new Date().getTime() - animationStartedAt);
    for (const animation of document.getAnimations()) {
      if (!(animation instanceof CSSAnimation)) continue;
      if (animation.animationName === "spider-swing" && spiderWiggleStartedAt !== undefined) {
        animation.currentTime = Math.max(0, new Date().getTime() - spiderWiggleStartedAt);
      } else if (
        animation.animationName?.startsWith("spider-") ||
        animation.animationName?.startsWith("ghost-flight")
      ) {
        animation.currentTime = elapsed - animationOffset;
      }
    }
  }

  /**
   * Resolve the inclusive October 24 through November 1 window in local time.
   * This runs in the visitor's browser, not in the build server's timezone.
   */
  function isHalloween(nowAt: Date) {
    return (
      (nowAt.getMonth() === 9 && nowAt.getDate() >= 24) ||
      (nowAt.getMonth() === 10 && nowAt.getDate() === 1)
    );
  }

  /**
   * Return this year's explicit boolean preference, including a saved opt-out.
   * Missing, expired, or invalid storage returns undefined so the calendar
   * remains authoritative and manual controls continue to work.
   */
  function readPreference(nowAt: Date): boolean | undefined {
    const saved = readStoredObject("localStorage", "arkoren-season");
    if (saved?.year === nowAt.getFullYear() && typeof saved.enabled === "boolean") {
      return saved.enabled;
    }
    return undefined;
  }

  /** Synchronize both controls and the palette without replacing page content. */
  function applySeason() {
    const nowAt = new Date();
    const inSeason = isHalloween(nowAt);
    const isEnabled = preference ?? (automaticReady && inSeason);
    if (root.dataset.season !== (isEnabled ? "halloween" : "normal")) {
      fadePalette();
      // Preserve the previous page's clock while automatic activation is pending.
      if (preference !== undefined || automaticReady || !inSeason) {
        connectAnimationClock(nowAt, isEnabled);
      }
    }
    root.dataset.season = isEnabled ? "halloween" : "normal";
    for (const button of document.querySelectorAll<HTMLButtonElement>("[data-season-toggle]")) {
      button.setAttribute("aria-pressed", String(isEnabled));
      button.hidden = false;
    }
  }

  /** Toggle in memory even when the browser refuses persistent storage. */
  function toggleSeason() {
    window.clearTimeout(activationTimer);
    automaticReady = true;
    const nowAt = new Date();
    preferenceYear = nowAt.getFullYear();
    preference = root.dataset.season !== "halloween";
    try {
      localStorage.setItem(
        "arkoren-season",
        JSON.stringify({
          year: nowAt.getFullYear(),
          enabled: preference,
        }),
      );
    } catch {
      /** The current page still retains the visitor's choice. */
    }
    applySeason();
  }

  /** Re-read on page restoration or return to a tab across a date boundary. */
  function refreshSeason() {
    const nowAt = new Date();
    if (preferenceYear !== nowAt.getFullYear()) {
      preferenceYear = nowAt.getFullYear();
      preference = readPreference(nowAt);
    }
    applySeason();
  }

  /** Enable transitions only after the initial palette has been painted. */
  function connectControls() {
    applySeason();
    if (!automaticReady) {
      activationTimer = window.setTimeout(() => {
        automaticReady = true;
        refreshSeason();
      }, 2_000);
    }
    for (const button of document.querySelectorAll("[data-season-toggle]")) {
      button.addEventListener("click", toggleSeason);
    }
    document.querySelector("[data-season-spider]")?.addEventListener("click", wiggleSpider);
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        root.dataset.seasonReady = "";
      }),
    );
    window.addEventListener("pageshow", (event) => {
      if (event.persisted) restoreAnimations();
      else refreshSeason();
    });
    window.addEventListener("storage", () => {
      preference = readPreference(new Date());
      refreshSeason();
    });
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") refreshSeason();
    });
    /** A short check also handles a page left open through local midnight. */
    window.setInterval(refreshSeason, 60_000);
  }

  preference = readPreference(new Date());
  automaticReady = isHalloween(new Date()) && readAnimationClock(new Date()) !== undefined;
  refreshSeason();
  window.addEventListener("pagereveal", restoreAnimations);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", connectControls, { once: true });
  } else {
    connectControls();
  }
})();
