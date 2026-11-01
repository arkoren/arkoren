import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { Window } from "happy-dom";
import { collectReleases, syncReleases, type ReleaseSource } from "../scripts/releases.ts";
import {
  parseRelease,
  readReleases,
  releaseLinks,
  releaseModules,
  releaseSchema,
  renderReleaseBody,
  serializeRelease,
  type ReleaseData,
} from "../src/lib/releases/content.ts";

/** A full commit SHA is distinct from the signed annotation object's SHA. */
const COMMIT = "1234567890123456789012345678901234567890";
/** An annotation points at COMMIT, not itself. */
const ANNOTATION = "abcdefabcdefabcdefabcdefabcdefabcdefabcd";
/** Controlled publication facts, unrelated to published marketing copy. */
function facts(): ReleaseData {
  return {
    module: "arkoren.dev/core",
    version: "v0.2.3",
    publishedAt: "2026-11-01T13:16:18Z",
    commit: COMMIT,
    prerelease: true,
  };
}
/** A published GitHub response with a branch target, as GitHub permits. */
function published() {
  return {
    tag_name: "core/v0.2.3",
    published_at: "2026-11-01T13:16:18Z",
    target_commitish: "main",
    prerelease: true,
    draft: false,
    body: "## Fixed\n\nA fixture.",
  };
}
/** An offline source models annotation peeling and records every requested URL. */
function source(pages: unknown = [[published()]]): ReleaseSource {
  return {
    pages: () => pages,
    object: (endpoint) => {
      if (endpoint.includes("/git/ref/tags/")) return { object: { type: "tag", sha: ANNOTATION } };
      assert.equal(endpoint, `repos/arkoren/arkoren/git/tags/${ANNOTATION}`);
      return { object: { type: "commit", sha: COMMIT } };
    },
  };
}

test("published timestamps and signed annotated tags retain their actual facts", () => {
  const [record] = collectReleases({ source: source(), modules: ["arkoren.dev/core"] });
  assert.deepEqual(record.data, facts());
  assert.equal(record.path, "core/v0.2.3.md");
  assert.deepEqual(releaseLinks(record.data), {
    tag: "core/v0.2.3",
    github: "https://github.com/arkoren/arkoren/releases/tag/core/v0.2.3",
    route: "/changelog/core/v0.2.3",
    commit: `https://github.com/arkoren/arkoren/commit/${COMMIT}`,
  });
});

test("all pages contribute prereleases while drafts are excluded", () => {
  const records = collectReleases({
    modules: ["arkoren.dev/core", "arkoren.dev/driver/new-driver"],
    source: source([
      [{ ...published(), draft: true }],
      [published()],
      [{ ...published(), tag_name: "driver/new-driver/v0.1.0" }],
    ]),
  });
  assert.deepEqual(
    records.map((record) => record.path),
    ["core/v0.2.3.md", "driver/new-driver/v0.1.0.md"],
  );
  assert.equal(
    records.every((record) => record.data.prerelease),
    true,
  );
});

test("API ordering and unchanged repeated syncs produce identical bytes", (t) => {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "release-notes-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const modules = ["arkoren.dev/core"];
  const second = { ...published(), tag_name: "core/v0.2.4" };
  assert.equal(
    syncReleases({ root, modules, source: source([[second, published()]]) }).changed.length,
    2,
  );
  const bytes = readFileSync(join(root, "core/v0.2.3.md"));
  assert.deepEqual(syncReleases({ root, modules, source: source([[published()], [second]]) }), {
    count: 2,
    changed: [],
  });
  assert.deepEqual(readFileSync(join(root, "core/v0.2.3.md")), bytes);
});

test("quotes, YAML fences, Unicode and code fences survive newline-only normalization", () => {
  const body = '"quoted": café\r\n---\r\n```yaml\r\nmodule: "evil"\r\n```\rfinal';
  const text = serializeRelease(facts(), body);
  const entry = parseRelease({
    source: text,
    path: "core/v0.2.3.md",
    modules: ["arkoren.dev/core"],
  });
  assert.deepEqual(entry.data, facts());
  assert.equal(entry.body, '"quoted": café\n---\n```yaml\nmodule: "evil"\n```\nfinal');
  assert.equal(text.endsWith("\n"), false);
});

