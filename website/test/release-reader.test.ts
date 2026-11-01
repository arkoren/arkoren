import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { Window } from "happy-dom";
import { required } from "./dom.ts";
import {
  readReleases,
  releaseLinks,
  releaseModules,
  renderReleaseBody,
  type ReleaseData,
} from "../src/lib/releases/content.ts";
import { releaseCommand, releaseDate, releaseHistory } from "../src/lib/releases/history.ts";

/** Controlled metadata exercises ordering independently of real publications. */
function facts(overrides: Partial<ReleaseData> = {}): ReleaseData {
  return {
    module: "arkoren.dev/core",
    version: "v0.2.0",
    publishedAt: "2026-11-01T23:59:59Z",
    commit: "a".repeat(40),
    prerelease: true,
    ...overrides,
  };
}

test("history groups UTC days newest first, then publication time and stable identity", () => {
  const entries = [
    { data: facts({ module: "arkoren.dev/driver/redis" }) },
    { data: facts({ publishedAt: "2026-11-02T00:00:00Z" }) },
    { data: facts({ publishedAt: "2026-11-01T00:00:00Z" }) },
    { data: facts() },
  ];
  const groups = releaseHistory(entries);
  assert.deepEqual(
    groups.map((group) => group.day),
    ["2026-11-02", "2026-11-01"],
  );
  assert.deepEqual(
    groups[1].releases.map((entry) => entry.data.module),
    ["arkoren.dev/core", "arkoren.dev/driver/redis", "arkoren.dev/core"],
  );
  assert.equal(entries[0].data.module, "arkoren.dev/driver/redis");
  assert.equal(releaseDate("2026-11-02T00:00:00Z"), "November 2, 2026");
});

test("empty history has no invented publication", () => {
  assert.deepEqual(releaseHistory([]), []);
});

test("installation pins library and CLI module versions without Git tag prefixes", () => {
  assert.equal(releaseCommand(facts()), "go get arkoren.dev/core@v0.2.0");
  assert.equal(
    releaseCommand(facts({ module: "arkoren.dev/driver/redis" })),
    "go get arkoren.dev/driver/redis@v0.2.0",
  );
  assert.equal(
    releaseCommand(facts({ module: "arkoren.dev/cmd/arkoren" })),
    "go install arkoren.dev/cmd/arkoren@v0.2.0",
  );
});

test("relative links and references use the module source commit, not website routes", () => {
  const window = new Window();
  try {
    window.document.body.innerHTML = renderReleaseBody(
      "## Breaking changes\n\n[Local](README.md) [Parent][p] [Root](/LICENSE) [Section](#breaking-changes)\n\n[p]: ../../core/README.md\n",
      facts({ module: "arkoren.dev/driver/redis" }),
    );
    const links = [...window.document.querySelectorAll("a")].map((link) =>
      link.getAttribute("href"),
    );
    assert.ok(
      links.includes(
        `https://github.com/arkoren/arkoren/blob/${"a".repeat(40)}/driver/redis/README.md`,
      ),
    );
    assert.ok(
      links.includes(`https://github.com/arkoren/arkoren/blob/${"a".repeat(40)}/core/README.md`),
    );
    assert.ok(links.includes(`https://github.com/arkoren/arkoren/blob/${"a".repeat(40)}/LICENSE`));
    assert.ok(links.includes("#release-breaking-changes"));
    assert.ok(window.document.getElementById("release-breaking-changes"));
  } finally {
    window.happyDOM.abort();
  }
});

test("source-context rendering retains the unsafe content gate and rejects ambiguous paths", () => {
  assert.throws(
    () => renderReleaseBody("[unsafe](javascript&#58;alert%281%29)", facts()),
    /unsafe URL/,
  );
  assert.throws(() => renderReleaseBody('<img src="x" onerror="bad()">', facts()), /raw HTML/);
  assert.throws(() => renderReleaseBody("[ambiguous](?tab=readme)", facts()), /explicit HTTPS/);
  assert.throws(
    () => renderReleaseBody("[outside](../../README.md)", facts()),
    /escapes the repository/,
  );
  assert.throws(
    () => renderReleaseBody("[missing](#unknown)", facts()),
    /unknown release heading fragment/,
  );
  assert.throws(() => renderReleaseBody("[ambiguous](..%2fREADME.md)", facts()), /explicit HTTPS/);
});

