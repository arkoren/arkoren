/**
 * Refresh the embedded skill's navigation using the website's guide tree.
 * Run `npm run guides:generate` to write, or `npm run guides:check` to verify.
 * Metadata schema validation remains part of the Astro build.
 */
import { readFile, readdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { parseFrontmatter } from "astro/markdown";
import { buildGuideTree } from "../src/lib/docs/tree.ts";

/** Authored consumer skill root; independent of the invoking directory. */
const skill = new URL("../../cmd/arkoren/skills/arkoren/", import.meta.url);

/**
 * Read all guide Markdown, retaining source-relative ids for shared navigation.
 * Traversal, read, and YAML errors propagate instead of silently losing pages.
 */
async function readGuides(root: URL) {
  /** Recursive paths remain relative to the guide root, matching collection ids. */
  const files = await readdir(root, { recursive: true });
  return Promise.all(
    files
      .filter((file) => file.endsWith(".md"))
      .sort()
      .map(async (file) => {
        /** Narrow parser output at the file boundary before ordering or rendering. */
        const { title, order, description } = parseFrontmatter(
          await readFile(new URL(file, root), "utf8"),
        ).frontmatter;
        if (
          typeof title !== "string" ||
          typeof order !== "number" ||
          typeof description !== "string"
        ) {
          throw new Error(`invalid guide metadata: ${file}`);
        }
        return {
          id: file.replaceAll("\\", "/").replace(/\.md$/, ""),
          data: { title, order, description },
        };
      }),
  );
}

/**
 * Render the website's ordered groups as offline relative links and descriptions.
 * Reject orphan pages that the navigation tree would otherwise omit; empty
 * metadata-only groups are intentionally absent, just as on the website.
 */
function renderIndex(guides: Awaited<ReturnType<typeof readGuides>>): string {
  /** Shared navigation owns grouping and all ordering, including title ties. */
  const groups = buildGuideTree(guides);
  /** Membership guards against silently dropping pages without group metadata. */
  const included = new Set(groups.flatMap(({ pages }) => pages.map(({ id }) => id)));
  /** Metadata-only groups need no link; every other Markdown file must appear. */
  const orphan = guides.find(({ id }) => !id.endsWith("/index") && !included.has(id));
  if (orphan) throw new Error(`guide missing from navigation: ${orphan.id}`);
  return (
    "\n\n### Guides\n" +
    groups
      .map(
        ({ meta, pages }) =>
          `\n#### ${meta.data.title}\n\n` +
          pages
            .map(
              ({ id, data }) =>
                `- [${data.title}](references/guides/${id}.md): ${data.description.trim().replace(/\s+/g, " ")}\n`,
            )
            .join(""),
      )
      .join("")
  );
}

/**
 * Replace exactly one ordered marker pair, preserving all surrounding text.
 * Missing, repeated, or reversed markers fail before any write can occur.
 */
function replaceIndex(source: string, index: string): string {
  /** Literal delimiters identify the only generated region in the authored skill. */
  const begin = "<!-- toc:begin -->";
  /** The closing delimiter is retained along with the untouched suffix. */
  const end = "<!-- toc:end -->";
  if (source.split(begin).length !== 2 || source.split(end).length !== 2) {
    throw new Error("expected one toc marker pair");
  }
  /** Offsets exclude the delimiters themselves from the replacement. */
  const start = source.indexOf(begin) + begin.length;
  /** Reversed delimiters fail instead of deleting unrelated source. */
  const stop = source.indexOf(end);
  if (stop < start) throw new Error("toc end precedes begin");
  return source.slice(0, start) + index + source.slice(stop);
}

/**
 * Refresh only SKILL.md's guide index, writing only when its contents change.
 * `--check` rejects a stale index without writing; no arguments generates it.
 * The optional root directory URL (with a trailing slash) lets fixture tests
 * use isolated authored trees. The default is the bundled consumer source.
 * Filesystem, parser, marker, and freshness errors propagate to the caller.
 *
 * @example
 * try {
 *   await refreshGuideIndex(["--check"]);
 * } catch (error) {
 *   console.error(error.message); // Report stale navigation or invalid inputs.
 * }
 */
export async function refreshGuideIndex(args: string[] = [], root: URL = skill): Promise<void> {
  if (args.some((arg) => arg !== "--check")) throw new Error("usage: guides.ts [--check]");
  /** Read authored pages before calculating the replacement; never edit them. */
  const guides = await readGuides(new URL("references/guides/", root));
  /** Only the skill entry file is writable by this command. */
  const target = new URL("SKILL.md", root);
  /** Retain original line endings and prose outside the marker pair. */
  const source = await readFile(target, "utf8");
  /** Compute the complete result before choosing read-only or write mode. */
  const updated = replaceIndex(source, renderIndex(guides));
  if (source === updated) return;
  if (args.includes("--check"))
    throw new Error("guide index is stale; run npm run guides:generate");
  await writeFile(target, updated);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await refreshGuideIndex(process.argv.slice(2));
}
