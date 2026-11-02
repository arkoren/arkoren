import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { z } from "astro/zod";

/**
 * Vanity import paths: every `arkoren.dev/...` path resolves to one repository.
 *
 * The `go` command fetches `https://arkoren.dev/<path>?go-get=1` and reads the
 * `go-import` meta tag. All modules live in subdirectories of the repository
 * (`core/go.mod`, `driver/redis/go.mod`, ...) and are tagged with their
 * directory as prefix (`core/v0.1.0`), so one tag mapping the `arkoren.dev`
 * prefix to the repository root covers every module. `go` also verifies the
 * prefix itself (`https://arkoren.dev/?go-get=1`), so every page carries it.
 */
export const IMPORT_PREFIX = "arkoren.dev";
export const REPOSITORY = "https://github.com/arkoren/arkoren";
export const GO_IMPORT = `${IMPORT_PREFIX} git ${REPOSITORY}`;
export const GO_SOURCE = `${IMPORT_PREFIX} ${REPOSITORY} ${REPOSITORY}/tree/main{/dir} ${REPOSITORY}/blob/main{/dir}/{file}#L{line}`;

/** Repository checkout the build scans; `ARKOREN_REPO` overrides it. */
const REPO_ROOT = resolve(process.env.ARKOREN_REPO ?? "..");

/**
 * Samples do not publish vanity-import landing pages. Keep their module roots
 * private even when an example checkout is present beside the libraries.
 */
const PRIVATE_MODULES = new Set(["arkoren.dev/example"]);

/** Directories never scanned. */
const SKIP_DIRS = new Set(["node_modules", "website", "testdata", "vendor", "dist"]);

export interface ImportPath {
  /** Full import path, e.g. `arkoren.dev/core/http/router`. */
  path: string;
  /** Module that contains the path; unset for a directory that only groups modules (`arkoren.dev/driver`). */
  module?: string;
  /** Directory in the repository, e.g. `core/http/router`. */
  dir: string;
  /** Whether the directory holds a Go package (else it only groups packages). */
  isPackage: boolean;
  /** First sentence of the package doc comment, if any. */
  synopsis?: string;
}

/** First sentence of the `// Package x ...` comment in a directory's Go files. */
function synopsis(dir: string, files: string[]): string | undefined {
  for (const file of files) {
    const match = readFileSync(join(dir, file), "utf8").match(/^((?:\/\/.*\n)+)package \w+/m);
    if (!match || !/^\/\/ Package \w+ /.test(match[1])) continue;
    const text = match[1]
      .replace(/^\/\/ ?/gm, "")
      .replace(/\s+/g, " ")
      .replace(/^Package \w+ /, "")
      .replace(/\[(\*?[\w.]+)\]/g, "$1") // doc links render as plain text
      .trim();
    const sentence = text.match(/^.*?[.!?](?=\s|$)/)?.[0] ?? text;
    return sentence.charAt(0).toUpperCase() + sentence.slice(1);
  }
  return undefined;
}

/** Module path declared by a `go.mod` file. */
const modulePath = (gomod: string) => readFileSync(gomod, "utf8").match(/^module\s+(\S+)/m)?.[1];

/**
 * Read library module declarations without loading packages or source files.
 * The repository's library boundary is core plus direct foundation and vendor
 * modules; command and private tooling modules are intentionally excluded.
 * Module areas absent from the checkout contribute no entries, so the website
 * can publish before the Go modules do. A present module directory with a
 * missing or malformed declaration fails the build instead of hiding an entry.
 * @example
 * const urls = libraryModules().map((path) => `https://pkg.go.dev/${path}`);
 */
export function libraryModules(): string[] {
  /** Module locations follow the repository's existing module boundaries. */
  const directories = [
    ...(existsSync(join(REPO_ROOT, "core")) ? ["core"] : []),
    ...["foundation", "driver"]
      .filter((area) => existsSync(join(REPO_ROOT, area)))
      .flatMap((area) =>
        readdirSync(join(REPO_ROOT, area), { withFileTypes: true })
          .filter((entry) => entry.isDirectory())
          .map((entry) => `${area}/${entry.name}`),
      ),
  ];
  return directories
    .map((directory) => {
      const path = modulePath(join(REPO_ROOT, directory, "go.mod"));
      if (path === undefined) throw new Error(`missing module declaration: ${directory}/go.mod`);
      return path;
    })
    .sort();
}

let cache: ImportPath[] | undefined;

/**
 * Every public import path under `arkoren.dev`: module roots, package
 * directories, and the directories between them (`go` tries each prefix of an
 * import path as a candidate module, so each must answer). `internal/`
 * packages, tests, and private modules are left out.
 */
export function importPaths(): ImportPath[] {
  if (cache) return cache;
  const found = new Map<string, ImportPath>();
  /** Local import discovery follows the same development boundary as CI policy. */
  const policyPath = join(REPO_ROOT, ".github/release-modules.json");
  const development = new Set(
    existsSync(policyPath)
      ? z
          .object({ schema: z.literal(1), development: z.array(z.string()) })
          .parse(JSON.parse(readFileSync(policyPath, "utf8"))).development
      : [],
  );

  const walk = (dir: string, module: { path: string; dir: string } | undefined) => {
    if (development.has(relative(REPO_ROOT, dir).split("\\").join("/"))) return;
    const entries = readdirSync(dir, { withFileTypes: true });
    const gomod = join(dir, "go.mod");
    if (existsSync(gomod)) {
      const path = modulePath(gomod);
      module =
        path?.startsWith(`${IMPORT_PREFIX}/`) && !PRIVATE_MODULES.has(path)
          ? { path, dir }
          : undefined;
    }
    if (module) {
      const rel = relative(module.dir, dir);
      if (rel.split("/").includes("internal")) return;
      const goFiles = entries
        .filter((e) => e.isFile() && e.name.endsWith(".go") && !e.name.endsWith("_test.go"))
        .map((e) => e.name);
      const path = rel ? `${module.path}/${rel}` : module.path;
      if (goFiles.length > 0 || !rel) {
        found.set(path, {
          path,
          module: module.path,
          dir: relative(REPO_ROOT, dir),
          isPackage: goFiles.length > 0,
          synopsis: synopsis(dir, goFiles),
        });
      }
    }
    for (const entry of entries) {
      if (entry.isDirectory() && !entry.name.startsWith(".") && !SKIP_DIRS.has(entry.name))
        walk(join(dir, entry.name), module);
    }
  };
  walk(REPO_ROOT, undefined);

  // Fill in the directories between the prefix and each path (`arkoren.dev/core/http`).
  for (const item of [...found.values()]) {
    const segments = item.path.split("/");
    for (let i = 2; i < segments.length; i++) {
      const path = segments.slice(0, i).join("/");
      if (found.has(path)) continue;
      const owner = [...found.values()]
        .filter((p) => p.module === p.path && path.startsWith(`${p.path}/`))
        .sort((a, b) => b.path.length - a.path.length)[0];
      found.set(path, {
        path,
        module: owner?.module,
        dir: path.slice(IMPORT_PREFIX.length + 1),
        isPackage: false,
      });
    }
  }

  cache = [...found.values()].sort((a, b) => (a.path < b.path ? -1 : 1));
  return cache;
}

/** Import paths directly below `path`. */
export const childrenOf = (path: string) =>
  importPaths().filter(
    (p) => p.path.startsWith(`${path}/`) && !p.path.slice(path.length + 1).includes("/"),
  );
