import { test, expect } from "@playwright/test";
import { dev, preview } from "astro";
import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { required } from "../dom.ts";

/** Software WebGL keeps surface picking available on headless CI runners. */
test.use({ launchOptions: { args: ["--enable-unsafe-swiftshader", "--ignore-gpu-blocklist"] } });

declare global {
  interface Window {
    /**
     * Recorded navigation snapshots belong to the browser fixture only. Each
     * promise resolves after its controlled frame sampling completes.
     * @example const frames = await window.newsRevealFrames;
     */
    newsRevealFrames: Promise<
      {
        opacity: number;
        translate: string;
        oldOpacity: number;
        oldTranslate: string;
      }[]
    >;
  }
}

/** Controlled posts exercise real rendering, client wiring, and the search index. */
const website = fileURLToPath(new URL("../../", import.meta.url));
/** Long tokens exercise scrolling; comments and literals exercise tonal contrast. */
const codeFixture = `// Comments must remain readable in both system themes.
func Message(count int) string {
    if count > 0 {
        return "${"readable-example-".repeat(8)}"
    }
    return "hello"
}`;
/** Fixture paths are populated by beforeAll before any journey runs. */
let directory = "";
/** Preview lifetime is owned by the suite, including failed setup cleanup. */
let server: Awaited<ReturnType<typeof preview>> | undefined;
/** The ephemeral preview origin is shared by serial journeys in this suite. */
let origin = "";
/** Each journey collects only its own uncaught browser errors. */
let failures: string[];

/** Treat uncaught client errors as journey failures rather than silent console noise. */
test.beforeEach(async ({ page }) => {
  failures = [];
  page.on("pageerror", (error) => failures.push(error.message));
});

test.afterEach(() => {
  expect(failures).toEqual([]);
});

test.beforeAll(async () => {
  const temporary = existsSync(resolve(tmpdir(), "opencode"))
    ? resolve(tmpdir(), "opencode")
    : tmpdir();
  directory = realpathSync(mkdtempSync(resolve(temporary, "arkoren-browser-")));
  const root = resolve(directory, "site");
  const posts = resolve(directory, "posts");
  mkdirSync(root);
  mkdirSync(posts);
  for (const [id, title, category, date, body, draft] of [
    ["alpha", "Alpha fixture", "Examples", "2026-01-01", "fixturequartz", false],
    ["beta", "Beta fixture", "Examples", "2026-01-02", "fixturequartz", false],
    [
      "gamma",
      "Gamma fixture",
      "Release notes and engineering",
      "2026-01-03",
      "fixturecopper",
      false,
    ],
    ["hidden", "Hidden fixture", "Notes", "2026-01-04", "fixturehiddenonly", true],
  ] as const) {
    const matrix =
      id === "alpha"
        ? "\n\n| Backend | Shared state | Expiry | Counters | Pull | Locks | App restart | Backend restart |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n| Fixture | Yes — Shared fixture state. | No | Unknown | Partial | Yes | No | Partial — Requires fixture persistence. |"
        : "";
    const bannerAsset = {
      alpha: { file: "introducing-arkoren.svg", alt: "Fixture banner" },
      beta: { file: "browser-fixture.svg", alt: "Second vector banner" },
      gamma: { file: "introducing-arkoren.png", alt: "Raster banner" },
      hidden: undefined,
    }[id];
    const banner = bannerAsset
      ? `banner:\n  src: ${resolve(root, "src/assets/images/blog", bannerAsset.file)}\n  alt: ${bannerAsset.alt}\n  width: 1200\n  height: 400\n`
      : "";
    const code =
      id === "alpha"
        ? `\n\n\`\`\`go\n${codeFixture}\n\`\`\`\n\n\`\`\`bash\n# Install a module\ngo get arkoren.dev/core\n\`\`\`\n\n\`\`\`json\n{"message": "hello", "count": 42, "enabled": true}\n\`\`\`\n\n\`\`\`text\nUnhighlighted output stays readable too.\n\`\`\``
        : "";
    writeFileSync(
      resolve(posts, `${id}.md`),
      `---\ntitle: ${title}\ndescription: Browser fixture\ncategory: ${category}\npublishedAt: ${date}\ndraft: ${draft}\n${banner}---\n\n## Fixture section\n\n${body}${matrix}${code}\n`,
    );
  }
  for (const entry of ["src", "public", "tsconfig.json"])
    cpSync(resolve(website, entry), resolve(root, entry), { recursive: true });
  writeFileSync(
    resolve(root, "src/assets/images/blog/browser-fixture.svg"),
    '<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="400" viewBox="0 0 1200 400"><rect width="1200" height="400" fill="orange"/></svg>',
  );
  writeFileSync(
    resolve(root, "src/pages/selector-fixture.astro"),
    `---
import Site from "../layouts/Site.astro";
import Selector from "../components/ui/Selector.astro";
import Code from "../components/ui/Code.astro";
const panels = ["Create", "Configure", "Start", "Serve", "Observe", "Drain", "Stop"];
---
<Site title="Selector fixture" description="Panel selection fixture"><main>
<Selector role="tablist" aria-label="Fixture panels">
  {panels.map((name, index) => <button type="button" role="tab" aria-selected={String(index === 0)}>{name}</button>)}
</Selector>
<Code file="fixture.go" code={${JSON.stringify(codeFixture)}} />
</main></Site>
<script>
  // The selector follows caller-owned ARIA state, so the fixture supplies the selection.
  for (const tab of document.querySelectorAll<HTMLElement>('[role="tab"]')) {
    tab.addEventListener("click", () => {
      for (const other of document.querySelectorAll('[role="tab"]'))
        other.setAttribute("aria-selected", String(other === tab));
    });
  }
</script>
`,
  );
  symlinkSync(resolve(website, "node_modules"), resolve(root, "node_modules"), "dir");
  writeFileSync(
    resolve(root, "astro.config.ts"),
    `import config from ${JSON.stringify(pathToFileURL(resolve(website, "astro.config.ts")).href)};\nexport default {...config, root:${JSON.stringify(root)}, cacheDir:${JSON.stringify(resolve(root, ".cache"))}};`,
  );
  const output = resolve(directory, "output");
  execFileSync(
    "npm",
    [
      "exec",
      "--",
      "astro",
      "build",
      "--root",
      root,
      "--config",
      "astro.config.ts",
      "--outDir",
      output,
    ],
    {
      // A fixture cwd also isolates Astro's fallback prerender staging directory.
      cwd: root,
      env: {
        ...process.env,
        ARKOREN_BLOG: posts,
        ARKOREN_REFERENCES: resolve(website, "../cmd/arkoren/skills/arkoren/references"),
        ARKOREN_REPO: resolve(website, ".."),
      },
      stdio: "pipe",
      timeout: 60000,
    },
  );
  execFileSync("npm", ["exec", "--", "pagefind", "--site", output], {
    cwd: website,
    stdio: "pipe",
    timeout: 30000,
  });
  server = await preview({
    root: website,
    outDir: output,
    logLevel: "silent",
    server: { host: "127.0.0.1", port: 0 },
  });
  origin = `http://127.0.0.1:${server.port}`;
});

