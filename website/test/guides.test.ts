import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { pathToFileURL } from "node:url";
import test from "node:test";
import type { TestContext } from "node:test";
import { refreshGuideIndex } from "../scripts/guides.ts";

/**
 * Create an isolated authored skill with folded YAML and CRLF surrounding text.
 * Each test owns its directory; cleanup removes only that fixture tree.
 */
async function fixture(t: TestContext): Promise<URL> {
  /** Unique directory keeps fixtures independent of the actual embedded skill. */
  const directory = await mkdtemp(`${tmpdir()}/guides-`);
  t.after(() => rm(directory, { recursive: true, force: true }));
  /** Directory URLs use the same resolution rules as the production command. */
  const root = pathToFileURL(`${directory}/`);
  await mkdir(new URL("references/guides/start/", root), { recursive: true });
  await writeFile(
    new URL("references/guides/start/index.md", root),
    "---\ntitle: Start\ndescription: Group\norder: 1\n---\n",
  );
  await writeFile(
    new URL("references/guides/start/hello.md", root),
    "---\ntitle: 'Hello: world'\ndescription: >\n  First line\n  second line\norder: 1\n---\n",
  );
  await writeFile(
    new URL("SKILL.md", root),
    "before\r\n<!-- toc:begin -->old<!-- toc:end -->\r\nafter",
  );
  return root;
}

test("check rejects stale navigation without writing; generation is idempotent", async (t) => {
  /** All writes in this scenario target the disposable skill. */
  const root = await fixture(t);
  /** The entry file is the command's only permitted output. */
  const target = new URL("SKILL.md", root);
  /** Exact stale bytes establish that check mode preserves authored content. */
  const original = await readFile(target, "utf8");
  await assert.rejects(refreshGuideIndex(["--check"], root), /stale/);
  assert.equal(await readFile(target, "utf8"), original);
  await refreshGuideIndex([], root);
  assert.equal(
    await readFile(target, "utf8"),
    "before\r\n<!-- toc:begin -->\n\n### Guides\n\n#### Start\n\n" +
      "- [Hello: world](references/guides/start/hello.md): First line second line\n<!-- toc:end -->\r\nafter",
  );
  /** An unchanged timestamp confirms fresh generation does not rewrite the file. */
  const written = await stat(target);
  await refreshGuideIndex([], root);
  await refreshGuideIndex(["--check"], root);
  assert.equal((await stat(target)).mtimeMs, written.mtimeMs);
});

test("ambiguous and reversed markers fail without changing authored text", async (t) => {
  /** Valid guides isolate marker failures from traversal and metadata errors. */
  const root = await fixture(t);
  /** Each invalid source must survive the failed command unchanged. */
  const target = new URL("SKILL.md", root);
  for (const source of [
    "missing",
    "<!-- toc:begin --><!-- toc:begin --><!-- toc:end -->",
    "<!-- toc:end --><!-- toc:begin -->",
  ]) {
    await writeFile(target, source);
    await assert.rejects(refreshGuideIndex([], root), /toc/);
    assert.equal(await readFile(target, "utf8"), source);
  }
});

test("missing guide trees propagate read failures", async (t) => {
  /** Remove only the fixture's guides, retaining a readable skill entry file. */
  const root = await fixture(t);
  await rm(new URL("references/guides/", root), { recursive: true });
  await assert.rejects(refreshGuideIndex([], root), { code: "ENOENT" });
});

test("orphan guides cannot disappear from the generated navigation", async (t) => {
  /** A page without its group's index must fail rather than silently vanish. */
  const root = await fixture(t);
  await rm(new URL("references/guides/start/index.md", root));
  await assert.rejects(refreshGuideIndex([], root), /missing from navigation/);
});
