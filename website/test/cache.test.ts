import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

/** Renderer changes must reach unchanged Markdown even when its content cache is warm. */
test("warm collection caches do not preserve HTML from an older Markdown renderer", () => {
  const website = fileURLToPath(new URL("../", import.meta.url));
  const temporary = existsSync(resolve(tmpdir(), "opencode"))
    ? resolve(tmpdir(), "opencode")
    : tmpdir();
  const directory = mkdtempSync(resolve(temporary, "arkoren-render-cache-"));
  const root = resolve(directory, "site");
  const references = resolve(directory, "references");
  const posts = resolve(directory, "posts");
  try {
    mkdirSync(resolve(root, "src/pages"), { recursive: true });
    mkdirSync(resolve(root, "src/lib/docs"), { recursive: true });
    mkdirSync(resolve(references, "guides/fixture"), { recursive: true });
    mkdirSync(posts);
    symlinkSync(resolve(website, "node_modules"), resolve(root, "node_modules"), "dir");
    cpSync(resolve(website, "src/content.config.ts"), resolve(root, "src/content.config.ts"));
    cpSync(
      resolve(website, "src/lib/docs/references.ts"),
      resolve(root, "src/lib/docs/references.ts"),
    );
    writeFileSync(
      resolve(references, "guides/fixture/page.md"),
      "---\ntitle: Fixture Guide\ndescription: Cache fixture\norder: 10\n---\n\nrenderer-fixture\n",
    );
    writeFileSync(
      resolve(posts, "page.md"),
      "---\ntitle: Fixture Post\ndescription: Cache fixture\npublishedAt: 2026-01-01\ncategory: Fixture\n---\n\nrenderer-fixture\n",
    );
    writeFileSync(
      resolve(root, "src/pages/index.astro"),
      `---
import { getCollection, render } from "astro:content";
const entries = (await Promise.all([getCollection("guides"), getCollection("blog")])).flat();
const rendered = await Promise.all(entries.map(async (entry) => ({entry, ...await render(entry)})));
---
<html lang="en"><body>{rendered.map(({entry, Content}) => <article data-collection={entry.collection}><Content /></article>)}</body></html>
`,
    );
    /** Change only the visitor implementation; serialized plugin metadata stays identical. */
    function build(label: string) {
      writeFileSync(
        resolve(root, "astro.config.ts"),
        `
import { defineConfig } from "astro/config";
import { satteri } from "@astrojs/markdown-satteri";
import { defineHastPlugin } from "satteri";
export default defineConfig({
  root: ${JSON.stringify(root)}, cacheDir: ${JSON.stringify(resolve(root, ".cache"))},
  markdown: { processor: satteri({ hastPlugins: [defineHastPlugin({
    name: "fixture-renderer", element: { filter: ["p"], visit(node, ctx) {
      if (ctx.textContent(node) === "renderer-fixture") ctx.setProperty(node.children[0], "value", ${JSON.stringify(label)});
    } }
  })] }) }
});
`,
      );
      execFileSync(
        "npm",
        ["exec", "--", "astro", "build", "--root", root, "--config", "astro.config.ts"],
        {
          // Isolate Astro's fallback prerender staging as well as its cache.
          cwd: root,
          env: { ...process.env, ARKOREN_REFERENCES: references, ARKOREN_BLOG: posts },
          stdio: "pipe",
          timeout: 60000,
        },
      );
      return readFileSync(resolve(root, "dist/index.html"), "utf8");
    }
    assert.equal((build("first-render").match(/first-render/g) ?? []).length, 2);
    const updated = build("updated-render");
    assert.equal(
      (updated.match(/updated-render/g) ?? []).length,
      2,
      "guides and posts must use the updated renderer without Markdown edits or cache deletion",
    );
    assert.doesNotMatch(updated, /first-render/);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