test.afterAll(async () => {
  await server?.stop();
  if (directory) rmSync(directory, { recursive: true, force: true });
});

/** Prevent faded syntax and fixed-theme examples from becoming unreadable. */
test("code remains readable and scrolls within mobile columns in both OS themes", async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 800 });
  for (const path of ["/", "/blog/alpha", "/selector-fixture"]) {
    await page.goto(`${origin}${path}`);
    const blocks = page.locator("pre.astro-code:visible");
    expect(await blocks.count()).toBeGreaterThan(0);
    for (const colorScheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme });
      const surfaces = page.locator("pre.astro-code:visible, [data-code-surface]:visible");
      const unreadable = () =>
        surfaces.evaluateAll((elements) => {
          /** Compute WCAG relative luminance from a browser-resolved RGB color. */
          function luminance(color: string) {
            const match = color.match(/[\d.]+/g);
            if (match === null) throw new Error(`invalid browser color: ${color}`);
            const channels = match
              .slice(0, 3)
              .map(Number)
              .map((value) => {
                const channel = value / 255;
                return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
              });
            return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
          }
          const failures = [];
          for (const block of elements) {
            const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT);
            while (walker.nextNode()) {
              const node = walker.currentNode;
              if (!node.textContent?.trim()) continue;
              const element = node.parentElement;
              if (element === null) throw new Error("text has no parent element");
              let surface: HTMLElement | null = element;
              while (
                surface !== null &&
                getComputedStyle(surface).backgroundColor === "rgba(0, 0, 0, 0)"
              ) {
                surface = surface.parentElement;
              }
              if (surface === null) throw new Error("text has no opaque background");
              const foreground = luminance(getComputedStyle(element).color);
              const background = luminance(getComputedStyle(surface).backgroundColor);
              const ratio =
                (Math.max(foreground, background) + 0.05) /
                (Math.min(foreground, background) + 0.05);
              if (ratio < 4.5) failures.push({ text: node.textContent, ratio });
            }
          }
          return failures;
        });
      /** Allow inherited seasonal color transitions to settle after an OS switch. */
      await expect
        .poll(unreadable, {
          message: `${path}: ${colorScheme} code and labels must meet 4.5:1 contrast`,
        })
        .toEqual([]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      for (const block of await blocks.all()) {
        expect(
          await block.evaluate((element) => element.getBoundingClientRect().right <= innerWidth),
        ).toBe(true);
      }
    }
  }
});

/** Prevent unreadable status symbols, inaccessible hints, and oversized icon cells. */
test("status matrices fit desktop columns and explain icons on hover, focus, and tap", async ({
  page,
  browser,
}) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`${origin}/blog/alpha`);
  const table = page.getByRole("table");
  const container = table.locator("..");
  expect(await container.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(
    true,
  );
  const yes = table.getByRole("button", { name: "Yes", exact: true }).first();
  const no = table.getByRole("img", { name: "No", exact: true }).first();
  // Readers must be able to distinguish icons that offer additional context.
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    const plainColor = await no.evaluate((element) => getComputedStyle(element).color);
    await expect
      .poll(() => yes.evaluate((element) => getComputedStyle(element).color))
      .not.toBe(plainColor);
  }
  await page.clock.install();
  await page.clock.pauseAt(new Date());
  await yes.hover();
  const tooltip = page.getByRole("tooltip");
  await expect(tooltip).toBeHidden();
  await page.mouse.move(0, 0);
  await page.clock.runFor(1000);
  await expect(tooltip).toBeHidden();
  await page.clock.resume();
  await yes.hover();
  await expect(tooltip).toHaveText("Shared fixture state.");
  /** Hints must remain readable and distinguishable from the surrounding page. */
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    const contrast = await tooltip.evaluate((element) => {
      /** Resolve browser RGB values to WCAG relative luminance. */
      function luminance(color: string) {
        const match = color.match(/[\d.]+/g);
        if (match === null) throw new Error(`invalid browser color: ${color}`);
        const channels = match
          .slice(0, 3)
          .map(Number)
          .map((value) => {
            const channel = value / 255;
            return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
          });
        return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
      }
      /** Measure contrast without coupling the check to a particular palette. */
      function ratio(first: number, second: number) {
        return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
      }
      const style = getComputedStyle(element);
      const background = luminance(style.backgroundColor);
      return {
        text: ratio(luminance(style.color), background),
        surface: ratio(luminance(getComputedStyle(document.body).backgroundColor), background),
      };
    });
    expect(contrast.text).toBeGreaterThanOrEqual(4.5);
    expect(contrast.surface).toBeGreaterThanOrEqual(3);
  }
  await tooltip.hover();
  await expect(tooltip).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(tooltip).toBeHidden();
  await no.hover();
  await expect(tooltip).toBeHidden();
  await table.getByRole("button", { name: "Partial", exact: true }).focus();
  await expect(tooltip).toHaveText("Requires fixture persistence.");
  await page.keyboard.press("Escape");
  await expect(tooltip).toBeHidden();
  const touch = await browser.newPage({ hasTouch: true, viewport: { width: 375, height: 800 } });
  touch.on("pageerror", (error) => failures.push(error.message));
  try {
    await touch.goto(`${origin}/blog/alpha`);
    await touch.getByRole("button", { name: "Partial", exact: true }).last().tap();
    const hint = touch.getByRole("tooltip");
    await expect(hint).toHaveText("Requires fixture persistence.");
    const bounds = required(await hint.boundingBox());
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(375);
    expect(await touch.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await touch.getByRole("heading", { name: "Fixture section" }).tap();
    await expect(hint).toBeHidden();
  } finally {
    await touch.close();
  }
});

