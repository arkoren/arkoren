import { execFileSync } from "node:child_process";
import {
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  renameSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { z } from "astro/zod";
import {
  RELEASE_ROOT,
  readReleases,
  releaseIdentity,
  releaseModules,
  releaseSchema,
  serializeRelease,
  type ReleaseData,
} from "../src/lib/releases/content.ts";

/** Read-only GitHub namespace; callers cannot select another host or repository. */
const API = "repos/arkoren/arkoren";
/** Validate remote fields before using them in file identities or API paths. */
const githubRelease = z.object({
  tag_name: z.string(),
  draft: z.boolean(),
  prerelease: z.boolean(),
  published_at: z.string().nullable(),
  body: z.string().nullable(),
  target_commitish: z.string(),
});
/** Git object responses can point to an annotation, never assume it is a commit. */
const gitObject = z.object({
  object: z.object({
    type: z.enum(["tag", "commit"]),
    sha: z.string().regex(/^[0-9a-f]{40}$/),
  }),
});

/** An injectable read-only transport keeps unit tests offline. */
export interface ReleaseSource {
  /** All release-list pages, obtained in one successful paginated request. */
  pages(): unknown;
  /** A GitHub Git ref or annotated-tag response. */
  object(endpoint: string): unknown;
}

/** Fetch JSON without a shell and never print subprocess stderr or credentials. */
function githubJSON(args: string[]): unknown {
  try {
    return JSON.parse(
      execFileSync("gh", ["api", ...args], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
        maxBuffer: 64 * 1024 * 1024,
      }),
    );
  } catch {
    throw new Error(
      "GitHub read failed; check gh authentication, connectivity and repository read access",
    );
  }
}

/** Resolve nested annotated tags to the full commit; reject cycles and noncommits. */
function publishedCommit(source: ReleaseSource, tag: string): string {
  let object = gitObject.parse(
    source.object(`${API}/git/ref/tags/${encodeURIComponent(tag)}`),
  ).object;
  const seen = new Set<string>();
  while (object.type === "tag") {
    if (seen.has(object.sha) || seen.size >= 16)
      throw new Error("cyclic or excessive annotated tag chain");
    seen.add(object.sha);
    object = gitObject.parse(source.object(`${API}/git/tags/${object.sha}`)).object;
  }
  return object.sha;
}

/**
 * Validate every page and confirmed publication before returning any writes.
 * Drafts are excluded; published unknown/malformed tags fail rather than vanish.
 */
export function collectReleases(options: { source: ReleaseSource; modules: readonly string[] }) {
  const pages = z.array(z.array(z.unknown())).min(1).parse(options.source.pages());
  const found = new Set<string>();
  const records: { data: ReleaseData; text: string; path: string }[] = [];
  for (const raw of pages.flat()) {
    const release = githubRelease.parse(raw);
    if (release.draft) continue;
    const split = release.tag_name.lastIndexOf("/");
    const data = releaseSchema(options.modules).parse({
      module: `arkoren.dev/${release.tag_name.slice(0, split)}`,
      version: release.tag_name.slice(split + 1),
      publishedAt: release.published_at,
      commit: publishedCommit(options.source, release.tag_name),
      prerelease: release.prerelease,
    });
    const id = releaseIdentity(data);
    if (id !== release.tag_name) throw new Error("published tag does not match release identity");
    if (found.has(id)) throw new Error("duplicate published release identity");
    found.add(id);
    if (
      /^[0-9a-f]{40}$/.test(release.target_commitish) &&
      release.target_commitish !== data.commit
    ) {
      throw new Error("release target_commitish disagrees with peeled tag commit");
    }
    records.push({ data, text: serializeRelease(data, release.body ?? ""), path: `${id}.md` });
  }
  return records.sort((a, b) => a.path.localeCompare(b.path, "en"));
}

/**
 * Reconcile only confirmed identities. No deletion and no write on read/validation
 * failure. Renames are atomic per file, not a multi-file filesystem transaction.
 */
export function syncReleases(options: {
  source: ReleaseSource;
  modules: readonly string[];
  root?: string;
  isCheck?: boolean;
}) {
  const root = options.root ?? RELEASE_ROOT;
  readReleases({ root, modules: options.modules });
  const records = collectReleases(options);
  const changed = records.filter((record) => {
    const destination = join(root, record.path);
    return !existsSync(destination) || readFileSync(destination, "utf8") !== record.text;
  });
  if (options.isCheck === true)
    return { count: records.length, changed: changed.map((record) => record.path) };
  for (const record of changed) {
    const destination = join(root, record.path);
    /** Check every existing ancestor, including a symlinked root's parents. */
    let directory = dirname(destination);
    while (directory !== dirname(directory)) {
      if (existsSync(directory) && lstatSync(directory).isSymbolicLink()) {
        throw new Error("release destination must not traverse symlinks");
      }
      directory = dirname(directory);
    }
    if (relative(root, destination).startsWith(".."))
      throw new Error("release destination escapes managed root");
  }
  for (const record of changed) {
    const destination = join(root, record.path);
    mkdirSync(dirname(destination), { recursive: true });
    const temporary = `${destination}.${process.pid}.tmp`;
    let hasTemporary = false;
    try {
      writeFileSync(temporary, record.text, { encoding: "utf8", flag: "wx" });
      hasTemporary = true;
      renameSync(temporary, destination);
    } finally {
      if (hasTemporary && existsSync(temporary)) unlinkSync(temporary);
    }
  }
  return { count: records.length, changed: changed.map((record) => record.path) };
}

/** The CLI has no API mutation operations and no build-time network dependency. */
function main(): void {
  const args = process.argv.slice(2);
  if (args.length > 1 || (args.length === 1 && args[0] !== "--check")) {
    throw new Error("usage: npm run releases:sync -- [--check]");
  }
  const result = syncReleases({
    modules: releaseModules(),
    isCheck: args[0] === "--check",
    source: {
      pages: () => githubJSON(["--paginate", "--slurp", `${API}/releases?per_page=100`]),
      object: (endpoint) => githubJSON([endpoint]),
    },
  });
  console.log(`${result.count} published module releases; ${result.changed.length} changed files`);
  if (args[0] === "--check" && result.changed.length > 0) process.exitCode = 1;
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main();
  } catch (error) {
    /** Remote schema values and process output can contain secrets; don't echo them. */
    console.error(
      error instanceof z.ZodError
        ? "invalid published release metadata; check canonical module, tag, timestamp and Git object fields"
        : "release sync failed; check GitHub read access, canonical release identities, Markdown safety and managed file permissions (no remote error text is logged)",
    );
    process.exitCode = 1;
  }
}
