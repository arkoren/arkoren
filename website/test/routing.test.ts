import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { preview } from "astro";
import { Window } from "happy-dom";
import { browserWindow, required } from "./dom.ts";

test("the changelog renders with active desktop and mobile navigation and release links", () => {
  const window = new Window();
  try {
    window.document.write(readFileSync(new URL("../dist/changelog.html", import.meta.url), "utf8"));
    const document = browserWindow(window).document;
    assert.equal(document.querySelectorAll("main").length, 1);
    assert.equal(document.querySelectorAll("h1").length, 1);
    for (const label of ["Primary", "Mobile primary"]) {
      const nav = document.querySelector(`nav[aria-label="${label}"]`);
      assert.ok(nav);
      const active = nav.querySelectorAll('[aria-current="page"]');
      assert.equal(active.length, 1);
      assert.equal(active[0].getAttribute("href"), "/changelog");
    }
    const history = required(document.querySelector("main section[aria-labelledby]"));
    assert.ok(document.getElementById(required(history.getAttribute("aria-labelledby"))));
    assert.ok(document.querySelector('main a[href="https://github.com/arkoren/arkoren/releases"]'));
    assert.ok(document.querySelector('main a[href="/docs"]'));
    assert.ok(document.querySelector('main a[href="/blog"]'));
    assert.ok(
      required(document.querySelector<HTMLLinkElement>('link[rel="canonical"]')).href.endsWith(
        "/changelog",
      ),
    );
  } finally {
    window.close();
  }
});

test("the shared navbar links to the changelog without marking it current on other pages", () => {
  for (const page of ["index.html", "blog.html", "docs.html"]) {
    const window = new Window();
    try {
      window.document.write(readFileSync(new URL(`../dist/${page}`, import.meta.url), "utf8"));
      for (const label of ["Primary", "Mobile primary"]) {
        const link = window.document.querySelector(
          `nav[aria-label="${label}"] a[href="/changelog"]`,
        );
        assert.ok(link, `${page}: ${label}`);
        assert.equal(link.hasAttribute("aria-current"), false);
      }
    } finally {
      window.close();
    }
  }
});

test("only the homepage renders the navbar announcement hidden before scripts run", () => {
  for (const [page, isHidden] of [
    ["index.html", true],
    ["blog.html", false],
    ["docs.html", false],
  ] as const) {
    const window = new Window();
    try {
      window.document.write(readFileSync(new URL(`../dist/${page}`, import.meta.url), "utf8"));
      const news = required(window.document.querySelector("[data-news]"));
      assert.equal(news.hasAttribute("data-hero-visible"), isHidden, page);
      assert.equal(news.hasAttribute("inert"), isHidden, page);
    } finally {
      window.close();
    }
  }
});

test("the former app HTML API route redirects to application", () => {
  const html = readFileSync(new URL("../dist/docs/api/core/app.html", import.meta.url), "utf8");
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=https:\/\/pkg.go.dev\/arkoren.dev\/core\/application">/,
  );
});

test("the retired app Markdown URL redirects to canonical package docs", () => {
  const html = readFileSync(new URL("../dist/docs/api/core/app.md.html", import.meta.url), "utf8");
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=https:\/\/pkg.go.dev\/arkoren.dev\/core\/application">/,
  );
});

test("retired API exports lead to discovery and testing concepts lead to guides", () => {
  const archive = readFileSync(
    new URL("../dist/docs/api/llms-full.txt.html", import.meta.url),
    "utf8",
  );
  assert.match(archive, /content="0;url=\/docs\/api\/llms\.txt"/);
  const testing = readFileSync(
    new URL("../dist/docs/api/core/testing.html", import.meta.url),
    "utf8",
  );
  assert.match(testing, /content="0;url=\/docs\/guides\/testing\/overview"/);
});

test("former HTTP fundamentals routes retain HTML and Markdown redirects", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/routing.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-fundamentals\/routing">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/http/routing.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-fundamentals\/routing\.md">/,
  );
});

test("former HTTP capabilities routes retain HTML and Markdown redirects", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/sessions.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/sessions">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/http/sessions.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/sessions\.md">/,
  );
});

test("former standalone webhook routes redirect back to capabilities", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/webhooks/receiving.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/webhooks">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/webhooks/receiving.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/webhooks\.md">/,
  );
});

test("the older webhook alias redirects directly to its current destination", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/webhooks.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/webhooks">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/http/webhooks.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/webhooks\.md">/,
  );
});