/** Prevent duplicate announcements and lost links during scrolling and navigation. */
test("latest post hands off from the homepage hero to the sticky navbar", async ({ page }) => {
  /**
   * Fast machines run the hand-off script before the first view-transition
   * capture and can hide a missing server-rendered state, so slow the page
   * down the way CI runners are.
   */
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 6 });
  /**
   * Sample the actual navigation snapshot, not just the underlying link. The
   * announcement's view-transition animations are paused and seeked to fixed
   * points, so slow runners cannot finish the short animation between frames.
   */
  await page.addInitScript(() => {
    window.addEventListener("pagereveal", (event) => {
      window.newsRevealFrames = (async () => {
        if (!event.viewTransition) return [];
        await event.viewTransition.ready;
        const animations = document.getAnimations().filter((animation) => {
          const effect = animation.effect;
          return effect instanceof KeyframeEffect && effect.pseudoElement?.endsWith("(nav-news)");
        });
        for (const animation of animations) animation.pause();
        const frames = [];
        for (const progress of [0.25, 0.5, 0.75]) {
          for (const animation of animations) {
            const duration = Number(animation.effect?.getComputedTiming().duration ?? 0);
            animation.currentTime = duration * progress;
          }
          const style = getComputedStyle(
            document.documentElement,
            "::view-transition-new(nav-news)",
          );
          const oldStyle = getComputedStyle(
            document.documentElement,
            "::view-transition-old(nav-news)",
          );
          frames.push({
            opacity: Number(style.opacity),
            translate: style.translate,
            oldOpacity: Number(oldStyle.opacity),
            oldTranslate: oldStyle.translate,
          });
        }
        for (const animation of animations) animation.play();
        return frames;
      })();
    });
  });
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(origin);
  const heroLink = page.locator("[data-hero-news]");
  const navLink = page.locator("[data-news]");
  await expect(heroLink).toHaveAttribute("href", "/blog/gamma");
  await expect(heroLink).toContainText("Gamma fixture");
  await expect(heroLink.locator("time")).toBeVisible();
  await expect(heroLink.locator("time")).not.toBeEmpty();
  await expect(heroLink.locator("time")).toHaveAttribute("datetime", /^2026-01-03(?:T|$)/);
  await expect(navLink).not.toBeVisible();
  await expect(navLink).toHaveJSProperty("inert", true);

  await page.evaluate(() => {
    const hero = document.querySelector("[data-home-hero]");
    const header = document.querySelector("[data-header]");
    if (hero === null || header === null) throw new Error("missing homepage navigation fixture");
    window.scrollTo({
      top:
        hero.getBoundingClientRect().bottom + scrollY - header.getBoundingClientRect().height - 20,
      behavior: "instant",
    });
  });
  await expect(navLink).not.toBeVisible();
  const isFadingIn = await page.evaluate(async () => {
    const news = document.querySelector("[data-news]");
    if (news === null) throw new Error("missing navigation announcement");
    window.scrollBy({ top: 40, behavior: "instant" });
    for (let frame = 0; frame < 30; frame++) {
      await new Promise(requestAnimationFrame);
      const style = getComputedStyle(news);
      const opacity = Number(style.opacity);
      if (style.visibility === "visible" && opacity > 0 && opacity < 1) return true;
    }
    return false;
  });
  expect(isFadingIn, "the navbar announcement must render intermediate fade-in frames").toBe(true);
  await expect(navLink).toBeVisible();
  await expect(navLink).toHaveCSS("opacity", "1");
  await expect(navLink).toHaveJSProperty("inert", false);

  await page.reload();
  await expect(navLink).toBeVisible();
  await expect(navLink).toHaveCSS("opacity", "1");
  const isFadingOut = await page.evaluate(async () => {
    const news = document.querySelector("[data-news]");
    if (news === null) throw new Error("missing navigation announcement");
    window.scrollTo({ top: 0, behavior: "instant" });
    for (let frame = 0; frame < 30; frame++) {
      await new Promise(requestAnimationFrame);
      const style = getComputedStyle(news);
      const opacity = Number(style.opacity);
      if (style.visibility === "visible" && opacity > 0 && opacity < 1) return true;
    }
    return false;
  });
  expect(isFadingOut, "the navbar announcement must remain rendered while fading out").toBe(true);
  await expect(navLink).not.toBeVisible();
  await heroLink.click();
  await expect(page).toHaveURL(`${origin}/blog/gamma`);
  await expect(navLink).toBeVisible();
  const revealFrames = await page.evaluate(() => window.newsRevealFrames);
  expect(
    revealFrames.some(
      ({ opacity, translate }) =>
        opacity > 0 &&
        opacity < 1 &&
        translate !== "none" &&
        translate !== "0px" &&
        translate !== "0px 0px",
    ),
    "a newly visible navbar announcement must slide up while fading in across pages",
  ).toBe(true);
  await page.locator('a[aria-label="Arkoren home"]').click();
  await expect(page).toHaveURL(`${origin}/`);
  await page.evaluate(() => document.activeViewTransition?.finished);
  await expect(navLink).not.toBeVisible();
  const exitFrames = await page.evaluate(() => window.newsRevealFrames);
  expect(
    exitFrames.some(
      ({ oldOpacity, oldTranslate }) =>
        oldOpacity > 0 &&
        oldOpacity < 1 &&
        oldTranslate !== "none" &&
        oldTranslate !== "0px" &&
        oldTranslate !== "0px 0px",
    ),
    "the navbar announcement must slide down while fading out across pages",
  ).toBe(true);

  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.evaluate(() => window.scrollTo(0, innerHeight * 2));
  await expect(navLink).toBeVisible();
  await expect(navLink).toHaveCSS("transition-duration", "0s");
  await navLink.click();
  await expect(page).toHaveURL(`${origin}/blog/gamma`);
});

