import { execFileSync } from "node:child_process";
import { existsSync, lstatSync, readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { z } from "astro/zod";
import { defineMdastPlugin, markdownToHtml } from "satteri";

/** The checkout, independent of the command's working directory. */
export const RELEASE_REPO =
  process.env.ARKOREN_REPO !== undefined
    ? resolve(process.env.ARKOREN_REPO)
    : fileURLToPath(new URL("../../../../", import.meta.url));
/** The only directory the importer owns. */
export const RELEASE_ROOT = join(RELEASE_REPO, "website/src/content/releases");
/** Canonical module directories, shared with the repository inventory policy. */
const MODULE = /^(core|cmd\/arkoren|(?:driver|foundation|ark)\/[a-z0-9][a-z0-9-]*)$/;
/** Normal pre-1.0 versions; prerelease status belongs to GitHub metadata. */
const VERSION = /^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;

/** Read committed manifests only; ignored local modules cannot enter history. */
export function releaseModules(repo = RELEASE_REPO): string[] {
  const files = execFileSync("git", ["ls-tree", "-r", "--name-only", "HEAD"], {
    cwd: repo,
    encoding: "utf8",
  }).split("\n");
  return files
    .filter((file) => file.endsWith("/go.mod") && MODULE.test(file.slice(0, -7)))
    .map((file) => {
      const directory = file.slice(0, -7);
      const manifest = execFileSync("git", ["show", `HEAD:${file}`], {
        cwd: repo,
        encoding: "utf8",
      });
      if (manifest.match(/^module\s+(\S+)/m)?.[1] !== `arkoren.dev/${directory}`) {
        throw new Error(`noncanonical module declaration: ${directory}`);
      }
      return `arkoren.dev/${directory}`;
    })
    .sort();
}

/** Validate exact source metadata without coercing timestamps or booleans. */
export function releaseSchema(modules: readonly string[]) {
  return z.strictObject({
    module: z.string().refine((value) => modules.includes(value), "unknown canonical module"),
    version: z.string().regex(VERSION),
    publishedAt: z.iso.datetime().refine((value) => {
      const date = new Date(value);
      return !Number.isNaN(date.valueOf()) && date.toISOString().replace(".000Z", "Z") === value;
    }, "invalid GitHub publication timestamp"),
    commit: z.string().regex(/^[0-9a-f]{40}$/),
    prerelease: z.boolean(),
  });
}

/** Published facts; title, tag and URLs are derived rather than trusted input. */
export type ReleaseData = z.infer<ReturnType<typeof releaseSchema>>;

/** Derive the stable collection identity and module-prefixed Git tag. */
export function releaseIdentity(data: ReleaseData): string {
  return `${data.module.slice("arkoren.dev/".length)}/${data.version}`;
}

/** Derive reader links from validated identity, never frontmatter URLs. */
export function releaseLinks(data: ReleaseData) {
  const id = releaseIdentity(data);
  return {
    tag: id,
    github: `https://github.com/arkoren/arkoren/releases/tag/${id}`,
    route: `/changelog/${id}`,
  };
}

/** Normalize CRLF and bare CR only; preserve all other body bytes and EOF. */
export function normalizeReleaseBody(body: string): string {
  return body.replace(/\r\n?/g, "\n");
}

/** Reject active content using parsed Markdown nodes, not source heuristics. */
const inertReleaseMarkdown = defineMdastPlugin({
  name: "inert-release-markdown",
  html() {
    throw new Error("release Markdown contains raw HTML; correct the GitHub body before syncing");
  },
  link(node) {
    validateReleaseLink(node.url);
  },
  image(node) {
    validateReleaseLink(node.url);
  },
  definition(node) {
    validateReleaseLink(node.url);
  },
});

/** Permit HTTPS, HTTP, mail links and relative references, not active schemes. */
function validateReleaseLink(url: string): void {
  const compact = url.replace(/[\u0000-\u0020\u007f]/g, "");
  if (/^[a-z][a-z0-9+.-]*:/i.test(compact) && !/^(https?:|mailto:)/i.test(compact)) {
    throw new Error("release Markdown contains an unsafe URL scheme");
  }
  if (compact.startsWith("//") || compact.includes("\\")) {
    throw new Error("release Markdown contains an ambiguous URL");
  }
}

/**
 * Render untrusted notes as plain Markdown, without MDX or raw HTML.
 * Relative URLs remain unchanged here; a reader must resolve them against
 * GitHub, not the website route. Importing and loading both enforce this gate.
 */
export function renderReleaseBody(body: string): string {
  return markdownToHtml(body, { mdastPlugins: [inertReleaseMarkdown] }).html;
}

/** Serialize JSON (a YAML subset) so remote text cannot inject frontmatter. */
export function serializeRelease(data: ReleaseData, body: string): string {
  renderReleaseBody(body);
  return `---\n${JSON.stringify(data, null, 2)}\n---\n${normalizeReleaseBody(body)}`;
}

/** Parse the managed format and enforce its exact filename-to-identity mapping. */
export function parseRelease(options: {
  source: string;
  path: string;
  modules: readonly string[];
}) {
  const match = options.source.match(/^---\n(\{[\s\S]*?\n\})\n---\n([\s\S]*)$/);
  if (match === null) throw new Error(`invalid managed release format: ${options.path}`);
  const data = releaseSchema(options.modules).parse(JSON.parse(match[1]));
  const id = releaseIdentity(data);
  if (options.path !== `${id}.md`)
    throw new Error(`release filename does not match identity: ${options.path}`);
  renderReleaseBody(match[2]);
  return { id, data, body: match[2] };
}

/** Load only ordinary Markdown; reject symlinks and alternate identity files. */
export function readReleases(options: { root?: string; modules: readonly string[] }) {
  const root = options.root ?? RELEASE_ROOT;
  if (!existsSync(root)) return [];
  const paths: string[] = [];
  /** Walk managed history without following links outside its ownership boundary. */
  function walk(directory: string): void {
    for (const entry of readdirSync(join(root, directory))) {
      const path = directory ? `${directory}/${entry}` : entry;
      const stat = lstatSync(join(root, path));
      if (stat.isDirectory()) walk(path);
      else if (stat.isFile() && path.endsWith(".md")) paths.push(path);
      else throw new Error(`unexpected release history file: ${path}`);
    }
  }
  if (lstatSync(root).isSymbolicLink()) throw new Error("release root must not be a symlink");
  walk("");
  const entries = paths.sort().map((path) =>
    parseRelease({
      source: readFileSync(join(root, path), "utf8"),
      path,
      modules: options.modules,
    }),
  );
  if (new Set(entries.map((entry) => entry.id)).size !== entries.length) {
    throw new Error("duplicate release identity");
  }
  return entries;
}