test("removed standalone provider pages redirect to the shared adapter links", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/webhooks/providers.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/webhooks#provider-adapters">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/webhooks/providers.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/webhooks\.md#provider-adapters">/,
  );
});

test("removed payment webhook pages redirect to payment drivers", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/payments/webhooks.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/payments\/drivers">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/payments/webhooks.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/payments\/drivers\.md">/,
  );
});

test("former HTTP networking routes retain HTML and Markdown redirects", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/server.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-networking\/server">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/http/server.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-networking\/server\.md">/,
  );
});

test("the older signed-links alias redirects directly to its current destination", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/signed-links.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/secure-links">/,
  );
  const markdown = readFileSync(
    new URL("../dist/docs/guides/http/signed-links.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    markdown,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/http-capabilities\/secure-links\.md">/,
  );
});

test("the former HTTP agent index redirects to the complete guide index", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/llms.txt.html", import.meta.url),
    "utf8",
  );
  assert.match(html, /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/llms\.txt">/);
});

test("the former OpenAPI HTML route redirects to its new overview", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/openapi.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/openapi\/overview">/,
  );
});

test("the former OpenAPI Markdown route redirects to raw Markdown", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/http/openapi.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/openapi\/overview\.md">/,
  );
});

test("the former AsyncAPI HTML route redirects to its new overview", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/events/asyncapi.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/asyncapi\/overview">/,
  );
});

test("the former AsyncAPI Markdown route redirects to raw Markdown", () => {
  const html = readFileSync(
    new URL("../dist/docs/guides/events/asyncapi.md.html", import.meta.url),
    "utf8",
  );
  assert.match(
    html,
    /<meta http-equiv="refresh" content="0;url=\/docs\/guides\/asyncapi\/overview\.md">/,
  );
});

/** Exercise Astro's real static fallback rather than a test-only file server. */
test("missing routes share the custom error page without redirects or successful status codes", async (t) => {
  const server = await preview({
    root: fileURLToPath(new URL("../", import.meta.url)),
    logLevel: "silent",
    server: { host: "127.0.0.1", port: 0 },
  });
  t.after(() => server.stop());
  const origin = `http://127.0.0.1:${server.port}`;
  const expected = readFileSync(new URL("../dist/404.html", import.meta.url), "utf8");

  await t.test("an unknown top-level URL returns the error page", async () => {
    const response = await fetch(`${origin}/__missing-page__`, { redirect: "manual" });
    assert.equal(response.status, 404);
    assert.equal(response.headers.get("location"), null);
    assert.equal(await response.text(), expected);
  });

  await t.test("an unknown generated guide returns the error page", async () => {
    const response = await fetch(`${origin}/docs/guides/first-steps/__missing-page__`);
    assert.equal(response.status, 404);
    assert.equal(response.redirected, false);
    assert.equal(await response.text(), expected);
  });

  await t.test("an unknown generated API page returns the error page", async () => {
    const response = await fetch(`${origin}/docs/api/core/service/__missing-page__`);
    assert.equal(response.status, 404);
    assert.equal(response.redirected, false);
    assert.equal(await response.text(), expected);
  });

  await t.test("the API catalog is a canonical successful page", async () => {
    const response = await fetch(`${origin}/docs/api`, { redirect: "manual" });
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("location"), null);
    const html = await response.text();
    if (existsSync(new URL("../../core/go.mod", import.meta.url))) {
      assert.match(html, /https:\/\/pkg.go.dev\/arkoren.dev\/core/);
    }
  });

  await t.test("an unknown raw Markdown URL returns a genuine 404", async () => {
    const response = await fetch(`${origin}/docs/api/core/service/__missing-page__.md`);
    assert.equal(response.status, 404);
    assert.equal(await response.text(), expected);
  });

  await t.test("an unknown generated import path returns the error page", async () => {
    const response = await fetch(`${origin}/core/service/__missing-package__?go-get=1`);
    assert.equal(response.status, 404);
    assert.equal(await response.text(), expected);
  });

  await t.test("an unknown blog post returns the error page", async () => {
    const response = await fetch(`${origin}/blog/__missing-post__`);
    assert.equal(response.status, 404);
    assert.equal(await response.text(), expected);
  });

  await t.test("recovery links resolve to successful pages", async () => {
    const window = new Window();
    try {
      window.document.write(expected);
      const main = window.document.querySelector("main");
      assert.ok(main);
      const links = [...main.querySelectorAll("a")];
      assert.ok(links.length > 0);
      for (const link of links) {
        assert.ok(link.textContent.trim());
        const href = required(link.getAttribute("href"));
        const response = await fetch(new URL(href, origin));
        assert.equal(response.status, 200, href);
        await response.arrayBuffer();
      }
    } finally {
      window.close();
    }
  });
});