test("search loads its generated index, supports keyboard navigation, and excludes drafts", async ({
  page,
}) => {
  await page.goto(origin);
  await page.keyboard.press("/");
  const dialog = page.locator("[data-search]");
  const input = page.locator("[data-search-input]");
  await expect(input).toBeFocused();
  await input.fill('"fixturehiddenonly"');
  await expect(page.locator("[data-search-status]")).not.toBeEmpty();
  await expect(page.locator("[data-search-results] a")).toHaveCount(0);
  await input.fill('"fixturequartz"');
  const result = page.locator('[data-search-results] a[href^="/blog/"]').first();
  await expect(result).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await page.locator("[data-search-open]").click();
  await expect(input).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(page.locator("[data-search-results] a").first()).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/blog\/(alpha|beta)(?:#.*)?$/);
});

test("blog controls filter, sort, restore URLs, and follow browser history", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 900 });
  await page.goto(`${origin}/blog`);
  const visible = page.locator("[data-blog-post]:visible");
  await expect(visible).toHaveCount(3);
  await page.locator('[data-blog-category="Examples"]').click();
  await expect(visible).toHaveCount(2);
  await page.locator("[data-blog-sort]").click();
  await expect(visible.first()).toHaveAttribute("data-id", "alpha");
  const input = page.locator('form[role="search"] input');
  await input.fill("Beta");
  await input.press("Enter");
  await expect(visible).toHaveCount(1);
  await expect(visible).toHaveAttribute("data-id", "beta");
  await page.reload();
  await expect(input).toHaveValue("Beta");
  await expect(visible).toHaveCount(1);
  await page.goBack();
  await expect(input).toHaveValue("");
  await expect(visible).toHaveCount(2);
  await page.locator("[data-blog-reset]").click();
  await expect(page).toHaveURL(`${origin}/blog`);
  await expect(visible).toHaveCount(3);
  const categories = page.getByRole("navigation", { name: "Post categories" });
  const category = categories.locator('[data-blog-category="Release notes and engineering"]');
  await categories.locator('[data-blog-category="Examples"]').focus();
  await page.keyboard.press("Tab");
  await expect(category).toBeFocused();
  await expect
    .poll(async () => {
      const container = required(await categories.boundingBox());
      const control = required(await category.boundingBox());
      return (
        control.x >= container.x - 1 &&
        control.x + control.width <= container.x + container.width + 1
      );
    })
    .toBe(true);
  await page.keyboard.press("Enter");
  await expect(category).toHaveAttribute("aria-current", "true");
  await expect(visible).toHaveCount(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

/** The selector must follow caller-owned ARIA state and keep the selection reachable. */
test("selectors follow the selected control and reveal it on narrow screens", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 900 });
  await page.goto(`${origin}/selector-fixture`);
  const selector = page.locator("[data-selector]");
  const tabs = selector.getByRole("tab");
  await expect(selector).toHaveAttribute("data-selector-ready", "");
  await tabs.last().click();
  await expect(tabs.last()).toHaveAttribute("aria-selected", "true");
  await expect(tabs.first()).toHaveAttribute("aria-selected", "false");
  await expect
    .poll(async () => {
      const [group, selected] = await Promise.all([
        selector.boundingBox(),
        tabs.last().boundingBox(),
      ]);
      return (
        group !== null && selected !== null && selected.x + selected.width <= group.x + group.width
      );
    })
    .toBe(true);
  await tabs.first().click();
  await expect
    .poll(async () => {
      const [group, selected] = await Promise.all([
        selector.boundingBox(),
        tabs.first().boundingBox(),
      ]);
      return group !== null && selected !== null && selected.x >= group.x;
    })
    .toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("mobile navigation reaches documentation and copy preserves snippet text", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto(origin);
  const menu = page.locator("[data-mobile-nav]");
  await menu.locator("summary").click();
  await menu.locator('a[href="/docs"]').click();
  await expect(page).toHaveURL(`${origin}/docs`);
  await page.goto(`${origin}/docs/guides/first-steps/quickstart`);
  const code = page.locator(".prose pre").first();
  const expected = await code.textContent();
  await code.locator("..").locator("button").click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(expected);
  const contents = page.locator("[data-toc-mobile]");
  await contents.locator("summary").click();
  const link = contents.locator('a[href^="#"]').last();
  const fragment = await link.getAttribute("href");
  await link.click();
  await expect(page).toHaveURL(`${origin}/docs/guides/first-steps/quickstart${fragment}`);
  await page.goto(origin);
  for (const block of await page.locator("main .command-step").all()) {
    const command = await block.locator("pre").textContent();
    await block.locator("button").click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(command);
  }
});