test("duplicate publication identities fail instead of overwriting", () => {
  assert.throws(
    () =>
      collectReleases({
        source: source([[published()], [published()]]),
        modules: ["arkoren.dev/core"],
      }),
    /duplicate/,
  );
});

test("unrecognized published modules fail instead of disappearing", () => {
  assert.throws(() =>
    collectReleases({
      source: source([[{ ...published(), tag_name: "other/v0.1.0" }]]),
      modules: ["arkoren.dev/core"],
    }),
  );
});

test("version suffixes and leading zeros are not accepted", () => {
  assert.equal(
    releaseSchema(["arkoren.dev/core"]).safeParse({ ...facts(), version: "v0.01.0" }).success,
    false,
  );
  assert.equal(
    releaseSchema(["arkoren.dev/core"]).safeParse({ ...facts(), version: "v0.1.0-alpha" }).success,
    false,
  );
});

test("nonexistent dates and date-only metadata are rejected", () => {
  assert.equal(
    releaseSchema(["arkoren.dev/core"]).safeParse({
      ...facts(),
      publishedAt: "2026-02-30T00:00:00Z",
    }).success,
    false,
  );
  assert.equal(
    releaseSchema(["arkoren.dev/core"]).safeParse({ ...facts(), publishedAt: "2026-11-01" })
      .success,
    false,
  );
});

test("path traversal cannot become a managed identity", () => {
  assert.throws(
    () =>
      parseRelease({
        source: serializeRelease(facts(), "safe"),
        path: "../core/v0.2.3.md",
        modules: ["arkoren.dev/core"],
      }),
    /filename/,
  );
  assert.equal(
    releaseSchema(["arkoren.dev/core"]).safeParse({ ...facts(), module: "arkoren.dev/../core" })
      .success,
    false,
  );
});

test("mismatched files and alternate copies cannot alias a published identity", (t) => {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "release-notes-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "core"));
  writeFileSync(join(root, "core/v0.2.3.md"), serializeRelease(facts(), "safe"));
  writeFileSync(join(root, "copy.md"), serializeRelease(facts(), "safe"));
  assert.throws(() => readReleases({ root, modules: ["arkoren.dev/core"] }), /filename/);
});

test("full target SHAs must agree with the peeled commit", () => {
  assert.throws(
    () =>
      collectReleases({
        source: source([[{ ...published(), target_commitish: ANNOTATION }]]),
        modules: ["arkoren.dev/core"],
      }),
    /disagrees/,
  );
});

test("cyclic annotation chains cannot hang the importer", () => {
  assert.throws(
    () =>
      collectReleases({
        modules: ["arkoren.dev/core"],
        source: {
          pages: () => [[published()]],
          object: () => ({ object: { type: "tag", sha: ANNOTATION } }),
        },
      }),
    /cyclic/,
  );
});

test("API failure after earlier valid entries leaves history unchanged", (t) => {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "release-notes-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const modules = ["arkoren.dev/core"];
  syncReleases({ root, modules, source: source() });
  const before = readFileSync(join(root, "core/v0.2.3.md"));
  let requests = 0;
  assert.throws(
    () =>
      syncReleases({
        root,
        modules,
        source: {
          pages: () => [
            [{ ...published(), body: "correction" }],
            [{ ...published(), tag_name: "core/v0.2.4" }],
          ],
          object: () => {
            if (++requests > 1) throw new Error("offline");
            return { object: { type: "commit", sha: COMMIT } };
          },
        },
      }),
    /offline/,
  );
  assert.deepEqual(readFileSync(join(root, "core/v0.2.3.md")), before);
});

test("partial publication and check mode never delete existing history", (t) => {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "release-notes-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const modules = ["arkoren.dev/core"];
  syncReleases({ root, modules, source: source() });
  assert.deepEqual(syncReleases({ root, modules, source: source([[]]) }), {
    count: 0,
    changed: [],
  });
  const before = readFileSync(join(root, "core/v0.2.3.md"));
  assert.equal(
    syncReleases({
      root,
      modules,
      isCheck: true,
      source: source([[{ ...published(), body: "correction" }]]),
    }).changed.length,
    1,
  );
  assert.deepEqual(readFileSync(join(root, "core/v0.2.3.md")), before);
});