test("the error page retains accessible navigation and search but is not indexed", () => {
  const window = new Window();
  try {
    window.document.write(readFileSync(new URL("../dist/404.html", import.meta.url), "utf8"));
    const document = browserWindow(window).document;
    assert.equal(document.querySelectorAll("main").length, 1);
    assert.equal(document.querySelectorAll("h1").length, 1);
    const section = required(document.querySelector("main section"));
    assert.equal(
      document.getElementById(required(section.getAttribute("aria-labelledby"))),
      document.querySelector("h1"),
    );
    assert.ok(document.querySelector('nav[aria-label="Primary"]'));
    assert.ok(document.querySelector("[data-search-open]"));
    assert.ok(document.querySelector("dialog[data-search][aria-label]"));
    assert.equal(document.querySelectorAll('meta[name="robots"]').length, 1);
    assert.equal(
      required(document.querySelector<HTMLMetaElement>('meta[name="robots"]')).content,
      "noindex",
    );
    assert.ok(document.querySelector("main[data-pagefind-ignore]"));
    assert.equal(
      required(document.querySelector("main svg"))
        .closest('[aria-hidden="true"]')
        ?.getAttribute("aria-hidden"),
      "true",
    );
  } finally {
    window.close();
  }
});

/**
 * Each public homepage route resolves without a redirect or fallback response.
 */
test("homepage routes resolve successfully with their own canonical URLs", async (t) => {
  const server = await preview({
    root: fileURLToPath(new URL("../", import.meta.url)),
    logLevel: "silent",
    server: { host: "127.0.0.1", port: 0 },
  });
  t.after(() => server.stop());
  const origin = `http://127.0.0.1:${server.port}`;

  for (const route of ["/"]) {
    const response = await fetch(`${origin}${route}`, { redirect: "manual" });
    assert.equal(response.status, 200, route);
    const html = await response.text();
    const window = new Window();
    try {
      window.document.write(html);
      const canonical = required(window.document.querySelector('link[rel="canonical"]'));
      assert.equal(new URL(required(canonical.getAttribute("href"))).pathname, route);
    } finally {
      window.close();
    }
  }
  for (const route of ["/2", "/3", "/4", "/5", "/6", "/7", "/8"]) {
    const response = await fetch(`${origin}${route}`, { redirect: "manual" });
    assert.equal(response.status, 404, route);
    await response.arrayBuffer();
  }
});

/**
 * Preserve shared navigation, meaningful landmarks, and keyboard access across
 * designs without testing their wording, topics, or number of examples.
 */
test("the homepage retains accessible document structure", () => {
  for (const route of ["index"]) {
    const window = new Window();
    try {
      window.document.write(
        readFileSync(new URL(`../dist/${route}.html`, import.meta.url), "utf8"),
      );
      const document = browserWindow(window).document;
      assert.equal(document.querySelectorAll("main").length, 1, `/${route}`);
      assert.equal(document.querySelectorAll("h1").length, 1, `/${route}`);
      assert.ok(document.querySelector('nav[aria-label="Primary"]'));
      assert.ok(document.querySelector('nav[aria-label="Mobile primary"]'));
      assert.ok(document.querySelector("footer"));
      assert.ok(document.querySelector("[data-search-open]"));
      assert.equal(
        new URL(required(document.querySelector<HTMLLinkElement>('link[rel="canonical"]')).href)
          .pathname,
        "/",
      );

      const levels = [...document.querySelectorAll("main :is(h1, h2, h3, h4, h5, h6)")].map(
        (heading) => Number(heading.tagName.slice(1)),
      );
      for (let index = 1; index < levels.length; index++) {
        assert.ok(
          levels[index] <= levels[index - 1] + 1,
          `/${route}: heading levels must not skip`,
        );
      }
      for (const section of document.querySelectorAll("main section")) {
        const heading = document.getElementById(required(section.getAttribute("aria-labelledby")));
        assert.ok(heading, `/${route}: sections must reference an existing heading`);
        assert.ok(section.contains(heading));
      }
      for (const block of document.querySelectorAll<HTMLPreElement>("main pre")) {
        assert.equal(block.tabIndex, 0);
        assert.equal(block.getAttribute("role"), "region");
        assert.ok(block.getAttribute("aria-label"));
      }
    } finally {
      window.close();
    }
  }
});