test("the API catalog exposes canonical module links on narrow and wide screens", async ({
  page,
}) => {
  test.skip(
    !existsSync(resolve(website, "../core/go.mod")),
    "the Go modules are not in this checkout yet",
  );
  for (const width of [320, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`${origin}/docs/api`);
    if (width <= 860) await page.locator("[data-menu-toggle]").click();
    const tree = page.getByRole("navigation", { name: "API Reference", exact: true });
    await expect(tree.locator('a[href="https://pkg.go.dev/arkoren.dev/core"]')).toBeVisible();
    const active = tree.locator('a[aria-current="page"]');
    await expect(active).toBeVisible();
    await expect(active).toHaveAttribute("href", "/docs/api");
    const reference = page.locator('main a[href="https://pkg.go.dev/arkoren.dev/driver/redis"]');
    await reference.focus();
    await expect(reference).toBeFocused();
    await page.route("https://pkg.go.dev/**", (route) =>
      route.fulfill({
        contentType: "text/html",
        body: "<h1>Canonical reference fixture</h1>",
      }),
    );
    await reference.press("Enter");
    await expect(page).toHaveURL("https://pkg.go.dev/arkoren.dev/driver/redis");
    await page.goBack();
    if (width > 860) await expect(active).toBeInViewport();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  }
});

test("documentation areas, article breadcrumbs, and release metadata stay discoverable", async ({
  page,
}) => {
  for (const width of [320, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`${origin}/docs/guides/first-steps/quickstart`);
    const navigation = page.getByRole("navigation", {
      name: "Documentation navigation",
      exact: true,
    });
    const breadcrumbs = page
      .locator("main")
      .getByRole("navigation", { name: "Breadcrumb", exact: true });
    await expect(navigation).toBeVisible();
    await expect(breadcrumbs).toBeVisible();
    const title = required(await page.locator("main h1").boundingBox());
    const trail = required(await breadcrumbs.boundingBox());
    expect(trail.y + trail.height).toBeLessThanOrEqual(title.y);
    await expect(page.locator("[data-version]:visible time[datetime]")).toBeVisible();
    await expect(page.locator('[data-doc-panel="api"]')).toBeHidden();
    await breadcrumbs.getByRole("link", { name: "Home", exact: true }).click();
    await expect(page).toHaveURL(`${origin}/`);
    await page.goto(`${origin}/docs/guides/first-steps/quickstart`);
    if (width > 860) {
      await page.evaluate(() => window.scrollTo({ top: 600, behavior: "instant" }));
      await expect(navigation).toBeVisible();
    } else {
      await page.locator("[data-menu-toggle]").click();
    }
    const reading = await page.locator("main").evaluate((article) => {
      article.dataset.readerState = "preserved";
      const title = article.querySelector("h1")?.textContent;
      if (title == null) throw new Error("missing article heading");
      return { title, scroll: scrollY, url: location.href };
    });
    const guides = navigation.getByRole("button", { name: "Guides", exact: true });
    const api = navigation.getByRole("button", { name: "API Reference", exact: true });
    await api.click();
    await expect(page).toHaveURL(reading.url);
    await expect(page.locator("main")).toHaveAttribute("data-reader-state", "preserved");
    await expect(page.locator("main h1")).toHaveText(reading.title);
    await expect(api).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator('[data-doc-panel="api"]')).toBeVisible();
    await expect(page.locator('[data-doc-panel="guides"]')).toBeHidden();
    if (width > 860) expect(await page.evaluate(() => scrollY)).toBe(reading.scroll);
    await guides.click();
    await expect(page).toHaveURL(reading.url);
    await expect(guides).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator('[data-doc-panel="guides"]')).toBeVisible();
    await expect(page.locator('[data-doc-panel="api"]')).toBeHidden();
    await expect(page.locator("main")).toHaveAttribute("data-reader-state", "preserved");
    await api.click();
    const app = page.locator('[data-doc-panel="api"] a[href="/docs/api"]');
    await app.click();
    await expect(page).toHaveURL(`${origin}/docs/api`);
    await expect(api).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator('[data-doc-panel="guides"]')).toBeHidden();
    await guides.click();
    await expect(page).toHaveURL(`${origin}/docs/api`);
    if (width <= 860) await page.locator("[data-menu-toggle]").click();
    const overview = page.locator('[data-doc-panel="guides"] a[href="/docs"]');
    if (!(await overview.isVisible())) {
      await page
        .locator('[data-doc-panel="guides"] details')
        .filter({
          has: page.locator('a[href="/docs"]'),
        })
        .locator(":scope > summary")
        .click();
    }
    await overview.click();
    await expect(page).toHaveURL(`${origin}/docs`);
    await expect(navigation.getByRole("link", { name: "Agent docs" })).toHaveCount(0);
    await expect(page.locator("[data-version]:visible time[datetime]")).toBeVisible();
    await page.goto(origin);
    await expect(navigation).toHaveCount(0);
  }
});