test("raw HTML is rejected, including event handlers, rather than silently rewritten", () => {
  assert.throws(() => renderReleaseBody('<img src="x" onerror="alert(1)">'), /raw HTML/);
  assert.throws(() => renderReleaseBody("<script>alert(1)</script>"), /raw HTML/);
});

test("parsed links reject encoded active schemes and reference definitions", () => {
  assert.throws(() => renderReleaseBody("[click](javascript&#58;alert%281%29)"), /unsafe URL/);
  assert.throws(() => renderReleaseBody("[click][x]\n\n[x]: data:text/html,evil"), /unsafe URL/);
});

test("plain Markdown renders inert code, links and text, never MDX expressions", () => {
  const html = renderReleaseBody(
    "## Notes\n\n{globalThis.compromised = true}\n\n```html\n<script>alert(1)</script>\n```\n\n[Source](https://example.com/notes)",
  );
  const window = new Window();
  try {
    window.document.body.innerHTML = html;
    assert.equal(window.document.querySelector("script"), null);
    assert.equal(window.document.querySelector("h2")?.textContent, "Notes");
    assert.equal(window.document.querySelector("code")?.textContent, "<script>alert(1)</script>\n");
    assert.equal(
      window.document.querySelector("a")?.getAttribute("href"),
      "https://example.com/notes",
    );
    assert.match(window.document.body.textContent ?? "", /globalThis.compromised/);
  } finally {
    window.happyDOM.abort();
  }
});

test("committed history loads offline and covers only canonical manifests", () => {
  const modules = releaseModules();
  const entries = readReleases({ modules });
  assert.ok(entries.length > 0);
  assert.equal(new Set(entries.map((entry) => entry.id)).size, entries.length);
  assert.equal(
    entries.every((entry) => modules.includes(entry.data.module)),
    true,
  );
});

test("CLI errors do not echo credentials from failing GitHub commands", (t) => {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "release-transport-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  writeFileSync(
    join(root, "gh"),
    '#!/usr/bin/env node\nprocess.stderr.write("secret-fixture-token"); process.exit(1);\n',
    { mode: 0o755 },
  );
  const result = spawnSync(
    process.execPath,
    ["--experimental-strip-types", "scripts/releases.ts", "--check"],
    {
      cwd: new URL("../", import.meta.url),
      env: { ...process.env, PATH: `${root}:${process.env.PATH}` },
      encoding: "utf8",
    },
  );
  assert.equal(result.status, 1);
  assert.match(result.stderr, /release sync failed/);
  assert.doesNotMatch(result.stdout + result.stderr, /secret-fixture-token/);
});

test("unsafe content in a later publication cannot partially update earlier notes", (t) => {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "release-notes-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const modules = ["arkoren.dev/core"];
  syncReleases({ root, modules, source: source() });
  const before = readFileSync(join(root, "core/v0.2.3.md"));
  assert.throws(
    () =>
      syncReleases({
        root,
        modules,
        source: source([
          [
            { ...published(), body: "correction" },
            { ...published(), tag_name: "core/v0.2.4", body: "<script>bad()</script>" },
          ],
        ]),
      }),
    /raw HTML/,
  );
  assert.deepEqual(readFileSync(join(root, "core/v0.2.3.md")), before);
});

test("managed history rejects symlinks instead of writing through them", (t) => {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "release-notes-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "outside"));
  mkdirSync(join(root, "history"));
  symlinkSync(join(root, "outside"), join(root, "history/core"));
  assert.throws(
    () =>
      syncReleases({
        root: join(root, "history"),
        modules: ["arkoren.dev/core"],
        source: source(),
      }),
    /unexpected/,
  );
});

test("a missing publication timestamp cannot create a release record", () => {
  assert.throws(() =>
    collectReleases({
      source: source([[{ ...published(), published_at: null }]]),
      modules: ["arkoren.dev/core"],
    }),
  );
});