test("rendering preserves frontmatter-like prose, table text, images and duplicate section targets", () => {
  const window = new Window();
  try {
    window.document.body.innerHTML = renderReleaseBody(
      "---\nnot: metadata\n---\n\n## Fixed\n\n## Fixed\n\n[Again](#fixed-1)\n\n![Diagram](diagram.png)\n\n| Backend | Shared state |\n| --- | --- |\n| Fixture | Yes |",
      facts(),
    );
    assert.ok(window.document.body.textContent?.includes("not: metadata"));
    assert.ok(window.document.getElementById("release-fixed-1"));
    assert.ok(window.document.querySelector('a[href="#release-fixed-1"]'));
    assert.equal(
      required(window.document.querySelector("img")).getAttribute("src"),
      `https://raw.githubusercontent.com/arkoren/arkoren/${"a".repeat(40)}/core/diagram.png`,
    );
    assert.equal(required(window.document.querySelector("tbody td:last-child")).textContent, "Yes");
    assert.ok(window.document.querySelector('[role="region"][tabindex="0"] table'));
  } finally {
    window.happyDOM.abort();
  }
});

test("each verified record has one linked canonical detail, exact command and safe complete notes", () => {
  const window = new Window();
  try {
    const root = new URL("../dist/", import.meta.url);
    window.document.write(readFileSync(new URL("changelog.html", root), "utf8"));
    const entries = readReleases({ modules: releaseModules() });
    const routes = [...window.document.querySelectorAll("[data-release-entry] a")].map((link) =>
      link.getAttribute("href"),
    );
    assert.deepEqual(
      [...routes].sort(),
      entries.map((entry) => releaseLinks(entry.data).route).sort(),
    );
    const sitemap = readFileSync(new URL("sitemap-0.xml", root), "utf8");
    assert.ok(sitemap.includes("https://arkoren.dev/changelog<"));
    for (const entry of entries) {
      const links = releaseLinks(entry.data);
      window.document.documentElement.innerHTML = readFileSync(
        new URL(`${links.route.slice(1)}.html`, root),
        "utf8",
      );
      const document = window.document;
      assert.equal(
        required(document.querySelector('link[rel="canonical"]')).getAttribute("href"),
        `https://arkoren.dev${links.route}`,
      );
      assert.equal(
        required(document.querySelector('meta[property="og:url"]')).getAttribute("content"),
        `https://arkoren.dev${links.route}`,
      );
      assert.ok(required(document.querySelector("h1")).textContent.includes(entry.data.module));
      assert.equal(
        required(document.querySelector("[data-copy]")).getAttribute("data-copy"),
        releaseCommand(entry.data),
      );
      assert.ok(document.querySelector(`a[href="${links.github}"]`));
      assert.ok(document.querySelector(`a[href="${links.commit}"]`));
      assert.ok(document.querySelector('a[href="/changelog"]'));
      assert.equal(
        required(document.querySelector("time")).getAttribute("datetime"),
        entry.data.publishedAt,
      );
      if (entry.body !== "") {
        const expected = new Window();
        try {
          expected.document.body.innerHTML = renderReleaseBody(entry.body, entry.data);
          assert.equal(
            required(document.querySelector('[aria-label="Release notes"]')).textContent?.trim(),
            expected.document.body.textContent?.trim(),
          );
        } finally {
          expected.happyDOM.abort();
        }
      }
      assert.ok(sitemap.includes(`https://arkoren.dev${links.route}<`));
      assert.equal(document.querySelector('meta[name="robots"][content*="noindex"]'), null);
      assert.ok(
        !required(
          document.querySelector('script[type="application/ld+json"]'),
        ).textContent.includes("BlogPosting"),
      );
    }
  } finally {
    window.happyDOM.abort();
  }
});