test("onboarding is reachable from the homepage and docs at narrow and wide widths", async ({
  page,
}) => {
  for (const width of [320, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(origin);
    await page.locator('main a[href="#agents"]').click();
    await expect(page).toHaveURL(`${origin}/#agents`);
    const agentSetup = page.locator(
      '#agents a[href="/docs/guides/agent-skills/working-with-agents"]',
    );
    await expect(agentSetup).toBeVisible();
    await agentSetup.click();
    await expect(page).toHaveURL(`${origin}/docs/guides/agent-skills/working-with-agents`);
    await expect(page.locator("main h1")).toBeVisible();
    await page.goto(origin);
    const start = page.locator('main a[href="/docs/guides/first-steps/existing-project"]').first();
    await expect(start).toBeVisible();
    await start.click();
    await expect(page).toHaveURL(`${origin}/docs/guides/first-steps/existing-project`);
    await expect(page.locator("main h1")).toBeVisible();
    await expect(page.locator(".prose pre").first()).toBeVisible();
    for (const route of ["quickstart", "existing-project"]) {
      await page.goto(`${origin}/docs`);
      const next = page.locator(`#first-steps a[href="/docs/guides/first-steps/${route}"]`);
      await expect(next).toBeVisible();
      await next.click();
      await expect(page).toHaveURL(`${origin}/docs/guides/first-steps/${route}`);
      await expect(page.locator("main h1")).toBeVisible();
    }
  }
});

test("layouts contain overflow and respect reduced motion in both color schemes", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const width of [320, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      for (const route of [
        "/",
        "/blog",
        "/docs/guides/first-steps/quickstart",
        "/docs/guides/first-steps/existing-project",
      ]) {
        await page.goto(`${origin}${route}`);
        await page.evaluate(() => document.fonts.ready);
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
          route,
        ).toBe(true);
        const moving = await page.evaluate(
          () =>
            document.getAnimations().filter((animation) => {
              const timing = animation.effect?.getComputedTiming();
              return animation.playState === "running" && timing?.iterations === Infinity;
            }).length,
        );
        expect(moving, `${route}: continuous decorative motion`).toBe(0);
      }
    }
  }
});

test("season changes interpolate page surfaces and accents in both directions", async ({
  page,
}) => {
  await page.addInitScript(() =>
    localStorage.setItem(
      "arkoren-season",
      JSON.stringify({ year: new Date().getFullYear(), enabled: false }),
    ),
  );
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme, reducedMotion: "no-preference" });
    await page.goto(origin);
    await expect(page.locator("html")).toHaveAttribute("data-season-ready", "");
    for (const direction of ["halloween", "normal"]) {
      const samples = await page.evaluate(() => {
        const targets = [
          ["[data-home-hero]", "background-color"],
          ["#toolkit article", "background-color"],
          ["#start", "background-color"],
          ["#principles .eyebrow", "color"],
          ["[data-step-number]", "border-left-color"],
          ["#build aside", "background-color"],
          ["#principles li svg", "color"],
        ].map(([selector, property]) => {
          const element = document.querySelector(selector);
          if (element === null) throw new Error(`missing seasonal surface: ${selector}`);
          return {
            selector,
            property,
            element,
            before: getComputedStyle(element).getPropertyValue(property),
          };
        });
        const toggle = document.querySelector<HTMLButtonElement>(
          "[data-header-actions] [data-season-toggle]",
        );
        if (toggle === null) throw new Error("missing seasonal toggle");
        toggle.click();
        return targets.map(({ selector, property, element, before }) => {
          getComputedStyle(element).getPropertyValue(property);
          const animation = element
            .getAnimations()
            .find((item) => item instanceof CSSTransition && item.transitionProperty === property);
          if (!animation) return { selector, property, animated: false };
          animation.pause();
          if (animation.effect === null) throw new Error("transition has no effect");
          animation.currentTime = Number(animation.effect.getTiming().duration) / 2;
          const middle = getComputedStyle(element).getPropertyValue(property);
          animation.finish();
          const after = getComputedStyle(element).getPropertyValue(property);
          return { selector, property, animated: true, before, middle, after };
        });
      });
      for (const sample of samples) {
        expect(
          sample.animated,
          `${colorScheme}/${direction}: ${sample.selector} ${sample.property}`,
        ).toBe(true);
        expect(sample.middle).not.toBe(sample.before);
        expect(sample.middle).not.toBe(sample.after);
      }
      await page.evaluate(() =>
        document
          .getAnimations()
          .filter((item) => item instanceof CSSTransition)
          .forEach((item) => item.finish()),
      );
    }
  }
  await page.emulateMedia({ reducedMotion: "reduce" });
  const immediate = await page.evaluate(() => {
    const surfaces = [
      ...document.querySelectorAll("[data-home-hero], #toolkit article:first-of-type, #start"),
    ];
    const before = surfaces.map((element) => getComputedStyle(element).backgroundColor);
    const toggle = document.querySelector<HTMLButtonElement>(
      "[data-header-actions] [data-season-toggle]",
    );
    if (toggle === null) throw new Error("missing seasonal toggle");
    toggle.click();
    return surfaces.map((element, index) => ({
      before: before[index],
      after: getComputedStyle(element).backgroundColor,
      transitions: element.getAnimations().filter((item) => item instanceof CSSTransition).length,
    }));
  });
  for (const surface of immediate) {
    expect(surface.after).not.toBe(surface.before);
    expect(surface.transitions).toBe(0);
  }
});

/** New vector assets must work without a template edit; raster assets still load normally. */
test("blog banners discover new local SVGs and retain raster rendering", async ({ page }) => {
  await page.goto(`${origin}/blog/beta`);
  const vector = page.getByRole("img", { name: "Second vector banner" });
  await expect(vector).toBeVisible();
  expect(await vector.evaluate((element) => element.tagName.toLowerCase())).toBe("svg");
  await page.goto(`${origin}/blog/gamma`);
  const raster = page.getByRole("img", { name: "Raster banner" });
  await expect(raster).toBeVisible();
  expect(
    await raster.evaluate(
      (element) =>
        element instanceof HTMLImageElement && element.complete && element.naturalWidth > 0,
    ),
  ).toBe(true);
});

/** The inline banner must follow the toggle without snapping or adding motion when disabled. */
test("blog banner colors transition with the season and respect reduced motion", async ({
  page,
}) => {
  await page.addInitScript(() =>
    localStorage.setItem(
      "arkoren-season",
      JSON.stringify({ year: new Date().getFullYear(), enabled: false }),
    ),
  );
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.goto(`${origin}/blog/alpha`);
  await expect(page.getByRole("img", { name: "Fixture banner" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("data-season-ready", "");
  for (const direction of ["halloween", "normal"]) {
    const samples = await page.evaluate(() => {
      const stops = [...document.querySelectorAll("[data-banner] stop")];
      const before = stops.map((stop) => getComputedStyle(stop).stopColor);
      const toggle = document.querySelector<HTMLButtonElement>(
        "[data-header-actions] [data-season-toggle]",
      );
      if (toggle === null) throw new Error("missing seasonal toggle");
      toggle.click();
      return stops.map((stop, index) => {
        getComputedStyle(stop).stopColor;
        const animation = stop
          .getAnimations()
          .find(
            (item) => item instanceof CSSTransition && item.transitionProperty === "stop-color",
          );
        if (!animation) return { animated: false };
        animation.pause();
        if (animation.effect === null) throw new Error("transition has no effect");
        animation.currentTime = Number(animation.effect.getTiming().duration) / 2;
        const middle = getComputedStyle(stop).stopColor;
        animation.finish();
        return {
          animated: true,
          before: before[index],
          middle,
          after: getComputedStyle(stop).stopColor,
        };
      });
    });
    expect(samples.length).toBeGreaterThan(0);
    for (const sample of samples) {
      expect(sample.animated, direction).toBe(true);
      expect(sample.middle).not.toBe(sample.before);
      expect(sample.middle).not.toBe(sample.after);
    }
  }
  await page.emulateMedia({ reducedMotion: "reduce" });
  const samples = await page.evaluate(() => {
    const stops = [...document.querySelectorAll("[data-banner] stop")];
    const before = stops.map((stop) => getComputedStyle(stop).stopColor);
    const toggle = document.querySelector<HTMLButtonElement>(
      "[data-header-actions] [data-season-toggle]",
    );
    if (toggle === null) throw new Error("missing seasonal toggle");
    toggle.click();
    return stops.map((stop, index) => ({
      before: before[index],
      after: getComputedStyle(stop).stopColor,
      transitions: stop.getAnimations().filter((item) => item instanceof CSSTransition).length,
    }));
  });
  for (const sample of samples) {
    expect(sample.after).not.toBe(sample.before);
    expect(sample.transitions).toBe(0);
  }
});

/** Dev asset URLs differ from bundled URLs and must still resolve to inline artwork. */
test("development blog banners remain inline and follow the seasonal toggle", async ({ page }) => {
  const previousBlog = process.env.ARKOREN_BLOG;
  process.env.ARKOREN_BLOG = resolve(directory, "posts");
  let development;
  try {
    development = await dev({
      root: resolve(directory, "site"),
      logLevel: "silent",
      server: { host: "127.0.0.1", port: 0 },
    });
    await page.addInitScript(() =>
      localStorage.setItem(
        "arkoren-season",
        JSON.stringify({ year: new Date().getFullYear(), enabled: false }),
      ),
    );
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.goto(`http://127.0.0.1:${development.address.port}/blog/alpha`);
    const banner = page.getByRole("img", { name: "Fixture banner" });
    await expect(banner).toBeVisible();
    expect(await banner.evaluate((element) => element.tagName.toLowerCase())).toBe("svg");
    const stop = banner.locator("stop").first();
    const normal = await stop.evaluate((element) => getComputedStyle(element).stopColor);
    await page.locator("[data-header-actions] [data-season-toggle]").click();
    await expect
      .poll(() => stop.evaluate((element) => getComputedStyle(element).stopColor))
      .not.toBe(normal);
    await page.locator("[data-header-actions] [data-season-toggle]").click();
    await expect
      .poll(() => stop.evaluate((element) => getComputedStyle(element).stopColor))
      .toBe(normal);
  } finally {
    await development?.stop();
    if (previousBlog === undefined) delete process.env.ARKOREN_BLOG;
    else process.env.ARKOREN_BLOG = previousBlog;
  }
});

test("the workbench cards support keyboard inspection and keep their links available", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${origin}/`);
  const bench = page.locator("[data-bench]");
  const controls = bench.getByRole("complementary", { name: "Workbench parts" });
  const stage = bench.locator("[data-bench-stage]");
  await stage.focus();
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("+");
  const azimuth = await bench.getAttribute("data-camera-azimuth");
  const zoom = await bench.getAttribute("data-camera-zoom");
  await controls.locator('[data-bench-part="foundation"]').focus();
  await page.keyboard.press("ArrowRight");
  await expect(controls.locator('[data-bench-part="core"]')).toBeFocused();
  await expect(controls.locator('[data-bench-part="core"]')).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  for (const card of await controls.locator("[data-bench-card]").all()) {
    await expect(card).toBeVisible();
    await expect(card.getByRole("link")).toBeVisible();
  }
  await controls.locator('[data-bench-part="app"]').click();
  await expect(bench).toHaveAttribute("data-camera-azimuth", azimuth ?? "");
  await expect(bench).toHaveAttribute("data-camera-zoom", zoom ?? "");
  await expect(bench.locator('[data-bench-card="app"]')).toHaveAttribute("data-active", "");
  await controls.locator('[data-bench-card="foundation"]').hover();
  await expect(bench.locator('[data-bench-card="foundation"]')).toHaveAttribute("data-active", "");
  await expect(bench.locator('[data-assembly="foundation"]').first()).toHaveAttribute(
    "data-active",
    "",
  );
  await expect(bench.locator('[data-bench-card="app"] a')).toHaveAttribute(
    "href",
    "/docs/guides/application/composition",
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await bench.locator('[data-bench-card="app"] a').click();
  await expect(page).toHaveURL(/\/docs\/guides\/application\/composition/);
});

test.describe("workbench surface inspection", () => {
  test("resizing preserves the drawing fit and stacked views allow page scrolling", async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`${origin}/`);
    const bench = page.locator("[data-bench]");
    const stage = bench.locator("[data-bench-stage]");
    await stage.scrollIntoViewIfNeeded();
    await expect(bench).toHaveAttribute("data-renderer", "webgl");
    const initial = await stage.boundingBox();
    if (initial === null) throw new Error("Workbench stage is not visible");
    await page.setViewportSize({ width: 1000, height: 1000 });
    await stage.scrollIntoViewIfNeeded();
    await page.setViewportSize({ width: 390, height: 844 });
    await stage.scrollIntoViewIfNeeded();
    await page.setViewportSize({ width: 1440, height: 1000 });
    await stage.scrollIntoViewIfNeeded();
    await expect
      .poll(async () => {
        const restored = await stage.boundingBox();
        return restored === null ? Infinity : Math.abs(restored.height - initial.height);
      })
      .toBeLessThan(2);
    for (const width of [1000, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await stage.scrollIntoViewIfNeeded();
      await stage.hover();
      const scrollAt = await page.evaluate(() => scrollY);
      const zoom = await bench.getAttribute("data-camera-zoom");
      await page.mouse.wheel(0, 300);
      await expect.poll(() => page.evaluate(() => scrollY)).toBeGreaterThan(scrollAt);
      await expect(bench).toHaveAttribute("data-camera-zoom", zoom ?? "");
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    await stage.scrollIntoViewIfNeeded();
    await stage.hover();
    await page.mouse.wheel(0, -160);
    await expect
      .poll(async () => Number(await bench.getAttribute("data-camera-zoom")))
      .toBeGreaterThan(1);
  });

  test("dragging right reverses orbit and wheel zoom stays bounded without scrolling the page", async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.goto(`${origin}/`);
    const bench = page.locator("[data-bench]");
    const stage = bench.locator("[data-bench-stage]");
    await stage.scrollIntoViewIfNeeded();
    await expect(bench).toHaveAttribute("data-renderer", "webgl");
    const bounds = await stage.boundingBox();
    if (bounds === null) throw new Error("Workbench stage is not visible");
    const azimuth = Number(await bench.getAttribute("data-camera-azimuth"));
    await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
    await page.mouse.down();
    await page.mouse.move(bounds.x + bounds.width / 2 + 60, bounds.y + bounds.height / 2, {
      steps: 6,
    });
    await page.mouse.up();
    expect(Number(await bench.getAttribute("data-camera-azimuth"))).toBeLessThan(azimuth);
    const scrollAt = await page.evaluate(() => scrollY);
    for (let index = 0; index < 6; index++) await page.mouse.wheel(0, -400);
    await expect(bench).toHaveAttribute("data-camera-zoom", "1.800");
    await page.mouse.wheel(0, -400);
    await expect(bench).toHaveAttribute("data-camera-zoom", "1.800");
    for (let index = 0; index < 8; index++) await page.mouse.wheel(0, 400);
    await expect(bench).toHaveAttribute("data-camera-zoom", "0.850");
    await page.mouse.wheel(0, 400);
    await expect(bench).toHaveAttribute("data-camera-zoom", "0.850");
    expect(await page.evaluate(() => scrollY)).toBe(scrollAt);
    await stage.focus();
    await page.keyboard.press("+");
    await expect
      .poll(async () => Number(await bench.getAttribute("data-camera-zoom")))
      .toBeGreaterThan(0.85);
    await page.keyboard.press("Home");
    await expect(bench).toHaveAttribute("data-camera-zoom", "1.000");
  });

  test("clicking a hovered surface restores the inspection camera", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.goto(`${origin}/`);
    const bench = page.locator("[data-bench]");
    await bench.scrollIntoViewIfNeeded();
    await expect(bench).toHaveAttribute("data-renderer", "webgl");
    const stage = bench.locator("[data-bench-stage]");
    await stage.focus();
    await page.keyboard.press("Home");
    const home = await bench.getAttribute("data-camera-azimuth");
    await page.keyboard.press("ArrowRight");
    await expect(bench).not.toHaveAttribute("data-camera-azimuth", home ?? "");
    const bounds = await stage.boundingBox();
    if (bounds === null) throw new Error("Workbench stage is not visible");
    let hasClicked = false;
    for (let row = 2; row < 9 && !hasClicked; row++) {
      for (let column = 2; column < 9 && !hasClicked; column++) {
        const x = bounds.x + (bounds.width * column) / 10;
        const y = bounds.y + (bounds.height * row) / 10;
        await page.mouse.move(x, y);
        if (
          (await bench.locator('[data-bench-part="core"]').getAttribute("aria-pressed")) !== "true"
        )
          continue;
        await page.mouse.click(x, y);
        hasClicked = true;
      }
    }
    expect(hasClicked).toBe(true);
    await expect(bench).toHaveAttribute("data-camera-azimuth", home ?? "");
    await expect(bench.locator('[data-bench-card="core"]')).toHaveAttribute("data-active", "");
  });
});

test("agents and the Go tool can fetch their discovery formats", async ({ request }) => {
  for (const route of ["/llms.txt", "/docs/guides/first-steps/quickstart.md"]) {
    const response = await request.get(`${origin}${route}`);
    expect(response.status()).toBe(200);
    expect(response.headers()["content-type"]).not.toContain("text/html");
    expect(await response.text()).not.toContain("<!DOCTYPE html>");
  }
  // Before the Go modules land, import paths fall back to the 404 page. Go still
  // reads its go-import tag, so module discovery works from either response.
  const status = existsSync(resolve(website, "../core/go.mod")) ? 200 : 404;
  for (const route of ["/core", "/driver/redis/rediscache", "/foundation/query"]) {
    const response = await request.get(`${origin}${route}?go-get=1`);
    expect(response.status()).toBe(status);
    expect(await response.text()).toMatch(
      /name="go-import" content="arkoren\.dev git https:\/\/github\.com\/arkoren\/arkoren"/,
    );
  }
});
